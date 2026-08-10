package registry

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/platform/database"
)

type ReleaseReferenceType string

const (
	ReleaseReferenceReportVersion    ReleaseReferenceType = "REPORT_VERSION"
	ReleaseReferenceCertifiedExample ReleaseReferenceType = "CERTIFIED_EXAMPLE"
	ReleaseReferenceSavedQuestion    ReleaseReferenceType = "SAVED_QUESTION"
	ReleaseReferenceKPIBundle        ReleaseReferenceType = "KPI_BUNDLE"
	ReleaseReferenceEvaluationCase   ReleaseReferenceType = "EVALUATION_CASE"
)

const (
	ReleaseRetireBlockedCode       = "RELEASE_RETIRE_BLOCKED"
	ReleaseNotRunnableCode         = "RELEASE_NOT_RUNNABLE"
	ReleaseRetentionNotExpiredCode = "RELEASE_RETENTION_NOT_EXPIRED"
)

var (
	ErrReleaseReferenceInvalid  = errors.New("release reference is invalid")
	ErrReleaseRetentionNotFound = errors.New("semantic release was not found")
	ErrReleaseRetireState       = errors.New("semantic release cannot retire from its current state")
	ErrReleaseProjectionCleanup = errors.New("retained release projection cleanup failed")
)

type ReleaseReference struct {
	ID            string               `json:"id"`
	TenantID      string               `json:"tenantId"`
	DomainID      string               `json:"domainId"`
	ReleaseID     string               `json:"releaseId"`
	Type          ReleaseReferenceType `json:"referenceType"`
	ReferenceID   string               `json:"referenceId"`
	ReferenceName string               `json:"referenceName"`
	OwnerID       string               `json:"ownerId"`
	CreatedAt     time.Time            `json:"createdAt"`
	ReleasedAt    *time.Time           `json:"releasedAt,omitempty"`
}

func (reference ReleaseReference) Validate() error {
	for name, value := range map[string]string{
		"tenant ID": reference.TenantID, "domain ID": reference.DomainID,
		"release ID": reference.ReleaseID, "reference ID": reference.ReferenceID,
		"owner ID": reference.OwnerID,
	} {
		if !canonicalRetentionUUID(value) {
			return fmt.Errorf("%w: %s must be a canonical UUID", ErrReleaseReferenceInvalid, name)
		}
	}
	if reference.ID != "" && !canonicalRetentionUUID(reference.ID) {
		return fmt.Errorf("%w: ID must be a canonical UUID", ErrReleaseReferenceInvalid)
	}
	if !reference.Type.Valid() {
		return fmt.Errorf("%w: reference type is unsupported", ErrReleaseReferenceInvalid)
	}
	if !boundedReferenceName(reference.ReferenceName) {
		return fmt.Errorf("%w: reference name is invalid", ErrReleaseReferenceInvalid)
	}
	return nil
}

func (value ReleaseReferenceType) Valid() bool {
	switch value {
	case ReleaseReferenceReportVersion, ReleaseReferenceCertifiedExample,
		ReleaseReferenceSavedQuestion, ReleaseReferenceKPIBundle,
		ReleaseReferenceEvaluationCase:
		return true
	default:
		return false
	}
}

type ReleaseRetentionError struct {
	Code       string             `json:"code"`
	ReleaseID  string             `json:"releaseId"`
	References []ReleaseReference `json:"references"`
}

func (failure *ReleaseRetentionError) Error() string {
	if failure == nil {
		return ""
	}
	if failure.Code == ReleaseRetireBlockedCode {
		return fmt.Sprintf("%s: release %s has %d active references",
			failure.Code, failure.ReleaseID, len(failure.References))
	}
	return fmt.Sprintf("%s: release %s", failure.Code, failure.ReleaseID)
}

func (store *PostgresStore) AddReference(
	ctx context.Context,
	reference ReleaseReference,
) (ReleaseReference, error) {
	if store == nil || store.pool == nil {
		return ReleaseReference{}, errors.New("semantic registry PostgreSQL store is not configured")
	}
	if err := reference.Validate(); err != nil {
		return ReleaseReference{}, err
	}
	if err := validateRetentionAccessContext(ctx, reference.DomainID); err != nil {
		return ReleaseReference{}, err
	}
	var releasedAt *time.Time
	err := database.WithTenantTx(ctx, store.pool, reference.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO askdata.release_references(
			tenant_id,release_id,reference_type,reference_id,reference_name,owner_id
		) VALUES($1,$2,$3,$4,$5,$6)
		ON CONFLICT(release_id,reference_type,reference_id) DO UPDATE SET
			reference_name=EXCLUDED.reference_name,
			owner_id=EXCLUDED.owner_id,
			released_at=NULL
		RETURNING id::text,created_at,released_at`,
			reference.TenantID, reference.ReleaseID, reference.Type,
			reference.ReferenceID, reference.ReferenceName, reference.OwnerID,
		).Scan(&reference.ID, &reference.CreatedAt, &releasedAt)
	})
	if err != nil {
		return ReleaseReference{}, mapReleaseRetentionDatabaseError(err)
	}
	reference.ReleasedAt = releasedAt
	return reference, nil
}

func (store *PostgresStore) ReleaseReference(
	ctx context.Context,
	tenantID, domainID, releaseID string,
	referenceType ReleaseReferenceType,
	referenceID string,
) error {
	if store == nil || store.pool == nil {
		return errors.New("semantic registry PostgreSQL store is not configured")
	}
	if err := validateReleaseReferenceKey(
		tenantID, domainID, releaseID, referenceType, referenceID,
	); err != nil {
		return err
	}
	if err := validateRetentionAccessContext(ctx, domainID); err != nil {
		return err
	}
	return mapReleaseRetentionDatabaseError(database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE askdata.release_references
			SET released_at=clock_timestamp()
			WHERE tenant_id=$1 AND release_id=$2 AND reference_type=$3
			  AND reference_id=$4 AND released_at IS NULL`,
			tenantID, releaseID, referenceType, referenceID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrReleaseRetentionNotFound
		}
		return nil
	}))
}

func (store *PostgresStore) CountActiveReferences(
	ctx context.Context,
	tenantID, domainID, releaseID string,
) (int, error) {
	if store == nil || store.pool == nil {
		return 0, errors.New("semantic registry PostgreSQL store is not configured")
	}
	if err := validateReleaseIdentity(tenantID, domainID, releaseID); err != nil {
		return 0, err
	}
	if err := validateRetentionAccessContext(ctx, domainID); err != nil {
		return 0, err
	}
	count := 0
	err := database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*)::integer
			FROM askdata.release_references AS reference
			JOIN askdata.releases AS release
			  ON release.id=reference.release_id AND release.tenant_id=reference.tenant_id
			WHERE reference.tenant_id=$1 AND reference.release_id=$2
			  AND release.domain_id=$3 AND reference.released_at IS NULL`,
			tenantID, releaseID, domainID).Scan(&count)
	})
	return count, mapReleaseRetentionDatabaseError(err)
}

func (store *PostgresStore) ListActiveReferences(
	ctx context.Context,
	tenantID, domainID, releaseID string,
) ([]ReleaseReference, error) {
	if store == nil || store.pool == nil {
		return nil, errors.New("semantic registry PostgreSQL store is not configured")
	}
	if err := validateReleaseIdentity(tenantID, domainID, releaseID); err != nil {
		return nil, err
	}
	if err := validateRetentionAccessContext(ctx, domainID); err != nil {
		return nil, err
	}
	references := []ReleaseReference{}
	err := database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT reference.id::text,reference.tenant_id::text,
			release.domain_id::text,reference.release_id::text,reference.reference_type,
			reference.reference_id::text,reference.reference_name,reference.owner_id::text,
			reference.created_at
		FROM askdata.release_references AS reference
		JOIN askdata.releases AS release
		  ON release.id=reference.release_id AND release.tenant_id=reference.tenant_id
		WHERE reference.tenant_id=$1 AND reference.release_id=$2
		  AND release.domain_id=$3 AND reference.released_at IS NULL
		ORDER BY reference.reference_type,reference.reference_name,
		  reference.reference_id,reference.id`, tenantID, releaseID, domainID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var reference ReleaseReference
			if err := rows.Scan(
				&reference.ID, &reference.TenantID, &reference.DomainID,
				&reference.ReleaseID, &reference.Type, &reference.ReferenceID,
				&reference.ReferenceName, &reference.OwnerID, &reference.CreatedAt,
			); err != nil {
				return err
			}
			references = append(references, reference)
		}
		return rows.Err()
	})
	return references, mapReleaseRetentionDatabaseError(err)
}

func (store *PostgresStore) Retire(
	ctx context.Context,
	tenantID, domainID, releaseID string,
) error {
	if store == nil || store.pool == nil {
		return errors.New("semantic registry PostgreSQL store is not configured")
	}
	if err := validateReleaseIdentity(tenantID, domainID, releaseID); err != nil {
		return err
	}
	if err := validateRetentionAccessContext(ctx, domainID); err != nil {
		return err
	}
	var outcome string
	var references []ReleaseReference
	err := database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT askdata.retire_release($1)`, releaseID).Scan(&outcome); err != nil {
			return err
		}
		if outcome != "BLOCKED" {
			return nil
		}
		rows, err := tx.Query(ctx, `SELECT reference.id::text,reference.tenant_id::text,
			release.domain_id::text,reference.release_id::text,reference.reference_type,
			reference.reference_id::text,reference.reference_name,reference.owner_id::text,
			reference.created_at
		FROM askdata.release_references AS reference
		JOIN askdata.releases AS release
		  ON release.id=reference.release_id AND release.tenant_id=reference.tenant_id
		WHERE reference.tenant_id=$1 AND reference.release_id=$2
		  AND release.domain_id=$3 AND reference.released_at IS NULL
		ORDER BY reference.reference_type,reference.reference_name,
		  reference.reference_id,reference.id`, tenantID, releaseID, domainID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var reference ReleaseReference
			if err := rows.Scan(
				&reference.ID, &reference.TenantID, &reference.DomainID,
				&reference.ReleaseID, &reference.Type, &reference.ReferenceID,
				&reference.ReferenceName, &reference.OwnerID, &reference.CreatedAt,
			); err != nil {
				return err
			}
			references = append(references, reference)
		}
		return rows.Err()
	})
	if err != nil {
		return mapReleaseRetentionDatabaseError(err)
	}
	switch outcome {
	case "RETIRED":
		return nil
	case "BLOCKED":
		return &ReleaseRetentionError{
			Code: ReleaseRetireBlockedCode, ReleaseID: releaseID, References: references,
		}
	case "NOT_EXPIRED":
		return &ReleaseRetentionError{Code: ReleaseRetentionNotExpiredCode, ReleaseID: releaseID}
	case "NOT_FOUND":
		return ErrReleaseRetentionNotFound
	case "INVALID_STATE":
		return ErrReleaseRetireState
	default:
		return errors.New("semantic release retirement returned an invalid outcome")
	}
}

type RetainedProjectionCleanup struct {
	TenantID    string             `json:"tenantId"`
	DomainID    string             `json:"domainId"`
	Release     askdata.ReleaseRef `json:"release"`
	ObjectCount int                `json:"objectCount"`
}

func (cleanup RetainedProjectionCleanup) Validate() error {
	if err := validateReleaseIdentity(cleanup.TenantID, cleanup.DomainID, string(cleanup.Release.ReleaseID)); err != nil {
		return err
	}
	if cleanup.Release.ContentHash.Validate() != nil || cleanup.ObjectCount < 1 || cleanup.ObjectCount > 10000 {
		return ErrReleaseProjectionCleanup
	}
	return nil
}

type RetainedProjectionCleaner interface {
	CleanupRetainedProjection(context.Context, RetainedProjectionCleanup) error
}

type retainedProjectionStore interface {
	PrepareRetainedProjectionCleanup(context.Context, string, string, string) (RetainedProjectionCleanup, error)
	CompleteRetainedProjectionCleanup(context.Context, RetainedProjectionCleanup) error
}

type ReleaseProjectionCleanupWorker struct {
	store    retainedProjectionStore
	cleaners []RetainedProjectionCleaner
}

func NewReleaseProjectionCleanupWorker(
	store retainedProjectionStore,
	cleaners ...RetainedProjectionCleaner,
) (*ReleaseProjectionCleanupWorker, error) {
	if store == nil {
		return nil, ErrReleaseProjectionCleanup
	}
	for _, cleaner := range cleaners {
		if cleaner == nil {
			return nil, ErrReleaseProjectionCleanup
		}
	}
	return &ReleaseProjectionCleanupWorker{
		store: store, cleaners: append([]RetainedProjectionCleaner(nil), cleaners...),
	}, nil
}

func (worker *ReleaseProjectionCleanupWorker) Run(
	ctx context.Context,
	tenantID, domainID, releaseID string,
) error {
	if worker == nil || worker.store == nil {
		return ErrReleaseProjectionCleanup
	}
	cleanup, err := worker.store.PrepareRetainedProjectionCleanup(ctx, tenantID, domainID, releaseID)
	if err != nil {
		return err
	}
	for _, cleaner := range worker.cleaners {
		if err := cleaner.CleanupRetainedProjection(ctx, cleanup); err != nil {
			return fmt.Errorf("%w: %v", ErrReleaseProjectionCleanup, err)
		}
	}
	return worker.store.CompleteRetainedProjectionCleanup(ctx, cleanup)
}

func (store *PostgresStore) PrepareRetainedProjectionCleanup(
	ctx context.Context,
	tenantID, domainID, releaseID string,
) (RetainedProjectionCleanup, error) {
	if store == nil || store.pool == nil {
		return RetainedProjectionCleanup{}, errors.New("semantic registry PostgreSQL store is not configured")
	}
	if err := validateReleaseIdentity(tenantID, domainID, releaseID); err != nil {
		return RetainedProjectionCleanup{}, err
	}
	cleanup := RetainedProjectionCleanup{TenantID: tenantID, DomainID: domainID}
	err := database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		var releaseHash, status string
		var factsComplete bool
		if err := tx.QueryRow(ctx, `SELECT content_hash,status,object_count,
			askdata.release_registry_facts_complete(id)
			FROM askdata.releases
			WHERE tenant_id=$1 AND domain_id=$2 AND id=$3`, tenantID, domainID, releaseID).Scan(
			&releaseHash, &status, &cleanup.ObjectCount, &factsComplete,
		); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrReleaseRetentionNotFound
			}
			return err
		}
		if status != "RETAINED" || !factsComplete {
			return ErrReleaseProjectionCleanup
		}
		cleanup.Release = askdata.ReleaseRef{
			ReleaseID: askdata.ID(releaseID), ContentHash: askdata.ContentHash(releaseHash),
		}
		return cleanup.Validate()
	})
	return cleanup, mapReleaseRetentionDatabaseError(err)
}

func (store *PostgresStore) CompleteRetainedProjectionCleanup(
	ctx context.Context,
	cleanup RetainedProjectionCleanup,
) error {
	if store == nil || store.pool == nil {
		return errors.New("semantic registry PostgreSQL store is not configured")
	}
	if err := cleanup.Validate(); err != nil {
		return err
	}
	return mapReleaseRetentionDatabaseError(database.WithTenantTx(
		ctx, store.pool, cleanup.TenantID, func(tx pgx.Tx) error {
			var completed bool
			if err := tx.QueryRow(ctx, `SELECT askdata.cleanup_retained_release_projections($1)`,
				cleanup.Release.ReleaseID).Scan(&completed); err != nil {
				return err
			}
			if !completed {
				return ErrReleaseProjectionCleanup
			}
			return nil
		},
	))
}

func validateReleaseReferenceKey(
	tenantID, domainID, releaseID string,
	referenceType ReleaseReferenceType,
	referenceID string,
) error {
	if err := validateReleaseIdentity(tenantID, domainID, releaseID); err != nil {
		return err
	}
	if !referenceType.Valid() || !canonicalRetentionUUID(referenceID) {
		return ErrReleaseReferenceInvalid
	}
	return nil
}

func validateReleaseIdentity(tenantID, domainID, releaseID string) error {
	if !canonicalRetentionUUID(tenantID) || !canonicalRetentionUUID(domainID) ||
		!canonicalRetentionUUID(releaseID) {
		return ErrReleaseReferenceInvalid
	}
	return nil
}

func validateRetentionAccessContext(ctx context.Context, domainID string) error {
	if access, ok := database.AccessContextFromContext(ctx); ok && access.DomainID != domainID {
		return ErrRegistryPermissionDenied
	}
	return nil
}

func boundedReferenceName(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || !utf8.ValidString(value) ||
		utf8.RuneCountInString(value) > 200 {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func canonicalRetentionUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}

func mapReleaseRetentionDatabaseError(err error) error {
	if err == nil {
		return nil
	}
	for _, known := range []error{
		ErrReleaseReferenceInvalid, ErrReleaseRetentionNotFound,
		ErrReleaseRetireState, ErrReleaseProjectionCleanup,
		ErrRegistryPermissionDenied,
	} {
		if errors.Is(err, known) {
			return err
		}
	}
	var databaseError *pgconn.PgError
	if !errors.As(err, &databaseError) {
		return err
	}
	switch {
	case strings.Contains(databaseError.Message, ReleaseRetireBlockedCode):
		return &ReleaseRetentionError{Code: ReleaseRetireBlockedCode}
	case strings.Contains(databaseError.Message, ReleaseRetentionNotExpiredCode):
		return &ReleaseRetentionError{Code: ReleaseRetentionNotExpiredCode}
	case strings.Contains(databaseError.Message, ReleaseNotRunnableCode):
		return &ReleaseRetentionError{Code: ReleaseNotRunnableCode}
	case databaseError.Code == "42501":
		return ErrRegistryPermissionDenied
	case databaseError.Code == "23503":
		return ErrReleaseRetentionNotFound
	case databaseError.Code == "23514" || databaseError.Code == "22023":
		return ErrReleaseReferenceInvalid
	default:
		return err
	}
}
