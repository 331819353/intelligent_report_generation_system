package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/platform/database"
	reportmodel "intelligent-report-generation-system/internal/report"
	"intelligent-report-generation-system/internal/report/compiler"
	"intelligent-report-generation-system/internal/report/operation"
)

type PostgresStore struct {
	pool     *pgxpool.Pool
	tenantTx func(context.Context, string, func(pgx.Tx) error) error
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

func (store *PostgresStore) withTenantTx(
	ctx context.Context, tenantID string, operation func(pgx.Tx) error,
) error {
	if store.tenantTx != nil {
		return store.tenantTx(ctx, tenantID, operation)
	}
	return database.WithTenantTx(ctx, store.pool, tenantID, operation)
}

func (store *PostgresStore) requestContext(
	ctx context.Context, identity Identity, ids ...askdata.ID,
) (context.Context, error) {
	if store == nil || (store.pool == nil && store.tenantTx == nil) {
		return nil, errors.New("report store is unavailable")
	}
	if err := identity.Validate(); err != nil {
		return nil, fmt.Errorf("invalid report identity: %w", err)
	}
	for _, id := range ids {
		if id.Validate() != nil {
			return nil, errors.New("invalid report store ID")
		}
		if _, err := uuid.Parse(string(id)); err != nil {
			return nil, errors.New("report store IDs must be UUIDs")
		}
	}
	return database.WithAccessContext(ctx, string(identity.ActorID), string(identity.DomainID)), nil
}

type CreateInput struct {
	ID         askdata.ID
	Code       string
	Name       string
	ReportType reportmodel.ReportType
	Definition reportmodel.ReportDefinition
}

func (store *PostgresStore) CreateReport(ctx context.Context, identity Identity, input CreateInput) (Report, Draft, error) {
	var err error
	ctx, err = store.requestContext(ctx, identity, input.ID)
	if err != nil {
		return Report{}, Draft{}, err
	}
	prepared, err := Prepare(input.Definition)
	if err != nil {
		return Report{}, Draft{}, err
	}
	if input.ID != prepared.Definition.Metadata.ID {
		return Report{}, Draft{}, errors.New("report ID must match definition metadata ID")
	}
	if input.Code != prepared.Definition.Metadata.Code || strings.TrimSpace(input.Name) != prepared.Definition.Metadata.Name ||
		input.ReportType != prepared.Definition.Metadata.ReportType {
		return Report{}, Draft{}, errors.New("report metadata must match the normalized definition")
	}
	var created Report
	var draft Draft
	err = store.withTenantTx(ctx, string(identity.TenantID), func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `INSERT INTO platform.reports(
			id,tenant_id,domain_id,code,name,report_type,owner_user_id,status,created_by
		) VALUES($1,$2,NULLIF($3,'')::uuid,$4,$5,$6,$7,'ACTIVE',$7)
		RETURNING id::text,tenant_id::text,COALESCE(domain_id::text,''),code,name,report_type,
			owner_user_id::text,COALESCE(current_published_version_id::text,''),status,created_at,updated_at`,
			input.ID, identity.TenantID, identity.DomainID, input.Code, input.Name, input.ReportType, identity.ActorID)
		if err := scanReport(row, &created); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.report_drafts(
			report_id,tenant_id,definition_json,definition_hash,schema_version,revision_no,updated_by
		) VALUES($1,$2,$3,$4,$5,0,$6)`, input.ID, identity.TenantID, prepared.Canonical, prepared.Hash, reportmodel.SchemaVersion, identity.ActorID); err != nil {
			return err
		}
		if err := rebuildDraftIndexes(ctx, tx, identity.TenantID, input.ID, 0, prepared.Indexes); err != nil {
			return err
		}
		draft, err = loadDraftTx(ctx, tx, input.ID, false)
		return err
	})
	return created, draft, err
}

func (store *PostgresStore) ListReports(ctx context.Context, identity Identity, limit int) ([]Report, error) {
	var err error
	ctx, err = store.requestContext(ctx, identity)
	if err != nil {
		return nil, err
	}
	if limit < 1 || limit > 500 {
		return nil, errors.New("limit must be between 1 and 500")
	}
	result := []Report{}
	err = store.withTenantTx(ctx, string(identity.TenantID), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text,tenant_id::text,COALESCE(domain_id::text,''),
			code,name,report_type,owner_user_id::text,COALESCE(current_published_version_id::text,''),
			status,created_at,updated_at FROM platform.reports
			WHERE status='ACTIVE' ORDER BY updated_at DESC,id LIMIT $1`, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item Report
			if err := scanReport(rows, &item); err != nil {
				return err
			}
			result = append(result, item)
		}
		return rows.Err()
	})
	return result, err
}

func (store *PostgresStore) GetReport(ctx context.Context, identity Identity, reportID askdata.ID) (Report, error) {
	var err error
	ctx, err = store.requestContext(ctx, identity, reportID)
	if err != nil {
		return Report{}, err
	}
	var result Report
	err = store.withTenantTx(ctx, string(identity.TenantID), func(tx pgx.Tx) error {
		return scanReport(tx.QueryRow(ctx, `SELECT id::text,tenant_id::text,COALESCE(domain_id::text,''),
			code,name,report_type,owner_user_id::text,COALESCE(current_published_version_id::text,''),
			status,created_at,updated_at FROM platform.reports WHERE id=$1`, reportID), &result)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Report{}, ErrNotFound
	}
	return result, err
}

func (store *PostgresStore) GetDraft(ctx context.Context, identity Identity, reportID askdata.ID) (Draft, error) {
	return store.GetDraftRevision(ctx, identity, reportID, nil)
}

func (store *PostgresStore) GetDraftRevision(ctx context.Context, identity Identity, reportID askdata.ID, revisionNo *int64) (Draft, error) {
	var err error
	ctx, err = store.requestContext(ctx, identity, reportID)
	if err != nil {
		return Draft{}, err
	}
	var result Draft
	err = store.withTenantTx(ctx, string(identity.TenantID), func(tx pgx.Tx) error {
		current, loadErr := loadDraftTx(ctx, tx, reportID, false)
		if loadErr != nil {
			return loadErr
		}
		if revisionNo == nil || *revisionNo == current.RevisionNo {
			result = current
			return nil
		}
		if *revisionNo < 0 || *revisionNo > current.RevisionNo {
			return ErrRevisionUnavailable
		}
		var raw []byte
		if err := tx.QueryRow(ctx, `SELECT before_snapshot,before_hash
			FROM platform.report_revisions WHERE report_id=$1 AND revision_no=$2
			  AND before_snapshot IS NOT NULL`, reportID, *revisionNo+1).Scan(&raw, &result.DefinitionHash); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrRevisionUnavailable
			}
			return err
		}
		result.ReportID, result.TenantID = reportID, identity.TenantID
		result.RevisionNo, result.SchemaVersion = *revisionNo, reportmodel.SchemaVersion
		if *revisionNo == 0 {
			if err := tx.QueryRow(ctx, `SELECT created_by::text,created_at FROM platform.reports WHERE id=$1`, reportID).Scan(&result.UpdatedBy, &result.UpdatedAt); err != nil {
				return err
			}
		} else if err := tx.QueryRow(ctx, `SELECT actor_user_id::text,created_at FROM platform.report_revisions
			WHERE report_id=$1 AND revision_no=$2`, reportID, *revisionNo).Scan(&result.UpdatedBy, &result.UpdatedAt); err != nil {
			return err
		}
		return hydrateStoredDefinition(raw, result.DefinitionHash, &result.Definition, &result.DefinitionRaw)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Draft{}, ErrNotFound
	}
	return result, err
}

type SaveInput struct {
	ExpectedRevision    int64
	Operations          []operation.Operation
	Source              string
	AIRunID             askdata.ID
	Scope               *operation.Scope
	InverseOfRevisionNo *int64
}

func (store *PostgresStore) SaveDraftWithRevision(ctx context.Context, identity Identity, reportID askdata.ID, input SaveInput) (Draft, Revision, error) {
	var err error
	ctx, err = store.requestContext(ctx, identity, reportID)
	if err != nil {
		return Draft{}, Revision{}, err
	}
	if len(input.Operations) == 0 {
		return Draft{}, Revision{}, errors.New("operations are required")
	}
	if input.Source == "" {
		input.Source = string(operation.SourceUser)
	}
	var saved Draft
	var revision Revision
	err = store.withTenantTx(ctx, string(identity.TenantID), func(tx pgx.Tx) error {
		var err error
		saved, revision, err = saveDraftWithRevisionTx(ctx, tx, identity, reportID, input)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Draft{}, Revision{}, ErrNotFound
	}
	return saved, revision, err
}

func saveDraftWithRevisionTx(
	ctx context.Context, tx pgx.Tx, identity Identity, reportID askdata.ID, input SaveInput,
) (Draft, Revision, error) {
	if input.Source == string(operation.SourceAI) && input.AIRunID.Validate() != nil {
		return Draft{}, Revision{}, errors.New("AI revision requires a valid AI run")
	}
	current, err := loadDraftTx(ctx, tx, reportID, true)
	if err != nil {
		return Draft{}, Revision{}, err
	}
	if err := authorizeSaveInput(ctx, tx, identity, input); err != nil {
		return Draft{}, Revision{}, err
	}
	if current.RevisionNo != input.ExpectedRevision {
		summaries, summaryErr := revisionSummariesTx(ctx, tx, reportID, input.ExpectedRevision)
		if summaryErr != nil {
			return Draft{}, Revision{}, summaryErr
		}
		return Draft{}, Revision{}, &RevisionConflict{Expected: input.ExpectedRevision, Current: current.RevisionNo, Summaries: summaries}
	}
	if err := validateAIScope(reportID, current.Definition, input); err != nil {
		return Draft{}, Revision{}, err
	}
	updated, canonical, hash, err := operation.ApplyAndValidate(current.Definition, input.Operations)
	if err != nil {
		return Draft{}, Revision{}, err
	}
	if updated.Metadata.ID != reportID {
		return Draft{}, Revision{}, errors.New("report operation cannot change report ID")
	}
	reportUpdate, err := tx.Exec(ctx, `UPDATE platform.reports SET
		code=$1,name=$2,report_type=$3,updated_at=now() WHERE id=$4`,
		updated.Metadata.Code, updated.Metadata.Name, updated.Metadata.ReportType, reportID)
	if err != nil {
		return Draft{}, Revision{}, err
	}
	if reportUpdate.RowsAffected() != 1 {
		return Draft{}, Revision{}, ErrNotFound
	}
	bundleJSON, err := json.Marshal(input.Operations)
	if err != nil {
		return Draft{}, Revision{}, err
	}
	// Every revision keeps its exact pre-image. Operation payloads intentionally
	// contain only requested values, so update/move/reorder inverses cannot be
	// reconstructed safely from the current draft alone after a restart.
	beforeSnapshot := current.DefinitionRaw
	nextRevision := current.RevisionNo + 1
	var saved Draft
	row := tx.QueryRow(ctx, `UPDATE platform.report_drafts SET
		definition_json=$1,definition_hash=$2,schema_version=$3,revision_no=$4,updated_by=$5,updated_at=now()
		WHERE report_id=$6 AND revision_no=$7
		RETURNING report_id::text,tenant_id::text,definition_json,definition_hash,schema_version,
			revision_no,updated_by::text,updated_at`, canonical, hash, reportmodel.SchemaVersion,
		nextRevision, identity.ActorID, reportID, current.RevisionNo)
	if err := scanDraft(row, &saved); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Draft{}, Revision{}, &RevisionConflict{Expected: input.ExpectedRevision, Current: current.RevisionNo + 1}
		}
		return Draft{}, Revision{}, err
	}
	revisionID := askdata.ID(newUUID())
	var revision Revision
	row = tx.QueryRow(ctx, `INSERT INTO platform.report_revisions(
		id,tenant_id,report_id,revision_no,base_revision_no,source,operation_json,before_hash,
		after_hash,before_snapshot,inverse_of_revision_no,actor_user_id,ai_run_id
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NULLIF($11,0),$12,NULLIF($13,'')::uuid)
	RETURNING id::text,report_id::text,revision_no,base_revision_no,source,operation_json,
		before_hash,after_hash,COALESCE(before_snapshot,'null'::jsonb),inverse_of_revision_no,
		actor_user_id::text,COALESCE(ai_run_id::text,''),created_at`,
		revisionID, identity.TenantID, reportID, nextRevision, current.RevisionNo, input.Source,
		bundleJSON, current.DefinitionHash, hash, nullableJSON(beforeSnapshot), nullableInt(input.InverseOfRevisionNo),
		identity.ActorID, input.AIRunID)
	if err := scanRevision(row, &revision); err != nil {
		return Draft{}, Revision{}, err
	}
	if err := rebuildDraftIndexes(ctx, tx, identity.TenantID, reportID, nextRevision, compiler.BuildIndexes(updated)); err != nil {
		return Draft{}, Revision{}, err
	}
	if input.Source == string(operation.SourceAI) {
		for _, item := range input.Operations {
			raw, err := json.Marshal(item)
			if err != nil {
				return Draft{}, Revision{}, err
			}
			tag, err := tx.Exec(ctx, `WITH candidate AS (
				SELECT operation.id FROM platform.report_ai_operations operation
				JOIN platform.report_ai_runs run ON run.id=operation.ai_run_id AND run.tenant_id=operation.tenant_id
				WHERE operation.ai_run_id=$1 AND operation.validation_state='VALID'
				  AND operation.applied_revision_no IS NULL AND operation.operation_json=$2
				  AND run.report_id=$3 AND run.actor_user_id=$4 AND run.state='SUCCEEDED'
				ORDER BY operation.created_at,operation.id FOR UPDATE SKIP LOCKED LIMIT 1
			) UPDATE platform.report_ai_operations operation SET applied_revision_no=$5
			FROM candidate WHERE operation.id=candidate.id`, input.AIRunID, raw, reportID, identity.ActorID, nextRevision)
			if err != nil || tag.RowsAffected() != 1 {
				return Draft{}, Revision{}, errors.Join(err, errors.New("AI run is unavailable for operation audit"))
			}
		}
	}
	return saved, revision, nil
}

func authorizeSaveInput(
	ctx context.Context,
	tx pgx.Tx,
	identity Identity,
	input SaveInput,
) error {
	switch input.Source {
	case string(operation.SourceUser), string(operation.SourceImport), string(operation.SourceSystem):
		if input.AIRunID != "" || input.InverseOfRevisionNo != nil {
			return errors.New("non-AI revision contains incompatible source metadata")
		}
	case string(operation.SourceAI):
		if input.AIRunID.Validate() != nil || input.Scope == nil || input.InverseOfRevisionNo != nil {
			return errors.New("AI revision requires aiRunId and scope")
		}
		allowed, err := hasAIEditPermissionTx(ctx, tx, identity)
		if err != nil {
			return err
		}
		if !allowed {
			return ErrAIEditForbidden
		}
	case "UNDO", "REDO":
		if input.AIRunID != "" || input.Scope != nil || input.InverseOfRevisionNo == nil {
			return errors.New("inverse revision metadata is invalid")
		}
	default:
		return errors.New("revision source is invalid")
	}
	return nil
}

func validateAIScope(reportID askdata.ID, definition reportmodel.ReportDefinition, input SaveInput) error {
	if input.Source != string(operation.SourceAI) {
		return nil
	}
	aiRunID := input.AIRunID
	bundle := operation.Bundle{
		SchemaVersion: operation.SchemaVersion,
		ReportID:      reportID,
		BaseRevision:  input.ExpectedRevision,
		Source:        operation.SourceAI,
		AIRunID:       &aiRunID,
		Scope:         input.Scope,
		Operations:    input.Operations,
	}
	if err := bundle.Validate(); err != nil {
		return err
	}
	return operation.GuardAI(bundle, &definition)
}

func hasAIEditPermissionTx(ctx context.Context, tx pgx.Tx, identity Identity) (bool, error) {
	var allowed bool
	err := tx.QueryRow(ctx, `SELECT platform.is_system_access() OR EXISTS(
		SELECT 1 FROM platform.user_roles assignment
		JOIN platform.roles role ON role.id=assignment.role_id AND role.tenant_id=assignment.tenant_id
		JOIN platform.role_permissions grant_row ON grant_row.role_id=role.id AND grant_row.tenant_id=role.tenant_id
		JOIN platform.permissions permission ON permission.id=grant_row.permission_id
			AND permission.tenant_id=grant_row.tenant_id
		WHERE assignment.tenant_id=$1 AND assignment.user_id=$2
		  AND role.status='ACTIVE' AND role.deleted_at IS NULL
		  AND permission.code='report.ai_edit'
	)`, identity.TenantID, identity.ActorID).Scan(&allowed)
	return allowed, err
}

type InboundInput struct {
	IntentID           askdata.ID
	IdempotencyKeyHash askdata.ContentHash
	Bundle             operation.Bundle
}

type InboundResult struct {
	RevisionNo int64
	Replayed   bool
}

// ApplyInbound atomically persists the operation revision and its report-side
// idempotency receipt. A worker crash can therefore never create a second
// revision after the first commit.
func (store *PostgresStore) ApplyInbound(ctx context.Context, identity Identity, input InboundInput) (InboundResult, error) {
	if input.IntentID.Validate() != nil ||
		input.IdempotencyKeyHash.Validate() != nil || input.Bundle.Validate() != nil {
		return InboundResult{}, errors.New("invalid report inbound request")
	}
	if input.Bundle.ReportID.Validate() != nil {
		return InboundResult{}, errors.New("invalid inbound report ID")
	}
	var err error
	ctx, err = store.requestContext(ctx, identity, input.IntentID, input.Bundle.ReportID)
	if err != nil {
		return InboundResult{}, err
	}
	raw, err := json.Marshal(input.Bundle)
	if err != nil {
		return InboundResult{}, err
	}
	bundleHash := askdata.HashBytes(raw)
	result := InboundResult{}
	err = store.withTenantTx(ctx, string(identity.TenantID), func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO platform.report_inbound_idempotency(
			tenant_id,intent_id,actor_user_id,report_id,idempotency_key_hash,bundle_hash,state
		) VALUES($1,$2,$3,$4,$5,$6,'PROCESSING') ON CONFLICT DO NOTHING`,
			identity.TenantID, input.IntentID, identity.ActorID, input.Bundle.ReportID,
			input.IdempotencyKeyHash, bundleHash)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			var intentID, actorID, reportID askdata.ID
			var keyHash, storedBundleHash askdata.ContentHash
			var state string
			var revision *int64
			err := tx.QueryRow(ctx, `SELECT intent_id::text,actor_user_id::text,report_id::text,
				idempotency_key_hash,bundle_hash,state,applied_revision_no
				FROM platform.report_inbound_idempotency
				WHERE intent_id=$1 OR (actor_user_id=$2 AND idempotency_key_hash=$3) FOR UPDATE`,
				input.IntentID, identity.ActorID, input.IdempotencyKeyHash,
			).Scan(&intentID, &actorID, &reportID, &keyHash, &storedBundleHash, &state, &revision)
			if err != nil {
				return err
			}
			if intentID != input.IntentID || actorID != identity.ActorID || reportID != input.Bundle.ReportID ||
				keyHash != input.IdempotencyKeyHash || storedBundleHash != bundleHash {
				return ErrInboundConflict
			}
			if state != "APPLIED" || revision == nil {
				return ErrInboundConflict
			}
			result.RevisionNo, result.Replayed = *revision, true
			return nil
		}
		_, revision, err := saveDraftWithRevisionTx(ctx, tx, identity, input.Bundle.ReportID, SaveInput{
			ExpectedRevision: input.Bundle.BaseRevision, Operations: input.Bundle.Operations,
			Source: string(operation.SourceSystem),
		})
		if err != nil {
			return err
		}
		result.RevisionNo = revision.RevisionNo
		command, err := tx.Exec(ctx, `UPDATE platform.report_inbound_idempotency SET
			state='APPLIED',applied_revision_no=$1,updated_at=now()
			WHERE tenant_id=$2 AND intent_id=$3 AND state='PROCESSING'`,
			result.RevisionNo, identity.TenantID, input.IntentID)
		if err != nil || command.RowsAffected() != 1 {
			return errors.Join(err, ErrInboundConflict)
		}
		return nil
	})
	return result, err
}

func (store *PostgresStore) ListRevisions(ctx context.Context, identity Identity, reportID askdata.ID, limit int) ([]Revision, error) {
	var err error
	ctx, err = store.requestContext(ctx, identity, reportID)
	if err != nil {
		return nil, err
	}
	if limit < 1 || limit > 500 {
		return nil, errors.New("limit must be between 1 and 500")
	}
	result := []Revision{}
	err = store.withTenantTx(ctx, string(identity.TenantID), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text,report_id::text,revision_no,base_revision_no,source,
			operation_json,before_hash,after_hash,COALESCE(before_snapshot,'null'::jsonb),inverse_of_revision_no,
			actor_user_id::text,COALESCE(ai_run_id::text,''),created_at
			FROM platform.report_revisions WHERE report_id=$1 ORDER BY revision_no DESC LIMIT $2`, reportID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item Revision
			if err := scanRevision(rows, &item); err != nil {
				return err
			}
			result = append(result, item)
		}
		return rows.Err()
	})
	return result, err
}

type CreateVersionInput struct {
	ID                        askdata.ID
	SourceRevisionNo          int64
	Definition                reportmodel.ReportDefinition
	ObjectURI                 string
	RollbackOfVersionNo       *int
	RollbackReason            string
	StaleInsightsAcknowledged bool
	Operation                 string
	IdempotencyKey            string
	RequestHash               askdata.ContentHash
	Prepared                  *PreparedDefinition
}

func (store *PostgresStore) CreateVersion(ctx context.Context, identity Identity, reportID askdata.ID, input CreateVersionInput) (Version, error) {
	var err error
	ctx, err = store.requestContext(ctx, identity, reportID, input.ID)
	if err != nil {
		return Version{}, err
	}
	verified, err := Prepare(input.Definition)
	if err != nil {
		return Version{}, err
	}
	prepared := verified
	if input.Prepared != nil {
		if input.Prepared.Hash != verified.Hash || !bytes.Equal(input.Prepared.Canonical, verified.Canonical) ||
			!reflect.DeepEqual(input.Prepared.Definition, verified.Definition) ||
			!reflect.DeepEqual(input.Prepared.Indexes, verified.Indexes) {
			return Version{}, errors.New("prepared report version does not match its definition")
		}
		prepared = *input.Prepared
	}
	if prepared.Definition.Metadata.ID != reportID {
		return Version{}, errors.New("version definition metadata ID must match report ID")
	}
	if input.RollbackOfVersionNo != nil {
		if *input.RollbackOfVersionNo < 1 || input.Operation != "ROLLBACK" || input.RollbackReason != strings.TrimSpace(input.RollbackReason) ||
			input.RollbackReason == "" || utf8.RuneCountInString(input.RollbackReason) > 1000 ||
			strings.IndexFunc(input.RollbackReason, unicode.IsControl) >= 0 {
			return Version{}, errors.New("invalid report rollback version or reason")
		}
	} else if input.RollbackReason != "" || input.Operation == "ROLLBACK" {
		return Version{}, errors.New("report rollback metadata is incomplete")
	}
	var result Version
	err = store.withTenantTx(ctx, string(identity.TenantID), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT 1 FROM platform.reports WHERE id=$1 FOR UPDATE`, reportID); err != nil {
			return err
		}
		if input.IdempotencyKey != "" {
			if len(input.IdempotencyKey) < 8 || len(input.IdempotencyKey) > 128 ||
				(input.Operation != "PUBLISH" && input.Operation != "ROLLBACK") ||
				input.RequestHash.Validate() != nil {
				return errors.New("invalid report publication idempotency input")
			}
			if _, err := tx.Exec(ctx, `DELETE FROM platform.report_publication_idempotency
				WHERE report_id=$1 AND actor_user_id=$2 AND operation=$3
				  AND idempotency_key=$4 AND expires_at<=now()`, reportID, identity.ActorID,
				input.Operation, input.IdempotencyKey); err != nil {
				return err
			}
			var storedHash askdata.ContentHash
			row := tx.QueryRow(ctx, `SELECT receipt.request_hash,version.id::text,version.report_id::text,
				version.version_no,version.source_revision_no,version.definition_json,version.definition_hash,
				version.schema_version,version.object_uri,version.published_by::text,version.published_at,
				version.rollback_of_version_no,COALESCE(version.rollback_reason,''),
				version.stale_insights_acknowledged,version.artifact_state,
				version.artifact_attempt,version.artifact_next_attempt_at
				FROM platform.report_publication_idempotency receipt
				JOIN platform.report_versions version ON version.id=receipt.report_version_id
				 AND version.report_id=receipt.report_id AND version.tenant_id=receipt.tenant_id
				WHERE receipt.report_id=$1 AND receipt.actor_user_id=$2
				 AND receipt.operation=$3 AND receipt.idempotency_key=$4`, reportID, identity.ActorID,
				input.Operation, input.IdempotencyKey)
			err := row.Scan(&storedHash, &result.ID, &result.ReportID, &result.VersionNo,
				&result.SourceRevisionNo, &result.DefinitionRaw, &result.DefinitionHash,
				&result.SchemaVersion, &result.ObjectURI, &result.PublishedBy, &result.PublishedAt,
				&result.RollbackOfVersionNo, &result.RollbackReason,
				&result.StaleInsightsAcknowledged, &result.ArtifactState,
				&result.ArtifactAttempt, &result.ArtifactNextAttemptAt)
			if err == nil {
				if storedHash != input.RequestHash {
					return ErrPublicationConflict
				}
				if err := hydrateStoredDefinition(result.DefinitionRaw, result.DefinitionHash, &result.Definition, &result.DefinitionRaw); err != nil {
					return err
				}
				result.Replayed = true
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		if input.RollbackOfVersionNo != nil {
			var targetState, targetHash string
			var targetSourceRevision int64
			err := tx.QueryRow(ctx, `SELECT artifact_state,definition_hash,source_revision_no
				FROM platform.report_versions
				WHERE report_id=$1 AND version_no=$2`, reportID, *input.RollbackOfVersionNo).
				Scan(&targetState, &targetHash, &targetSourceRevision)
			if errors.Is(err, pgx.ErrNoRows) {
				return errors.Join(ErrNotFound, errors.New("report rollback target was not found"))
			}
			if err != nil {
				return err
			}
			if targetState != "READY" {
				return errors.New("report rollback target is not a completed published version")
			}
			if targetHash != prepared.Hash || targetSourceRevision != input.SourceRevisionNo {
				return errors.New("report rollback definition does not match its target version")
			}
		}
		var nextVersion int
		if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(version_no),0)+1 FROM platform.report_versions WHERE report_id=$1`, reportID).Scan(&nextVersion); err != nil {
			return err
		}
		row := tx.QueryRow(ctx, `INSERT INTO platform.report_versions(
			id,tenant_id,report_id,version_no,source_revision_no,definition_json,definition_bytes,
			definition_hash,schema_version,object_uri,published_by,rollback_of_version_no,
			rollback_reason,stale_insights_acknowledged,artifact_state
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,NULLIF($13,''),$14,'PENDING')
		RETURNING id::text,report_id::text,version_no,source_revision_no,definition_json,definition_hash,
			schema_version,object_uri,published_by::text,published_at,rollback_of_version_no,
			COALESCE(rollback_reason,''),stale_insights_acknowledged,artifact_state,
			artifact_attempt,artifact_next_attempt_at`,
			input.ID, identity.TenantID, reportID, nextVersion, input.SourceRevisionNo, prepared.Canonical,
			len(prepared.Canonical), prepared.Hash, reportmodel.SchemaVersion, input.ObjectURI, identity.ActorID,
			input.RollbackOfVersionNo, input.RollbackReason, input.StaleInsightsAcknowledged)
		if err := scanVersion(row, &result); err != nil {
			return err
		}
		if err := insertVersionIndexes(ctx, tx, identity.TenantID, reportID, result.ID, prepared.Indexes); err != nil {
			return err
		}
		if input.IdempotencyKey != "" {
			_, err := tx.Exec(ctx, `INSERT INTO platform.report_publication_idempotency(
				tenant_id,report_id,actor_user_id,operation,idempotency_key,request_hash,
				report_version_id,response_json
			) VALUES($1,$2,$3,$4,$5,$6,$7::uuid,jsonb_build_object('versionId',($7::uuid)::text))`,
				identity.TenantID, reportID, identity.ActorID, input.Operation,
				input.IdempotencyKey, input.RequestHash, result.ID)
			return err
		}
		return nil
	})
	return result, err
}

// CompletePublication is called only after the temporary object has been
// atomically promoted. The published pointer never references a missing object.
func (store *PostgresStore) CompletePublication(ctx context.Context, identity Identity, reportID, versionID askdata.ID) error {
	var err error
	ctx, err = store.requestContext(ctx, identity, reportID, versionID)
	if err != nil {
		return err
	}
	return store.withTenantTx(ctx, string(identity.TenantID), func(tx pgx.Tx) error {
		command, err := tx.Exec(ctx, `UPDATE platform.report_versions SET artifact_state='READY',
			artifact_lease_token=NULL,artifact_lease_expires_at=NULL,artifact_error_code='',
			artifact_next_attempt_at=now()
			WHERE id=$1 AND report_id=$2 AND artifact_state IN ('PENDING','RETRY')`, versionID, reportID)
		if err != nil {
			return err
		}
		if command.RowsAffected() == 0 {
			var ready bool
			if err := tx.QueryRow(ctx, `SELECT artifact_state='READY' FROM platform.report_versions
				WHERE id=$1 AND report_id=$2`, versionID, reportID).Scan(&ready); err != nil || !ready {
				return errors.Join(err, ErrNotFound)
			}
		}
		command, err = tx.Exec(ctx, `UPDATE platform.reports AS report SET current_published_version_id=$1
			WHERE report.id=$2 AND report.tenant_id=$3 AND (
			  report.current_published_version_id IS NULL OR
			  (SELECT current.version_no FROM platform.report_versions current
			   WHERE current.id=report.current_published_version_id)<
			  (SELECT candidate.version_no FROM platform.report_versions candidate WHERE candidate.id=$1)
			)`, versionID, reportID, identity.TenantID)
		if err != nil {
			return err
		}
		if command.RowsAffected() == 0 {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform.reports WHERE id=$1)`, reportID).Scan(&exists); err != nil || !exists {
				return errors.Join(err, ErrNotFound)
			}
		}
		return nil
	})
}

func (store *PostgresStore) MarkPublicationRetry(
	ctx context.Context, identity Identity, reportID, versionID askdata.ID, cause error,
) error {
	var err error
	ctx, err = store.requestContext(ctx, identity, reportID, versionID)
	if err != nil {
		return err
	}
	code := "REPORT_ARTIFACT_PROMOTE_FAILED"
	if cause == nil {
		code = "REPORT_ARTIFACT_RECOVERY_REQUIRED"
	}
	return store.withTenantTx(ctx, string(identity.TenantID), func(tx pgx.Tx) error {
		command, err := tx.Exec(ctx, `UPDATE platform.report_versions SET artifact_state='RETRY',
			artifact_next_attempt_at=now()+(LEAST(300,power(2,GREATEST(artifact_attempt,1))::integer)*interval '1 second'),
			artifact_lease_token=NULL,artifact_lease_expires_at=NULL,artifact_error_code=$1
			WHERE id=$2 AND report_id=$3 AND artifact_state<>'READY'`, code, versionID, reportID)
		if err != nil || command.RowsAffected() != 1 {
			return errors.Join(err, ErrNotFound)
		}
		return nil
	})
}

func (store *PostgresStore) GetVersion(ctx context.Context, identity Identity, reportID askdata.ID, versionNo *int) (Version, error) {
	var err error
	ctx, err = store.requestContext(ctx, identity, reportID)
	if err != nil {
		return Version{}, err
	}
	if versionNo != nil && *versionNo < 1 {
		return Version{}, errors.New("version number must be positive")
	}
	var result Version
	err = store.withTenantTx(ctx, string(identity.TenantID), func(tx pgx.Tx) error {
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM platform.reports WHERE id=$1`, reportID).Scan(&status); err != nil {
			return err
		}
		if status == "ARCHIVED" {
			return ErrReportOffline
		}
		query := `SELECT version.id::text,version.report_id::text,version.version_no,version.source_revision_no,
			version.definition_json,version.definition_hash,version.schema_version,version.object_uri,
			version.published_by::text,version.published_at,version.rollback_of_version_no,
			COALESCE(version.rollback_reason,''),version.stale_insights_acknowledged,version.artifact_state,
			version.artifact_attempt,version.artifact_next_attempt_at
			FROM platform.report_versions AS version JOIN platform.reports AS report
			ON report.id=version.report_id AND report.tenant_id=version.tenant_id WHERE version.report_id=$1 AND `
		args := []any{reportID}
		if versionNo == nil {
			query += `version.id=report.current_published_version_id`
		} else {
			query += `version.version_no=$2`
			args = append(args, *versionNo)
		}
		return scanVersion(tx.QueryRow(ctx, query, args...), &result)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Version{}, ErrNotFound
	}
	return result, err
}

func (store *PostgresStore) ListVersions(ctx context.Context, identity Identity, reportID askdata.ID, limit int) ([]Version, error) {
	var err error
	ctx, err = store.requestContext(ctx, identity, reportID)
	if err != nil {
		return nil, err
	}
	if limit < 1 || limit > 500 {
		return nil, errors.New("limit must be between 1 and 500")
	}
	result := []Version{}
	err = store.withTenantTx(ctx, string(identity.TenantID), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text,report_id::text,version_no,source_revision_no,
			definition_json,definition_hash,schema_version,object_uri,published_by::text,published_at,
			rollback_of_version_no,COALESCE(rollback_reason,''),stale_insights_acknowledged,
			artifact_state,artifact_attempt,artifact_next_attempt_at
			FROM platform.report_versions WHERE report_id=$1
			ORDER BY version_no DESC LIMIT $2`, reportID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item Version
			if err := scanVersion(rows, &item); err != nil {
				return err
			}
			result = append(result, item)
		}
		return rows.Err()
	})
	return result, err
}

func rebuildDraftIndexes(ctx context.Context, tx pgx.Tx, tenantID, reportID askdata.ID, revision int64, indexes compiler.Indexes) error {
	if _, err := tx.Exec(ctx, `DELETE FROM platform.report_draft_component_indexes WHERE report_id=$1`, reportID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM platform.report_draft_dependencies WHERE report_id=$1`, reportID); err != nil {
		return err
	}
	for _, item := range indexes.Components {
		if _, err := tx.Exec(ctx, `INSERT INTO platform.report_draft_component_indexes(
			report_id,tenant_id,revision_no,component_id,component_type,component_version,page_id,
			section_id,block_id,slot_id,binding_mode
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NULLIF($11,''))`, reportID, tenantID, revision,
			item.ComponentID, item.ComponentType, item.ComponentVersion, item.PageID, item.SectionID,
			item.BlockID, item.SlotID, item.BindingMode); err != nil {
			return err
		}
	}
	for _, item := range indexes.Dependencies {
		if _, err := tx.Exec(ctx, `INSERT INTO platform.report_draft_dependencies(
			report_id,tenant_id,dependency_type,dependency_id,component_ids
		) VALUES($1,$2,$3,$4,$5)`, reportID, tenantID, item.DependencyType, item.DependencyID, idsToStrings(item.ComponentIDs)); err != nil {
			return err
		}
	}
	return nil
}

func insertVersionIndexes(ctx context.Context, tx pgx.Tx, tenantID, reportID, versionID askdata.ID, indexes compiler.Indexes) error {
	for _, item := range indexes.Components {
		if _, err := tx.Exec(ctx, `INSERT INTO platform.report_version_component_indexes(
			report_version_id,report_id,tenant_id,component_id,component_type,component_version,
			page_id,section_id,block_id,slot_id,binding_mode
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NULLIF($11,''))`, versionID, reportID, tenantID,
			item.ComponentID, item.ComponentType, item.ComponentVersion, item.PageID, item.SectionID,
			item.BlockID, item.SlotID, item.BindingMode); err != nil {
			return err
		}
	}
	for _, item := range indexes.Dependencies {
		if _, err := tx.Exec(ctx, `INSERT INTO platform.report_version_dependencies(
			report_version_id,report_id,tenant_id,dependency_type,dependency_id,component_ids
		) VALUES($1,$2,$3,$4,$5,$6)`, versionID, reportID, tenantID, item.DependencyType,
			item.DependencyID, idsToStrings(item.ComponentIDs)); err != nil {
			return err
		}
	}
	return nil
}

func loadDraftTx(ctx context.Context, tx pgx.Tx, reportID askdata.ID, lock bool) (Draft, error) {
	query := `SELECT report_id::text,tenant_id::text,definition_json,definition_hash,schema_version,
		revision_no,updated_by::text,updated_at FROM platform.report_drafts WHERE report_id=$1`
	if lock {
		query += ` FOR UPDATE`
	}
	var result Draft
	if err := scanDraft(tx.QueryRow(ctx, query, reportID), &result); err != nil {
		return Draft{}, err
	}
	return result, nil
}

type scanner interface{ Scan(...any) error }

func scanReport(row scanner, destination *Report) error {
	return row.Scan(&destination.ID, &destination.TenantID, &destination.DomainID, &destination.Code,
		&destination.Name, &destination.ReportType, &destination.OwnerUserID,
		&destination.CurrentPublishedVersionID, &destination.Status, &destination.CreatedAt, &destination.UpdatedAt)
}

func scanDraft(row scanner, destination *Draft) error {
	var raw []byte
	if err := row.Scan(&destination.ReportID, &destination.TenantID, &raw, &destination.DefinitionHash,
		&destination.SchemaVersion, &destination.RevisionNo, &destination.UpdatedBy, &destination.UpdatedAt); err != nil {
		return err
	}
	return hydrateStoredDefinition(raw, destination.DefinitionHash, &destination.Definition, &destination.DefinitionRaw)
}

func scanRevision(row scanner, destination *Revision) error {
	var snapshot []byte
	if err := row.Scan(&destination.ID, &destination.ReportID, &destination.RevisionNo,
		&destination.BaseRevisionNo, &destination.Source, &destination.OperationJSON,
		&destination.BeforeHash, &destination.AfterHash, &snapshot, &destination.InverseOfRevisionNo,
		&destination.ActorUserID, &destination.AIRunID, &destination.CreatedAt); err != nil {
		return err
	}
	if string(snapshot) != "null" {
		destination.BeforeSnapshot = snapshot
	}
	return nil
}

func scanVersion(row scanner, destination *Version) error {
	var raw []byte
	if err := row.Scan(&destination.ID, &destination.ReportID, &destination.VersionNo,
		&destination.SourceRevisionNo, &raw, &destination.DefinitionHash, &destination.SchemaVersion,
		&destination.ObjectURI, &destination.PublishedBy, &destination.PublishedAt,
		&destination.RollbackOfVersionNo, &destination.RollbackReason,
		&destination.StaleInsightsAcknowledged, &destination.ArtifactState,
		&destination.ArtifactAttempt, &destination.ArtifactNextAttemptAt); err != nil {
		return err
	}
	return hydrateStoredDefinition(raw, destination.DefinitionHash, &destination.Definition, &destination.DefinitionRaw)
}

func hydrateStoredDefinition(
	raw []byte, expectedHash string, definition *reportmodel.ReportDefinition, canonicalRaw *json.RawMessage,
) error {
	if err := json.Unmarshal(raw, definition); err != nil {
		return fmt.Errorf("decode stored report definition: %w", err)
	}
	canonical, hash, err := compiler.Normalize(*definition)
	if err != nil {
		return fmt.Errorf("normalize stored report definition: %w", err)
	}
	if hash != expectedHash {
		return errors.New("stored report definition hash mismatch")
	}
	if err := json.Unmarshal(canonical, definition); err != nil {
		return fmt.Errorf("decode canonical report definition: %w", err)
	}
	*canonicalRaw = append((*canonicalRaw)[:0], canonical...)
	return nil
}

func revisionSummariesTx(ctx context.Context, tx pgx.Tx, reportID askdata.ID, baseRevision int64) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT revision_no,operation_json FROM platform.report_revisions
		WHERE report_id=$1 AND revision_no>$2 ORDER BY revision_no LIMIT 100`, reportID, baseRevision)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var revision int64
		var raw []byte
		if err := rows.Scan(&revision, &raw); err != nil {
			return nil, err
		}
		var envelopes []struct {
			Op string `json:"op"`
		}
		if json.Unmarshal(raw, &envelopes) != nil {
			result = append(result, fmt.Sprintf("r%d", revision))
			continue
		}
		names := make([]string, 0, len(envelopes))
		for _, envelope := range envelopes {
			names = append(names, envelope.Op)
		}
		result = append(result, fmt.Sprintf("r%d:%s", revision, strings.Join(names, ",")))
	}
	return result, rows.Err()
}

func containsSnapshotOperation(operations []operation.Operation) bool {
	for _, item := range operations {
		if item.Op == operation.TemplateApply || item.Op == operation.ThemeUpdate {
			return true
		}
	}
	return false
}

func idsToStrings(ids []askdata.ID) []string {
	result := make([]string, len(ids))
	for index, id := range ids {
		result[index] = string(id)
	}
	return result
}

func nullableJSON(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}

func nullableInt(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func newUUID() string {
	return uuid.NewString()
}
