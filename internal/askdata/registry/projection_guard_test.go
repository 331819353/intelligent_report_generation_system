package registry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/platform/database"
)

func TestProjectionGuardFailsClosedWithStableFourProjectionReport(t *testing.T) {
	tenantID, domainID, actorID, releaseID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	releaseHash := string(testProjectionHash("release"))
	updated := time.Date(2026, 8, 7, 9, 30, 0, 0, time.UTC)
	base := runnableProjectionSnapshot(releaseID, releaseHash, updated)

	tests := []struct {
		name       string
		mutate     func(*releaseProjectionSnapshot)
		projection string
		status     string
	}{
		{
			name: "registry hash", projection: "REGISTRY", status: "READY",
			mutate: func(snapshot *releaseProjectionSnapshot) {
				record := snapshot.projections["POSTGRES_REGISTRY"]
				record.applied = string(testProjectionHash("wrong-registry"))
				snapshot.projections["POSTGRES_REGISTRY"] = record
			},
		},
		{
			name: "search hash", projection: "SEARCH", status: "READY",
			mutate: func(snapshot *releaseProjectionSnapshot) {
				record := snapshot.projections["SEARCH_INDEX"]
				record.applied = string(testProjectionHash("wrong-search"))
				snapshot.projections["SEARCH_INDEX"] = record
			},
		},
		{
			name: "graph hash", projection: "GRAPH", status: "READY",
			mutate: func(snapshot *releaseProjectionSnapshot) {
				record := snapshot.projections["NEBULA_GRAPH"]
				record.applied = string(testProjectionHash("wrong-graph"))
				snapshot.projections["NEBULA_GRAPH"] = record
			},
		},
		{
			name: "member hash", projection: "MEMBER", status: "READY",
			mutate: func(snapshot *releaseProjectionSnapshot) {
				record := snapshot.projections["EXECUTION_SEMANTIC_LAYER"]
				record.applied = string(testProjectionHash("wrong-member"))
				snapshot.projections["EXECUTION_SEMANTIC_LAYER"] = record
			},
		},
		{
			name: "status", projection: "SEARCH", status: "FAILED",
			mutate: func(snapshot *releaseProjectionSnapshot) {
				record := snapshot.projections["SEARCH_INDEX"]
				record.status = "FAILED"
				snapshot.projections["SEARCH_INDEX"] = record
			},
		},
		{
			name: "missing", projection: "GRAPH", status: "MISSING",
			mutate: func(snapshot *releaseProjectionSnapshot) {
				delete(snapshot.projections, "NEBULA_GRAPH")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := cloneProjectionSnapshot(base)
			test.mutate(&snapshot)
			loader := &memoryProjectionSnapshotLoader{snapshot: snapshot}
			guard := newProjectionGuard(loader, DefaultProjectionGuardTTL, func() time.Time { return updated })
			ctx := projectionGuardTestContext(tenantID, domainID, actorID)
			err := guard.AssertRunnable(ctx, releaseID)
			var failure *ReleaseProjectionMismatchError
			if !errors.As(err, &failure) || !errors.Is(err, ErrReleaseProjectionMismatch) ||
				failure.Code != ReleaseProjectionMismatchCode || len(failure.Mismatches) != 1 {
				t.Fatalf("AssertRunnable() error = %#v, %v", failure, err)
			}
			mismatch := failure.Mismatches[0]
			if mismatch.Projection != test.projection || mismatch.Status != test.status ||
				mismatch.Expected != releaseHash {
				t.Fatalf("mismatch = %#v", mismatch)
			}
			if test.status != "MISSING" && mismatch.LastUpdated != updated {
				t.Fatalf("lastUpdated = %v", mismatch.LastUpdated)
			}
		})
	}
}

func TestProjectionGuardAllowsOnlyReadyOrActiveFourHashSnapshot(t *testing.T) {
	tenantID, domainID, actorID, releaseID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	now := time.Date(2026, 8, 7, 10, 0, 0, 0, time.UTC)
	snapshot := runnableProjectionSnapshot(releaseID, string(testProjectionHash("ready")), now)
	loader := &memoryProjectionSnapshotLoader{snapshot: snapshot}
	guard := newProjectionGuard(loader, DefaultProjectionGuardTTL, func() time.Time { return now })
	ctx := projectionGuardTestContext(tenantID, domainID, actorID)
	if err := guard.AssertRunnable(ctx, releaseID); err != nil {
		t.Fatalf("READY AssertRunnable() error = %v", err)
	}

	guard.Invalidate(releaseID)
	loader.snapshot.status = "ACTIVE"
	if err := guard.AssertRunnable(ctx, releaseID); err != nil {
		t.Fatalf("ACTIVE AssertRunnable() error = %v", err)
	}

	guard.Invalidate(releaseID)
	loader.snapshot.status = "SUPERSEDED"
	err := guard.AssertRunnable(ctx, releaseID)
	var failure *ReleaseProjectionMismatchError
	if !errors.As(err, &failure) || len(failure.Mismatches) != 1 ||
		failure.Mismatches[0].Projection != "RELEASE" || failure.ReleaseStatus != "SUPERSEDED" {
		t.Fatalf("SUPERSEDED AssertRunnable() = %#v, %v", failure, err)
	}

	guard.Invalidate(releaseID)
	loader.snapshot = releaseProjectionSnapshot{releaseID: releaseID, projections: map[string]releaseProjectionRecord{}}
	err = guard.AssertRunnable(ctx, releaseID)
	if !errors.As(err, &failure) || failure.ReleaseStatus != "MISSING" ||
		failure.Mismatches[0].Status != "MISSING" {
		t.Fatalf("missing AssertRunnable() = %#v, %v", failure, err)
	}
}

func TestProjectionGuardCachesForThirtySecondsAndInvalidates(t *testing.T) {
	tenantID, domainID, actorID, releaseID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	now := time.Date(2026, 8, 7, 11, 0, 0, 0, time.UTC)
	loader := &memoryProjectionSnapshotLoader{
		snapshot: runnableProjectionSnapshot(releaseID, string(testProjectionHash("cached")), now),
	}
	guard := newProjectionGuard(loader, DefaultProjectionGuardTTL, func() time.Time { return now })
	ctx := projectionGuardTestContext(tenantID, domainID, actorID)
	if err := guard.AssertRunnable(ctx, releaseID); err != nil {
		t.Fatal(err)
	}
	if err := guard.AssertRunnable(ctx, releaseID); err != nil || loader.calls != 1 || loader.revisionCalls != 1 {
		t.Fatalf("cache hit = calls %d/%d, err %v", loader.calls, loader.revisionCalls, err)
	}
	loader.snapshot.projections = map[string]releaseProjectionRecord{}
	if err := guard.AssertRunnable(ctx, releaseID); !errors.Is(err, ErrReleaseProjectionMismatch) ||
		loader.calls != 2 || loader.revisionCalls != 2 {
		t.Fatalf("revision invalidation = calls %d/%d, err %v", loader.calls, loader.revisionCalls, err)
	}

	loader.snapshot = runnableProjectionSnapshot(releaseID, string(testProjectionHash("cached")), now)
	if err := guard.AssertRunnable(ctx, releaseID); err != nil || loader.calls != 3 || loader.revisionCalls != 3 {
		t.Fatalf("revision recovery = calls %d/%d, err %v", loader.calls, loader.revisionCalls, err)
	}
	guard.Invalidate(releaseID)
	if err := guard.AssertRunnable(ctx, releaseID); err != nil || loader.calls != 4 {
		t.Fatalf("explicit invalidation = calls %d, err %v", loader.calls, err)
	}

	now = now.Add(DefaultProjectionGuardTTL)
	if err := guard.AssertRunnable(ctx, releaseID); err != nil || loader.calls != 5 {
		t.Fatalf("expired lookup = calls %d, err %v", loader.calls, err)
	}

	guard.InvalidateAll()
	if err := guard.AssertRunnable(ctx, releaseID); err != nil || loader.calls != 6 {
		t.Fatalf("InvalidateAll lookup = calls %d, err %v", loader.calls, err)
	}
}

func TestProjectionGuardRejectsUnscopedOrMismatchedContext(t *testing.T) {
	releaseID := uuid.NewString()
	guard := newProjectionGuard(&memoryProjectionSnapshotLoader{}, time.Second, time.Now)
	if err := guard.AssertRunnable(context.Background(), releaseID); !errors.Is(err, ErrProjectionGuardInvalid) {
		t.Fatalf("unscoped error = %v", err)
	}
	tenantID, domainID, actorID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	ctx := database.WithAccessContext(context.Background(), actorID, uuid.NewString())
	ctx = WithProjectionGuardScope(ctx, tenantID, domainID)
	if err := guard.AssertRunnable(ctx, releaseID); !errors.Is(err, ErrProjectionGuardInvalid) {
		t.Fatalf("domain mismatch error = %v", err)
	}
}

type memoryProjectionSnapshotLoader struct {
	snapshot      releaseProjectionSnapshot
	err           error
	calls         int
	revisionCalls int
}

func (loader *memoryProjectionSnapshotLoader) LoadReleaseProjectionRevision(
	context.Context, string, string, string,
) (releaseProjectionRevision, error) {
	loader.revisionCalls++
	return projectionSnapshotRevision(loader.snapshot), loader.err
}

func (loader *memoryProjectionSnapshotLoader) LoadReleaseProjectionSnapshot(
	context.Context, string, string, string,
) (releaseProjectionSnapshot, error) {
	loader.calls++
	return cloneProjectionSnapshot(loader.snapshot), loader.err
}

func runnableProjectionSnapshot(releaseID, releaseHash string, updated time.Time) releaseProjectionSnapshot {
	projections := make(map[string]releaseProjectionRecord, len(governedProjectionTargets))
	for _, target := range governedProjectionTargets {
		projections[target.databaseTarget] = releaseProjectionRecord{
			target: target.databaseTarget, status: "READY",
			expected: releaseHash, applied: releaseHash, lastUpdated: updated,
		}
	}
	return releaseProjectionSnapshot{
		found: true, releaseID: releaseID, status: "READY",
		contentHash: releaseHash, lastUpdated: updated, projections: projections,
	}
}

func cloneProjectionSnapshot(snapshot releaseProjectionSnapshot) releaseProjectionSnapshot {
	cloned := snapshot
	cloned.projections = make(map[string]releaseProjectionRecord, len(snapshot.projections))
	for key, value := range snapshot.projections {
		cloned.projections[key] = value
	}
	return cloned
}

func projectionGuardTestContext(tenantID, domainID, actorID string) context.Context {
	ctx := database.WithAccessContext(context.Background(), actorID, domainID)
	return WithProjectionGuardScope(ctx, tenantID, domainID)
}

func testProjectionHash(value string) askdata.ContentHash { return askdata.HashBytes([]byte(value)) }
