package semanticasset

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"intelligent-report-generation-system/internal/platform/database"
)

func (store *PostgresStore) ListTenantIDs(
	ctx context.Context,
) ([]string, error) {
	rows, err := store.pool.Query(ctx, `SELECT id::text
		FROM platform.tenants
		WHERE status='ACTIVE' AND deleted_at IS NULL
		ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var tenantID string
		if scanErr := rows.Scan(&tenantID); scanErr != nil {
			return nil, scanErr
		}
		result = append(result, tenantID)
	}
	return result, rows.Err()
}

func (store *PostgresStore) ClaimEmbeddingBatch(
	ctx context.Context,
	tenantID string,
	workerID string,
	lease time.Duration,
	limit int,
) (claims []EmbeddingClaim, err error) {
	if !validUUID(tenantID) || strings.TrimSpace(workerID) == "" ||
		len(workerID) > 128 || lease < time.Second ||
		limit < 1 || limit > MaxBatchSize {
		return nil, ErrInvalidRequest
	}
	claims = []EmbeddingClaim{}
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		if _, updateErr := tx.Exec(ctx, `UPDATE platform.semantic_term_embedding_outbox
			SET status='FAILED',error_code='LEASE_EXPIRED',
				lease_owner='',lease_expires_at=NULL,
				completed_at=now()
			WHERE status='RUNNING'
			  AND lease_expires_at<=now()
			  AND attempt>=3`); updateErr != nil {
			return updateErr
		}
		rows, queryErr := tx.Query(ctx, `WITH picked AS (
				SELECT id
				FROM platform.semantic_term_embedding_outbox
				WHERE attempt<3
				  AND (
				    (status IN ('PENDING','FAILED') AND next_attempt_at<=now())
				    OR (status='RUNNING' AND lease_expires_at<=now())
				  )
				ORDER BY updated_at,id
				FOR UPDATE SKIP LOCKED
				LIMIT $1
			)
			UPDATE platform.semantic_term_embedding_outbox AS event SET
				status='RUNNING',
				attempt=attempt+1,
				error_code='',
				lease_owner=$2,
				lease_expires_at=now()+($3*interval '1 second'),
				completed_at=NULL
			FROM picked
			WHERE event.id=picked.id
			RETURNING event.id::text,event.semantic_term_asset_id::text,
				event.event_version,event.attempt`,
			limit, workerID, int64(lease/time.Second),
		)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var claim EmbeddingClaim
			claim.TenantID = tenantID
			if scanErr := rows.Scan(
				&claim.ID, &claim.AssetID,
				&claim.EventVersion, &claim.Attempt,
			); scanErr != nil {
				return scanErr
			}
			claims = append(claims, claim)
		}
		return rows.Err()
	})
	return claims, err
}

func (store *PostgresStore) PrepareEmbedding(
	ctx context.Context,
	claim EmbeddingClaim,
	workerID string,
	model string,
) (document EmbeddingDocument, err error) {
	document.EmbeddingClaim = claim
	if !validEmbeddingClaim(claim) || strings.TrimSpace(workerID) == "" ||
		strings.TrimSpace(model) == "" {
		return EmbeddingDocument{}, ErrInvalidRequest
	}
	err = database.WithTenantTx(ctx, store.pool, claim.TenantID, func(tx pgx.Tx) error {
		if verifyErr := verifyEmbeddingLease(
			ctx, tx, claim, workerID,
		); verifyErr != nil {
			return verifyErr
		}
		var status, embeddingStatus, embeddingModel, storedHash string
		queryErr := tx.QueryRow(ctx, `SELECT common_term::text,status,
				embedding_status,embedding_model,embedding_input_hash
			FROM platform.semantic_term_assets
			WHERE id=$1::uuid`,
			claim.AssetID,
		).Scan(
			&document.Text, &status, &embeddingStatus,
			&embeddingModel, &storedHash,
		)
		if errors.Is(queryErr, pgx.ErrNoRows) {
			document.IneligibleCode = "ASSET_NOT_FOUND"
			return nil
		}
		if queryErr != nil {
			return queryErr
		}
		if status != "ACTIVE" {
			document.IneligibleCode = "ASSET_DEPRECATED"
			return nil
		}
		if !validText(document.Text, 1, 256) {
			document.IneligibleCode = "COMMON_TERM_INVALID"
			return nil
		}
		document.InputHash = semanticTermInputHash(document.Text)
		document.Eligible = true
		if embeddingStatus == "SUCCEEDED" &&
			embeddingModel == model && storedHash == document.InputHash {
			document.Current = true
			return nil
		}
		tag, updateErr := tx.Exec(ctx, `UPDATE platform.semantic_term_assets
			SET embedding=NULL,
				embedding_model='',
				embedding_input_hash=$1,
				embedding_status='PENDING',
				embedding_error_code='',
				embedded_at=NULL
			WHERE id=$2::uuid`,
			document.InputHash, claim.AssetID,
		)
		if updateErr != nil {
			return updateErr
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		return nil
	})
	return document, err
}

func (store *PostgresStore) AcknowledgeEmbedding(
	ctx context.Context,
	document EmbeddingDocument,
	workerID string,
) error {
	return store.finishEmbeddingEvent(
		ctx, document, workerID, "SUCCEEDED", "",
	)
}

func (store *PostgresStore) CompleteEmbedding(
	ctx context.Context,
	document EmbeddingDocument,
	workerID string,
	model string,
	vector []float32,
) error {
	if !document.Eligible || document.Current ||
		document.InputHash == "" || strings.TrimSpace(model) == "" ||
		!validVectors([][]float32{vector}, 1) {
		return ErrInvalidRequest
	}
	return database.WithTenantTx(
		ctx, store.pool, document.TenantID, func(tx pgx.Tx) error {
			if verifyErr := verifyEmbeddingLease(
				ctx, tx, document.EmbeddingClaim, workerID,
			); verifyErr != nil {
				return verifyErr
			}
			tag, updateErr := tx.Exec(ctx, `UPDATE platform.semantic_term_assets
				SET embedding=$1::halfvec,
					embedding_model=$2,
					embedding_status='SUCCEEDED',
					embedding_error_code='',
					embedded_at=now()
				WHERE id=$3::uuid
				  AND status='ACTIVE'
				  AND embedding_input_hash=$4
				  AND embedding_status='PENDING'`,
				formatVector(vector), model, document.AssetID,
				document.InputHash,
			)
			if updateErr != nil {
				return updateErr
			}
			if tag.RowsAffected() != 1 {
				return ErrConflict
			}
			return updateEmbeddingEvent(
				ctx, tx, document.EmbeddingClaim, workerID,
				"SUCCEEDED", "",
			)
		},
	)
}

func (store *PostgresStore) SkipEmbedding(
	ctx context.Context,
	document EmbeddingDocument,
	workerID string,
) error {
	if document.IneligibleCode == "" {
		return ErrInvalidRequest
	}
	return database.WithTenantTx(
		ctx, store.pool, document.TenantID, func(tx pgx.Tx) error {
			if verifyErr := verifyEmbeddingLease(
				ctx, tx, document.EmbeddingClaim, workerID,
			); verifyErr != nil {
				return verifyErr
			}
			if document.AssetID != "" {
				if _, updateErr := tx.Exec(ctx, `UPDATE platform.semantic_term_assets
					SET embedding=NULL,
						embedding_model='',
						embedding_input_hash='',
						embedding_status='SKIPPED',
						embedding_error_code=$1,
						embedded_at=NULL
					WHERE id=$2::uuid`,
					document.IneligibleCode, document.AssetID,
				); updateErr != nil {
					return updateErr
				}
			}
			return updateEmbeddingEvent(
				ctx, tx, document.EmbeddingClaim, workerID,
				"SKIPPED", "",
			)
		},
	)
}

func (store *PostgresStore) FailEmbedding(
	ctx context.Context,
	document EmbeddingDocument,
	workerID string,
	code string,
) error {
	if strings.TrimSpace(code) == "" {
		return ErrInvalidRequest
	}
	return database.WithTenantTx(
		ctx, store.pool, document.TenantID, func(tx pgx.Tx) error {
			if verifyErr := verifyEmbeddingLease(
				ctx, tx, document.EmbeddingClaim, workerID,
			); verifyErr != nil {
				return verifyErr
			}
			if document.AssetID != "" {
				if _, updateErr := tx.Exec(ctx, `UPDATE platform.semantic_term_assets
					SET embedding=NULL,
						embedding_model='',
						embedding_status='FAILED',
						embedding_error_code=$1,
						embedded_at=NULL
					WHERE id=$2::uuid`,
					code, document.AssetID,
				); updateErr != nil {
					return updateErr
				}
			}
			status := "PENDING"
			if document.Attempt >= 3 {
				status = "FAILED"
			}
			return updateEmbeddingEvent(
				ctx, tx, document.EmbeddingClaim, workerID, status, code,
			)
		},
	)
}

func (store *PostgresStore) finishEmbeddingEvent(
	ctx context.Context,
	document EmbeddingDocument,
	workerID string,
	status string,
	code string,
) error {
	return database.WithTenantTx(
		ctx, store.pool, document.TenantID, func(tx pgx.Tx) error {
			return updateEmbeddingEvent(
				ctx, tx, document.EmbeddingClaim, workerID, status, code,
			)
		},
	)
}

func verifyEmbeddingLease(
	ctx context.Context,
	tx pgx.Tx,
	claim EmbeddingClaim,
	workerID string,
) error {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1
		FROM platform.semantic_term_embedding_outbox
		WHERE id=$1::uuid
		  AND status='RUNNING'
		  AND lease_owner=$2
		  AND event_version=$3
	)`, claim.ID, workerID, claim.EventVersion).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrConflict
	}
	return nil
}

func updateEmbeddingEvent(
	ctx context.Context,
	tx pgx.Tx,
	claim EmbeddingClaim,
	workerID string,
	status string,
	code string,
) error {
	completed := status == "SUCCEEDED" || status == "FAILED" ||
		status == "SKIPPED"
	tag, err := tx.Exec(ctx, `UPDATE platform.semantic_term_embedding_outbox
		SET status=$1,
			error_code=$2,
			next_attempt_at=CASE
			  WHEN $1='PENDING' AND attempt=1 THEN now()+interval '30 seconds'
			  WHEN $1='PENDING' AND attempt=2 THEN now()+interval '2 minutes'
			  ELSE next_attempt_at
			END,
			lease_owner='',
			lease_expires_at=NULL,
			completed_at=CASE WHEN $3 THEN now() ELSE NULL END
		WHERE id=$4::uuid
		  AND status='RUNNING'
		  AND lease_owner=$5
		  AND event_version=$6`,
		status, code, completed, claim.ID, workerID, claim.EventVersion,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

func validEmbeddingClaim(claim EmbeddingClaim) bool {
	return validUUID(claim.ID) && validUUID(claim.TenantID) &&
		validUUID(claim.AssetID) && claim.EventVersion > 0 &&
		claim.Attempt > 0 && claim.Attempt <= 3
}
