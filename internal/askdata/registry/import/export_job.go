package registryimport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/platform/database"
)

const (
	DefaultExportArtifactTTL = 24 * time.Hour
	DefaultExportLease       = 10 * time.Minute
	MaxExportAttempts        = 5
)

type ExportJobState string

const (
	ExportPending ExportJobState = "PENDING"
	ExportRunning ExportJobState = "RUNNING"
	ExportReady   ExportJobState = "READY"
	ExportFailed  ExportJobState = "FAILED"
	ExportExpired ExportJobState = "EXPIRED"
)

type SemanticExportJob struct {
	ID, TenantID, DomainID, ReleaseID, Format string
	AssetTypes                                []AssetType
	PinnedVersionIDs                          map[AssetType][]string
	State                                     ExportJobState
	SourceRowCount, RowCount                  int
	OmittedSensitiveMembers                   int
	ObjectURI, ContentHash, FailureCode       string
	CreatedBy                                 string
	Attempt                                   int
	LeaseOwner, LeaseToken                    string
	LeaseExpiresAt, StartedAt, CompletedAt    *time.Time
	ExpiresAt, CreatedAt, UpdatedAt           time.Time
}

func (job SemanticExportJob) EffectiveState(now time.Time) ExportJobState {
	if !now.Before(job.ExpiresAt) && job.State != ExportFailed {
		return ExportExpired
	}
	return job.State
}

type CreateExportJobInput struct {
	Selection ExportSelection
	ExpiresAt time.Time
}

type ExportJobClaim struct {
	SemanticExportJob
}

type ExportJobReader interface {
	Create(context.Context, CreateExportJobInput) (SemanticExportJob, error)
	Get(context.Context, string, string, string, string) (SemanticExportJob, error)
}

type ExportJobWorkerStore interface {
	ListTenantIDs(context.Context) ([]string, error)
	Claim(context.Context, string, string, time.Duration) (*ExportJobClaim, error)
	Complete(context.Context, ExportJobClaim, string, ExportArtifact, string) error
	Fail(context.Context, ExportJobClaim, string, string, bool) error
}

type PostgresExportJobStore struct{ pool *pgxpool.Pool }

func NewPostgresExportJobStore(pool *pgxpool.Pool) *PostgresExportJobStore {
	return &PostgresExportJobStore{pool: pool}
}

func (store *PostgresExportJobStore) Create(
	ctx context.Context,
	input CreateExportJobInput,
) (SemanticExportJob, error) {
	selection := input.Selection
	if store == nil || store.pool == nil || validateExportSelection(selection) != nil ||
		selection.System || selection.PinnedVersionIDs != nil {
		return SemanticExportJob{}, ErrExportInvalid
	}
	if input.ExpiresAt.IsZero() {
		input.ExpiresAt = time.Now().UTC().Add(DefaultExportArtifactTTL)
	}
	if !input.ExpiresAt.After(time.Now().UTC()) || input.ExpiresAt.After(time.Now().UTC().Add(7*24*time.Hour)) {
		return SemanticExportJob{}, ErrExportInvalid
	}
	var result SemanticExportJob
	err := database.WithTenantTx(ctx, store.pool, selection.TenantID, func(tx pgx.Tx) error {
		if err := authorizeExport(ctx, tx, selection); err != nil {
			return err
		}
		if err := validateExportRelease(ctx, tx, selection); err != nil {
			return err
		}
		manifest := make(map[AssetType][]string, len(selection.AssetTypes))
		sourceRows := 0
		for _, assetType := range CanonicalAssetTypes(selection.AssetTypes) {
			versionIDs, err := selectedExportVersionIDs(ctx, tx, selection, assetType)
			if err != nil {
				return err
			}
			manifest[assetType] = versionIDs
			count := len(versionIDs)
			if assetType == AssetHierarchy && len(versionIDs) > 0 {
				if err := tx.QueryRow(ctx, `SELECT count(*) FROM askdata.hierarchy_levels
					WHERE hierarchy_version_id=ANY($1::uuid[])`, versionIDs).Scan(&count); err != nil {
					return err
				}
			}
			sourceRows += count
			if sourceRows > MaxSemanticExportRows {
				return ErrExportTooLarge
			}
		}
		manifestJSON, err := marshalExportManifest(manifest)
		if err != nil {
			return err
		}
		assetTypes := make([]string, 0, len(selection.AssetTypes))
		for _, assetType := range CanonicalAssetTypes(selection.AssetTypes) {
			assetTypes = append(assetTypes, string(assetType))
		}
		id := uuid.NewString()
		row := tx.QueryRow(ctx, `INSERT INTO askdata.semantic_export_jobs(
			id,tenant_id,domain_id,release_id,asset_types,format,manifest_json,
			source_row_count,created_by,expires_at
		) VALUES($1,$2,$3,NULLIF($4,'')::uuid,$5,'xlsx',$6,$7,$8,$9)
		RETURNING `+exportJobColumns,
			id, selection.TenantID, selection.DomainID, selection.ReleaseID,
			assetTypes, manifestJSON, sourceRows, selection.ActorID, input.ExpiresAt)
		return scanExportJob(row, &result)
	})
	return result, normalizeExportJobError(err)
}

func (store *PostgresExportJobStore) Get(
	ctx context.Context,
	tenantID, domainID, actorID, jobID string,
) (SemanticExportJob, error) {
	if store == nil || store.pool == nil || !canonicalUUID(tenantID) ||
		!canonicalUUID(domainID) || !canonicalUUID(actorID) || !canonicalUUID(jobID) {
		return SemanticExportJob{}, ErrExportInvalid
	}
	var result SemanticExportJob
	err := database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		return scanExportJob(tx.QueryRow(ctx, `SELECT `+exportJobColumns+`
			FROM askdata.semantic_export_jobs
			WHERE id=$1 AND domain_id=$2 AND created_by=$3`, jobID, domainID, actorID), &result)
	})
	return result, normalizeExportJobError(err)
}

func (store *PostgresExportJobStore) ListTenantIDs(ctx context.Context) ([]string, error) {
	if store == nil || store.pool == nil {
		return nil, ErrExportInvalid
	}
	rows, err := store.pool.Query(ctx, `SELECT tenant_id::text FROM askdata.list_semantic_export_tenants()`)
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

func (store *PostgresExportJobStore) Claim(
	ctx context.Context,
	tenantID, workerID string,
	lease time.Duration,
) (*ExportJobClaim, error) {
	if store == nil || store.pool == nil || !canonicalUUID(tenantID) ||
		strings.TrimSpace(workerID) == "" || len(workerID) > 128 ||
		lease < 30*time.Second || lease > 30*time.Minute {
		return nil, ErrExportInvalid
	}
	var job SemanticExportJob
	found := false
	err := database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `SELECT
			job_id::text,tenant_id::text,domain_id::text,COALESCE(release_id::text,''),
			asset_types,format,manifest_json,state,source_row_count,row_count,
			omitted_sensitive_members,COALESCE(object_uri,''),COALESCE(content_hash,''),
			COALESCE(failure_code,''),created_by::text,attempt,lease_owner,
			lease_token::text,lease_expires_at,started_at,completed_at,expires_at,
			created_at,updated_at
			FROM askdata.claim_semantic_export($1,$2,$3)`,
			tenantID, workerID, int64(lease/time.Second))
		if err := scanExportJob(row, &job); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		found = true
		return nil
	})
	if err != nil {
		return nil, normalizeExportJobError(err)
	}
	if !found {
		return nil, nil
	}
	return &ExportJobClaim{SemanticExportJob: job}, nil
}

func (store *PostgresExportJobStore) Complete(
	ctx context.Context,
	claim ExportJobClaim,
	workerID string,
	artifact ExportArtifact,
	objectURI string,
) error {
	if store == nil || store.pool == nil || !canonicalUUID(claim.TenantID) ||
		!canonicalUUID(claim.ID) || !canonicalUUID(claim.LeaseToken) ||
		strings.TrimSpace(workerID) == "" || objectURI == "" ||
		artifact.ContentHash == "" || artifact.RowCount < 0 || artifact.OmittedSensitiveMembers < 0 {
		return ErrExportInvalid
	}
	return database.WithTenantTx(ctx, store.pool, claim.TenantID, func(tx pgx.Tx) error {
		var completed bool
		if err := tx.QueryRow(ctx, `SELECT askdata.complete_semantic_export(
			$1,$2,$3,$4,$5,$6,$7,$8)`, claim.TenantID, claim.ID, workerID,
			claim.LeaseToken, objectURI, artifact.ContentHash, artifact.RowCount,
			artifact.OmittedSensitiveMembers).Scan(&completed); err != nil {
			return err
		}
		if !completed {
			return ErrImportLeaseLost
		}
		return nil
	})
}

func (store *PostgresExportJobStore) Fail(
	ctx context.Context,
	claim ExportJobClaim,
	workerID, code string,
	retryable bool,
) error {
	if store == nil || store.pool == nil || !canonicalUUID(claim.TenantID) ||
		!canonicalUUID(claim.ID) || !canonicalUUID(claim.LeaseToken) ||
		strings.TrimSpace(workerID) == "" || !validCode(code) {
		return ErrExportInvalid
	}
	return database.WithTenantTx(ctx, store.pool, claim.TenantID, func(tx pgx.Tx) error {
		var failed bool
		if err := tx.QueryRow(ctx, `SELECT askdata.fail_semantic_export(
			$1,$2,$3,$4,$5,$6)`, claim.TenantID, claim.ID, workerID,
			claim.LeaseToken, code, retryable).Scan(&failed); err != nil {
			return err
		}
		if !failed {
			return ErrImportLeaseLost
		}
		return nil
	})
}

const exportJobColumns = `
	id::text,tenant_id::text,domain_id::text,COALESCE(release_id::text,''),
	asset_types,format,manifest_json,state,source_row_count,row_count,
	omitted_sensitive_members,COALESCE(object_uri,''),COALESCE(content_hash,''),
	COALESCE(failure_code,''),created_by::text,attempt,lease_owner,
	COALESCE(lease_token::text,''),lease_expires_at,started_at,completed_at,
	expires_at,created_at,updated_at`

type exportJobScanner interface{ Scan(...any) error }

func scanExportJob(row exportJobScanner, target *SemanticExportJob) error {
	var assetTypes []string
	var manifestJSON json.RawMessage
	if err := row.Scan(
		&target.ID, &target.TenantID, &target.DomainID, &target.ReleaseID,
		&assetTypes, &target.Format, &manifestJSON, &target.State,
		&target.SourceRowCount, &target.RowCount, &target.OmittedSensitiveMembers,
		&target.ObjectURI, &target.ContentHash, &target.FailureCode, &target.CreatedBy,
		&target.Attempt, &target.LeaseOwner, &target.LeaseToken,
		&target.LeaseExpiresAt, &target.StartedAt, &target.CompletedAt,
		&target.ExpiresAt, &target.CreatedAt, &target.UpdatedAt,
	); err != nil {
		return err
	}
	target.AssetTypes = make([]AssetType, len(assetTypes))
	for index, value := range assetTypes {
		target.AssetTypes[index] = AssetType(value)
		if !target.AssetTypes[index].Valid() {
			return ErrExportContract
		}
	}
	manifest, err := unmarshalExportManifest(manifestJSON)
	if err != nil {
		return err
	}
	target.PinnedVersionIDs = manifest
	return nil
}

func marshalExportManifest(manifest map[AssetType][]string) ([]byte, error) {
	value := make(map[string][]string, len(manifest))
	for assetType, versionIDs := range manifest {
		value[string(assetType)] = versionIDs
	}
	return json.Marshal(value)
}

func unmarshalExportManifest(raw []byte) (map[AssetType][]string, error) {
	var value map[string][]string
	if json.Unmarshal(raw, &value) != nil {
		return nil, ErrExportContract
	}
	result := make(map[AssetType][]string, len(value))
	for rawType, versionIDs := range value {
		assetType := AssetType(rawType)
		if !assetType.Valid() {
			return nil, ErrExportContract
		}
		result[assetType] = versionIDs
	}
	return result, nil
}

func normalizeExportJobError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrExportNotFound
	}
	return err
}

type ExportObjectStorage interface {
	Put(context.Context, string, string, io.Reader, int64, string) error
}

type ExportWorker struct {
	store   ExportJobWorkerStore
	export  *ExportService
	storage ExportObjectStorage
	bucket  string
}

func NewExportWorker(
	store ExportJobWorkerStore,
	export *ExportService,
	storage ExportObjectStorage,
	bucket string,
) *ExportWorker {
	return &ExportWorker{store: store, export: export, storage: storage, bucket: bucket}
}

func (worker *ExportWorker) TenantIDs(ctx context.Context) ([]string, error) {
	if worker == nil || worker.store == nil {
		return nil, ErrExportInvalid
	}
	return worker.store.ListTenantIDs(ctx)
}

func (worker *ExportWorker) ProcessNext(
	ctx context.Context,
	tenantID, workerID string,
	lease time.Duration,
) (bool, error) {
	if worker == nil || worker.store == nil || worker.export == nil ||
		worker.storage == nil || strings.TrimSpace(worker.bucket) == "" {
		return false, ErrExportInvalid
	}
	claim, err := worker.store.Claim(ctx, tenantID, workerID, lease)
	if err != nil || claim == nil {
		return false, err
	}
	selection := ExportSelection{
		TenantID: claim.TenantID, DomainID: claim.DomainID, ReleaseID: claim.ReleaseID,
		AssetTypes: claim.AssetTypes, PinnedVersionIDs: claim.PinnedVersionIDs, System: true,
	}
	artifact, err := worker.export.Generate(ctx, selection)
	if err != nil {
		return true, worker.fail(ctx, *claim, workerID, err)
	}
	key := path.Join(
		"semantic-exports", claim.TenantID, claim.DomainID, claim.ID,
		artifact.ContentHash+".xlsx",
	)
	if err := worker.storage.Put(
		ctx, worker.bucket, key, bytes.NewReader(artifact.Bytes),
		int64(len(artifact.Bytes)), artifact.ContentType,
	); err != nil {
		if ctx.Err() != nil {
			return true, ctx.Err()
		}
		return true, worker.store.Fail(ctx, *claim, workerID, "EXPORT_STORAGE_FAILED", true)
	}
	objectURI := fmt.Sprintf("s3://%s/%s", worker.bucket, key)
	if err := worker.store.Complete(ctx, *claim, workerID, artifact, objectURI); err != nil {
		return true, err
	}
	return true, nil
}

func (worker *ExportWorker) fail(
	ctx context.Context,
	claim ExportJobClaim,
	workerID string,
	cause error,
) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	code, retryable := "EXPORT_GENERATION_FAILED", true
	switch {
	case errors.Is(cause, ErrExportTooLarge):
		code, retryable = "EXPORT_TOO_LARGE", false
	case errors.Is(cause, ErrExportNotFound):
		code, retryable = "EXPORT_VERSION_NOT_FOUND", false
	case errors.Is(cause, ErrExportInvalid), errors.Is(cause, ErrExportContract):
		code, retryable = "EXPORT_CONTRACT_INVALID", false
	}
	return worker.store.Fail(ctx, claim, workerID, code, retryable)
}
