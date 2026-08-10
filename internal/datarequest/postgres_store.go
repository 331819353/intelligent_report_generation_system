package datarequest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/platform/database"
)

type tenantTxRunner func(context.Context, string, func(pgx.Tx) error) error

type PostgresStore struct {
	pool     *pgxpool.Pool
	tenantTx tenantTxRunner
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{
		pool: pool,
		tenantTx: func(ctx context.Context, tenantID string, operation func(pgx.Tx) error) error {
			return database.WithTenantTx(ctx, pool, tenantID, operation)
		},
	}
}

func (store *PostgresStore) ready() bool {
	return store != nil && store.pool != nil && store.tenantTx != nil
}

const requestColumns = `request.id::text,request.tenant_id::text,request.domain_id::text,
  request.requester_user_id::text,COALESCE(request.source_question_run_id::text,''),
  request.request_text,request.parsed_context_json,request.business_purpose,
  request.required_fields_json,request.sensitivity_level::text,request.state,
  request.approver_user_ids::text[],COALESCE(request.security_cosign_user_id::text,''),
  COALESCE(request.assignee_user_id::text,''),request.sla_due_at,
  COALESCE(request.delivery_type,''),request.delivery_ref,request.status_note,
  request.record_version,request.created_at,request.updated_at,request.submitted_at,
  request.approved_at,request.rejected_at,request.started_at,request.delivered_at,
  request.closed_at`

func (store *PostgresStore) Create(
	ctx context.Context, identity Identity, command CreateCommand,
) (result Request, err error) {
	if !store.ready() || !identity.Valid() {
		return Request{}, ErrInvalidRequest
	}
	err = store.tenantTx(ctx, identity.TenantID, func(tx pgx.Tx) error {
		var releaseID string
		if command.SourceQuestionRunID != "" {
			if err := tx.QueryRow(ctx, `SELECT release_id::text
				FROM askdata.question_runs
				WHERE id=$1 AND tenant_id=$2 AND domain_id=$3 AND actor_id=$4`,
				command.SourceQuestionRunID, identity.TenantID, identity.DomainID, identity.ActorID,
			).Scan(&releaseID); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return ErrSourceRunNotFound
				}
				return err
			}
			if err := validateParsedContextTx(ctx, tx, identity, releaseID, command.ParsedContext); err != nil {
				return err
			}
		} else if !command.ParsedContext.Empty() {
			return ErrInvalidRequest
		}
		if err := validateRequiredFieldsTx(ctx, tx, command.RequiredFields); err != nil {
			return err
		}
		sensitivity, err := deriveSensitivityTx(
			ctx, tx, identity, releaseID, command.RequiredFields, command.ParsedContext.DimensionIDs,
		)
		if err != nil {
			return err
		}
		var approverIDs []string
		if err := tx.QueryRow(ctx, `SELECT COALESCE(array_agg(membership.user_id::text
				ORDER BY membership.user_id::text),'{}'::text[])
			FROM platform.domain_memberships AS membership
			JOIN platform.users AS user_account
			  ON user_account.id=membership.user_id AND user_account.tenant_id=membership.tenant_id
			WHERE membership.tenant_id=$1 AND membership.domain_id=$2
			  AND membership.member_role='DOMAIN_ADMIN' AND membership.status='ACTIVE'
			  AND user_account.status='ACTIVE' AND user_account.deleted_at IS NULL`,
			identity.TenantID, identity.DomainID,
		).Scan(&approverIDs); err != nil {
			return err
		}
		if len(approverIDs) == 0 {
			return ErrApproverUnavailable
		}
		contextJSON, err := json.Marshal(command.ParsedContext)
		if err != nil {
			return err
		}
		fieldsJSON, err := json.Marshal(command.RequiredFields)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO platform.data_requests(
			id,tenant_id,domain_id,requester_user_id,source_question_run_id,
			request_text,parsed_context_json,business_purpose,required_fields_json,
			sensitivity_level,state,approver_user_ids,sla_due_at,record_version,
			created_at,updated_at
		) VALUES($1,$2,$3,$4,NULLIF($5,'')::uuid,$6,$7,$8,$9,
			$10::platform.asset_sensitivity,'DRAFT',$11,$12,1,$13,$13)`,
			command.ID, identity.TenantID, identity.DomainID, identity.ActorID,
			command.SourceQuestionRunID, command.RequestText, contextJSON,
			command.BusinessPurpose, fieldsJSON, sensitivity, approverIDs, command.SLADueAt,
			command.CreatedAt,
		)
		if err != nil {
			return mapPostgresError(err)
		}
		if err := insertEventTx(ctx, tx, identity, command.ID, "", StateDraft, "", command.CreatedAt); err != nil {
			return err
		}
		result, err = loadRequestTx(ctx, tx, command.ID, false)
		if err != nil {
			return err
		}
		result.Events, err = loadEventsTx(ctx, tx, command.ID)
		return err
	})
	return result, err
}

func (store *PostgresStore) List(
	ctx context.Context, identity Identity, limit int,
) (result []Request, err error) {
	if !store.ready() || !identity.Valid() || limit < 1 || limit > 100 {
		return nil, ErrInvalidRequest
	}
	result = []Request{}
	err = store.tenantTx(ctx, identity.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+requestColumns+`
			FROM platform.data_requests AS request
			WHERE request.tenant_id=$1 AND request.domain_id=$2
			  AND request.requester_user_id=$3
			ORDER BY request.updated_at DESC,request.id DESC LIMIT $4`,
			identity.TenantID, identity.DomainID, identity.ActorID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			item, err := scanRequest(rows)
			if err != nil {
				return err
			}
			result = append(result, item)
		}
		return rows.Err()
	})
	return result, err
}

func (store *PostgresStore) Get(
	ctx context.Context, identity Identity, requestID string,
) (result Request, err error) {
	if !store.ready() || !identity.Valid() {
		return Request{}, ErrInvalidRequest
	}
	err = store.tenantTx(ctx, identity.TenantID, func(tx pgx.Tx) error {
		result, err = loadRequestTx(ctx, tx, requestID, false)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		result.Events, err = loadEventsTx(ctx, tx, requestID)
		return err
	})
	return result, err
}

func (store *PostgresStore) Submit(
	ctx context.Context, identity Identity, requestID string, recordVersion int64, now time.Time,
) (Request, error) {
	return store.transition(ctx, identity, requestID, TransitionInput{
		ToState: StateSubmitted, RecordVersion: recordVersion,
	}, now)
}

func (store *PostgresStore) Transition(
	ctx context.Context, identity Identity, requestID string, input TransitionInput, now time.Time,
) (Request, error) {
	return store.transition(ctx, identity, requestID, input, now)
}

func (store *PostgresStore) transition(
	ctx context.Context, identity Identity, requestID string, input TransitionInput, now time.Time,
) (result Request, err error) {
	if !store.ready() || !identity.Valid() {
		return Request{}, ErrInvalidRequest
	}
	err = store.tenantTx(ctx, identity.TenantID, func(tx pgx.Tx) error {
		current, err := loadRequestTx(ctx, tx, requestID, true)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if current.RecordVersion != input.RecordVersion {
			return ErrVersionConflict
		}
		if !ValidTransition(current.State, input.ToState) {
			return ErrInvalidTransition
		}
		approver := slices.Contains(current.ApproverUserIDs, identity.ActorID)
		switch input.ToState {
		case StateSubmitted:
			if identity.ActorID != current.RequesterUserID {
				return ErrPermissionDenied
			}
		case StateApproved, StateRejected:
			if !approver {
				return ErrPermissionDenied
			}
			if input.ToState == StateApproved {
				members, err := activeDomainMemberIDsTx(ctx, tx, identity)
				if err != nil {
					return err
				}
				if err := ValidateApproval(ApprovalPolicyInput{
					Sensitivity: current.SensitivityLevel, RequesterUserID: current.RequesterUserID,
					ApproverUserID: identity.ActorID, SecurityCosignID: input.SecurityCosignUserID,
					ActiveMemberIDs: members,
				}); err != nil {
					return err
				}
			}
		case StateInProgress:
			if !approver {
				return ErrPermissionDenied
			}
			if input.AssigneeUserID == "" {
				input.AssigneeUserID = identity.ActorID
			}
			if err := requireActiveDomainMemberTx(ctx, tx, identity, input.AssigneeUserID); err != nil {
				return err
			}
		case StateDelivered:
			if identity.ActorID != current.AssigneeUserID && !approver {
				return ErrPermissionDenied
			}
			if input.DeliveryType == DeliveryOneTimeExport {
				if err := validateControlledExportDeliveryTx(
					ctx, tx, current, input.DeliveryRef, now,
				); err != nil {
					return err
				}
			}
		case StateClosed:
			if identity.ActorID != current.RequesterUserID && !approver {
				return ErrPermissionDenied
			}
		}
		row := tx.QueryRow(ctx, `UPDATE platform.data_requests AS request SET
			state=$1,status_note=$2,
			security_cosign_user_id=CASE WHEN $1='APPROVED' THEN NULLIF($3,'')::uuid ELSE request.security_cosign_user_id END,
			assignee_user_id=CASE WHEN $1='IN_PROGRESS' THEN NULLIF($4,'')::uuid ELSE request.assignee_user_id END,
			delivery_type=CASE WHEN $1='DELIVERED' THEN NULLIF($5,'') ELSE request.delivery_type END,
			delivery_ref=CASE WHEN $1='DELIVERED' THEN $6 ELSE request.delivery_ref END,
			record_version=request.record_version+1,updated_at=$7,
			submitted_at=CASE WHEN $1='SUBMITTED' THEN $7 ELSE request.submitted_at END,
			approved_at=CASE WHEN $1='APPROVED' THEN $7 ELSE request.approved_at END,
			rejected_at=CASE WHEN $1='REJECTED' THEN $7 ELSE request.rejected_at END,
			started_at=CASE WHEN $1='IN_PROGRESS' THEN $7 ELSE request.started_at END,
			delivered_at=CASE WHEN $1='DELIVERED' THEN $7 ELSE request.delivered_at END,
			closed_at=CASE WHEN $1='CLOSED' THEN $7 ELSE request.closed_at END
			WHERE request.id=$8 AND request.record_version=$9
			RETURNING `+requestColumns,
			input.ToState, input.Note, input.SecurityCosignUserID, input.AssigneeUserID,
			input.DeliveryType, input.DeliveryRef, now, requestID, input.RecordVersion)
		result, err = scanRequest(row)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrVersionConflict
		}
		if err != nil {
			return mapPostgresError(err)
		}
		if err := insertEventTx(ctx, tx, identity, requestID, current.State, input.ToState, input.Note, now); err != nil {
			return err
		}
		result.Events, err = loadEventsTx(ctx, tx, requestID)
		return err
	})
	return result, err
}

func loadRequestTx(ctx context.Context, tx pgx.Tx, requestID string, lock bool) (Request, error) {
	query := `SELECT ` + requestColumns + ` FROM platform.data_requests AS request WHERE request.id=$1`
	if lock {
		query += ` FOR UPDATE`
	}
	return scanRequest(tx.QueryRow(ctx, query, requestID))
}

type requestScanner interface{ Scan(...any) error }

func scanRequest(row requestScanner) (Request, error) {
	var result Request
	var contextJSON, fieldsJSON []byte
	err := row.Scan(
		&result.ID, &result.TenantID, &result.DomainID, &result.RequesterUserID,
		&result.SourceQuestionRunID, &result.RequestText, &contextJSON,
		&result.BusinessPurpose, &fieldsJSON, &result.SensitivityLevel, &result.State,
		&result.ApproverUserIDs, &result.SecurityCosignUserID, &result.AssigneeUserID,
		&result.SLADueAt, &result.DeliveryType, &result.DeliveryRef, &result.StatusNote,
		&result.RecordVersion, &result.CreatedAt, &result.UpdatedAt, &result.SubmittedAt,
		&result.ApprovedAt, &result.RejectedAt, &result.StartedAt, &result.DeliveredAt,
		&result.ClosedAt,
	)
	if err != nil {
		return Request{}, err
	}
	if err := json.Unmarshal(contextJSON, &result.ParsedContext); err != nil {
		return Request{}, err
	}
	if err := json.Unmarshal(fieldsJSON, &result.RequiredFields); err != nil {
		return Request{}, err
	}
	if result.ApproverUserIDs == nil {
		result.ApproverUserIDs = []string{}
	}
	result.Events = []Event{}
	return result, nil
}

func loadEventsTx(ctx context.Context, tx pgx.Tx, requestID string) ([]Event, error) {
	rows, err := tx.Query(ctx, `SELECT id::text,data_request_id::text,event_type,audit_no,
		COALESCE(sequence_no,0),
		COALESCE(from_state,''),to_state,actor_user_id::text,note,details_json,created_at
		FROM platform.data_request_events WHERE data_request_id=$1
		ORDER BY audit_no`, requestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Event{}
	for rows.Next() {
		var event Event
		var detailsJSON []byte
		if err := rows.Scan(&event.ID, &event.RequestID, &event.EventType, &event.AuditNo,
			&event.SequenceNo, &event.FromState, &event.ToState,
			&event.ActorUserID, &event.Note, &detailsJSON, &event.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(detailsJSON, &event.Details); err != nil {
			return nil, err
		}
		result = append(result, event)
	}
	return result, rows.Err()
}

func insertEventTx(
	ctx context.Context, tx pgx.Tx, identity Identity, requestID string,
	from State, to State, note string, now time.Time,
) error {
	_, err := tx.Exec(ctx, `INSERT INTO platform.data_request_events(
		id,tenant_id,domain_id,data_request_id,event_type,audit_no,sequence_no,
		from_state,to_state,actor_user_id,note,details_json,created_at
	) SELECT gen_random_uuid(),$1,$2,$3,'STATE_TRANSITION',
		(SELECT COALESCE(max(event.audit_no),0)+1 FROM platform.data_request_events AS event
		 WHERE event.data_request_id=request.id),request.record_version,
		NULLIF($4,''),$5,$6,$7,jsonb_strip_nulls(jsonb_build_object(
			'sensitivityLevel',request.sensitivity_level,
			'securityCosignUserId',request.security_cosign_user_id,
			'exportJobId',CASE WHEN request.delivery_type='ONE_TIME_EXPORT'
				AND request.delivery_ref~'^[0-9a-fA-F-]{36}$' THEN request.delivery_ref ELSE NULL END
		)),$8
	FROM platform.data_requests AS request
	WHERE request.id=$3 AND request.tenant_id=$1 AND request.domain_id=$2`,
		identity.TenantID, identity.DomainID, requestID, from, to, identity.ActorID, note, now)
	return mapPostgresError(err)
}

func validateParsedContextTx(
	ctx context.Context, tx pgx.Tx, identity Identity, releaseID string, value ParsedContext,
) error {
	checks := []struct {
		objectType string
		ids        []string
	}{{"METRIC", value.MetricIDs}, {"DIMENSION", value.DimensionIDs}, {"MEMBER", value.MemberIDs}}
	for _, check := range checks {
		if len(check.ids) == 0 {
			continue
		}
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*)
			FROM askdata.release_objects
			WHERE tenant_id=$1 AND domain_id=$2 AND release_id=$3
			  AND object_type=$4 AND object_id=ANY($5::uuid[])`,
			identity.TenantID, identity.DomainID, releaseID, check.objectType, check.ids,
		).Scan(&count); err != nil {
			return err
		}
		if count != len(check.ids) {
			return ErrInvalidRequest
		}
	}
	return nil
}

func validateRequiredFieldsTx(ctx context.Context, tx pgx.Tx, fields []FieldRef) error {
	for _, field := range fields {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1 FROM platform.dataset_fields AS field
			JOIN platform.dataset_versions AS version
			  ON version.id=field.dataset_version_id AND version.tenant_id=field.tenant_id
			JOIN platform.datasets AS dataset
			  ON dataset.id=version.dataset_id AND dataset.tenant_id=version.tenant_id
			WHERE field.dataset_version_id=$1 AND field.field_id=$2 AND field.visible
			  AND version.status='PUBLISHED' AND dataset.deleted_at IS NULL
		)`, field.DatasetVersionID, field.FieldID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrInvalidRequest
		}
	}
	return nil
}

func deriveSensitivityTx(
	ctx context.Context,
	tx pgx.Tx,
	identity Identity,
	releaseID string,
	fields []FieldRef,
	dimensionIDs []string,
) (Sensitivity, error) {
	facts := make([]SensitivityFact, 0, len(fields)+len(dimensionIDs))
	for _, field := range fields {
		var sensitivity Sensitivity
		if err := tx.QueryRow(ctx, `SELECT field.sensitivity_level::text
			FROM platform.dataset_fields AS field
			JOIN platform.dataset_versions AS version
			  ON version.id=field.dataset_version_id AND version.tenant_id=field.tenant_id
			JOIN platform.datasets AS dataset
			  ON dataset.id=version.dataset_id AND dataset.tenant_id=version.tenant_id
			WHERE field.dataset_version_id=$1 AND field.field_id=$2 AND field.visible
			  AND version.status='PUBLISHED' AND dataset.deleted_at IS NULL
			  AND dataset.domain_id=$3`, field.DatasetVersionID, field.FieldID, identity.DomainID,
		).Scan(&sensitivity); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return "", ErrInvalidRequest
			}
			return "", err
		}
		facts = append(facts, SensitivityFact{
			SourceID:    "field:" + field.DatasetVersionID + ":" + field.FieldID,
			Sensitivity: sensitivity,
		})
	}
	if len(dimensionIDs) > 0 {
		if releaseID == "" {
			return "", ErrInvalidRequest
		}
		rows, err := tx.Query(ctx, `SELECT dimension.dimension_id::text,dimension.sensitivity
			FROM askdata.release_objects AS released
			JOIN askdata.dimensions AS dimension
			  ON dimension.id=released.object_version_id
			 AND dimension.tenant_id=released.tenant_id AND dimension.domain_id=released.domain_id
			WHERE released.tenant_id=$1 AND released.domain_id=$2 AND released.release_id=$3
			  AND released.object_type='DIMENSION' AND released.object_id=ANY($4::uuid[])
			ORDER BY dimension.dimension_id`,
			identity.TenantID, identity.DomainID, releaseID, dimensionIDs)
		if err != nil {
			return "", err
		}
		defer rows.Close()
		for rows.Next() {
			var dimensionID string
			var sensitivity Sensitivity
			if err := rows.Scan(&dimensionID, &sensitivity); err != nil {
				return "", err
			}
			facts = append(facts, SensitivityFact{
				SourceID: "dimension:" + dimensionID, Sensitivity: sensitivity,
			})
		}
		if err := rows.Err(); err != nil {
			return "", err
		}
		if len(facts) != len(fields)+len(dimensionIDs) {
			return "", ErrInvalidRequest
		}
	}
	return DeriveSensitivity(facts)
}

func activeDomainMemberIDsTx(
	ctx context.Context, tx pgx.Tx, identity Identity,
) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT membership.user_id::text
		FROM platform.domain_memberships AS membership
		JOIN platform.users AS user_account
		  ON user_account.id=membership.user_id AND user_account.tenant_id=membership.tenant_id
		WHERE membership.tenant_id=$1 AND membership.domain_id=$2
		  AND membership.status='ACTIVE' AND user_account.status='ACTIVE'
		  AND user_account.deleted_at IS NULL
		ORDER BY membership.user_id`, identity.TenantID, identity.DomainID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var userID string
		if err := rows.Scan(&userID); err != nil {
			return nil, err
		}
		result = append(result, userID)
	}
	return result, rows.Err()
}

func validateControlledExportDeliveryTx(
	ctx context.Context, tx pgx.Tx, request Request, jobID string, now time.Time,
) error {
	if uuid.Validate(jobID) != nil {
		return ErrControlledExportInvalid
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM platform.data_request_export_jobs AS export_job
		WHERE export_job.id=$1 AND export_job.tenant_id=$2 AND export_job.domain_id=$3
		  AND export_job.data_request_id=$4 AND export_job.state='READY'
		  AND export_job.expires_at>$5 AND export_job.content_hash IS NOT NULL
		  AND export_job.storage_key<>''
	)`, jobID, request.TenantID, request.DomainID, request.ID, now.UTC()).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrControlledExportNotReady
	}
	return nil
}

func requireActiveDomainMemberTx(
	ctx context.Context, tx pgx.Tx, identity Identity, userID string,
) error {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM platform.domain_memberships AS membership
		JOIN platform.users AS user_account
		  ON user_account.id=membership.user_id AND user_account.tenant_id=membership.tenant_id
		WHERE membership.tenant_id=$1 AND membership.domain_id=$2
		  AND membership.user_id=$3 AND membership.status='ACTIVE'
		  AND user_account.status='ACTIVE' AND user_account.deleted_at IS NULL
	)`, identity.TenantID, identity.DomainID, userID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrInvalidRequest
	}
	return nil
}

func mapPostgresError(err error) error {
	if err == nil {
		return nil
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		if postgresError.ConstraintName == "platform_data_requests_security_cosign_required" {
			return ErrSecurityCosignRequired
		}
		switch postgresError.Code {
		case "23503", "23514", "22P02", "22023":
			return fmt.Errorf("%w: %s", ErrInvalidRequest, postgresError.ConstraintName)
		case "42501":
			return ErrPermissionDenied
		}
	}
	return err
}
