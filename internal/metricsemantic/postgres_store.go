package metricsemantic

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/platform/database"
)

type PostgresStore struct{ pool *pgxpool.Pool }

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

func (s *PostgresStore) ListPendingTenantIDs(ctx context.Context) ([]string, error) {
	// The document table is FORCE RLS, so the global scheduler cannot discover tenant IDs by
	// joining it without a tenant setting. Enumerate active tenants, then claim inside RLS tx.
	rows, err := s.pool.Query(ctx, `SELECT id::text FROM platform.tenants
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

func (s *PostgresStore) Claim(ctx context.Context, tenantID, workerID string, lease time.Duration) (claim *EmbeddingClaim, err error) {
	if tenantID == "" || workerID == "" || lease < time.Second {
		return nil, ErrInvalidRequest
	}
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE platform.metric_semantic_documents SET
			embedding_status='FAILED',embedding_error_code='LEASE_EXPIRED',updated_at=now(),
			lease_owner='',lease_expires_at=NULL
			WHERE embedding_status='RUNNING' AND lease_expires_at<=now() AND embedding_attempt>=3`); err != nil {
			return err
		}
		item := EmbeddingClaim{TenantID: tenantID}
		err := tx.QueryRow(ctx, `WITH candidate AS (
			SELECT id FROM platform.metric_semantic_documents
			WHERE embedding_attempt<3 AND (
			  (embedding_status IN ('PENDING','FAILED') AND next_attempt_at<=now())
			  OR (embedding_status='RUNNING' AND lease_expires_at<=now())
			) ORDER BY created_at,id FOR UPDATE SKIP LOCKED LIMIT 1
		) UPDATE platform.metric_semantic_documents AS document SET
			embedding_status='RUNNING',embedding=NULL,embedding_model='',embedding_input_hash='',
			embedding_error_code='',embedded_at=NULL,embedding_attempt=embedding_attempt+1,
			lease_owner=$1,lease_expires_at=now()+($2*interval '1 second'),updated_at=now()
		FROM candidate WHERE document.id=candidate.id
		RETURNING document.id::text,document.document`, workerID, int64(lease/time.Second)).Scan(&item.ID, &item.Document)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		claim = &item
		return nil
	})
	return claim, err
}

func (s *PostgresStore) Complete(ctx context.Context, claim EmbeddingClaim, workerID, model string, vector []float32) error {
	if claim.ID == "" || claim.TenantID == "" || workerID == "" || strings.TrimSpace(model) == "" || len(vector) == 0 {
		return ErrInvalidRequest
	}
	vectorLiteral := formatVector(vector)
	inputHash := documentHash(claim.Document)
	return database.WithTenantTx(ctx, s.pool, claim.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE platform.metric_semantic_documents SET
			embedding=$1::halfvec,embedding_model=$2,embedding_input_hash=$3,
			embedding_status='SUCCEEDED',embedding_error_code='',embedded_at=now(),updated_at=now(),
			lease_owner='',lease_expires_at=NULL
			WHERE id=$4 AND embedding_status='RUNNING' AND lease_owner=$5 AND lease_expires_at>now()`,
			vectorLiteral, model, inputHash, claim.ID, workerID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("metric embedding lease was lost")
		}
		return nil
	})
}

func (s *PostgresStore) Fail(ctx context.Context, claim EmbeddingClaim, workerID, code string) error {
	if claim.ID == "" || claim.TenantID == "" || workerID == "" || strings.TrimSpace(code) == "" {
		return ErrInvalidRequest
	}
	return database.WithTenantTx(ctx, s.pool, claim.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE platform.metric_semantic_documents SET
			embedding_status='FAILED',embedding_error_code=$1,
			next_attempt_at=CASE WHEN embedding_attempt=1 THEN now()+interval '30 seconds'
			  WHEN embedding_attempt=2 THEN now()+interval '2 minutes' ELSE next_attempt_at END,
			updated_at=now(),lease_owner='',lease_expires_at=NULL
			WHERE id=$2 AND embedding_status='RUNNING' AND lease_owner=$3`, code, claim.ID, workerID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("metric embedding failure update lost its lease")
		}
		return nil
	})
}

func (s *PostgresStore) Search(ctx context.Context, tenantID, query string, vector []float32, limit int) (items []SearchResult, err error) {
	items = []SearchResult{}
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var rows pgx.Rows
		var queryErr error
		if len(vector) > 0 {
			rows, queryErr = tx.Query(ctx, semanticVectorSearchSQL, query, formatVector(vector), limit)
		} else {
			rows, queryErr = tx.Query(ctx, semanticLexicalSearchSQL, query, limit)
		}
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var item SearchResult
			var lineage json.RawMessage
			if err := rows.Scan(
				&item.SubjectType, &item.MetricID, &item.MetricVersionID,
				&item.DatasetID, &item.DatasetVersionID, &item.Name, &item.Description, &item.Caliber,
				&item.Dimensions, &item.Period, &item.PeriodDescription, &lineage,
				&item.LineageSummary, &item.Tags, &item.SemanticScore, &item.KeywordScore,
				&item.Score, &item.BindingAllowed, &item.EmbeddingReady,
			); err != nil {
				return err
			}
			item.Lineage = append(json.RawMessage(nil), lineage...)
			if item.Dimensions == nil {
				item.Dimensions = []string{}
			}
			if item.Tags == nil {
				item.Tags = []string{}
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, err
}

const semanticBaseCTE = `WITH eligible AS (
	SELECT document.*,
	  CASE
	    WHEN lower(document.name)=lower($1) THEN 1.0
	    WHEN EXISTS(SELECT 1 FROM unnest(document.tags) AS tag WHERE lower(tag)=lower($1)) THEN 0.85
	    WHEN strpos(lower(document.name),lower($1))>0 THEN 0.7
	    WHEN strpos(lower(document.document),lower($1))>0 THEN 0.45
	    ELSE 0.0
	  END AS keyword_score
	FROM platform.metric_semantic_documents AS document
	JOIN platform.metric_versions AS version
	  ON version.tenant_id=document.tenant_id AND version.id=document.metric_version_id
	JOIN platform.metrics AS metric
	  ON metric.tenant_id=document.tenant_id AND metric.id=document.metric_id
	WHERE document.tenant_id=platform.current_tenant_id()
	  AND document.subject_type='METRIC_VERSION' AND version.status='PUBLISHED'
	  AND metric.status='PUBLISHED' AND metric.current_published_version_id=version.id
	  AND metric.deleted_at IS NULL
) `

const semanticSelectColumns = `subject_type,COALESCE(metric_id::text,''),
	COALESCE(metric_version_id::text,''),dataset_id::text,dataset_version_id::text,
	name,description,caliber,dimensions,period,period_description,lineage,lineage_summary,tags`

const semanticLexicalSearchSQL = semanticBaseCTE + `
SELECT ` + semanticSelectColumns + `,0::float8 AS semantic_score,keyword_score::float8,
	keyword_score::float8 AS score,true AS binding_allowed,
	(embedding_status='SUCCEEDED') AS embedding_ready
FROM eligible
WHERE keyword_score>0
ORDER BY score DESC,name,id
LIMIT $2`

const semanticVectorSearchSQL = `WITH eligible AS (
	SELECT document.*,
	  CASE
	    WHEN lower(document.name)=lower($1) THEN 1.0
	    WHEN EXISTS(SELECT 1 FROM unnest(document.tags) AS tag WHERE lower(tag)=lower($1)) THEN 0.85
	    WHEN strpos(lower(document.name),lower($1))>0 THEN 0.7
	    WHEN strpos(lower(document.document),lower($1))>0 THEN 0.45
	    ELSE 0.0
	  END AS keyword_score,
	  CASE WHEN document.embedding_status='SUCCEEDED'
	    THEN GREATEST(0.0,1.0-(document.embedding <=> $2::halfvec)) ELSE 0.0 END AS semantic_score
	FROM platform.metric_semantic_documents AS document
	JOIN platform.metric_versions AS version
	  ON version.tenant_id=document.tenant_id AND version.id=document.metric_version_id
	JOIN platform.metrics AS metric
	  ON metric.tenant_id=document.tenant_id AND metric.id=document.metric_id
	WHERE document.tenant_id=platform.current_tenant_id()
	  AND document.subject_type='METRIC_VERSION' AND version.status='PUBLISHED'
	  AND metric.status='PUBLISHED' AND metric.current_published_version_id=version.id
	  AND metric.deleted_at IS NULL
)
SELECT ` + semanticSelectColumns + `,semantic_score::float8,keyword_score::float8,
	(semantic_score*0.75+keyword_score*0.25)::float8 AS score,
	true AS binding_allowed,(embedding_status='SUCCEEDED') AS embedding_ready
FROM eligible
WHERE semantic_score>0 OR keyword_score>0
ORDER BY score DESC,name,id
LIMIT $3`

func formatVector(values []float32) string {
	var builder strings.Builder
	builder.WriteByte('[')
	for index, value := range values {
		if index > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(strconv.FormatFloat(float64(value), 'g', -1, 32))
	}
	builder.WriteByte(']')
	return builder.String()
}

func documentHash(value string) string {
	// Kept local to avoid storing or logging the document in worker errors.
	return fmt.Sprintf("%x", sha256Sum([]byte(value)))
}

func sha256Sum(value []byte) [32]byte {
	return sha256.Sum256(value)
}
