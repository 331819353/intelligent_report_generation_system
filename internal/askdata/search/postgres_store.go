package search

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/platform/database"
)

var embeddingErrorCodePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)

type PostgresEmbeddingStore struct{ pool *pgxpool.Pool }

func NewPostgresEmbeddingStore(pool *pgxpool.Pool) *PostgresEmbeddingStore {
	return &PostgresEmbeddingStore{pool: pool}
}

func (store *PostgresEmbeddingStore) ListTenantIDs(ctx context.Context) ([]string, error) {
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

func (store *PostgresEmbeddingStore) ClaimBatch(
	ctx context.Context, tenantID, workerID, model string, lease time.Duration, limit int,
) (claims []EmbeddingClaim, err error) {
	if uuid.Validate(tenantID) != nil || strings.TrimSpace(workerID) == "" || len(workerID) > 128 ||
		strings.TrimSpace(model) == "" || len(model) > 128 || lease < time.Second || lease > 10*time.Minute ||
		limit < 1 || limit > 32 {
		return nil, ErrInvalidEmbeddingWork
	}
	claims = []EmbeddingClaim{}
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		// A configured embedding-model change invalidates only the vector, not the
		// authoritative semantic document. Reopen the idempotent outbox row.
		if _, err := tx.Exec(ctx, `UPDATE askdata.embedding_outbox AS event SET
			status='PENDING',attempt=0,next_attempt_at=now(),error_code='',
			lease_owner='',lease_token=NULL,lease_expires_at=NULL,completed_at=NULL,updated_at=now()
			FROM askdata.search_documents AS document
			WHERE event.search_document_id=document.id AND event.tenant_id=document.tenant_id
			  AND event.input_hash=document.input_hash AND event.status='SUCCEEDED'
			  AND document.embedding_status='SUCCEEDED'
			  AND (document.embedding_model<>$1 OR document.embedding_dim<>2560)`, model); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `WITH expired AS (
			UPDATE askdata.embedding_outbox SET
				status='FAILED',error_code='LEASE_EXPIRED',lease_owner='',lease_token=NULL,
				lease_expires_at=NULL,completed_at=now(),updated_at=now()
			WHERE status='RUNNING' AND lease_expires_at<=now() AND attempt>=max_attempts
			RETURNING search_document_id,input_hash
		) UPDATE askdata.search_documents AS document SET
			embedding_status='FAILED',embedding=NULL,embedding_model='',embedding_version='',embedding_dim=0,
			embedding_error_code='LEASE_EXPIRED',embedded_at=NULL,updated_at=now()
		FROM expired WHERE document.id=expired.search_document_id
		  AND document.input_hash=expired.input_hash`); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `WITH picked AS (
			SELECT event.id,event.domain_id,event.search_document_id,event.input_hash,
			       event.attempt,event.max_attempts,document.document,
			       document.embedding_status,document.embedding_model,document.embedding_dim
			FROM askdata.embedding_outbox AS event
			JOIN askdata.search_documents AS document
			  ON document.tenant_id=event.tenant_id AND document.id=event.search_document_id
			 AND document.input_hash=event.input_hash
			WHERE event.attempt<event.max_attempts
			  AND (
			    (event.status='PENDING' AND event.next_attempt_at<=now())
			    OR (event.status='RUNNING' AND event.lease_expires_at<=now())
			  )
			  AND document.index_policy IN ('VECTOR','HYBRID')
			  AND document.sensitivity IN ('PUBLIC','INTERNAL')
			ORDER BY event.next_attempt_at,event.created_at,event.id
			FOR UPDATE OF event SKIP LOCKED LIMIT $1
		), claimed AS (
			UPDATE askdata.embedding_outbox AS event SET
				status='RUNNING',attempt=event.attempt+1,error_code='',lease_owner=$2,
				lease_token=gen_random_uuid(),lease_expires_at=now()+($3*interval '1 second'),
				completed_at=NULL,updated_at=now()
			FROM picked WHERE event.id=picked.id
			RETURNING event.id,event.domain_id,event.search_document_id,event.input_hash,
			          event.lease_token,event.attempt,event.max_attempts
		) SELECT claimed.id::text,claimed.domain_id::text,claimed.search_document_id::text,
		         claimed.input_hash,picked.document,claimed.lease_token::text,
		         claimed.attempt,claimed.max_attempts,
		         (picked.embedding_status='SUCCEEDED' AND picked.embedding_model=$4
		          AND picked.embedding_dim=2560) AS current
		FROM claimed JOIN picked ON picked.id=claimed.id
		ORDER BY claimed.id`, limit, workerID, int64(lease/time.Second), model)
		if err != nil {
			return err
		}
		for rows.Next() {
			var claim EmbeddingClaim
			claim.TenantID = tenantID
			claim.ExpectedModel = model
			claim.ExpectedDimension = SearchEmbeddingDimension
			if err := rows.Scan(
				&claim.ID, &claim.DomainID, &claim.SearchDocumentID, &claim.InputHash,
				&claim.Text, &claim.LeaseToken, &claim.Attempt, &claim.MaxAttempts, &claim.Current,
			); err != nil {
				rows.Close()
				return err
			}
			claims = append(claims, claim)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, claim := range claims {
			if claim.Current {
				continue
			}
			tag, err := tx.Exec(ctx, `UPDATE askdata.search_documents SET
				embedding_status='RUNNING',embedding=NULL,embedding_model='',embedding_version='',embedding_dim=0,
				embedding_error_code='',embedded_at=NULL,updated_at=now()
				WHERE id=$1 AND input_hash=$2
				  AND embedding_status IN ('PENDING','FAILED','RUNNING','SUCCEEDED')`,
				claim.SearchDocumentID, claim.InputHash)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return errors.New("search document changed while claiming embedding work")
			}
		}
		return nil
	})
	return claims, err
}

func (store *PostgresEmbeddingStore) Acknowledge(
	ctx context.Context, claim EmbeddingClaim, workerID string,
) error {
	return store.finishOutbox(ctx, claim, workerID, "SUCCEEDED", "")
}

func (store *PostgresEmbeddingStore) Complete(
	ctx context.Context, claim EmbeddingClaim, workerID, model string, vector []float32,
) error {
	if strings.TrimSpace(model) == "" || len(model) > 128 ||
		claim.ExpectedModel == "" || model != claim.ExpectedModel ||
		claim.ExpectedDimension != SearchEmbeddingDimension || len(vector) != claim.ExpectedDimension {
		return ErrEmbeddingModelMismatch
	}
	if err := validateEmbeddingClaim(claim, workerID); err != nil {
		return ErrInvalidEmbeddingWork
	}
	return database.WithTenantTx(ctx, store.pool, claim.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE askdata.search_documents SET
			embedding=$1::halfvec,embedding_model=$2,embedding_version=$2,embedding_dim=$3,
			embedding_status='SUCCEEDED',embedding_error_code='',embedded_at=now(),updated_at=now()
			WHERE id=$4 AND input_hash=$5 AND embedding_status='RUNNING'`,
			formatEmbeddingVector(vector), model, claim.ExpectedDimension,
			claim.SearchDocumentID, claim.InputHash)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("search document changed before embedding completion")
		}
		return updateEmbeddingOutbox(ctx, tx, claim, workerID, "SUCCEEDED", "", true)
	})
}

func (store *PostgresEmbeddingStore) Fail(
	ctx context.Context, claim EmbeddingClaim, workerID, code string,
) error {
	if err := validateEmbeddingClaim(claim, workerID); err != nil || !embeddingErrorCodePattern.MatchString(code) {
		return ErrInvalidEmbeddingWork
	}
	return database.WithTenantTx(ctx, store.pool, claim.TenantID, func(tx pgx.Tx) error {
		var attempt, maximum int
		if err := tx.QueryRow(ctx, `SELECT attempt,max_attempts FROM askdata.embedding_outbox
			WHERE id=$1 AND input_hash=$2 AND status='RUNNING'
			  AND lease_owner=$3 AND lease_token=$4 FOR UPDATE`,
			claim.ID, claim.InputHash, workerID, claim.LeaseToken).Scan(&attempt, &maximum); err != nil {
			return err
		}
		status := "PENDING"
		terminal := false
		if attempt >= maximum {
			status = "FAILED"
			terminal = true
		}
		if _, err := tx.Exec(ctx, `UPDATE askdata.search_documents SET
			embedding_status=$1,embedding=NULL,embedding_model='',embedding_version='',embedding_dim=0,
			embedding_error_code=$2,embedded_at=NULL,updated_at=now()
			WHERE id=$3 AND input_hash=$4 AND embedding_status='RUNNING'`,
			status, code, claim.SearchDocumentID, claim.InputHash); err != nil {
			return err
		}
		return updateEmbeddingOutbox(ctx, tx, claim, workerID, status, code, terminal)
	})
}

func (store *PostgresEmbeddingStore) finishOutbox(
	ctx context.Context, claim EmbeddingClaim, workerID, status, code string,
) error {
	if err := validateEmbeddingClaim(claim, workerID); err != nil {
		return err
	}
	return database.WithTenantTx(ctx, store.pool, claim.TenantID, func(tx pgx.Tx) error {
		return updateEmbeddingOutbox(ctx, tx, claim, workerID, status, code, true)
	})
}

func updateEmbeddingOutbox(
	ctx context.Context, tx pgx.Tx, claim EmbeddingClaim, workerID, status, code string, completed bool,
) error {
	tag, err := tx.Exec(ctx, `UPDATE askdata.embedding_outbox SET
		status=$1,error_code=$2,next_attempt_at=CASE
		  WHEN $1='PENDING' THEN now()+(LEAST(300,power(2,attempt)::integer)*interval '1 second')
		  ELSE next_attempt_at END,
		lease_owner='',lease_token=NULL,lease_expires_at=NULL,
		completed_at=CASE WHEN $5 THEN now() ELSE NULL END,updated_at=now()
		WHERE id=$3 AND input_hash=$4 AND status='RUNNING'
		  AND lease_owner=$6 AND lease_token=$7`,
		status, code, claim.ID, claim.InputHash, completed, workerID, claim.LeaseToken)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("embedding outbox lease was lost or document changed")
	}
	return nil
}

func validateEmbeddingClaim(claim EmbeddingClaim, workerID string) error {
	if uuid.Validate(claim.ID) != nil || uuid.Validate(claim.TenantID) != nil ||
		uuid.Validate(claim.DomainID) != nil || uuid.Validate(claim.SearchDocumentID) != nil ||
		uuid.Validate(claim.LeaseToken) != nil || len(claim.InputHash) != 64 ||
		strings.TrimSpace(workerID) == "" || len(workerID) > 128 {
		return ErrInvalidEmbeddingWork
	}
	return nil
}

func formatEmbeddingVector(values []float32) string {
	var builder strings.Builder
	builder.Grow(len(values) * 8)
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

var _ EmbeddingStore = (*PostgresEmbeddingStore)(nil)
