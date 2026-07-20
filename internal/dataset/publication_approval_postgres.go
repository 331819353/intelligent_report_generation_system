package dataset

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"intelligent-report-generation-system/internal/platform/database"
)

const publicationRequestSelect = `request.id::text,request.dataset_id::text,request.status,request.version,
	request.draft_version_id::text,request.expected_dataset_version,request.expected_draft_record_version,
	request.expected_dsl_hash,request.expected_plan_hash,request.requester_user_id::text,request.request_note,
	COALESCE(request.reviewer_user_id::text,''),request.review_note,
	COALESCE(request.published_version_id::text,''),
	to_char(request.submitted_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
	COALESCE(to_char(request.reviewed_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),''),
	to_char(request.updated_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
	request.validation_parameters`

func (s *PostgresStore) SubmitPublicationRequest(
	ctx context.Context,
	tenantID, actorID, datasetID string,
	plan SubmitPublicationPlan,
) (request PublicationRequest, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		allowed, err := datasetActionAllowedTx(ctx, tx, tenantID, actorID, datasetID, "MANAGE")
		if err != nil {
			return err
		}
		if !allowed {
			return ErrForbidden
		}
		var datasetVersion, draftRecordVersion int64
		var status, draftVersionID, dslHash, planHash string
		err = tx.QueryRow(ctx, `SELECT dataset.version,dataset.status,draft.id::text,draft.record_version,
			draft.schema_hash,draft.plan_hash
			FROM platform.datasets AS dataset
			JOIN platform.dataset_versions AS draft
			  ON draft.id=dataset.current_draft_version_id AND draft.dataset_id=dataset.id AND draft.tenant_id=dataset.tenant_id
			WHERE dataset.id::text=$1 AND dataset.deleted_at IS NULL FOR SHARE OF dataset,draft`, datasetID).
			Scan(&datasetVersion, &status, &draftVersionID, &draftRecordVersion, &dslHash, &planHash)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if status == "DISABLED" {
			return ErrInvalidTransition
		}
		input := plan.Input
		if datasetVersion != input.ExpectedVersion || draftVersionID != input.DraftVersionID ||
			draftRecordVersion != input.ExpectedDraftRecordVersion || dslHash != input.ExpectedDSLHash ||
			planHash != plan.ExpectedPlanHash {
			return ErrConflict
		}
		var requestID string
		err = tx.QueryRow(ctx, `INSERT INTO platform.dataset_publication_requests(
			tenant_id,dataset_id,draft_version_id,expected_dataset_version,expected_draft_record_version,
			expected_dsl_hash,expected_plan_hash,validation_parameters,requester_user_id,request_note
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT(tenant_id,dataset_id,draft_version_id,expected_draft_record_version) DO NOTHING
		RETURNING id::text`, tenantID, datasetID, input.DraftVersionID, input.ExpectedVersion,
			input.ExpectedDraftRecordVersion, input.ExpectedDSLHash, plan.ExpectedPlanHash,
			plan.ParametersJSON, actorID, input.Note).Scan(&requestID)
		inserted := true
		if errors.Is(err, pgx.ErrNoRows) {
			inserted = false
			err = tx.QueryRow(ctx, `SELECT id::text FROM platform.dataset_publication_requests
				WHERE dataset_id::text=$1 AND draft_version_id::text=$2 AND expected_draft_record_version=$3`,
				datasetID, input.DraftVersionID, input.ExpectedDraftRecordVersion).Scan(&requestID)
		}
		if err != nil {
			return err
		}
		if inserted {
			if _, err := tx.Exec(ctx, `INSERT INTO platform.audit_logs(
				tenant_id,actor_user_id,action,resource_type,resource_id,detail
			) VALUES($1,$2,'SUBMIT_APPROVAL','DATASET_PUBLICATION_REQUEST',$3,
				jsonb_build_object('datasetId',$4::text,'draftVersionId',$5::text,'draftRecordVersion',$6::bigint,'dslHash',$7::text))`,
				tenantID, actorID, requestID, datasetID, input.DraftVersionID,
				input.ExpectedDraftRecordVersion, input.ExpectedDSLHash); err != nil {
				return err
			}
		}
		return scanPublicationRequest(tx.QueryRow(ctx, `SELECT `+publicationRequestSelect+`
			FROM platform.dataset_publication_requests AS request WHERE request.id::text=$1`, requestID), &request)
	})
	return request, err
}

func (s *PostgresStore) ListPublicationRequests(
	ctx context.Context,
	tenantID, datasetID string,
	limit, offset int,
) (items []PublicationRequest, total int, err error) {
	items = []PublicationRequest{}
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform.datasets WHERE id::text=$1 AND deleted_at IS NULL)`, datasetID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM platform.dataset_publication_requests WHERE dataset_id::text=$1`, datasetID).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+publicationRequestSelect+`
			FROM platform.dataset_publication_requests AS request WHERE request.dataset_id::text=$1
			ORDER BY request.submitted_at DESC,request.id DESC LIMIT $2 OFFSET $3`, datasetID, limit, offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item PublicationRequest
			if err := scanPublicationRequest(rows, &item); err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, total, err
}

func (s *PostgresStore) GetPublicationRequest(
	ctx context.Context,
	tenantID, datasetID, requestID string,
) (request PublicationRequest, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		return scanPublicationRequest(tx.QueryRow(ctx, `SELECT `+publicationRequestSelect+`
			FROM platform.dataset_publication_requests AS request
			WHERE request.id::text=$1 AND request.dataset_id::text=$2`, requestID, datasetID), &request)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return PublicationRequest{}, ErrPublicationRequestNotFound
	}
	return request, err
}

func (s *PostgresStore) ApproveAndPublish(
	ctx context.Context,
	tenantID, actorID, datasetID, requestID string,
	expectedRequestVersion int64,
	note string,
	plan PublishPlan,
) (request PublicationRequest, published VersionRecord, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		allowed, err := datasetActionAllowedTx(ctx, tx, tenantID, actorID, datasetID, "PUBLISH")
		if err != nil {
			return err
		}
		if !allowed {
			return ErrForbidden
		}
		var status, requestDatasetID, draftVersionID, dslHash, planHash, publishedVersionID string
		var version, expectedDatasetVersion, draftRecordVersion int64
		err = tx.QueryRow(ctx, `SELECT status,version,dataset_id::text,draft_version_id::text,
			expected_dataset_version,expected_draft_record_version,expected_dsl_hash,expected_plan_hash,
			COALESCE(published_version_id::text,'')
			FROM platform.dataset_publication_requests WHERE id::text=$1 AND dataset_id::text=$2 FOR UPDATE`, requestID, datasetID).
			Scan(&status, &version, &requestDatasetID, &draftVersionID, &expectedDatasetVersion,
				&draftRecordVersion, &dslHash, &planHash, &publishedVersionID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrPublicationRequestNotFound
		}
		if err != nil {
			return err
		}
		if status == PublicationRequestApproved {
			if err := scanVersionTx(ctx, tx, datasetID, publishedVersionID, &published); err != nil {
				return err
			}
			return scanPublicationRequest(tx.QueryRow(ctx, `SELECT `+publicationRequestSelect+`
				FROM platform.dataset_publication_requests AS request WHERE request.id::text=$1`, requestID), &request)
		}
		if status != PublicationRequestPending {
			return ErrPublicationRequestNotPending
		}
		if version != expectedRequestVersion {
			return ErrPublicationRequestConflict
		}
		if requestDatasetID != datasetID || plan.DraftVersionID != draftVersionID ||
			plan.ExpectedVersion != expectedDatasetVersion || plan.ExpectedDraftRecordVersion != draftRecordVersion ||
			plan.ExpectedDSLHash != dslHash || plan.Prepared.DSLHash != dslHash || plan.Prepared.PlanHash != planHash ||
			plan.IdempotencyKey != requestID {
			return ErrPublicationRequestConflict
		}
		err = s.publishTx(ctx, tx, tenantID, actorID, datasetID, plan, &published)
		if err != nil {
			return err
		}
		if tag, err := tx.Exec(ctx, `UPDATE platform.dataset_publication_requests SET
			status='APPROVED',reviewer_user_id=$1,review_note=$2,reviewed_at=now(),
			published_version_id=$3,version=version+1,updated_at=now()
			WHERE id::text=$4 AND status='PENDING' AND version=$5`,
			actorID, note, published.ID, requestID, expectedRequestVersion); err != nil {
			return err
		} else if tag.RowsAffected() != 1 {
			return ErrPublicationRequestConflict
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.audit_logs(
			tenant_id,actor_user_id,action,resource_type,resource_id,detail
		) VALUES($1,$2,'APPROVE','DATASET_PUBLICATION_REQUEST',$3,
			jsonb_build_object('datasetId',$4::text,'publishedVersionId',$5::text,'reviewNote',$6::text))`,
			tenantID, actorID, requestID, datasetID, published.ID, note); err != nil {
			return err
		}
		return scanPublicationRequest(tx.QueryRow(ctx, `SELECT `+publicationRequestSelect+`
			FROM platform.dataset_publication_requests AS request WHERE request.id::text=$1`, requestID), &request)
	})
	if err != nil {
		return PublicationRequest{}, VersionRecord{}, mapPublicationPostgresError(err)
	}
	return request, published, nil
}

func (s *PostgresStore) RejectPublicationRequest(
	ctx context.Context,
	tenantID, actorID, datasetID, requestID string,
	input RejectPublicationInput,
) (request PublicationRequest, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		allowed, err := datasetActionAllowedTx(ctx, tx, tenantID, actorID, datasetID, "PUBLISH")
		if err != nil {
			return err
		}
		if !allowed {
			return ErrForbidden
		}
		var status, priorReason string
		var version int64
		err = tx.QueryRow(ctx, `SELECT status,version,review_note FROM platform.dataset_publication_requests
			WHERE id::text=$1 AND dataset_id::text=$2 FOR UPDATE`, requestID, datasetID).
			Scan(&status, &version, &priorReason)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrPublicationRequestNotFound
		}
		if err != nil {
			return err
		}
		if status == PublicationRequestRejected && priorReason == input.Reason {
			return scanPublicationRequest(tx.QueryRow(ctx, `SELECT `+publicationRequestSelect+`
				FROM platform.dataset_publication_requests AS request WHERE request.id::text=$1`, requestID), &request)
		}
		if status != PublicationRequestPending {
			return ErrPublicationRequestNotPending
		}
		if version != input.ExpectedVersion {
			return ErrPublicationRequestConflict
		}
		if tag, err := tx.Exec(ctx, `UPDATE platform.dataset_publication_requests SET
			status='REJECTED',reviewer_user_id=$1,review_note=$2,reviewed_at=now(),
			version=version+1,updated_at=now() WHERE id::text=$3 AND status='PENDING' AND version=$4`,
			actorID, input.Reason, requestID, input.ExpectedVersion); err != nil {
			return err
		} else if tag.RowsAffected() != 1 {
			return ErrPublicationRequestConflict
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.audit_logs(
			tenant_id,actor_user_id,action,resource_type,resource_id,detail
		) VALUES($1,$2,'REJECT','DATASET_PUBLICATION_REQUEST',$3,
			jsonb_build_object('datasetId',$4::text,'reason',$5::text))`,
			tenantID, actorID, requestID, datasetID, input.Reason); err != nil {
			return err
		}
		return scanPublicationRequest(tx.QueryRow(ctx, `SELECT `+publicationRequestSelect+`
			FROM platform.dataset_publication_requests AS request WHERE request.id::text=$1`, requestID), &request)
	})
	return request, err
}

func scanPublicationRequest(row interface{ Scan(...any) error }, request *PublicationRequest) error {
	var parameters json.RawMessage
	if err := row.Scan(
		&request.ID, &request.DatasetID, &request.Status, &request.Version,
		&request.DraftVersionID, &request.ExpectedDatasetVersion, &request.ExpectedDraftRecordVersion,
		&request.ExpectedDSLHash, &request.ExpectedPlanHash, &request.RequesterID, &request.RequestNote,
		&request.ReviewerID, &request.ReviewNote, &request.PublishedVersionID,
		&request.SubmittedAt, &request.ReviewedAt, &request.UpdatedAt, &parameters,
	); err != nil {
		return err
	}
	if err := json.Unmarshal(parameters, &request.ValidationParameters); err != nil {
		return err
	}
	if request.ValidationParameters == nil {
		request.ValidationParameters = map[string]any{}
	}
	return nil
}
