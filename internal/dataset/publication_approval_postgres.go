package dataset

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"intelligent-report-generation-system/internal/platform/database"
)

func (s *PostgresStore) ValidateDWDPublicationDependencies(
	ctx context.Context,
	tenantID, datasetID, draftVersionID string,
) (err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		return validateDWDPublicationDependenciesTx(
			ctx, tx, datasetID, draftVersionID,
		)
	})
	return err
}

func validateDWDPublicationDependenciesTx(
	ctx context.Context,
	tx pgx.Tx,
	datasetID, draftVersionID string,
) error {
	var layer, targetDomainID, targetDomain string
	err := tx.QueryRow(ctx, `SELECT version.layer,
			dataset.domain_id::text,business_domain.name
		FROM platform.dataset_versions AS version
		JOIN platform.datasets AS dataset
		  ON dataset.tenant_id=version.tenant_id
		 AND dataset.id=version.dataset_id
		 AND dataset.current_draft_version_id=version.id
		JOIN platform.business_domains AS business_domain
		  ON business_domain.tenant_id=dataset.tenant_id
		 AND business_domain.id=dataset.domain_id
		 AND business_domain.status='ACTIVE'
		WHERE dataset.id::text=$1
		  AND version.id::text=$2
		  AND version.status='DRAFT'
		  AND dataset.deleted_at IS NULL
		FOR SHARE OF dataset,version`,
		datasetID, draftVersionID,
	).Scan(&layer, &targetDomainID, &targetDomain)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrConflict
	}
	if err != nil {
		return err
	}

	if targetDomainID == "" || targetDomain == "" {
		return &PublicationValidationError{Issues: []PublicationIssue{{
			Path:   "dataset.domain",
			Code:   "DATASET_DOMAIN_REQUIRED",
			Reason: "发布数据集必须填写业务领域",
		}}}
	}
	if layer != string(LayerODS) {
		var dependencyCount, mismatchedCount int
		err = tx.QueryRow(ctx, `SELECT count(*)::integer,
				count(*) FILTER(
				  WHERE upstream.id IS NULL
				     OR upstream_dataset.id IS NULL
				     OR upstream_dataset.domain_id<>$2::uuid
				)::integer
			FROM platform.dataset_versions AS draft
			CROSS JOIN LATERAL jsonb_array_elements(
			  COALESCE(draft.dsl_json->'nodes','[]'::jsonb)
			) AS node
			LEFT JOIN platform.dataset_versions AS upstream
			  ON upstream.tenant_id=draft.tenant_id
			 AND upstream.id=(node->>'datasetVersionId')::uuid
			 AND upstream.status='PUBLISHED'
			LEFT JOIN platform.datasets AS upstream_dataset
			  ON upstream_dataset.tenant_id=upstream.tenant_id
			 AND upstream_dataset.id=upstream.dataset_id
			 AND upstream_dataset.deleted_at IS NULL
			WHERE draft.id::text=$1
			  AND node->>'type'='DATASET'`,
			draftVersionID, targetDomainID,
		).Scan(&dependencyCount, &mismatchedCount)
		if err != nil {
			return err
		}
		if dependencyCount == 0 || mismatchedCount > 0 {
			return &PublicationValidationError{Issues: []PublicationIssue{{
				Path:   "dataset.domain",
				Code:   "DATASET_DOMAIN_LINEAGE_MISMATCH",
				Reason: "数据集业务领域必须与所有已发布上游一致；请修正领域后重新提交审批",
			}}}
		}
	}
	if layer != string(LayerDWD) {
		return nil
	}

	var unavailable int
	err = tx.QueryRow(ctx, `SELECT count(*)
		FROM platform.dataset_dependencies AS dependency
		JOIN platform.dataset_versions AS dimension_version
		  ON dimension_version.tenant_id=dependency.tenant_id
		 AND dimension_version.id::text=dependency.source_id
		JOIN platform.datasets AS dimension
		  ON dimension.tenant_id=dimension_version.tenant_id
		 AND dimension.id=dimension_version.dataset_id
		WHERE dependency.dataset_version_id::text=$1
		  AND dependency.source_type='DATASET_VERSION'
		  AND dimension_version.layer='DIM'
		  AND (
		    dimension_version.status<>'PUBLISHED'
		    OR dimension.status<>'PUBLISHED'
		    OR dimension.deleted_at IS NOT NULL
		    OR dimension.current_published_version_id IS DISTINCT FROM
		       dimension_version.id
		  )`, draftVersionID).Scan(&unavailable)
	if err != nil {
		return err
	}
	if unavailable == 0 {
		return nil
	}
	return &PublicationValidationError{Issues: []PublicationIssue{{
		Path: "nodes",
		Code: "DIM_PUBLICATION_REQUIRED",
		Reason: fmt.Sprintf(
			"DWD 依赖的 %d 张 DIM 尚未发布；请先完成 DIM 审批发布，再提交或批准 DWD",
			unavailable,
		),
	}}}
}

const publicationRequestSelect = `request.id::text,request.dataset_id::text,request.status,request.version,
	request.draft_version_id::text,request.expected_dataset_version,request.expected_draft_record_version,
	request.expected_dsl_hash,request.expected_plan_hash,request.requester_user_id::text,request.request_note,
	COALESCE(request.reviewer_user_id::text,''),request.review_note,
	COALESCE(request.published_version_id::text,''),
	to_char(request.submitted_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
	COALESCE(to_char(request.reviewed_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),''),
	to_char(request.updated_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
	request.validation_parameters,request.reserved_published_version_id::text,
	request.metric_candidate_result,request.metric_candidate_generation_status,
	request.metric_candidate_total,request.metric_candidate_ready_count,
	request.metric_candidate_review_count,request.metric_candidate_blocked_count,
	request.metric_candidate_warning,request.metric_candidate_error_code,
	COALESCE(to_char(request.metric_candidate_generated_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),'')`

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
		if err := validateDWDPublicationDependenciesTx(
			ctx, tx, datasetID, draftVersionID,
		); err != nil {
			return err
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
		// Submission freezes the exact draft only. Metric extraction and derived-layer
		// materialization are publication outboxes and must not run before a human
		// approval decision has committed.
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

func (s *PostgresStore) SavePublicationCandidatePreparation(
	ctx context.Context,
	tenantID, datasetID string,
	request PublicationRequest,
	preparation PublicationCandidatePreparation,
) (saved PublicationRequest, err error) {
	if preparation.Status != PublicationCandidateSucceeded &&
		preparation.Status != PublicationCandidatePartial &&
		preparation.Status != PublicationCandidateFailed {
		return PublicationRequest{}, ErrInvalidDocument
	}
	if preparation.Total < 0 || preparation.Ready < 0 || preparation.Review < 0 || preparation.Blocked < 0 ||
		preparation.Ready+preparation.Review+preparation.Blocked > preparation.Total {
		return PublicationRequest{}, ErrInvalidDocument
	}
	if preparation.Status == PublicationCandidateFailed {
		preparation.Result = nil
		if preparation.ErrorCode == "" {
			preparation.ErrorCode = "METRIC_CANDIDATE_GENERATION_FAILED"
		}
	} else if len(preparation.Result) == 0 || !json.Valid(preparation.Result) {
		return PublicationRequest{}, ErrInvalidDocument
	}
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE platform.dataset_publication_requests SET
			metric_candidate_generation_status=$1,
			metric_candidate_result=$2,
			metric_candidate_total=$3,
			metric_candidate_ready_count=$4,
			metric_candidate_review_count=$5,
			metric_candidate_blocked_count=$6,
			metric_candidate_warning=$7,
			metric_candidate_error_code=$8,
			metric_candidate_generated_at=CASE WHEN $1='FAILED' THEN NULL ELSE now() END,
			updated_at=now()
			WHERE id::text=$9 AND dataset_id::text=$10 AND status='PENDING'
			  AND version=$11 AND draft_version_id::text=$12
			  AND expected_draft_record_version=$13 AND expected_dsl_hash=$14
			  AND reserved_published_version_id::text=$15
			  AND metric_candidate_generation_status IN ('PENDING','FAILED')`,
			preparation.Status, preparation.Result, preparation.Total, preparation.Ready,
			preparation.Review, preparation.Blocked, preparation.Warning, preparation.ErrorCode,
			request.ID, datasetID, request.Version, request.DraftVersionID,
			request.ExpectedDraftRecordVersion, request.ExpectedDSLHash, request.ReservedPublishedVersionID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			var current PublicationRequest
			if getErr := scanPublicationRequest(tx.QueryRow(ctx, `SELECT `+publicationRequestSelect+`
				FROM platform.dataset_publication_requests AS request
				WHERE request.id::text=$1 AND request.dataset_id::text=$2`, request.ID, datasetID), &current); getErr != nil {
				return getErr
			}
			if current.MetricCandidateStatus != PublicationCandidateSucceeded &&
				current.MetricCandidateStatus != PublicationCandidatePartial {
				return ErrPublicationRequestConflict
			}
			saved = current
			return nil
		}
		return scanPublicationRequest(tx.QueryRow(ctx, `SELECT `+publicationRequestSelect+`
			FROM platform.dataset_publication_requests AS request WHERE request.id::text=$1`, request.ID), &saved)
	})
	return saved, err
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
		if status == PublicationRequestCancelled {
			return ErrPublicationRequestCancelled
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
		if err := validateDWDPublicationDependenciesTx(
			ctx, tx, datasetID, draftVersionID,
		); err != nil {
			return err
		}
		err = s.publishTx(
			ctx, tx, tenantID, actorID, datasetID,
			PublicationOriginHumanApproval, plan, &published,
		)
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
		if status == PublicationRequestCancelled {
			return ErrPublicationRequestCancelled
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
		&request.ReservedPublishedVersionID, &request.MetricCandidateResult,
		&request.MetricCandidateStatus, &request.MetricCandidateTotal,
		&request.MetricCandidateReady, &request.MetricCandidateReview,
		&request.MetricCandidateBlocked, &request.MetricCandidateWarning,
		&request.MetricCandidateErrorCode, &request.MetricCandidateGeneratedAt,
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
