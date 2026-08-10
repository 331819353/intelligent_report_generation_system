package datarequest

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"intelligent-report-generation-system/internal/askdata"
	semanticregistry "intelligent-report-generation-system/internal/askdata/registry"
)

func (store *PostgresStore) EnqueueControlledExport(
	ctx context.Context, command ControlledExportCommand,
) (result ControlledExportJob, err error) {
	if !store.ready() || ctx == nil || uuid.Validate(command.TenantID) != nil ||
		uuid.Validate(command.DomainID) != nil || uuid.Validate(command.ActorID) != nil ||
		uuid.Validate(command.DataRequestID) != nil || command.RequestHash.Validate() != nil ||
		command.ExpiresAt.IsZero() || command.MaxDownloads != DefaultExportDownloads ||
		sensitivityRank(command.Sensitivity) < 0 {
		return ControlledExportJob{}, ErrControlledExportInvalid
	}
	fields, normalizeErr := normalizeFields(command.FieldRefs)
	if normalizeErr != nil || !slices.Equal(fields, command.FieldRefs) {
		return ControlledExportJob{}, ErrControlledExportInvalid
	}
	err = store.tenantTx(ctx, command.TenantID, func(tx pgx.Tx) error {
		request, loadErr := loadRequestTx(ctx, tx, command.DataRequestID, true)
		if errors.Is(loadErr, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if loadErr != nil {
			return loadErr
		}
		if request.TenantID != command.TenantID || request.DomainID != command.DomainID ||
			request.SensitivityLevel != command.Sensitivity ||
			(request.State != StateApproved && request.State != StateInProgress) ||
			(!slices.Contains(request.ApproverUserIDs, command.ActorID) && request.AssigneeUserID != command.ActorID) ||
			!slices.Equal(request.RequiredFields, fields) ||
			RequiresSecurityCosign(request.SensitivityLevel) && request.SecurityCosignUserID == "" {
			return ErrControlledExportInvalid
		}
		expectedHash, hashErr := controlledExportRequestHash(request.ID, fields, request.SensitivityLevel)
		if hashErr != nil || expectedHash != command.RequestHash {
			return ErrControlledExportInvalid
		}
		fieldsJSON, marshalErr := json.Marshal(fields)
		if marshalErr != nil {
			return marshalErr
		}
		jobID := uuid.NewString()
		row := tx.QueryRow(ctx, `INSERT INTO platform.data_request_export_jobs(
			id,tenant_id,domain_id,data_request_id,requested_by,request_hash,
			required_fields_json,sensitivity_level,expires_at,max_downloads
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT(tenant_id,data_request_id,request_hash) DO NOTHING
		RETURNING id::text,data_request_id::text,state,expires_at,max_downloads`,
			jobID, command.TenantID, command.DomainID, command.DataRequestID, command.ActorID,
			command.RequestHash, fieldsJSON, command.Sensitivity, command.ExpiresAt,
			command.MaxDownloads)
		if scanErr := row.Scan(&result.JobID, &result.DataRequestID, &result.State,
			&result.ExpiresAt, &result.MaxDownloads); errors.Is(scanErr, pgx.ErrNoRows) {
			return tx.QueryRow(ctx, `SELECT id::text,data_request_id::text,state,expires_at,max_downloads
				FROM platform.data_request_export_jobs
				WHERE tenant_id=$1 AND data_request_id=$2 AND request_hash=$3`,
				command.TenantID, command.DataRequestID, command.RequestHash,
			).Scan(&result.JobID, &result.DataRequestID, &result.State,
				&result.ExpiresAt, &result.MaxDownloads)
		} else if scanErr != nil {
			return scanErr
		}
		return insertExportAuditTx(
			ctx, tx, command, result.JobID, "EXPORT_ENQUEUED", 0, command.ExpiresAt,
		)
	})
	return result, err
}

func controlledExportRequestHash(
	requestID string, fields []FieldRef, sensitivity Sensitivity,
) (askdata.ContentHash, error) {
	hash, _, err := semanticregistry.CanonicalContentHash(struct {
		DataRequestID string      `json:"dataRequestId"`
		Fields        []FieldRef  `json:"fields"`
		Sensitivity   Sensitivity `json:"sensitivity"`
	}{requestID, fields, sensitivity})
	return hash, err
}

func (store *PostgresStore) MarkControlledExportReady(
	ctx context.Context,
	tenantID, jobID, storageKey string,
	contentHash askdata.ContentHash,
	byteSize int64,
	now time.Time,
) error {
	storageKey = strings.TrimSpace(storageKey)
	if !store.ready() || ctx == nil || uuid.Validate(tenantID) != nil || uuid.Validate(jobID) != nil ||
		!boundedText(storageKey, 1, 500) || contentHash.Validate() != nil || byteSize < 0 || now.IsZero() {
		return ErrControlledExportInvalid
	}
	return store.tenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE platform.data_request_export_jobs SET
			state='READY',storage_key=$1,content_hash=$2,byte_size=$3,ready_at=$4,updated_at=$4
			WHERE id=$5 AND state='PENDING' AND expires_at>$4`,
			storageKey, contentHash, byteSize, now.UTC(), jobID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrControlledExportNotReady
		}
		return nil
	})
}

func (store *PostgresStore) AcquireControlledExportDownload(
	ctx context.Context,
	identity Identity,
	jobID string,
	now time.Time,
) (result ControlledDownloadGrant, err error) {
	if !store.ready() || ctx == nil || !identity.Valid() || uuid.Validate(jobID) != nil || now.IsZero() {
		return ControlledDownloadGrant{}, ErrControlledExportInvalid
	}
	err = store.tenantTx(ctx, identity.TenantID, func(tx pgx.Tx) error {
		var requestID string
		var downloadCount, maxDownloads int
		row := tx.QueryRow(ctx, `UPDATE platform.data_request_export_jobs AS export_job SET
			download_count=export_job.download_count+1,updated_at=$1
			WHERE export_job.id=$2 AND export_job.tenant_id=$3 AND export_job.domain_id=$4
			  AND export_job.state='READY' AND export_job.expires_at>$1
			  AND export_job.download_count<export_job.max_downloads
			RETURNING export_job.data_request_id::text,export_job.storage_key,
			  export_job.content_hash,export_job.expires_at,export_job.download_count,
			  export_job.max_downloads`, now.UTC(), jobID, identity.TenantID, identity.DomainID)
		if scanErr := row.Scan(&requestID, &result.StorageKey, &result.ContentHash,
			&result.ExpiresAt, &downloadCount, &maxDownloads); errors.Is(scanErr, pgx.ErrNoRows) {
			return controlledDownloadFailureTx(ctx, tx, identity, jobID, now.UTC())
		} else if scanErr != nil {
			return scanErr
		}
		result.JobID = jobID
		result.RemainingDownloads = maxDownloads - downloadCount
		command := ControlledExportCommand{
			TenantID: identity.TenantID, DomainID: identity.DomainID, ActorID: identity.ActorID,
			DataRequestID: requestID,
		}
		return insertExportAuditTx(
			ctx, tx, command, jobID, "EXPORT_DOWNLOADED", downloadCount, result.ExpiresAt,
		)
	})
	return result, err
}

func controlledDownloadFailureTx(
	ctx context.Context, tx pgx.Tx, identity Identity, jobID string, now time.Time,
) error {
	var state string
	var expiresAt time.Time
	var downloadCount, maxDownloads int
	err := tx.QueryRow(ctx, `SELECT state,expires_at,download_count,max_downloads
		FROM platform.data_request_export_jobs
		WHERE id=$1 AND tenant_id=$2 AND domain_id=$3`,
		jobID, identity.TenantID, identity.DomainID,
	).Scan(&state, &expiresAt, &downloadCount, &maxDownloads)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return ValidateControlledDownload(
		now, ControlledExportState(state), expiresAt, downloadCount, maxDownloads,
	)
}

func insertExportAuditTx(
	ctx context.Context,
	tx pgx.Tx,
	command ControlledExportCommand,
	jobID, eventType string,
	downloadNo int,
	expiresAt time.Time,
) error {
	_, err := tx.Exec(ctx, `INSERT INTO platform.data_request_events(
		id,tenant_id,domain_id,data_request_id,event_type,audit_no,sequence_no,
		from_state,to_state,actor_user_id,note,details_json,created_at
	) SELECT gen_random_uuid(),request.tenant_id,request.domain_id,request.id,$1,
		(SELECT COALESCE(max(event.audit_no),0)+1 FROM platform.data_request_events AS event
		 WHERE event.data_request_id=request.id),NULL,request.state,request.state,$2,'',
		jsonb_strip_nulls(jsonb_build_object(
		  'sensitivityLevel',request.sensitivity_level,
		  'securityCosignUserId',request.security_cosign_user_id,
		  'exportJobId',$3::text,'downloadNo',NULLIF($4,0),'expiresAt',$5::timestamptz
		)),now()
	FROM platform.data_requests AS request
	WHERE request.id=$6 AND request.tenant_id=$7 AND request.domain_id=$8`,
		eventType, command.ActorID, jobID, downloadNo, expiresAt,
		command.DataRequestID, command.TenantID, command.DomainID)
	return mapPostgresError(err)
}
