package reportasset

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/platform/database"
	"intelligent-report-generation-system/internal/report/operation"
)

type PostgresIntentRepository struct{ pool *pgxpool.Pool }

func NewPostgresIntentRepository(pool *pgxpool.Pool) *PostgresIntentRepository {
	return &PostgresIntentRepository{pool: pool}
}

func (repository *PostgresIntentRepository) CreatePreview(
	ctx context.Context, identity IntentIdentity, request BuildIntentRequest,
	bundle operation.Bundle, previewHash askdata.ContentHash, now time.Time,
) (Intent, error) {
	if repository == nil || repository.pool == nil {
		return Intent{}, ErrInvalidIntent
	}
	ctx = database.WithAccessContext(ctx, string(identity.ActorID), string(identity.DomainID))
	raw, err := json.Marshal(bundle)
	if err != nil {
		return Intent{}, err
	}
	intentID := askdata.ID(uuid.NewSHA1(uuid.NameSpaceOID, []byte(
		"add-to-report\x00"+string(identity.TenantID)+"\x00"+string(identity.ActorID)+"\x00"+string(request.IdempotencyKeyHash),
	)).String())
	var result Intent
	err = database.WithTenantTx(ctx, repository.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		var state string
		var version int64
		if err := tx.QueryRow(ctx, `SELECT current_state,record_version FROM askdata.question_runs
			WHERE id=$1 AND tenant_id=$2 AND domain_id=$3 AND actor_id=$4`,
			request.QuestionRunID, identity.TenantID, identity.DomainID, identity.ActorID,
		).Scan(&state, &version); err != nil {
			return err
		}
		if state != "ANSWERED" || version != request.RunVersion {
			return ErrQuestionNotExportable
		}
		row := tx.QueryRow(ctx, `INSERT INTO askdata.add_to_report_intents(
			id,tenant_id,question_run_id,actor_user_id,idempotency_key,target_report_id,
			target_page_id,target_section_id,operation_bundle_json,preview_hash,state,created_at,expires_at,updated_at
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'PENDING_CONFIRMATION',$11,$12,$11)
		ON CONFLICT(tenant_id,actor_user_id,idempotency_key) DO NOTHING
		RETURNING id::text,tenant_id::text,actor_user_id::text,question_run_id::text,target_report_id::text,
		 operation_bundle_json,preview_hash,state,applied_revision_no,COALESCE(rejection_code,''),
		 COALESCE(rejection_detail,''),created_at,expires_at`,
			intentID, identity.TenantID, request.QuestionRunID, identity.ActorID,
			request.IdempotencyKeyHash, request.ReportID, request.TargetPageID, request.TargetSectionID,
			raw, previewHash, now, now.Add(IntentRetention))
		if err := scanIntent(row, &result); err == nil {
			return nil
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		row = tx.QueryRow(ctx, `SELECT id::text,tenant_id::text,actor_user_id::text,question_run_id::text,target_report_id::text,
			operation_bundle_json,preview_hash,state,applied_revision_no,COALESCE(rejection_code,''),
			COALESCE(rejection_detail,''),created_at,expires_at
			FROM askdata.add_to_report_intents WHERE actor_user_id=$1 AND idempotency_key=$2`,
			identity.ActorID, request.IdempotencyKeyHash)
		if err := scanIntent(row, &result); err != nil {
			return err
		}
		if result.QuestionRunID != request.QuestionRunID || result.ReportID != request.ReportID || result.PreviewHash != previewHash {
			return ErrIntentConflict
		}
		result.Replayed = true
		return nil
	})
	return result, err
}

func (repository *PostgresIntentRepository) Confirm(
	ctx context.Context, identity IntentIdentity, intentID askdata.ID,
	previewHash askdata.ContentHash, now time.Time,
) (Intent, error) {
	if repository == nil || repository.pool == nil || validateIntentIdentity(identity) != nil ||
		intentID.Validate() != nil || previewHash.Validate() != nil {
		return Intent{}, ErrInvalidIntent
	}
	ctx = database.WithAccessContext(ctx, string(identity.ActorID), string(identity.DomainID))
	var result Intent
	err := database.WithTenantTx(ctx, repository.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE askdata.add_to_report_intents SET
			state=CASE WHEN expires_at<=$4 THEN 'EXPIRED' ELSE 'PENDING' END,
			confirmed_at=CASE WHEN expires_at>$4 THEN $4 ELSE confirmed_at END,updated_at=$4
			WHERE id=$1 AND actor_user_id=$2 AND preview_hash=$3 AND state='PENDING_CONFIRMATION'`,
			intentID, identity.ActorID, previewHash, now)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			var state string
			var storedHash askdata.ContentHash
			if err := tx.QueryRow(ctx, `SELECT state,preview_hash FROM askdata.add_to_report_intents
				WHERE id=$1 AND actor_user_id=$2`, intentID, identity.ActorID).Scan(&state, &storedHash); err != nil {
				return err
			}
			if storedHash != previewHash {
				return ErrIntentConflict
			}
			if state == string(IntentExpired) {
				return ErrIntentExpired
			}
			if state != string(IntentPending) && state != string(IntentApplied) && state != string(IntentRejected) {
				return ErrIntentState
			}
			result.Replayed = true
		}
		var enqueued bool
		if err := tx.QueryRow(ctx, `SELECT askdata.enqueue_add_to_report_intent($1)`, intentID).Scan(&enqueued); err != nil {
			return err
		}
		if !enqueued {
			return ErrIntentState
		}
		row := tx.QueryRow(ctx, `SELECT id::text,tenant_id::text,actor_user_id::text,question_run_id::text,target_report_id::text,
			operation_bundle_json,preview_hash,state,applied_revision_no,COALESCE(rejection_code,''),
			COALESCE(rejection_detail,''),created_at,expires_at
			FROM askdata.add_to_report_intents WHERE id=$1 AND actor_user_id=$2`, intentID, identity.ActorID)
		return scanIntent(row, &result)
	})
	return result, err
}

func (repository *PostgresIntentRepository) Get(
	ctx context.Context, identity IntentIdentity, intentID askdata.ID,
) (Intent, error) {
	if repository == nil || repository.pool == nil || validateIntentIdentity(identity) != nil || intentID.Validate() != nil {
		return Intent{}, ErrInvalidIntent
	}
	ctx = database.WithAccessContext(ctx, string(identity.ActorID), string(identity.DomainID))
	var result Intent
	err := database.WithTenantTx(ctx, repository.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE askdata.add_to_report_intents SET state='EXPIRED'
			WHERE id=$1 AND actor_user_id=$2 AND state='PENDING_CONFIRMATION' AND expires_at<=now()`,
			intentID, identity.ActorID); err != nil {
			return err
		}
		row := tx.QueryRow(ctx, `SELECT id::text,tenant_id::text,actor_user_id::text,question_run_id::text,target_report_id::text,
			operation_bundle_json,preview_hash,state,applied_revision_no,COALESCE(rejection_code,''),
			COALESCE(rejection_detail,''),created_at,expires_at
			FROM askdata.add_to_report_intents WHERE id=$1 AND actor_user_id=$2`, intentID, identity.ActorID)
		return scanIntent(row, &result)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Intent{}, ErrInvalidIntent
	}
	return result, err
}

type IntentDeliveryClaim struct {
	OutboxID, IntentID, TenantID, ActorID, DomainID, ReportID askdata.ID
	IdempotencyKeyHash                                        askdata.ContentHash
	LeaseToken                                                askdata.ID
	Bundle                                                    operation.Bundle
	Attempt                                                   int
}

// IntentDeliveryStore is consumed by the dedicated add-to-report worker.
type IntentDeliveryStore interface {
	ListIntentTenantIDs(context.Context) ([]string, error)
	ClaimIntent(context.Context, string, time.Duration) (*IntentDeliveryClaim, error)
	CompleteIntent(context.Context, IntentDeliveryClaim, int64) error
	RejectIntent(context.Context, IntentDeliveryClaim, string, string) error
	RetryIntent(context.Context, IntentDeliveryClaim, string) error
}

func (repository *PostgresIntentRepository) ListIntentTenantIDs(ctx context.Context) ([]string, error) {
	if repository == nil || repository.pool == nil {
		return nil, ErrInvalidIntent
	}
	rows, err := repository.pool.Query(ctx, `SELECT tenant_id::text
		FROM askdata.list_add_to_report_tenants()`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, id)
	}
	return result, rows.Err()
}

func (repository *PostgresIntentRepository) ClaimIntent(ctx context.Context, tenantID string, lease time.Duration) (*IntentDeliveryClaim, error) {
	if uuid.Validate(tenantID) != nil || lease < 10*time.Second || lease > 10*time.Minute {
		return nil, ErrInvalidIntent
	}
	var claim *IntentDeliveryClaim
	err := database.WithTenantTx(ctx, repository.pool, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE askdata.add_to_report_outbox AS outbox SET
			state='DONE',lease_token=NULL,lease_expires_at=NULL,updated_at=now()
			FROM askdata.add_to_report_intents AS intent
			WHERE intent.id=outbox.intent_id AND intent.tenant_id=outbox.tenant_id
			  AND intent.state='PENDING' AND intent.expires_at<=now()
			  AND outbox.state IN ('PENDING','RUNNING','FAILED')`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE askdata.add_to_report_intents SET state='EXPIRED'
			WHERE state IN ('PENDING_CONFIRMATION','PENDING') AND expires_at<=now()`); err != nil {
			return err
		}
		row := tx.QueryRow(ctx, `WITH picked AS (
			SELECT outbox.id FROM askdata.add_to_report_outbox AS outbox
			JOIN askdata.add_to_report_intents AS intent ON intent.id=outbox.intent_id
			WHERE intent.state='PENDING' AND intent.expires_at>now() AND outbox.attempt<10
			  AND ((outbox.state IN ('PENDING','FAILED') AND outbox.next_attempt_at<=now())
			       OR (outbox.state='RUNNING' AND outbox.lease_expires_at<=now()))
			ORDER BY outbox.next_attempt_at,outbox.id FOR UPDATE OF outbox SKIP LOCKED LIMIT 1
		), claimed AS (
			UPDATE askdata.add_to_report_outbox AS outbox SET state='RUNNING',attempt=attempt+1,
			 lease_token=gen_random_uuid(),lease_expires_at=now()+($1*interval '1 second'),updated_at=now()
			FROM picked WHERE outbox.id=picked.id
			RETURNING outbox.id,outbox.intent_id,outbox.attempt,outbox.lease_token
		) SELECT claimed.id::text,intent.id::text,intent.tenant_id::text,intent.actor_user_id::text,
			run.domain_id::text,intent.target_report_id::text,intent.idempotency_key,
			claimed.lease_token::text,claimed.attempt,intent.operation_bundle_json
		  FROM claimed JOIN askdata.add_to_report_intents AS intent ON intent.id=claimed.intent_id
		  JOIN askdata.question_runs AS run ON run.id=intent.question_run_id`, int64(lease/time.Second))
		var value IntentDeliveryClaim
		var key string
		var raw []byte
		if err := row.Scan(&value.OutboxID, &value.IntentID, &value.TenantID, &value.ActorID, &value.DomainID,
			&value.ReportID, &key, &value.LeaseToken, &value.Attempt, &raw); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		value.IdempotencyKeyHash = askdata.ContentHash(key)
		bundle, err := operation.Decode(raw, nil)
		if err != nil {
			return err
		}
		value.Bundle = bundle
		claim = &value
		return nil
	})
	return claim, err
}

func (repository *PostgresIntentRepository) CompleteIntent(ctx context.Context, claim IntentDeliveryClaim, revision int64) error {
	if revision < 1 {
		return ErrInvalidIntent
	}
	return repository.finishIntent(ctx, claim, IntentApplied, revision, "", "")
}

func (repository *PostgresIntentRepository) RejectIntent(ctx context.Context, claim IntentDeliveryClaim, code, detail string) error {
	code = strings.TrimSpace(code)
	if code == "" || len(code) > 128 || len(detail) > 4096 {
		return ErrInvalidIntent
	}
	return repository.finishIntent(ctx, claim, IntentRejected, 0, code, detail)
}

func (repository *PostgresIntentRepository) finishIntent(ctx context.Context, claim IntentDeliveryClaim, state IntentState, revision int64, code, detail string) error {
	return database.WithTenantTx(ctx, repository.pool, string(claim.TenantID), func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE askdata.add_to_report_outbox SET state='DONE',lease_token=NULL,
			lease_expires_at=NULL,updated_at=now() WHERE id=$1 AND intent_id=$2 AND state='RUNNING' AND lease_token=$3`,
			claim.OutboxID, claim.IntentID, claim.LeaseToken)
		if err != nil || tag.RowsAffected() != 1 {
			return errors.Join(err, ErrIntentState)
		}
		tag, err = tx.Exec(ctx, `UPDATE askdata.add_to_report_intents SET state=$1,
			applied_revision_no=NULLIF($2,0),rejection_code=NULLIF($3,''),rejection_detail=NULLIF($4,''),updated_at=now()
			WHERE id=$5 AND state='PENDING'`, state, revision, code, detail, claim.IntentID)
		if err != nil || tag.RowsAffected() != 1 {
			return errors.Join(err, ErrIntentState)
		}
		return nil
	})
}

func (repository *PostgresIntentRepository) RetryIntent(ctx context.Context, claim IntentDeliveryClaim, code string) error {
	return database.WithTenantTx(ctx, repository.pool, string(claim.TenantID), func(tx pgx.Tx) error {
		state := "FAILED"
		if claim.Attempt >= 10 {
			state = "DONE"
		}
		tag, err := tx.Exec(ctx, `UPDATE askdata.add_to_report_outbox SET state=$1,lease_token=NULL,
			lease_expires_at=NULL,next_attempt_at=now()+(LEAST(300,power(2,attempt)::integer)*interval '1 second'),updated_at=now()
			WHERE id=$2 AND state='RUNNING' AND lease_token=$3`, state, claim.OutboxID, claim.LeaseToken)
		if err != nil || tag.RowsAffected() != 1 {
			return errors.Join(err, ErrIntentState)
		}
		if state == "DONE" {
			_, err = tx.Exec(ctx, `UPDATE askdata.add_to_report_intents SET state='REJECTED',
				rejection_code='REPORT_DELIVERY_RETRY_EXHAUSTED',rejection_detail=$1,updated_at=now()
				WHERE id=$2 AND state='PENDING'`, code, claim.IntentID)
		}
		return err
	})
}

func scanIntent(row pgx.Row, target *Intent) error {
	var raw []byte
	if err := row.Scan(&target.ID, &target.TenantID, &target.ActorID, &target.QuestionRunID, &target.ReportID,
		&raw, &target.PreviewHash, &target.State, &target.AppliedRevisionNo, &target.RejectionCode,
		&target.RejectionDetail, &target.CreatedAt, &target.ExpiresAt); err != nil {
		return err
	}
	bundle, err := operation.Decode(raw, nil)
	if err != nil {
		return err
	}
	target.Bundle = bundle
	return nil
}

var _ IntentRepository = (*PostgresIntentRepository)(nil)
var _ IntentDeliveryStore = (*PostgresIntentRepository)(nil)
