// Package registryimport owns durable semantic-asset import batches. It is a
// foundation layer: asset-specific parsing and four-layer business validation
// are supplied by IMPORT-003, while object-specific DRAFT creators are supplied
// by IMPORT-004.
package registryimport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/registry"
	"intelligent-report-generation-system/internal/platform/database"
)

const (
	MaxRowWriteBatch = 500
	MaxImportRows    = 100_000
)

var (
	ErrInvalidImport       = errors.New("semantic import request is invalid")
	ErrImportNotFound      = errors.New("semantic import was not found")
	ErrImportLeaseLost     = errors.New("semantic import validation lease was lost")
	ErrImportConflict      = errors.New("semantic import conflicts with persisted facts")
	ErrIllegalTransition   = errors.New("semantic import state transition is illegal")
	ErrInvalidImportRow    = errors.New("semantic import row is invalid")
	ErrImportDraftRejected = errors.New("semantic import DRAFT creation was rejected")
	ErrImportImpactAck     = errors.New("semantic import impact review must be acknowledged")
	ErrImportPermission    = errors.New("semantic import operation requires domain owner permission")
)

type RowOperationError struct {
	RowNo int
	Err   error
}

func (err *RowOperationError) Error() string {
	if err == nil {
		return "semantic import row operation failed"
	}
	return fmt.Sprintf("semantic import row %d: %v", err.RowNo, err.Err)
}

func (err *RowOperationError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Err
}

type AssetType string

const (
	AssetModel            AssetType = "MODEL"
	AssetMeasure          AssetType = "MEASURE"
	AssetMetric           AssetType = "METRIC"
	AssetMetricDimension  AssetType = "METRIC_DIMENSION"
	AssetDimension        AssetType = "DIMENSION"
	AssetMember           AssetType = "MEMBER"
	AssetHierarchy        AssetType = "HIERARCHY"
	AssetRelationship     AssetType = "RELATIONSHIP"
	AssetTerm             AssetType = "TERM"
	AssetCertifiedExample AssetType = "CERTIFIED_EXAMPLE"
	AssetKPIBundle        AssetType = "KPI_BUNDLE"
	AssetEvalCase         AssetType = "EVAL_CASE"
)

func (value AssetType) Valid() bool {
	switch value {
	case AssetModel, AssetMeasure, AssetMetric, AssetMetricDimension, AssetDimension,
		AssetMember, AssetHierarchy, AssetRelationship, AssetTerm,
		AssetCertifiedExample, AssetKPIBundle, AssetEvalCase:
		return true
	default:
		return false
	}
}

type State string

const (
	StateUploaded           State = "UPLOADED"
	StateValidating         State = "VALIDATING"
	StateValidated          State = "VALIDATED"
	StatePartiallyCommitted State = "PARTIALLY_COMMITTED"
	StateCommitted          State = "COMMITTED"
	StateWithdrawn          State = "WITHDRAWN"
	StateFailed             State = "FAILED"
)

func (value State) Valid() bool {
	switch value {
	case StateUploaded, StateValidating, StateValidated, StatePartiallyCommitted,
		StateCommitted, StateWithdrawn, StateFailed:
		return true
	default:
		return false
	}
}

func CanTransition(from, to State) bool {
	switch from {
	case StateUploaded:
		return to == StateValidating
	case StateValidating:
		return to == StateValidating || to == StateValidated || to == StateFailed
	case StateValidated:
		return to == StatePartiallyCommitted || to == StateCommitted || to == StateWithdrawn
	case StatePartiallyCommitted:
		return to == StatePartiallyCommitted || to == StateCommitted || to == StateWithdrawn
	case StateCommitted:
		return to == StateWithdrawn
	default:
		return false
	}
}

type RowState string

const (
	RowValid     RowState = "VALID"
	RowInvalid   RowState = "INVALID"
	RowSkipped   RowState = "SKIPPED"
	RowCommitted RowState = "COMMITTED"
)

type ValidationIssue struct {
	Column   string `json:"column"`
	Code     string `json:"code"`
	Message  string `json:"message"`
	Expected string `json:"expected"`
	Actual   string `json:"actual,omitempty"`
}

type SemanticImport struct {
	ID                    string     `json:"id"`
	TenantID              string     `json:"tenantId"`
	DomainID              string     `json:"domainId"`
	AssetType             AssetType  `json:"assetType"`
	FileObjectURI         string     `json:"fileObjectUri"`
	FileHash              string     `json:"fileHash"`
	FileName              string     `json:"fileName"`
	State                 State      `json:"state"`
	TotalRows             int        `json:"totalRows"`
	ValidRows             int        `json:"validRows"`
	InvalidRows           int        `json:"invalidRows"`
	FailureReason         string     `json:"failureReason,omitempty"`
	CreatedBy             string     `json:"createdBy"`
	Attempt               int        `json:"attempt"`
	LeaseOwner            string     `json:"-"`
	LeaseToken            string     `json:"-"`
	LeaseExpiresAt        *time.Time `json:"-"`
	StartedAt             *time.Time `json:"startedAt,omitempty"`
	ValidationCompletedAt *time.Time `json:"validationCompletedAt,omitempty"`
	CommittedAt           *time.Time `json:"committedAt,omitempty"`
	CreatedAt             time.Time  `json:"createdAt"`
	UpdatedAt             time.Time  `json:"updatedAt"`
}

type CreateImportInput struct {
	TenantID      string
	DomainID      string
	AssetType     AssetType
	FileObjectURI string
	FileHash      string
	FileName      string
	CreatedBy     string
}

type Claim struct {
	ImportID, TenantID, DomainID string
	AssetType                    AssetType
	FileObjectURI, FileHash      string
	FileName                     string
	LeaseToken                   string
	Attempt, ResumeAfterRow      int
}

type ValidatedRow struct {
	RowNo          int
	RawJSON        json.RawMessage
	NormalizedJSON json.RawMessage
	State          RowState
	Errors         []ValidationIssue
}

type ImportRow struct {
	ID, TenantID, ImportID  string
	RowNo                   int
	RawJSON, NormalizedJSON json.RawMessage
	ValidationState         RowState
	Errors                  []ValidationIssue
	CreatedObjectID         string
	CreatedVersionID        string
}

type DraftReference struct {
	ObjectID  string
	VersionID string
	Status    string
}

type DraftCreator interface {
	CreateDraft(context.Context, pgx.Tx, SemanticImport, ImportRow) (DraftReference, error)
}

type DraftWithdrawer interface {
	WithdrawDrafts(context.Context, pgx.Tx, SemanticImport, []ImportRow) error
}

type WithdrawalRejection struct {
	RowNo      int      `json:"rowNo"`
	VersionID  string   `json:"versionId"`
	Reason     string   `json:"reason"`
	References []string `json:"references,omitempty"`
}

type SelectiveDraftWithdrawer interface {
	WithdrawDraftsSelective(
		context.Context, pgx.Tx, SemanticImport, []ImportRow,
	) ([]WithdrawalRejection, error)
}

type CommitSelection struct {
	RowNumbers        []int
	AcknowledgeImpact bool
}

type PostgresStore struct{ pool *pgxpool.Pool }

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

func (store *PostgresStore) CreateImport(
	ctx context.Context,
	input CreateImportInput,
) (SemanticImport, bool, error) {
	if store == nil || store.pool == nil || validateCreateInput(input) != nil {
		return SemanticImport{}, false, ErrInvalidImport
	}
	created := false
	result := SemanticImport{}
	err := database.WithTenantTx(ctx, store.pool, input.TenantID, func(tx pgx.Tx) error {
		id := uuid.NewString()
		tag, err := tx.Exec(ctx, `INSERT INTO askdata.semantic_imports(
			id,tenant_id,domain_id,asset_type,file_object_uri,file_hash,file_name,created_by
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT(tenant_id,file_hash,asset_type) DO NOTHING`,
			id, input.TenantID, input.DomainID, input.AssetType, input.FileObjectURI,
			input.FileHash, input.FileName, input.CreatedBy)
		if err != nil {
			return err
		}
		created = tag.RowsAffected() == 1
		if created {
			if err := scanImport(tx.QueryRow(ctx, importSelect+` WHERE id=$1`, id), &result); err != nil {
				return err
			}
			return insertAudit(ctx, tx, result, input.CreatedBy, "SEMANTIC_IMPORT_UPLOADED", map[string]any{
				"assetType": result.AssetType, "fileHash": result.FileHash,
			})
		}
		if err := scanImport(tx.QueryRow(ctx, importSelect+`
			WHERE file_hash=$1 AND asset_type=$2`, input.FileHash, input.AssetType), &result); err != nil {
			return err
		}
		if result.DomainID != input.DomainID {
			return ErrImportConflict
		}
		return nil
	})
	return result, created, normalizeStoreError(err)
}

func (store *PostgresStore) Get(
	ctx context.Context,
	tenantID, domainID, importID string,
) (SemanticImport, error) {
	if store == nil || store.pool == nil || !canonicalUUID(tenantID) ||
		!canonicalUUID(domainID) || !canonicalUUID(importID) {
		return SemanticImport{}, ErrInvalidImport
	}
	var result SemanticImport
	err := database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		return scanImport(tx.QueryRow(ctx, importSelect+` WHERE id=$1 AND domain_id=$2`, importID, domainID), &result)
	})
	return result, normalizeStoreError(err)
}

// ListRows returns the immutable row facts for a validation report. Tenant and
// domain are both checked before rows are read so an import UUID cannot be used
// as a cross-domain oracle.
func (store *PostgresStore) ListRows(
	ctx context.Context,
	tenantID, domainID, importID string,
) ([]ImportRow, error) {
	if store == nil || store.pool == nil || !canonicalUUID(tenantID) ||
		!canonicalUUID(domainID) || !canonicalUUID(importID) {
		return nil, ErrInvalidImport
	}
	result := []ImportRow{}
	err := database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1 FROM askdata.semantic_imports WHERE id=$1 AND domain_id=$2
		)`, importID, domainID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return pgx.ErrNoRows
		}
		rows, err := tx.Query(ctx, `SELECT id::text,tenant_id::text,import_id::text,
			row_no,raw_json,normalized_json,validation_state,errors_json,
			COALESCE(created_object_id::text,''),COALESCE(created_version_id::text,'')
			FROM askdata.semantic_import_rows WHERE import_id=$1 ORDER BY row_no`, importID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var row ImportRow
			var errorsJSON json.RawMessage
			if err := rows.Scan(
				&row.ID, &row.TenantID, &row.ImportID, &row.RowNo,
				&row.RawJSON, &row.NormalizedJSON, &row.ValidationState, &errorsJSON,
				&row.CreatedObjectID, &row.CreatedVersionID,
			); err != nil {
				return err
			}
			if err := json.Unmarshal(errorsJSON, &row.Errors); err != nil {
				return ErrImportConflict
			}
			result = append(result, row)
		}
		return rows.Err()
	})
	return result, normalizeStoreError(err)
}

func (store *PostgresStore) ListTenantIDs(ctx context.Context) ([]string, error) {
	if store == nil || store.pool == nil {
		return nil, ErrInvalidImport
	}
	rows, err := store.pool.Query(ctx, `SELECT tenant_id::text FROM askdata.list_semantic_import_tenants()`)
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

func (store *PostgresStore) ClaimForValidation(
	ctx context.Context,
	tenantID, workerID string,
	lease time.Duration,
) (*Claim, error) {
	if store == nil || store.pool == nil || !canonicalUUID(tenantID) ||
		!validWorker(workerID) || lease < 30*time.Second || lease > 10*time.Minute {
		return nil, ErrInvalidImport
	}
	claim := Claim{TenantID: tenantID}
	err := store.pool.QueryRow(ctx, `SELECT import_id::text,domain_id::text,asset_type,
		file_object_uri,file_hash,file_name,lease_token::text,attempt,resume_after_row
		FROM askdata.claim_semantic_import($1,$2,$3)`,
		tenantID, workerID, int64(lease/time.Second)).Scan(
		&claim.ImportID, &claim.DomainID, &claim.AssetType, &claim.FileObjectURI,
		&claim.FileHash, &claim.FileName, &claim.LeaseToken, &claim.Attempt, &claim.ResumeAfterRow,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if validateClaim(claim, tenantID) != nil {
		return &claim, ErrInvalidImport
	}
	return &claim, nil
}

func (store *PostgresStore) Heartbeat(
	ctx context.Context,
	claim Claim,
	workerID string,
	lease time.Duration,
) error {
	if store == nil || store.pool == nil || validateClaim(claim, claim.TenantID) != nil ||
		!validWorker(workerID) || lease < 30*time.Second || lease > 10*time.Minute {
		return ErrInvalidImport
	}
	var current bool
	if err := store.pool.QueryRow(ctx, `SELECT askdata.heartbeat_semantic_import($1,$2,$3,$4,$5)`,
		claim.TenantID, claim.ImportID, workerID, claim.LeaseToken, int64(lease/time.Second)).Scan(&current); err != nil {
		return err
	}
	if !current {
		return ErrImportLeaseLost
	}
	return nil
}

func (store *PostgresStore) UpsertRows(
	ctx context.Context,
	claim Claim,
	workerID string,
	rows []ValidatedRow,
) error {
	if store == nil || store.pool == nil || validateClaim(claim, claim.TenantID) != nil ||
		!validWorker(workerID) || len(rows) < 1 || len(rows) > MaxRowWriteBatch {
		return ErrInvalidImport
	}
	prepared := make([]ValidatedRow, len(rows))
	previous := claim.ResumeAfterRow
	for index, row := range rows {
		canonical, err := normalizeValidatedRow(row)
		if err != nil || canonical.RowNo <= previous {
			return ErrInvalidImportRow
		}
		prepared[index] = canonical
		previous = canonical.RowNo
	}
	return database.WithTenantTx(ctx, store.pool, claim.TenantID, func(tx pgx.Tx) error {
		if err := requireLease(ctx, tx, claim, workerID); err != nil {
			return err
		}
		for _, row := range prepared {
			errorsJSON, _ := json.Marshal(row.Errors)
			tag, err := tx.Exec(ctx, `INSERT INTO askdata.semantic_import_rows(
				id,tenant_id,import_id,row_no,raw_json,normalized_json,validation_state,errors_json
			) VALUES($1,$2,$3,$4,$5,NULLIF($6,'null')::jsonb,$7,$8)
			ON CONFLICT(import_id,row_no) DO NOTHING`, uuid.NewString(), claim.TenantID,
				claim.ImportID, row.RowNo, row.RawJSON, nullableJSON(row.NormalizedJSON), row.State, errorsJSON)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 1 {
				continue
			}
			var identical bool
			if err := tx.QueryRow(ctx, `SELECT raw_json=$3::jsonb
				AND normalized_json IS NOT DISTINCT FROM NULLIF($4,'null')::jsonb
				AND validation_state=$5 AND errors_json=$6::jsonb
				FROM askdata.semantic_import_rows WHERE import_id=$1 AND row_no=$2`,
				claim.ImportID, row.RowNo, row.RawJSON, nullableJSON(row.NormalizedJSON), row.State, errorsJSON).
				Scan(&identical); err != nil {
				return err
			}
			if !identical {
				return ErrImportConflict
			}
		}
		return nil
	})
}

func (store *PostgresStore) CompleteValidation(
	ctx context.Context,
	claim Claim,
	workerID string,
) error {
	if store == nil || store.pool == nil || validateClaim(claim, claim.TenantID) != nil || !validWorker(workerID) {
		return ErrInvalidImport
	}
	return database.WithTenantTx(ctx, store.pool, claim.TenantID, func(tx pgx.Tx) error {
		if err := requireLease(ctx, tx, claim, workerID); err != nil {
			return err
		}
		var total, valid, invalid int
		if err := tx.QueryRow(ctx, `SELECT count(*)::integer,
			count(*) FILTER(WHERE validation_state='VALID')::integer,
			count(*) FILTER(WHERE validation_state='INVALID')::integer
			FROM askdata.semantic_import_rows WHERE import_id=$1`, claim.ImportID).
			Scan(&total, &valid, &invalid); err != nil {
			return err
		}
		if total < 1 {
			return ErrInvalidImportRow
		}
		tag, err := tx.Exec(ctx, `UPDATE askdata.semantic_imports SET
			state='VALIDATED',total_rows=$1,valid_rows=$2,invalid_rows=$3,
			lease_owner='',lease_token=NULL,lease_expires_at=NULL,
			validation_completed_at=now()
			WHERE id=$4 AND state='VALIDATING' AND lease_owner=$5 AND lease_token=$6
			  AND lease_expires_at>now()`, total, valid, invalid, claim.ImportID, workerID, claim.LeaseToken)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrImportLeaseLost
		}
		batch := SemanticImport{
			ID: claim.ImportID, TenantID: claim.TenantID, DomainID: claim.DomainID,
			AssetType: claim.AssetType, FileHash: claim.FileHash,
		}
		return insertAudit(ctx, tx, batch, "", "SEMANTIC_IMPORT_VALIDATED", map[string]any{
			"totalRows": total, "validRows": valid, "invalidRows": invalid,
		})
	})
}

func (store *PostgresStore) FailValidation(
	ctx context.Context,
	claim Claim,
	workerID, reason string,
) error {
	reason = strings.TrimSpace(reason)
	if store == nil || store.pool == nil || validateClaim(claim, claim.TenantID) != nil ||
		!validWorker(workerID) || !boundedText(reason, 1024) {
		return ErrInvalidImport
	}
	return database.WithTenantTx(ctx, store.pool, claim.TenantID, func(tx pgx.Tx) error {
		if err := requireLease(ctx, tx, claim, workerID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE askdata.semantic_imports SET
			state='FAILED',failure_reason=$1,lease_owner='',lease_token=NULL,
			lease_expires_at=NULL,validation_completed_at=now()
			WHERE id=$2 AND state='VALIDATING' AND lease_owner=$3 AND lease_token=$4
			  AND lease_expires_at>now()`, reason, claim.ImportID, workerID, claim.LeaseToken)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrImportLeaseLost
		}
		batch := SemanticImport{
			ID: claim.ImportID, TenantID: claim.TenantID, DomainID: claim.DomainID,
			AssetType: claim.AssetType, FileHash: claim.FileHash,
		}
		return insertAudit(ctx, tx, batch, "", "SEMANTIC_IMPORT_FAILED", map[string]any{
			"reason": reason,
		})
	})
}

// CommitValidRows provides the atomic DRAFT-only transaction boundary used by
// IMPORT-004. The creator must insert through the supplied transaction and
// return a DRAFT reference; INVALID/SKIPPED rows can never be selected.
func (store *PostgresStore) CommitValidRows(
	ctx context.Context,
	tenantID, domainID, actorID, importID string,
	rowNumbers []int,
	creator DraftCreator,
) (State, []DraftReference, error) {
	return store.CommitRows(ctx, tenantID, domainID, actorID, importID, CommitSelection{
		RowNumbers: rowNumbers,
	}, creator)
}

// CommitRows atomically turns selected VALID rows into DRAFT object versions.
// An empty RowNumbers selection means every remaining VALID row.
func (store *PostgresStore) CommitRows(
	ctx context.Context,
	tenantID, domainID, actorID, importID string,
	selection CommitSelection,
	creator DraftCreator,
) (State, []DraftReference, error) {
	if store == nil || store.pool == nil || creator == nil || !canonicalUUID(tenantID) ||
		!canonicalUUID(domainID) || !canonicalUUID(actorID) || !canonicalUUID(importID) ||
		len(selection.RowNumbers) > MaxImportRows {
		return "", nil, ErrInvalidImport
	}
	numbers := append([]int(nil), selection.RowNumbers...)
	sort.Ints(numbers)
	for index, value := range numbers {
		if value < 1 || index > 0 && value == numbers[index-1] {
			return "", nil, ErrInvalidImport
		}
	}
	resultState := State("")
	references := []DraftReference{}
	err := database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		if err := requireImportOwner(ctx, tx, tenantID, domainID, actorID); err != nil {
			return err
		}
		var batch SemanticImport
		if err := scanImport(tx.QueryRow(ctx, importSelect+`
			WHERE id=$1 AND domain_id=$2 FOR UPDATE`, importID, domainID), &batch); err != nil {
			return err
		}
		if batch.State != StateValidated && batch.State != StatePartiallyCommitted && batch.State != StateCommitted {
			return ErrIllegalTransition
		}
		query := `SELECT id::text,tenant_id::text,import_id::text,row_no,raw_json,
			normalized_json,validation_state,errors_json,
			COALESCE(created_object_id::text,''),COALESCE(created_version_id::text,'')
			FROM askdata.semantic_import_rows
			WHERE import_id=$1 AND validation_state='VALID'`
		args := []any{importID}
		if len(numbers) > 0 {
			query += ` AND row_no=ANY($2)`
			args = append(args, numbers)
		}
		query += ` ORDER BY row_no FOR UPDATE`
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return err
		}
		selected := []ImportRow{}
		for rows.Next() {
			var row ImportRow
			var errorsJSON json.RawMessage
			if err := rows.Scan(&row.ID, &row.TenantID, &row.ImportID, &row.RowNo,
				&row.RawJSON, &row.NormalizedJSON, &row.ValidationState, &errorsJSON,
				&row.CreatedObjectID, &row.CreatedVersionID); err != nil {
				rows.Close()
				return err
			}
			if json.Unmarshal(errorsJSON, &row.Errors) != nil {
				rows.Close()
				return ErrImportConflict
			}
			selected = append(selected, row)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if len(selected) == 0 || len(numbers) > 0 && len(selected) != len(numbers) || len(selected) > MaxImportRows {
			return ErrInvalidImportRow
		}
		for _, row := range selected {
			if !selection.AcknowledgeImpact && rowHasImpactWarning(row) {
				return &RowOperationError{RowNo: row.RowNo, Err: ErrImportImpactAck}
			}
			reference, err := creator.CreateDraft(ctx, tx, batch, row)
			if err != nil {
				return &RowOperationError{RowNo: row.RowNo, Err: err}
			}
			if reference.Status != "DRAFT" || !canonicalUUID(reference.ObjectID) || !canonicalUUID(reference.VersionID) {
				return ErrImportDraftRejected
			}
			if _, err := tx.Exec(ctx, `UPDATE askdata.semantic_import_rows SET
				validation_state='COMMITTED',created_object_id=$1,created_version_id=$2
				WHERE id=$3 AND validation_state='VALID'`,
				reference.ObjectID, reference.VersionID, row.ID); err != nil {
				return err
			}
			references = append(references, reference)
		}
		var remainingValid, invalid int
		if err := tx.QueryRow(ctx, `SELECT
			count(*) FILTER(WHERE validation_state='VALID')::integer,
			count(*) FILTER(WHERE validation_state='INVALID')::integer
			FROM askdata.semantic_import_rows WHERE import_id=$1`, importID).
			Scan(&remainingValid, &invalid); err != nil {
			return err
		}
		resultState = StatePartiallyCommitted
		if remainingValid == 0 && invalid == 0 {
			resultState = StateCommitted
		}
		if _, err := tx.Exec(ctx, `UPDATE askdata.semantic_imports SET
			state=$1,committed_at=COALESCE(committed_at,now()) WHERE id=$2`, resultState, importID); err != nil {
			return err
		}
		versionIDs := make([]string, len(references))
		for index, reference := range references {
			versionIDs[index] = reference.VersionID
		}
		return insertAudit(ctx, tx, batch, actorID, "SEMANTIC_IMPORT_COMMITTED", map[string]any{
			"rowCount": len(references), "state": resultState, "versionIds": versionIDs,
		})
	})
	return resultState, references, normalizeStoreError(err)
}

// WithdrawImport supplies the atomic batch boundary used by IMPORT-004. The
// injected implementation must reject any created version that is no longer a
// removable DRAFT or has entered a Release; the state changes only after all
// eligible DRAFT deletions succeed in the same transaction.
func (store *PostgresStore) WithdrawImport(
	ctx context.Context,
	tenantID, domainID, actorID, importID string,
	withdrawer DraftWithdrawer,
) error {
	if withdrawer == nil {
		return ErrInvalidImport
	}
	_, err := store.withdrawImport(ctx, tenantID, domainID, actorID, importID, "", func(
		ctx context.Context, tx pgx.Tx, batch SemanticImport, rows []ImportRow,
	) ([]WithdrawalRejection, error) {
		return nil, withdrawer.WithdrawDrafts(ctx, tx, batch, rows)
	})
	return err
}

func (store *PostgresStore) WithdrawImportSelective(
	ctx context.Context,
	tenantID, domainID, actorID, importID, reason string,
	withdrawer SelectiveDraftWithdrawer,
) ([]WithdrawalRejection, error) {
	if withdrawer == nil || reason != strings.TrimSpace(reason) || !boundedText(reason, 2000) {
		return nil, ErrInvalidImport
	}
	return store.withdrawImport(ctx, tenantID, domainID, actorID, importID, reason,
		withdrawer.WithdrawDraftsSelective)
}

func (store *PostgresStore) withdrawImport(
	ctx context.Context,
	tenantID, domainID, actorID, importID, reason string,
	withdraw func(context.Context, pgx.Tx, SemanticImport, []ImportRow) ([]WithdrawalRejection, error),
) ([]WithdrawalRejection, error) {
	if store == nil || store.pool == nil || withdraw == nil ||
		!canonicalUUID(tenantID) || !canonicalUUID(domainID) ||
		!canonicalUUID(actorID) || !canonicalUUID(importID) {
		return nil, ErrInvalidImport
	}
	rejections := []WithdrawalRejection{}
	err := database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		if err := requireImportOwner(ctx, tx, tenantID, domainID, actorID); err != nil {
			return err
		}
		var batch SemanticImport
		if err := scanImport(tx.QueryRow(ctx, importSelect+`
			WHERE id=$1 AND domain_id=$2 FOR UPDATE`, importID, domainID), &batch); err != nil {
			return err
		}
		if batch.State != StateValidated && batch.State != StatePartiallyCommitted && batch.State != StateCommitted {
			return ErrIllegalTransition
		}
		rows, err := tx.Query(ctx, `SELECT id::text,tenant_id::text,import_id::text,
			row_no,raw_json,normalized_json,validation_state,errors_json,
			COALESCE(created_object_id::text,''),COALESCE(created_version_id::text,'')
			FROM askdata.semantic_import_rows
			WHERE import_id=$1 AND validation_state='COMMITTED'
			ORDER BY row_no FOR UPDATE`, importID)
		if err != nil {
			return err
		}
		committed := []ImportRow{}
		for rows.Next() {
			var row ImportRow
			var errorsJSON json.RawMessage
			if err := rows.Scan(
				&row.ID, &row.TenantID, &row.ImportID, &row.RowNo,
				&row.RawJSON, &row.NormalizedJSON, &row.ValidationState, &errorsJSON,
				&row.CreatedObjectID, &row.CreatedVersionID,
			); err != nil {
				rows.Close()
				return err
			}
			if json.Unmarshal(errorsJSON, &row.Errors) != nil {
				rows.Close()
				return ErrImportConflict
			}
			committed = append(committed, row)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if batch.State == StatePartiallyCommitted && len(committed) == 0 {
			return ErrImportConflict
		}
		var withdrawErr error
		rejections, withdrawErr = withdraw(ctx, tx, batch, committed)
		if withdrawErr != nil {
			return withdrawErr
		}
		if _, err := tx.Exec(ctx, `UPDATE askdata.semantic_imports
			SET state='WITHDRAWN' WHERE id=$1`, importID); err != nil {
			return err
		}
		versionIDs := uniqueVersionIDs(committed)
		rejectedVersions := make(map[string]struct{}, len(rejections))
		for _, rejection := range rejections {
			rejectedVersions[rejection.VersionID] = struct{}{}
		}
		return insertAudit(ctx, tx, batch, actorID, "SEMANTIC_IMPORT_WITHDRAWN", map[string]any{
			"draftCount": len(versionIDs) - len(rejectedVersions), "versionIds": versionIDs,
			"reason": reason, "rejected": rejections,
		})
	})
	return rejections, normalizeStoreError(err)
}

func uniqueVersionIDs(rows []ImportRow) []string {
	seen := make(map[string]struct{}, len(rows))
	result := make([]string, 0, len(rows))
	for _, row := range rows {
		if _, exists := seen[row.CreatedVersionID]; exists {
			continue
		}
		seen[row.CreatedVersionID] = struct{}{}
		result = append(result, row.CreatedVersionID)
	}
	return result
}

func rowHasImpactWarning(row ImportRow) bool {
	for _, issue := range row.Errors {
		if issue.Code == ImportImpactRequiresReview {
			return true
		}
	}
	return false
}

func requireImportOwner(
	ctx context.Context,
	tx pgx.Tx,
	tenantID, domainID, actorID string,
) error {
	var allowed bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM platform.user_roles AS assignment
		JOIN platform.roles AS role
		  ON role.id=assignment.role_id AND role.tenant_id=assignment.tenant_id
		WHERE assignment.tenant_id=$1 AND assignment.user_id=$3
		  AND role.code::text='platform_admin' AND role.status='ACTIVE'
		  AND role.deleted_at IS NULL
		UNION ALL
		SELECT 1 FROM platform.domain_memberships AS membership
		WHERE membership.tenant_id=$1 AND membership.domain_id=$2
		  AND membership.user_id=$3 AND membership.status='ACTIVE'
		  AND membership.member_role='DOMAIN_ADMIN'
	)`, tenantID, domainID, actorID).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrImportPermission
	}
	return nil
}

func requireLease(ctx context.Context, tx pgx.Tx, claim Claim, workerID string) error {
	var current bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM askdata.semantic_imports
		WHERE id=$1 AND domain_id=$2 AND state='VALIDATING'
		  AND lease_owner=$3 AND lease_token=$4 AND lease_expires_at>now()
	)`, claim.ImportID, claim.DomainID, workerID, claim.LeaseToken).Scan(&current); err != nil {
		return err
	}
	if !current {
		return ErrImportLeaseLost
	}
	return nil
}

func insertAudit(
	ctx context.Context,
	tx pgx.Tx,
	batch SemanticImport,
	actorID, eventType string,
	detail any,
) error {
	payload, err := registry.CanonicalValue(detail)
	if err != nil || len(payload) > 60<<10 {
		return ErrInvalidImport
	}
	actionHash := askdata.HashBytes(bytes.Join([][]byte{
		[]byte(eventType), []byte(batch.ID), []byte(batch.FileHash), payload,
	}, []byte{0}))
	var actor any
	if actorID != "" {
		actor = actorID
	}
	_, err = tx.Exec(ctx, `INSERT INTO askdata.audit_events(
		id,tenant_id,domain_id,actor_id,event_type,resource_type,
		resource_id,action_hash,detail
	) VALUES($1,$2,$3,$4,$5,'SEMANTIC_IMPORT',$6,$7,$8)`,
		uuid.NewString(), batch.TenantID, batch.DomainID, actor,
		eventType, batch.ID, actionHash, payload)
	return err
}

func normalizeValidatedRow(row ValidatedRow) (ValidatedRow, error) {
	raw, err := canonicalObject(row.RawJSON)
	if err != nil || row.RowNo < 1 || row.RowNo > 10_000_000 {
		return ValidatedRow{}, ErrInvalidImportRow
	}
	row.RawJSON = raw
	if len(row.NormalizedJSON) > 0 {
		row.NormalizedJSON, err = canonicalObject(row.NormalizedJSON)
		if err != nil {
			return ValidatedRow{}, ErrInvalidImportRow
		}
	}
	switch row.State {
	case RowValid:
		if len(row.NormalizedJSON) == 0 {
			return ValidatedRow{}, ErrInvalidImportRow
		}
		if row.Errors == nil {
			row.Errors = []ValidationIssue{}
		}
	case RowInvalid, RowSkipped:
		if len(row.Errors) == 0 {
			return ValidatedRow{}, ErrInvalidImportRow
		}
	default:
		return ValidatedRow{}, ErrInvalidImportRow
	}
	for _, issue := range row.Errors {
		if !validIssue(issue) {
			return ValidatedRow{}, ErrInvalidImportRow
		}
	}
	if len(row.Errors) > 128 {
		return ValidatedRow{}, ErrInvalidImportRow
	}
	return row, nil
}

func canonicalObject(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || len(raw) > 1<<20 {
		return nil, ErrInvalidImportRow
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil || value == nil {
		return nil, ErrInvalidImportRow
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, ErrInvalidImportRow
	}
	payload, err := registry.CanonicalValue(value)
	if err != nil || len(payload) > 1<<20 {
		return nil, ErrInvalidImportRow
	}
	return payload, nil
}

func validIssue(issue ValidationIssue) bool {
	return boundedText(issue.Column, 128) && validCode(issue.Code) &&
		boundedText(issue.Message, 2048) && boundedText(issue.Expected, 1024) &&
		(issue.Actual == "" || boundedText(issue.Actual, 1024))
}

func validateCreateInput(input CreateImportInput) error {
	if !canonicalUUID(input.TenantID) || !canonicalUUID(input.DomainID) ||
		!canonicalUUID(input.CreatedBy) || !input.AssetType.Valid() ||
		!validHash(input.FileHash) || !boundedText(input.FileObjectURI, 2048) ||
		!boundedText(input.FileName, 255) {
		return ErrInvalidImport
	}
	return nil
}

func validateClaim(claim Claim, tenantID string) error {
	if !canonicalUUID(claim.ImportID) || !canonicalUUID(claim.TenantID) ||
		!canonicalUUID(claim.DomainID) || !canonicalUUID(claim.LeaseToken) ||
		claim.TenantID != tenantID || !claim.AssetType.Valid() || !validHash(claim.FileHash) ||
		!boundedText(claim.FileObjectURI, 2048) || !boundedText(claim.FileName, 255) ||
		claim.Attempt < 1 || claim.ResumeAfterRow < 0 {
		return ErrInvalidImport
	}
	return nil
}

func canonicalUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}

func validHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func validCode(value string) bool {
	if len(value) < 1 || len(value) > 128 || value[0] < 'A' || value[0] > 'Z' {
		return false
	}
	for _, char := range value[1:] {
		if (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '_' {
			return false
		}
	}
	return true
}

func boundedText(value string, maxLength int) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= maxLength &&
		!strings.ContainsAny(value, "\x00\n\r\t")
}

func validWorker(value string) bool { return boundedText(value, 128) }

func nullableJSON(value json.RawMessage) string {
	if len(value) == 0 {
		return "null"
	}
	return string(value)
}

func normalizeStoreError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrImportNotFound
	}
	return err
}

const importSelect = `SELECT id::text,tenant_id::text,domain_id::text,asset_type,
	file_object_uri,file_hash,file_name,state,total_rows,valid_rows,invalid_rows,
	COALESCE(failure_reason,''),created_by::text,attempt,lease_owner,
	COALESCE(lease_token::text,''),lease_expires_at,started_at,
	validation_completed_at,committed_at,created_at,updated_at
	FROM askdata.semantic_imports`

type rowScanner interface{ Scan(...any) error }

func scanImport(row rowScanner, target *SemanticImport) error {
	return row.Scan(
		&target.ID, &target.TenantID, &target.DomainID, &target.AssetType,
		&target.FileObjectURI, &target.FileHash, &target.FileName, &target.State,
		&target.TotalRows, &target.ValidRows, &target.InvalidRows, &target.FailureReason,
		&target.CreatedBy, &target.Attempt, &target.LeaseOwner, &target.LeaseToken,
		&target.LeaseExpiresAt, &target.StartedAt, &target.ValidationCompletedAt,
		&target.CommittedAt, &target.CreatedAt, &target.UpdatedAt,
	)
}

func (value SemanticImport) Validate() error {
	if !canonicalUUID(value.ID) || !canonicalUUID(value.TenantID) || !canonicalUUID(value.DomainID) ||
		!canonicalUUID(value.CreatedBy) || !value.AssetType.Valid() || !value.State.Valid() ||
		!validHash(value.FileHash) || value.TotalRows < 0 || value.ValidRows < 0 ||
		value.InvalidRows < 0 || value.ValidRows+value.InvalidRows > value.TotalRows {
		return ErrInvalidImport
	}
	return nil
}

func (row ImportRow) String() string {
	return fmt.Sprintf("%s:%d:%s", row.ImportID, row.RowNo, row.ValidationState)
}
