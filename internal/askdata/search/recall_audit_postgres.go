package search

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/platform/database"
)

const recallAuditEligibleDocumentsSQL = ` FROM askdata.search_documents AS document
	LEFT JOIN askdata.release_objects AS release_object
	  ON release_object.tenant_id=document.tenant_id
	 AND release_object.domain_id=document.domain_id
	 AND release_object.object_type=document.object_type
	 AND release_object.object_version_id=document.object_version_id
	 AND release_object.release_id=$2
	LEFT JOIN askdata.report_semantic_assets AS report_asset
	  ON document.object_type='REPORT_ASSET'
	 AND report_asset.tenant_id=document.tenant_id
	 AND report_asset.domain_id=document.domain_id
	 AND report_asset.id=document.object_version_id
	 AND report_asset.semantic_release_id=$2
	 AND report_asset.semantic_release_content_hash=$3
	 AND report_asset.state='CERTIFIED'
	 AND report_asset.projection_state='READY'
	LEFT JOIN platform.report_versions AS report_version
	  ON report_version.tenant_id=report_asset.tenant_id
	 AND report_version.report_id=report_asset.report_id
	 AND report_version.id=report_asset.report_version_id
	 AND report_version.artifact_state='READY'
	JOIN askdata.releases AS release
	  ON release.tenant_id=document.tenant_id
	 AND release.domain_id=document.domain_id
	 AND release.id=$2
	JOIN askdata.release_projections AS projection
	  ON projection.tenant_id=release.tenant_id
	 AND projection.domain_id=release.domain_id
	 AND projection.release_id=release.id
	 AND projection.target='SEARCH_INDEX'
	WHERE document.tenant_id=askdata.current_tenant_id()
	  AND document.domain_id=$1 AND release.id=$2 AND release.content_hash=$3
	  AND release.status IN ('READY','ACTIVE')
	  AND projection.status='READY'
	  AND projection.expected_content_hash=release.content_hash
	  AND projection.applied_content_hash=release.content_hash
	  AND document.object_type=$4
	  AND document.embedding_status='SUCCEEDED'
	  AND document.embedding IS NOT NULL
	  AND document.embedding_model=$5
	  AND document.embedding_dim=$6
	  AND ((document.object_type='REPORT_ASSET' AND report_asset.id IS NOT NULL
	        AND report_version.id IS NOT NULL)
	       OR (document.object_type<>'REPORT_ASSET'
	        AND release_object.object_version_id IS NOT NULL)) `

const recallAuditVectorSQL = `SELECT document.object_version_id::text` +
	recallAuditEligibleDocumentsSQL + `
	ORDER BY document.embedding <=> $7::halfvec,document.object_version_id
	LIMIT $8`

type PostgresRecallAuditStore struct{ pool *pgxpool.Pool }

func NewPostgresRecallAuditStore(pool *pgxpool.Pool) *PostgresRecallAuditStore {
	return &PostgresRecallAuditStore{pool: pool}
}

func (store *PostgresRecallAuditStore) ListTenantIDs(ctx context.Context) ([]string, error) {
	if store == nil || store.pool == nil {
		return nil, ErrInvalidRecallAudit
	}
	rows, err := store.pool.Query(ctx, `SELECT id::text FROM platform.tenants
		WHERE status='ACTIVE' AND deleted_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var tenantID string
		if err := rows.Scan(&tenantID); err != nil {
			return nil, err
		}
		result = append(result, tenantID)
	}
	return result, rows.Err()
}

func (store *PostgresRecallAuditStore) LastRunAt(
	ctx context.Context, tenantID string,
) (time.Time, bool, error) {
	if store == nil || store.pool == nil || uuid.Validate(tenantID) != nil {
		return time.Time{}, false, ErrInvalidRecallAudit
	}
	var last *time.Time
	err := database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT max(run_at) FROM askdata.search_recall_audits`).Scan(&last)
	})
	if err != nil || last == nil {
		return time.Time{}, false, err
	}
	return last.UTC(), true, nil
}

func (store *PostgresRecallAuditStore) LoadSamples(
	ctx context.Context, tenantID string, since time.Time, limit int, model string, dimension int,
) ([]QueryVectorSample, error) {
	if store == nil || store.pool == nil || uuid.Validate(tenantID) != nil || since.IsZero() ||
		limit < 1 || limit > 10_000 || strings.TrimSpace(model) == "" || len(model) > 128 ||
		dimension != SearchEmbeddingDimension {
		return nil, ErrInvalidRecallAudit
	}
	result := []QueryVectorSample{}
	err := database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `WITH ranked AS (
			SELECT sample.id::text,sample.tenant_id::text,sample.domain_id::text,
			       sample.release_id::text,sample.release_hash,sample.doc_type,
			       sample.embedding::text,sample.embedding_model,sample.embedding_dim,
			       sample.created_at,
			       row_number() OVER(
			         PARTITION BY sample.domain_id,sample.doc_type
			         ORDER BY sample.sample_hash,sample.id
			       ) AS sample_rank
			FROM askdata.search_query_samples AS sample
			JOIN askdata.releases AS release
			  ON release.tenant_id=sample.tenant_id
			 AND release.domain_id=sample.domain_id
			 AND release.id=sample.release_id
			 AND release.content_hash=sample.release_hash
			WHERE sample.created_at>=$1 AND sample.embedding_model=$2
			  AND sample.embedding_dim=$3 AND release.status IN ('READY','ACTIVE')
		) SELECT id,tenant_id,domain_id,release_id,release_hash,doc_type,
		         embedding,embedding_model,embedding_dim,created_at
		  FROM ranked WHERE sample_rank<=$4
		  ORDER BY domain_id,doc_type,sample_rank,id`, since.UTC(), strings.TrimSpace(model), dimension, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var sample QueryVectorSample
			var releaseID, releaseHash, vectorText string
			if err := rows.Scan(
				&sample.ID, &sample.TenantID, &sample.DomainID, &releaseID, &releaseHash,
				&sample.DocumentType, &vectorText, &sample.EmbeddingModel,
				&sample.EmbeddingDimension, &sample.CapturedAt,
			); err != nil {
				return err
			}
			sample.Release = askdata.ReleaseRef{
				ReleaseID: askdata.ID(releaseID), ContentHash: askdata.ContentHash(releaseHash),
			}
			sample.Embedding, err = parseEmbeddingVector(vectorText, dimension)
			if err != nil {
				return err
			}
			if err := sample.Validate(); err != nil {
				return err
			}
			result = append(result, sample)
		}
		return rows.Err()
	})
	return result, err
}

func (store *PostgresRecallAuditStore) SearchANN(
	ctx context.Context, sample QueryVectorSample, limit, efSearch int,
) ([]askdata.ID, time.Duration, error) {
	if efSearch < 1 || efSearch > 10_000 {
		return nil, 0, ErrInvalidRecallAudit
	}
	return store.searchVector(ctx, sample, limit, efSearch, false)
}

func (store *PostgresRecallAuditStore) SearchExact(
	ctx context.Context, sample QueryVectorSample, limit int,
) ([]askdata.ID, time.Duration, error) {
	return store.searchVector(ctx, sample, limit, 0, true)
}

func (store *PostgresRecallAuditStore) searchVector(
	ctx context.Context, sample QueryVectorSample, limit, efSearch int, exact bool,
) (ids []askdata.ID, elapsed time.Duration, err error) {
	if store == nil || store.pool == nil || sample.Validate() != nil || !validRecallK(limit) {
		return nil, 0, ErrInvalidRecallAudit
	}
	connection, err := store.pool.Acquire(ctx)
	if err != nil {
		return nil, 0, err
	}
	defer connection.Release()
	tx, err := connection.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT
		set_config('app.tenant_id',$1,true),
		set_config('app.access_mode','SYSTEM',true),
		set_config('app.user_id','',true),
		set_config('app.domain_id','',true),
		set_config('application_name','askdata-recall-audit',true),
		set_config('statement_timeout','5s',true),
		set_config('lock_timeout','250ms',true)`, sample.TenantID); err != nil {
		return nil, 0, err
	}
	if exact {
		if _, err := tx.Exec(ctx, `SELECT
			set_config('enable_indexscan','off',true),
			set_config('enable_bitmapscan','off',true)`); err != nil {
			return nil, 0, err
		}
	} else if _, err := tx.Exec(
		ctx, `SELECT set_config('hnsw.ef_search',$1,true)`, strconv.Itoa(efSearch),
	); err != nil {
		return nil, 0, err
	}
	started := time.Now()
	rows, err := tx.Query(ctx, recallAuditVectorSQL,
		sample.DomainID, string(sample.Release.ReleaseID), string(sample.Release.ContentHash),
		string(sample.DocumentType), sample.EmbeddingModel, sample.EmbeddingDimension,
		formatEmbeddingVector(sample.Embedding), limit,
	)
	if err != nil {
		return nil, 0, err
	}
	ids = []askdata.ID{}
	for rows.Next() {
		var id askdata.ID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, 0, err
		}
		if err := id.Validate(); err != nil {
			rows.Close()
			return nil, 0, errors.New("recall audit returned an invalid object version")
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, 0, err
	}
	rows.Close()
	elapsed = time.Since(started)
	if err := tx.Commit(ctx); err != nil {
		return nil, 0, err
	}
	return ids, elapsed, nil
}

func (store *PostgresRecallAuditStore) SaveRecallAudits(
	ctx context.Context, tenantID string, results []RecallAuditResult,
) error {
	if store == nil || store.pool == nil || uuid.Validate(tenantID) != nil || len(results) == 0 {
		return ErrInvalidRecallAudit
	}
	return database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		for _, result := range results {
			if err := result.Validate(); err != nil || result.TenantID != tenantID {
				return ErrInvalidRecallAudit
			}
			if _, err := tx.Exec(ctx, `INSERT INTO askdata.search_recall_audits(
				tenant_id,domain_id,run_at,doc_type,k,sample_size,recall,
				p95_latency_ann,p95_latency_exact,embedding_model,embedding_dim,
				ef_search,threshold,below_threshold
			) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
				result.TenantID, result.DomainID, result.RunAt.UTC(), result.DocumentType,
				result.K, result.SampleSize, result.Recall,
				result.P95LatencyANN.Microseconds(), result.P95LatencyExact.Microseconds(),
				result.EmbeddingModel, result.EmbeddingDimension, result.EFSearch,
				result.Threshold, result.BelowThreshold,
			); err != nil {
				return err
			}
		}
		return nil
	})
}

func (store *PostgresRecallAuditStore) PurgeSamplesBefore(
	ctx context.Context, tenantID string, before time.Time,
) error {
	if store == nil || store.pool == nil || uuid.Validate(tenantID) != nil || before.IsZero() {
		return ErrInvalidRecallAudit
	}
	return database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM askdata.search_query_samples WHERE created_at<$1`, before.UTC())
		return err
	})
}

func (store *PostgresRetrievalStore) recordQuerySamples(
	ctx context.Context, scope askdata.PolicyScope, vector []float32, model string,
	objectTypes []ObjectType,
) error {
	if store == nil || store.pool == nil || scope.Validate() != nil ||
		len(vector) != SearchEmbeddingDimension || strings.TrimSpace(model) == "" || len(model) > 128 {
		return ErrInvalidRetrieval
	}
	access, authenticated := database.AccessContextFromContext(ctx)
	if !authenticated || access.UserID != string(scope.ActorID) || uuid.Validate(access.DomainID) != nil {
		return ErrInvalidRetrieval
	}
	domainAllowed := false
	for _, domainID := range scope.DomainIDs {
		domainAllowed = domainAllowed || string(domainID) == access.DomainID
	}
	if !domainAllowed {
		return ErrInvalidRetrieval
	}
	vectorText := formatEmbeddingVector(vector)
	return database.WithTenantTx(ctx, store.pool, string(scope.TenantID), func(tx pgx.Tx) error {
		sampleTypes := make([]ObjectType, 0, len(objectTypes))
		seenTypes := map[ObjectType]bool{}
		for _, objectType := range objectTypes {
			if !ValidRetrievalObjectType(objectType) {
				return ErrInvalidRetrieval
			}
			// MEASURE is the historical projection name for a metric. Recall
			// sampling uses the canonical analytical class so one query cannot
			// create two statistically identical groups during mixed-version
			// rollouts.
			if objectType == ObjectMeasureLegacy {
				objectType = ObjectMetric
			}
			if seenTypes[objectType] {
				continue
			}
			seenTypes[objectType] = true
			sampleTypes = append(sampleTypes, objectType)
		}
		for _, objectType := range sampleTypes {
			sampleHash := askdata.HashBytes([]byte(strings.Join([]string{
				string(scope.TenantID), access.DomainID, string(scope.Release.ReleaseID),
				string(scope.Release.ContentHash), string(objectType), strings.TrimSpace(model), vectorText,
			}, "\x00")))
			if _, err := tx.Exec(ctx, `SELECT askdata.record_search_query_sample(
				$1,$2,$3,$4,$5,$6,$7,$8
			)`, access.DomainID, scope.Release.ReleaseID, scope.Release.ContentHash,
				objectType, vectorText, strings.TrimSpace(model), SearchEmbeddingDimension, sampleHash,
			); err != nil {
				return err
			}
		}
		return nil
	})
}

func parseEmbeddingVector(value string, dimension int) ([]float32, error) {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) < 2 || trimmed[0] != '[' || trimmed[len(trimmed)-1] != ']' {
		return nil, ErrInvalidRecallAudit
	}
	parts := strings.Split(trimmed[1:len(trimmed)-1], ",")
	if len(parts) != dimension {
		return nil, ErrInvalidRecallAudit
	}
	result := make([]float32, len(parts))
	for index, part := range parts {
		parsed, err := strconv.ParseFloat(strings.TrimSpace(part), 32)
		if err != nil {
			return nil, fmt.Errorf("parse query embedding: %w", err)
		}
		result[index] = float32(parsed)
	}
	return result, nil
}

var _ RecallAuditStore = (*PostgresRecallAuditStore)(nil)
