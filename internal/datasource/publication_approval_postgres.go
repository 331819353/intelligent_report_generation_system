package datasource

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"intelligent-report-generation-system/internal/platform/database"
)

const dataSourcePublicationRequestSelect = `request.id::text,request.data_source_id::text,
	request.data_source_version_id::text,request.config_hash,request.status,request.version,
	request.requester_user_id::text,request.request_note,
	COALESCE(request.reviewer_user_id::text,''),request.review_note,
	request.submitted_at,request.reviewed_at,request.updated_at,
	COALESCE(request.published_version_id::text,'')`

func (r *PostgresRepository) SubmitPublicationRequest(
	ctx context.Context,
	tenantID, actorID string,
	draft Source,
	note string,
) (request PublicationRequest, err error) {
	err = database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		var versionID, configHash string
		var validationStatus ValidationStatus
		var expiresAt *time.Time
		err := tx.QueryRow(ctx, `SELECT source.current_draft_version_id::text,version.config_hash,
			source.validation_status,source.test_expires_at
			FROM platform.data_sources AS source
			JOIN platform.data_source_versions AS version ON version.id=source.current_draft_version_id
			WHERE source.id::text=$1 AND source.deleted_at IS NULL
			FOR UPDATE OF source`, draft.ID).
			Scan(&versionID, &configHash, &validationStatus, &expiresAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrReviewRequestNotFound
		}
		if err != nil {
			return err
		}
		if versionID != draft.ConfigVersionID || configHash != draft.ConfigHash {
			return ErrSourceVersionChanged
		}
		if validationStatus != ValidationPassed {
			return ErrTestRequired
		}
		if expiresAt == nil || !expiresAt.After(time.Now().UTC()) {
			return ErrTestExpired
		}
		var valid bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1
			FROM platform.data_source_connection_test_attestations AS attestation
			JOIN platform.data_source_connection_test_jobs AS job
			  ON job.id=attestation.connection_test_job_id
			 AND job.tenant_id=attestation.tenant_id
			WHERE attestation.data_source_id=$1
			  AND attestation.data_source_version_id=$2
			  AND attestation.config_hash=$3
			  AND attestation.expires_at>clock_timestamp()
			  AND attestation.attestation_version='connection-test-worker-v1'
			  AND job.status='SUCCEEDED'
			  AND job.data_source_id=attestation.data_source_id
			  AND job.data_source_version_id=attestation.data_source_version_id
			  AND job.config_hash=attestation.config_hash
		)`, draft.ID, versionID, configHash).Scan(&valid); err != nil {
			return err
		}
		if !valid {
			return ErrTestRequired
		}
		err = scanDataSourcePublicationRequest(tx.QueryRow(ctx, `SELECT `+dataSourcePublicationRequestSelect+`
			FROM platform.data_source_publication_requests AS request
			WHERE request.data_source_id=$1 AND request.status='PENDING'
			ORDER BY request.submitted_at DESC LIMIT 1`, draft.ID), &request)
		if err == nil {
			if request.ConfigVersionID == versionID && request.ConfigHash == configHash {
				return nil
			}
			return ErrReviewPending
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO platform.data_source_publication_requests AS request(
			tenant_id,data_source_id,data_source_version_id,config_hash,requester_user_id,request_note
		) VALUES($1,$2,$3,$4,$5,$6)
		RETURNING `+dataSourcePublicationRequestSelect,
			tenantID, draft.ID, versionID, configHash, actorID, note).
			Scan(publicationRequestScanTargets(&request)...); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO platform.audit_logs(
			tenant_id,actor_user_id,action,resource_type,resource_id,detail
		) VALUES($1,$2,'SUBMIT_APPROVAL','DATA_SOURCE_PUBLICATION_REQUEST',$3,
			jsonb_build_object('dataSourceId',$4::text,'configVersionId',$5::text,'configHash',$6::text))`,
			tenantID, actorID, request.ID, draft.ID, versionID, configHash)
		return err
	})
	return request, err
}

func (r *PostgresRepository) ListPublicationRequests(
	ctx context.Context,
	tenantID, sourceID string,
) (items []PublicationRequest, err error) {
	items = []PublicationRequest{}
	err = database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+dataSourcePublicationRequestSelect+`
			FROM platform.data_source_publication_requests AS request
			WHERE request.data_source_id::text=$1
			ORDER BY request.submitted_at DESC,request.id DESC`, sourceID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item PublicationRequest
			if err := scanDataSourcePublicationRequest(rows, &item); err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, err
}

func (r *PostgresRepository) LatestPublicationRequest(
	ctx context.Context,
	tenantID, sourceID string,
) (request *PublicationRequest, err error) {
	err = database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		var item PublicationRequest
		err := scanDataSourcePublicationRequest(tx.QueryRow(ctx, `SELECT `+dataSourcePublicationRequestSelect+`
			FROM platform.data_source_publication_requests AS request
			WHERE request.data_source_id::text=$1
			ORDER BY request.submitted_at DESC,request.id DESC LIMIT 1`, sourceID), &item)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err == nil {
			request = &item
		}
		return err
	})
	return request, err
}

func (r *PostgresRepository) GetPublicationRequest(
	ctx context.Context,
	tenantID, sourceID, requestID string,
) (request PublicationRequest, err error) {
	err = database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		return scanDataSourcePublicationRequest(tx.QueryRow(ctx, `SELECT `+dataSourcePublicationRequestSelect+`
			FROM platform.data_source_publication_requests AS request
			WHERE request.id::text=$1 AND request.data_source_id::text=$2`, requestID, sourceID), &request)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return PublicationRequest{}, ErrReviewRequestNotFound
	}
	return request, err
}

func (r *PostgresRepository) WithdrawPublicationRequest(
	ctx context.Context,
	tenantID, actorID, sourceID, requestID string,
	input ReviewPublicationInput,
) (request PublicationRequest, err error) {
	err = database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		var status ReviewStatus
		var version int64
		var requesterID string
		if err := tx.QueryRow(ctx, `SELECT status,version,requester_user_id::text
			FROM platform.data_source_publication_requests
			WHERE id::text=$1 AND data_source_id::text=$2 FOR UPDATE`, requestID, sourceID).
			Scan(&status, &version, &requesterID); errors.Is(err, pgx.ErrNoRows) {
			return ErrReviewRequestNotFound
		} else if err != nil {
			return err
		}
		if status != ReviewPending {
			return ErrReviewRequestNotPending
		}
		if version != input.ExpectedVersion {
			return ErrReviewRequestConflict
		}
		if requesterID != actorID {
			return ErrReviewWithdrawForbidden
		}
		tag, err := tx.Exec(ctx, `UPDATE platform.data_source_publication_requests SET
			status='WITHDRAWN',version=version+1,reviewer_user_id=$1,
			review_note=$2,reviewed_at=now(),updated_at=now()
			WHERE id::text=$3 AND status='PENDING' AND version=$4`,
			actorID, input.Reason, requestID, input.ExpectedVersion)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrReviewRequestConflict
		}
		return scanDataSourcePublicationRequest(tx.QueryRow(ctx, `SELECT `+dataSourcePublicationRequestSelect+`
			FROM platform.data_source_publication_requests AS request WHERE request.id::text=$1`, requestID), &request)
	})
	return request, err
}

func (r *PostgresRepository) RejectPublicationRequest(
	ctx context.Context,
	tenantID, actorID, sourceID, requestID string,
	input ReviewPublicationInput,
) (request PublicationRequest, err error) {
	err = database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE platform.data_source_publication_requests SET
			status='REJECTED',version=version+1,reviewer_user_id=$1,
			review_note=$2,reviewed_at=now(),updated_at=now()
			WHERE id::text=$3 AND data_source_id::text=$4
			  AND status='PENDING' AND version=$5 AND requester_user_id<>$1`,
			actorID, input.Reason, requestID, sourceID, input.ExpectedVersion)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform.data_source_publication_requests
				WHERE id::text=$1 AND data_source_id::text=$2)`, requestID, sourceID).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return ErrReviewRequestNotFound
			}
			return ErrReviewRequestConflict
		}
		return scanDataSourcePublicationRequest(tx.QueryRow(ctx, `SELECT `+dataSourcePublicationRequestSelect+`
			FROM platform.data_source_publication_requests AS request WHERE request.id::text=$1`, requestID), &request)
	})
	return request, err
}

func (r *PostgresRepository) ApproveAndPublish(
	ctx context.Context,
	tenantID, actorID, sourceID, requestID string,
	input ReviewPublicationInput,
) (request PublicationRequest, published Source, err error) {
	err = database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		var status ReviewStatus
		var version int64
		var requesterID, configVersionID, configHash string
		if err := tx.QueryRow(ctx, `SELECT status,version,requester_user_id::text,
			data_source_version_id::text,config_hash
			FROM platform.data_source_publication_requests
			WHERE id::text=$1 AND data_source_id::text=$2 FOR UPDATE`, requestID, sourceID).
			Scan(&status, &version, &requesterID, &configVersionID, &configHash); errors.Is(err, pgx.ErrNoRows) {
			return ErrReviewRequestNotFound
		} else if err != nil {
			return err
		}
		if status != ReviewPending {
			return ErrReviewRequestNotPending
		}
		if version != input.ExpectedVersion {
			return ErrReviewRequestConflict
		}
		if requesterID == actorID {
			return ErrReviewSelfApproval
		}
		var currentVersionID, currentHash string
		if err := tx.QueryRow(ctx, `SELECT source.current_draft_version_id::text,version.config_hash
			FROM platform.data_sources AS source
			JOIN platform.data_source_versions AS version ON version.id=source.current_draft_version_id
			WHERE source.id::text=$1 AND source.deleted_at IS NULL FOR UPDATE OF source`,
			sourceID).Scan(&currentVersionID, &currentHash); err != nil {
			return err
		}
		if currentVersionID != configVersionID || currentHash != configHash {
			return ErrSourceVersionChanged
		}
		var valid bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1
			FROM platform.data_source_connection_test_attestations AS attestation
			JOIN platform.data_source_connection_test_jobs AS job
			  ON job.id=attestation.connection_test_job_id
			 AND job.tenant_id=attestation.tenant_id
			WHERE attestation.data_source_id=$1
			  AND attestation.data_source_version_id=$2
			  AND attestation.config_hash=$3
			  AND attestation.expires_at>clock_timestamp()
			  AND attestation.attestation_version='connection-test-worker-v1'
			  AND job.status='SUCCEEDED'
		)`, sourceID, configVersionID, configHash).Scan(&valid); err != nil {
			return err
		}
		if !valid {
			return ErrTestExpired
		}
		tag, err := tx.Exec(ctx, `UPDATE platform.data_sources AS source SET
			current_published_version_id=source.current_draft_version_id,
			publication_status='PUBLISHED',
			source_type=version.source_type,config=version.config,secret_ref=version.secret_ref,
			file_asset_id=version.file_asset_id,
			status=CASE WHEN source.status='DISABLED' THEN source.status ELSE 'ACTIVE'::platform.data_source_status END,
			last_error=NULL,updated_by=$1,version=source.version+1
			FROM platform.data_source_versions AS version
			WHERE source.id::text=$2 AND source.current_draft_version_id=$3
			  AND version.id=source.current_draft_version_id AND source.deleted_at IS NULL`,
			actorID, sourceID, configVersionID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrSourceVersionChanged
		}
		tag, err = tx.Exec(ctx, `UPDATE platform.data_source_publication_requests SET
			status='APPROVED',version=version+1,reviewer_user_id=$1,
			review_note=$2,published_version_id=data_source_version_id,
			reviewed_at=now(),updated_at=now()
			WHERE id::text=$3 AND status='PENDING' AND version=$4`,
			actorID, input.Reason, requestID, input.ExpectedVersion)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrReviewRequestConflict
		}
		if err := r.scanDraftSource(ctx, tx, sourceID, &published); err != nil {
			return err
		}
		if err := scanDataSourcePublicationRequest(tx.QueryRow(ctx, `SELECT `+dataSourcePublicationRequestSelect+`
			FROM platform.data_source_publication_requests AS request WHERE request.id::text=$1`, requestID), &request); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO platform.audit_logs(
			tenant_id,actor_user_id,action,resource_type,resource_id,detail
		) VALUES($1,$2,'APPROVE_AND_PUBLISH','DATA_SOURCE_PUBLICATION_REQUEST',$3,
			jsonb_build_object('dataSourceId',$4::text,'configVersionId',$5::text))`,
			tenantID, actorID, requestID, sourceID, configVersionID)
		return err
	})
	return request, published, err
}

func scanDataSourcePublicationRequest(
	row interface{ Scan(...any) error },
	request *PublicationRequest,
) error {
	return row.Scan(publicationRequestScanTargets(request)...)
}

func publicationRequestScanTargets(request *PublicationRequest) []any {
	return []any{
		&request.ID, &request.DataSourceID, &request.ConfigVersionID, &request.ConfigHash,
		&request.Status, &request.Version, &request.RequesterUserID, &request.RequestNote,
		&request.ReviewerUserID, &request.ReviewNote, &request.SubmittedAt,
		nullableTimeScanner{target: &request.ReviewedAt},
		&request.UpdatedAt, &request.PublishedVersionID,
	}
}

type nullableTimeScanner struct {
	target **time.Time
}

func (scanner nullableTimeScanner) Scan(value any) error {
	if value == nil {
		*scanner.target = nil
		return nil
	}
	timestamp, ok := value.(time.Time)
	if !ok {
		return errors.New("unexpected timestamp value")
	}
	*scanner.target = &timestamp
	return nil
}
