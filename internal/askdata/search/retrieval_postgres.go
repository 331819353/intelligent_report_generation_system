package search

import (
	"context"
	"errors"
	"math"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/platform/database"
)

type PostgresRetrievalStore struct{ pool *pgxpool.Pool }

func NewPostgresRetrievalStore(pool *pgxpool.Pool) *PostgresRetrievalStore {
	return &PostgresRetrievalStore{pool: pool}
}

const eligibleSearchDocumentsSQL = ` FROM askdata.search_documents AS document
	JOIN askdata.release_objects AS release_object
	  ON release_object.tenant_id=document.tenant_id
	 AND release_object.domain_id=document.domain_id
	 AND release_object.object_type=document.object_type
	 AND release_object.object_version_id=document.object_version_id
	JOIN askdata.releases AS release
	  ON release.tenant_id=release_object.tenant_id
	 AND release.domain_id=release_object.domain_id
	 AND release.id=release_object.release_id
	JOIN askdata.release_projections AS projection
	  ON projection.tenant_id=release.tenant_id
	 AND projection.domain_id=release.domain_id
	 AND projection.release_id=release.id
	 AND projection.target='SEARCH_INDEX'
	WHERE document.tenant_id=askdata.current_tenant_id()
	  AND release.id=$1 AND release.content_hash=$2
	  AND release.status IN ('READY','ACTIVE')
	  AND projection.status='READY'
	  AND projection.expected_content_hash=release.content_hash
	  AND projection.applied_content_hash=release.content_hash
	  AND document.domain_id=ANY($3::uuid[])
	  AND document.object_type=ANY($4::text[]) `

const exactRetrievalSQL = `WITH scored AS (
	SELECT document.object_type,document.object_version_id,document.input_hash,
	  CASE
	    WHEN lower(COALESCE(document.metadata->>'name',''))=$5 THEN 1.0
	    WHEN lower(COALESCE(document.metadata->>'canonicalValue',''))=$5 THEN 1.0
	    WHEN EXISTS(
	      SELECT 1 FROM jsonb_array_elements_text(COALESCE(document.metadata->'aliases','[]'::jsonb)) AS alias(value)
	      WHERE lower(alias.value)=$5
	    ) THEN 0.95 ELSE 0
	  END::float8 AS score` + eligibleSearchDocumentsSQL + `
), ranked AS (
	SELECT *,row_number() OVER(PARTITION BY object_type ORDER BY score DESC,object_version_id) AS type_rank
	FROM scored WHERE score>0
) SELECT object_type,object_version_id::text,input_hash,score
	FROM ranked WHERE type_rank<=$6 ORDER BY object_type,type_rank,object_version_id`

const lexicalRetrievalSQL = `WITH scored AS (
	SELECT document.object_type,document.object_version_id,document.input_hash,
	  (CASE WHEN strpos(lower(document.document),$5)>0 THEN 1.0 ELSE 0 END
	   + similarity(lower(document.document),$5)
	   + ts_rank_cd(document.document_tsv,plainto_tsquery('simple',$5)))::float8 AS score` + eligibleSearchDocumentsSQL + `
), ranked AS (
	SELECT *,row_number() OVER(PARTITION BY object_type ORDER BY score DESC,object_version_id) AS type_rank
	FROM scored WHERE score>0
) SELECT object_type,object_version_id::text,input_hash,score
	FROM ranked WHERE type_rank<=$6 ORDER BY object_type,type_rank,object_version_id`

const vectorRetrievalSQL = `WITH scored AS (
	SELECT document.object_type,document.object_version_id,document.input_hash,
	  (1-(document.embedding <=> $5::halfvec))::float8 AS score` + eligibleSearchDocumentsSQL + `
	  AND document.embedding_status='SUCCEEDED' AND document.embedding IS NOT NULL
	  AND document.embedding_model=$6
), ranked AS (
	SELECT *,row_number() OVER(PARTITION BY object_type ORDER BY score DESC,object_version_id) AS type_rank
	FROM scored WHERE score>=0
) SELECT object_type,object_version_id::text,input_hash,score
	FROM ranked WHERE type_rank<=$7 ORDER BY object_type,type_rank,object_version_id`

func (store *PostgresRetrievalStore) Exact(
	ctx context.Context, scope askdata.PolicyScope, mention string, objectTypes []ObjectType, limit int,
) ([]RawHit, error) {
	return store.query(ctx, scope, exactRetrievalSQL, mention, "", nil, objectTypes, limit)
}

func (store *PostgresRetrievalStore) Lexical(
	ctx context.Context, scope askdata.PolicyScope, mention string, objectTypes []ObjectType, limit int,
) ([]RawHit, error) {
	return store.query(ctx, scope, lexicalRetrievalSQL, mention, "", nil, objectTypes, limit)
}

func (store *PostgresRetrievalStore) Vector(
	ctx context.Context, scope askdata.PolicyScope, vector []float32, model string,
	objectTypes []ObjectType, limit int,
) ([]RawHit, error) {
	return store.query(ctx, scope, vectorRetrievalSQL, "", model, vector, objectTypes, limit)
}

func (store *PostgresRetrievalStore) query(
	ctx context.Context, scope askdata.PolicyScope, query, mention, model string,
	vector []float32, objectTypes []ObjectType, limit int,
) (hits []RawHit, err error) {
	tenantID, domainIDs, releaseID, types, err := validateRetrievalStoreRequest(ctx, scope, objectTypes, limit)
	if err != nil {
		return nil, err
	}
	hits = []RawHit{}
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		arguments := []any{releaseID, string(scope.Release.ContentHash), domainIDs, types}
		if vector == nil {
			arguments = append(arguments, mention, limit)
		} else {
			if len(vector) != 2_560 || strings.TrimSpace(model) == "" || len(model) > 128 {
				return ErrInvalidRetrieval
			}
			arguments = append(arguments, formatEmbeddingVector(vector), model, limit)
		}
		rows, err := tx.Query(ctx, query, arguments...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var hit RawHit
			if err := rows.Scan(&hit.ObjectType, &hit.ObjectVersionID, &hit.InputHash, &hit.Score); err != nil {
				return err
			}
			if !validRetrievalObjectType(hit.ObjectType) || math.IsNaN(hit.Score) || math.IsInf(hit.Score, 0) || hit.Score < 0 {
				return errors.New("database returned an invalid semantic retrieval score")
			}
			hits = append(hits, hit)
		}
		return rows.Err()
	})
	return hits, err
}

func validateRetrievalStoreRequest(
	ctx context.Context, scope askdata.PolicyScope, objectTypes []ObjectType, limit int,
) (string, []uuid.UUID, uuid.UUID, []string, error) {
	if err := scope.Validate(); err != nil || limit < 1 || limit > 100 || len(objectTypes) == 0 {
		return "", nil, uuid.Nil, nil, ErrInvalidRetrieval
	}
	access, authenticated := database.AccessContextFromContext(ctx)
	if !authenticated || access.UserID != string(scope.ActorID) {
		return "", nil, uuid.Nil, nil, ErrInvalidRetrieval
	}
	tenantID, err := uuid.Parse(string(scope.TenantID))
	if err != nil {
		return "", nil, uuid.Nil, nil, ErrInvalidRetrieval
	}
	releaseID, err := uuid.Parse(string(scope.Release.ReleaseID))
	if err != nil {
		return "", nil, uuid.Nil, nil, ErrInvalidRetrieval
	}
	domainIDs := make([]uuid.UUID, len(scope.DomainIDs))
	selectedDomainPresent := false
	for index, domainID := range scope.DomainIDs {
		parsed, err := uuid.Parse(string(domainID))
		if err != nil {
			return "", nil, uuid.Nil, nil, ErrInvalidRetrieval
		}
		domainIDs[index] = parsed
		selectedDomainPresent = selectedDomainPresent || string(domainID) == access.DomainID
	}
	if !selectedDomainPresent {
		return "", nil, uuid.Nil, nil, ErrInvalidRetrieval
	}
	types := make([]string, len(objectTypes))
	for index, objectType := range objectTypes {
		if !validRetrievalObjectType(objectType) {
			return "", nil, uuid.Nil, nil, ErrInvalidRetrieval
		}
		types[index] = string(objectType)
	}
	return tenantID.String(), domainIDs, releaseID, types, nil
}

var _ RetrievalStore = (*PostgresRetrievalStore)(nil)
