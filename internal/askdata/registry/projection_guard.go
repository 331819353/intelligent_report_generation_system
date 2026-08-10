package registry

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/platform/database"
)

const (
	ReleaseProjectionMismatchCode = "RELEASE_PROJECTION_MISMATCH"
	DefaultProjectionGuardTTL     = 30 * time.Second
)

var (
	ErrReleaseProjectionMismatch = errors.New("semantic release projections do not match")
	ErrProjectionGuardInvalid    = errors.New("semantic release projection guard input is invalid")
)

type ProjectionMismatch struct {
	Projection  string    `json:"projection"`
	Expected    string    `json:"expected"`
	Applied     string    `json:"applied"`
	Status      string    `json:"status"`
	LastUpdated time.Time `json:"lastUpdated"`
}

type ReleaseProjectionMismatchError struct {
	Code          string               `json:"code"`
	ReleaseID     string               `json:"releaseId"`
	ReleaseStatus string               `json:"releaseStatus"`
	ContentHash   string               `json:"contentHash"`
	Mismatches    []ProjectionMismatch `json:"mismatches"`
}

func (failure *ReleaseProjectionMismatchError) Error() string {
	if failure == nil {
		return ErrReleaseProjectionMismatch.Error()
	}
	return fmt.Sprintf("%s: release %s has %d projection mismatches",
		ReleaseProjectionMismatchCode, failure.ReleaseID, len(failure.Mismatches))
}

func (*ReleaseProjectionMismatchError) Unwrap() error { return ErrReleaseProjectionMismatch }

type projectionGuardScope struct {
	tenantID string
	domainID string
}

// WithProjectionGuardScope binds the already-authorized tenant/domain pair to
// AssertRunnable without changing the public AssertRunnable(ctx, releaseID)
// contract. The guard still re-applies USER-mode RLS in its own transaction.
func WithProjectionGuardScope(ctx context.Context, tenantID, domainID string) context.Context {
	return context.WithValue(ctx, projectionGuardScopeKey{}, projectionGuardScope{
		tenantID: tenantID,
		domainID: domainID,
	})
}

type projectionGuardScopeKey struct{}

type releaseProjectionRecord struct {
	target      string
	status      string
	expected    string
	applied     string
	lastUpdated time.Time
}

type releaseProjectionSnapshot struct {
	found       bool
	releaseID   string
	status      string
	contentHash string
	lastUpdated time.Time
	projections map[string]releaseProjectionRecord
}

type releaseProjectionSnapshotLoader interface {
	LoadReleaseProjectionSnapshot(
		context.Context, string, string, string,
	) (releaseProjectionSnapshot, error)
	LoadReleaseProjectionRevision(
		context.Context, string, string, string,
	) (releaseProjectionRevision, error)
}

type releaseProjectionRevision struct {
	found                 bool
	releaseUpdated        time.Time
	projectionCount       int
	lastProjectionUpdated time.Time
}

type projectionGuardCacheKey struct {
	tenantID  string
	domainID  string
	releaseID string
}

type projectionGuardCacheEntry struct {
	expiresAt time.Time
	revision  releaseProjectionRevision
	failure   *ReleaseProjectionMismatchError
}

// ProjectionGuard caches both successful and governed-failure decisions for
// 30 seconds. A lightweight revision read makes release/projection mutations
// invalidate the cached decision on the next assertion; writers can also call
// Invalidate immediately after a known mutation.
type ProjectionGuard struct {
	loader releaseProjectionSnapshotLoader
	ttl    time.Duration
	now    func() time.Time

	mu    sync.RWMutex
	cache map[projectionGuardCacheKey]projectionGuardCacheEntry
}

func NewProjectionGuard(pool *pgxpool.Pool) *ProjectionGuard {
	return newProjectionGuard(&postgresReleaseProjectionSnapshotLoader{pool: pool}, DefaultProjectionGuardTTL, time.Now)
}

func newProjectionGuard(
	loader releaseProjectionSnapshotLoader,
	ttl time.Duration,
	now func() time.Time,
) *ProjectionGuard {
	return &ProjectionGuard{
		loader: loader,
		ttl:    ttl,
		now:    now,
		cache:  make(map[projectionGuardCacheKey]projectionGuardCacheEntry),
	}
}

func (guard *ProjectionGuard) AssertRunnable(ctx context.Context, releaseID string) error {
	key, err := projectionGuardKey(ctx, releaseID)
	if err != nil || guard == nil || guard.loader == nil || guard.now == nil || guard.ttl <= 0 {
		return ErrProjectionGuardInvalid
	}
	now := guard.now()
	guard.mu.RLock()
	entry, cached := guard.cache[key]
	guard.mu.RUnlock()
	if cached && now.Before(entry.expiresAt) {
		revision, revisionErr := guard.loader.LoadReleaseProjectionRevision(
			ctx, key.tenantID, key.domainID, key.releaseID,
		)
		if revisionErr != nil {
			return revisionErr
		}
		if sameProjectionRevision(entry.revision, revision) {
			if entry.failure == nil {
				return nil
			}
			return cloneProjectionFailure(entry.failure)
		}
	}

	snapshot, err := guard.loader.LoadReleaseProjectionSnapshot(
		ctx, key.tenantID, key.domainID, key.releaseID,
	)
	if err != nil {
		return err
	}
	failure := evaluateProjectionSnapshot(snapshot)
	guard.mu.Lock()
	guard.cache[key] = projectionGuardCacheEntry{
		expiresAt: now.Add(guard.ttl),
		revision:  projectionSnapshotRevision(snapshot),
		failure:   cloneProjectionFailure(failure),
	}
	guard.mu.Unlock()
	if failure == nil {
		return nil
	}
	return failure
}

// AssertHistoricalRunnable is the narrow report-component exception to the
// normal ACTIVE/READY gate. RETAINED releases deliberately discard search and
// graph projections, but preserve the registry and execution semantic layer
// needed to recompile a report's immutable historical Semantic IR.
func (guard *ProjectionGuard) AssertHistoricalRunnable(ctx context.Context, releaseID string) error {
	key, err := projectionGuardKey(ctx, releaseID)
	if err != nil || guard == nil || guard.loader == nil {
		return ErrProjectionGuardInvalid
	}
	snapshot, err := guard.loader.LoadReleaseProjectionSnapshot(
		ctx, key.tenantID, key.domainID, key.releaseID,
	)
	if err != nil {
		return err
	}
	if failure := evaluateHistoricalProjectionSnapshot(snapshot); failure != nil {
		return failure
	}
	return nil
}

// Invalidate proactively removes every tenant/domain cache entry for a
// release. It is intentionally exported for projection completion/failure and
// the future REL-005 activation boundary.
func (guard *ProjectionGuard) Invalidate(releaseID string) {
	if guard == nil {
		return
	}
	guard.mu.Lock()
	defer guard.mu.Unlock()
	for key := range guard.cache {
		if key.releaseID == releaseID {
			delete(guard.cache, key)
		}
	}
}

func (guard *ProjectionGuard) InvalidateAll() {
	if guard == nil {
		return
	}
	guard.mu.Lock()
	guard.cache = make(map[projectionGuardCacheKey]projectionGuardCacheEntry)
	guard.mu.Unlock()
}

func projectionGuardKey(ctx context.Context, releaseID string) (projectionGuardCacheKey, error) {
	scope, ok := ctx.Value(projectionGuardScopeKey{}).(projectionGuardScope)
	access, authenticated := database.AccessContextFromContext(ctx)
	if !ok || !authenticated || access.DomainID != scope.domainID ||
		!canonicalProjectionUUID(scope.tenantID) || !canonicalProjectionUUID(scope.domainID) ||
		!canonicalProjectionUUID(releaseID) {
		return projectionGuardCacheKey{}, ErrProjectionGuardInvalid
	}
	return projectionGuardCacheKey{
		tenantID: scope.tenantID, domainID: scope.domainID, releaseID: releaseID,
	}, nil
}

func evaluateProjectionSnapshot(snapshot releaseProjectionSnapshot) *ReleaseProjectionMismatchError {
	failure := &ReleaseProjectionMismatchError{
		Code: ReleaseProjectionMismatchCode, ReleaseID: snapshot.releaseID,
		ReleaseStatus: snapshot.status, ContentHash: snapshot.contentHash,
		Mismatches: []ProjectionMismatch{},
	}
	if !snapshot.found {
		failure.ReleaseStatus = "MISSING"
		failure.Mismatches = append(failure.Mismatches, ProjectionMismatch{
			Projection: "RELEASE", Status: "MISSING",
		})
		return failure
	}
	if snapshot.status != "READY" && snapshot.status != "ACTIVE" {
		failure.Mismatches = append(failure.Mismatches, ProjectionMismatch{
			Projection: "RELEASE", Expected: snapshot.contentHash,
			Applied: snapshot.contentHash, Status: snapshot.status,
			LastUpdated: snapshot.lastUpdated,
		})
	}
	for _, target := range governedProjectionTargets {
		record, exists := snapshot.projections[target.databaseTarget]
		if !exists {
			failure.Mismatches = append(failure.Mismatches, ProjectionMismatch{
				Projection: target.publicName, Expected: snapshot.contentHash,
				Status: "MISSING",
			})
			continue
		}
		if record.status != "READY" || record.expected != snapshot.contentHash ||
			record.applied != snapshot.contentHash {
			failure.Mismatches = append(failure.Mismatches, ProjectionMismatch{
				Projection: target.publicName, Expected: snapshot.contentHash,
				Applied: record.applied, Status: record.status,
				LastUpdated: record.lastUpdated,
			})
		}
	}
	if len(failure.Mismatches) == 0 {
		return nil
	}
	return failure
}

func evaluateHistoricalProjectionSnapshot(snapshot releaseProjectionSnapshot) *ReleaseProjectionMismatchError {
	failure := &ReleaseProjectionMismatchError{
		Code: ReleaseProjectionMismatchCode, ReleaseID: snapshot.releaseID,
		ReleaseStatus: snapshot.status, ContentHash: snapshot.contentHash,
		Mismatches: []ProjectionMismatch{},
	}
	if !snapshot.found {
		failure.ReleaseStatus = "MISSING"
		failure.Mismatches = append(failure.Mismatches, ProjectionMismatch{Projection: "RELEASE", Status: "MISSING"})
		return failure
	}
	if snapshot.status != "ACTIVE" && snapshot.status != "SUPERSEDED" && snapshot.status != "RETAINED" {
		failure.Mismatches = append(failure.Mismatches, ProjectionMismatch{
			Projection: "RELEASE", Expected: snapshot.contentHash, Applied: snapshot.contentHash,
			Status: snapshot.status, LastUpdated: snapshot.lastUpdated,
		})
	}
	for _, target := range governedProjectionTargets {
		if target.databaseTarget != "POSTGRES_REGISTRY" && target.databaseTarget != "EXECUTION_SEMANTIC_LAYER" {
			continue
		}
		record, exists := snapshot.projections[target.databaseTarget]
		if !exists || record.status != "READY" || record.expected != snapshot.contentHash || record.applied != snapshot.contentHash {
			mismatch := ProjectionMismatch{Projection: target.publicName, Expected: snapshot.contentHash, Status: "MISSING"}
			if exists {
				mismatch.Applied, mismatch.Status, mismatch.LastUpdated = record.applied, record.status, record.lastUpdated
			}
			failure.Mismatches = append(failure.Mismatches, mismatch)
		}
	}
	if len(failure.Mismatches) == 0 {
		return nil
	}
	return failure
}

var governedProjectionTargets = []struct {
	databaseTarget string
	publicName     string
}{
	{databaseTarget: "POSTGRES_REGISTRY", publicName: "REGISTRY"},
	{databaseTarget: "SEARCH_INDEX", publicName: "SEARCH"},
	{databaseTarget: "NEBULA_GRAPH", publicName: "GRAPH"},
	{databaseTarget: "EXECUTION_SEMANTIC_LAYER", publicName: "MEMBER"},
}

func cloneProjectionFailure(failure *ReleaseProjectionMismatchError) *ReleaseProjectionMismatchError {
	if failure == nil {
		return nil
	}
	cloned := *failure
	cloned.Mismatches = append([]ProjectionMismatch(nil), failure.Mismatches...)
	return &cloned
}

func canonicalProjectionUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}

func projectionSnapshotRevision(snapshot releaseProjectionSnapshot) releaseProjectionRevision {
	revision := releaseProjectionRevision{
		found: snapshot.found, releaseUpdated: snapshot.lastUpdated,
		projectionCount: len(snapshot.projections),
	}
	for _, projection := range snapshot.projections {
		if projection.lastUpdated.After(revision.lastProjectionUpdated) {
			revision.lastProjectionUpdated = projection.lastUpdated
		}
	}
	return revision
}

func sameProjectionRevision(left, right releaseProjectionRevision) bool {
	return left.found == right.found && left.projectionCount == right.projectionCount &&
		left.releaseUpdated.Equal(right.releaseUpdated) &&
		left.lastProjectionUpdated.Equal(right.lastProjectionUpdated)
}

type postgresReleaseProjectionSnapshotLoader struct{ pool *pgxpool.Pool }

func (loader *postgresReleaseProjectionSnapshotLoader) LoadReleaseProjectionSnapshot(
	ctx context.Context,
	tenantID string,
	domainID string,
	releaseID string,
) (snapshot releaseProjectionSnapshot, err error) {
	if loader == nil || loader.pool == nil {
		return releaseProjectionSnapshot{}, ErrProjectionGuardInvalid
	}
	snapshot = releaseProjectionSnapshot{
		releaseID:   releaseID,
		projections: make(map[string]releaseProjectionRecord, len(governedProjectionTargets)),
	}
	err = database.WithTenantTx(ctx, loader.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT
			release.status,release.content_hash,release.updated_at,
			projection.target,projection.status,projection.expected_content_hash,
			projection.applied_content_hash,projection.updated_at
		FROM askdata.releases AS release
		LEFT JOIN askdata.release_projections AS projection
		  ON projection.tenant_id=release.tenant_id
		 AND projection.domain_id=release.domain_id
		 AND projection.release_id=release.id
		WHERE release.tenant_id=$1 AND release.domain_id=$2 AND release.id=$3
		ORDER BY projection.target`, tenantID, domainID, releaseID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var target, status, expected, applied sql.NullString
			var projectionUpdated sql.NullTime
			if err := rows.Scan(
				&snapshot.status, &snapshot.contentHash, &snapshot.lastUpdated,
				&target, &status, &expected, &applied, &projectionUpdated,
			); err != nil {
				return err
			}
			snapshot.found = true
			if target.Valid {
				snapshot.projections[target.String] = releaseProjectionRecord{
					target: target.String, status: status.String,
					expected: expected.String, applied: applied.String,
					lastUpdated: projectionUpdated.Time,
				}
			}
		}
		return rows.Err()
	})
	return snapshot, err
}

func (loader *postgresReleaseProjectionSnapshotLoader) LoadReleaseProjectionRevision(
	ctx context.Context,
	tenantID string,
	domainID string,
	releaseID string,
) (revision releaseProjectionRevision, err error) {
	if loader == nil || loader.pool == nil {
		return releaseProjectionRevision{}, ErrProjectionGuardInvalid
	}
	err = database.WithTenantTx(ctx, loader.pool, tenantID, func(tx pgx.Tx) error {
		var latest sql.NullTime
		err := tx.QueryRow(ctx, `SELECT release.updated_at,count(projection.id)::integer,
			max(projection.updated_at)
		FROM askdata.releases AS release
		LEFT JOIN askdata.release_projections AS projection
		  ON projection.tenant_id=release.tenant_id
		 AND projection.domain_id=release.domain_id
		 AND projection.release_id=release.id
		WHERE release.tenant_id=$1 AND release.domain_id=$2 AND release.id=$3
		GROUP BY release.updated_at`, tenantID, domainID, releaseID).Scan(
			&revision.releaseUpdated, &revision.projectionCount, &latest,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		revision.found = true
		revision.lastProjectionUpdated = latest.Time
		return nil
	})
	return revision, err
}

var _ releaseProjectionSnapshotLoader = (*postgresReleaseProjectionSnapshotLoader)(nil)
