package metriccandidate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/platform/database"
)

const maxPublicationPreparationAttempts = 3

// PublicationPreparationClaim binds one durable job to the exact immutable draft revision
// frozen by its publication request. Version.ID is the reserved published-version identity.
type PublicationPreparationClaim struct {
	JobID                string
	PublicationRequestID string
	TenantID             string
	ActorID              string
	Version              dataset.VersionRecord
}

func (s *PostgresStore) ClaimPublicationPreparation(
	ctx context.Context,
	tenantID, workerID string,
	lease time.Duration,
) (claim *PublicationPreparationClaim, err error) {
	if s == nil || tenantID == "" || workerID == "" || lease < time.Second {
		return nil, ErrInvalidRequest
	}
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		// A rejected/approved request no longer needs pre-approval work.
		if _, err := tx.Exec(ctx, `UPDATE platform.metric_candidate_preparation_jobs AS job SET
			status='CANCELLED',lease_owner='',lease_expires_at=NULL,
			completed_at=now(),updated_at=now()
			FROM platform.dataset_publication_requests AS request
			WHERE request.id=job.publication_request_id
			  AND job.status IN ('PENDING','RUNNING')
			  AND request.status<>'PENDING'`); err != nil {
			return err
		}
		// A worker that lost its third lease has exhausted the bounded retry budget.
		if _, err := tx.Exec(ctx, `WITH expired AS (
			UPDATE platform.metric_candidate_preparation_jobs SET
				status='FAILED',lease_owner='',lease_expires_at=NULL,
				error_code='LEASE_EXPIRED',
				error_message='worker lease expired after maximum attempts',
				completed_at=now(),updated_at=now()
			WHERE status='RUNNING' AND attempt>=$1 AND lease_expires_at<=now()
			RETURNING publication_request_id
		) UPDATE platform.dataset_publication_requests AS request SET
			metric_candidate_generation_status='FAILED',
			metric_candidate_result=NULL,
			metric_candidate_error_code='METRIC_CANDIDATE_GENERATION_FAILED',
			metric_candidate_generated_at=NULL,updated_at=now()
		FROM expired
		WHERE request.id=expired.publication_request_id
		  AND request.status='PENDING'
		  AND request.metric_candidate_generation_status IN ('PENDING','FAILED')`,
			maxPublicationPreparationAttempts); err != nil {
			return err
		}

		var item PublicationPreparationClaim
		err := tx.QueryRow(ctx, `WITH candidate AS (
			SELECT job.id
			FROM platform.metric_candidate_preparation_jobs AS job
			JOIN platform.dataset_publication_requests AS request
			  ON request.id=job.publication_request_id
			WHERE job.attempt<$3
			  AND request.status='PENDING'
			  AND request.metric_candidate_generation_status IN ('PENDING','FAILED')
			  AND request.metric_candidate_result IS NULL
			  AND (
			    (job.status='PENDING' AND job.next_attempt_at<=now())
			    OR (job.status='RUNNING' AND job.lease_expires_at<=now())
			  )
			ORDER BY job.created_at,job.id
			FOR UPDATE OF job SKIP LOCKED LIMIT 1
		) UPDATE platform.metric_candidate_preparation_jobs AS job SET
			status='RUNNING',attempt=attempt+1,lease_owner=$1,
			lease_expires_at=now()+($2 * interval '1 second'),
			error_code='',error_message='',
			started_at=COALESCE(started_at,now()),completed_at=NULL,updated_at=now()
		FROM candidate WHERE job.id=candidate.id
		RETURNING job.id::text,job.publication_request_id::text`,
			workerID, int64(lease/time.Second), maxPublicationPreparationAttempts).
			Scan(&item.JobID, &item.PublicationRequestID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}

		var revisionVersionNo int64
		var dsl, logicalPlan json.RawMessage
		err = tx.QueryRow(ctx, `SELECT
			request.requester_user_id::text,
			request.reserved_published_version_id::text,
			request.dataset_id::text,
			request.expected_dataset_version,
			request.draft_version_id::text,
			request.expected_draft_record_version,
			revision.version_no,revision.dsl_version,revision.schema_hash,
			revision.plan_hash,revision.dsl_json,revision.logical_plan_json
		FROM platform.dataset_publication_requests AS request
		JOIN platform.dataset_draft_revisions AS revision
		  ON revision.tenant_id=request.tenant_id
		 AND revision.dataset_id=request.dataset_id
		 AND revision.draft_version_id=request.draft_version_id
		 AND revision.draft_record_version=request.expected_draft_record_version
		 AND revision.schema_hash=request.expected_dsl_hash
		 AND revision.plan_hash=request.expected_plan_hash
		WHERE request.id::text=$1 AND request.status='PENDING'`,
			item.PublicationRequestID).Scan(
			&item.ActorID, &item.Version.ID, &item.Version.DatasetID,
			&item.Version.DatasetRecordVersion, &item.Version.DraftVersionID,
			&item.Version.DraftRecordVersion, &revisionVersionNo,
			&item.Version.DSLVersion, &item.Version.DSLHash, &item.Version.PlanHash,
			&dsl, &logicalPlan,
		)
		if err != nil {
			return fmt.Errorf("load frozen publication draft revision: %w", err)
		}
		item.TenantID = tenantID
		item.Version.VersionNo = int(revisionVersionNo)
		item.Version.Status = "PUBLISHED"
		item.Version.DSL = append(json.RawMessage(nil), dsl...)
		item.Version.LogicalPlan = append(json.RawMessage(nil), logicalPlan...)
		claim = &item
		return nil
	})
	return claim, err
}

func (s *PostgresStore) FinishPublicationPreparation(
	ctx context.Context,
	claim PublicationPreparationClaim,
	workerID string,
	preparation dataset.PublicationCandidatePreparation,
) error {
	if claim.JobID == "" || claim.PublicationRequestID == "" || claim.TenantID == "" || workerID == "" ||
		(preparation.Status != dataset.PublicationCandidateSucceeded &&
			preparation.Status != dataset.PublicationCandidatePartial) ||
		len(preparation.Result) == 0 || !json.Valid(preparation.Result) ||
		preparation.Total < 0 || preparation.Ready < 0 || preparation.Review < 0 || preparation.Blocked < 0 ||
		preparation.Ready+preparation.Review+preparation.Blocked > preparation.Total {
		return ErrInvalidRequest
	}
	return database.WithTenantTx(ctx, s.pool, claim.TenantID, func(tx pgx.Tx) error {
		var owned bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1 FROM platform.metric_candidate_preparation_jobs
			WHERE id::text=$1 AND status='RUNNING' AND lease_owner=$2
			  AND lease_expires_at>now()
		)`, claim.JobID, workerID).Scan(&owned); err != nil {
			return err
		}
		if !owned {
			return errors.New("publication candidate preparation lease was lost")
		}
		tag, err := tx.Exec(ctx, `UPDATE platform.dataset_publication_requests SET
			metric_candidate_generation_status=$1,
			metric_candidate_result=$2,
			metric_candidate_total=$3,
			metric_candidate_ready_count=$4,
			metric_candidate_review_count=$5,
			metric_candidate_blocked_count=$6,
			metric_candidate_warning=$7,
			metric_candidate_error_code='',
			metric_candidate_generated_at=now(),updated_at=now()
			WHERE id::text=$8 AND status='PENDING'
			  AND reserved_published_version_id::text=$9
			  AND expected_dsl_hash=$10
			  AND metric_candidate_generation_status IN ('PENDING','FAILED')`,
			preparation.Status, preparation.Result, preparation.Total, preparation.Ready,
			preparation.Review, preparation.Blocked, preparation.Warning,
			claim.PublicationRequestID, claim.Version.ID, claim.Version.DSLHash)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return dataset.ErrPublicationRequestConflict
		}
		tag, err = tx.Exec(ctx, `UPDATE platform.metric_candidate_preparation_jobs SET
			status=$1,lease_owner='',lease_expires_at=NULL,
			completed_at=now(),updated_at=now()
			WHERE id::text=$2 AND status='RUNNING' AND lease_owner=$3`,
			preparation.Status, claim.JobID, workerID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("publication candidate preparation lease was lost")
		}
		return nil
	})
}

func (s *PostgresStore) FailPublicationPreparation(
	ctx context.Context,
	claim PublicationPreparationClaim,
	workerID string,
	cause error,
) error {
	if claim.JobID == "" || claim.PublicationRequestID == "" || claim.TenantID == "" || workerID == "" {
		return ErrInvalidRequest
	}
	message := "candidate preparation failed"
	if cause != nil {
		message = strings.TrimSpace(cause.Error())
	}
	if runes := []rune(message); len(runes) > 2000 {
		message = string(runes[:2000])
	}
	return database.WithTenantTx(ctx, s.pool, claim.TenantID, func(tx pgx.Tx) error {
		var attempt int
		var status string
		err := tx.QueryRow(ctx, `UPDATE platform.metric_candidate_preparation_jobs SET
			status=CASE WHEN attempt>=$1 THEN 'FAILED' ELSE 'PENDING' END,
			next_attempt_at=CASE WHEN attempt>=$1 THEN next_attempt_at
			  ELSE now()+(CASE WHEN attempt=1 THEN 5 ELSE 30 END * interval '1 second') END,
			lease_owner='',lease_expires_at=NULL,
			error_code='METRIC_CANDIDATE_GENERATION_FAILED',error_message=$2,
			completed_at=CASE WHEN attempt>=$1 THEN now() ELSE NULL END,updated_at=now()
			WHERE id::text=$3 AND status='RUNNING' AND lease_owner=$4
			RETURNING attempt,status`,
			maxPublicationPreparationAttempts, message, claim.JobID, workerID).Scan(&attempt, &status)
		if errors.Is(err, pgx.ErrNoRows) {
			return errors.New("publication candidate preparation lease was lost")
		}
		if err != nil {
			return err
		}
		if status != "FAILED" {
			return nil
		}
		_, err = tx.Exec(ctx, `UPDATE platform.dataset_publication_requests SET
			metric_candidate_generation_status='FAILED',
			metric_candidate_result=NULL,
			metric_candidate_total=0,metric_candidate_ready_count=0,
			metric_candidate_review_count=0,metric_candidate_blocked_count=0,
			metric_candidate_error_code='METRIC_CANDIDATE_GENERATION_FAILED',
			metric_candidate_generated_at=NULL,updated_at=now()
			WHERE id::text=$1 AND status='PENDING'
			  AND metric_candidate_generation_status IN ('PENDING','FAILED')`,
			claim.PublicationRequestID)
		return err
	})
}

type PublicationPreparationWorker struct {
	store     *PostgresStore
	generator *PublicationGenerator
}

func NewPublicationPreparationWorker(
	store *PostgresStore,
	generator *PublicationGenerator,
) *PublicationPreparationWorker {
	return &PublicationPreparationWorker{store: store, generator: generator}
}

func (w *PublicationPreparationWorker) TenantIDs(ctx context.Context) ([]string, error) {
	if w == nil || w.store == nil {
		return nil, ErrInvalidRequest
	}
	return w.store.ListJobTenantIDs(ctx)
}

func (w *PublicationPreparationWorker) ProcessNext(
	ctx context.Context,
	tenantID, workerID string,
	lease time.Duration,
) (bool, error) {
	if w == nil || w.store == nil || w.generator == nil {
		return false, ErrInvalidRequest
	}
	claim, err := w.store.ClaimPublicationPreparation(ctx, tenantID, workerID, lease)
	if err != nil || claim == nil {
		return false, err
	}
	preparation, generationErr := w.generator.GeneratePublicationCandidates(
		ctx, claim.TenantID, claim.ActorID, claim.Version,
	)
	if generationErr != nil {
		failErr := w.store.FailPublicationPreparation(ctx, *claim, workerID, generationErr)
		return true, errors.Join(generationErr, failErr)
	}
	return true, w.store.FinishPublicationPreparation(ctx, *claim, workerID, preparation)
}
