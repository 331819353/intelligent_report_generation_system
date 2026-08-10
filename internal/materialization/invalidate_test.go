package materialization

import (
	"fmt"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	askdataqueryruntime "intelligent-report-generation-system/internal/askdata/queryruntime"
)

func TestSnapshotCompletionActivelyInvalidatesResultCache(t *testing.T) {
	release := askdata.ReleaseRef{
		ReleaseID: "release-v1", ContentHash: askdata.HashBytes([]byte("release-v1")),
	}
	scope, err := askdata.NewPolicyScope(
		"00000000-0000-0000-0000-000000000001", "actor-1",
		[]askdata.ID{"domain-sales"}, []askdata.ID{"role-1"}, release,
	)
	if err != nil {
		t.Fatal(err)
	}
	completedAt := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	request := askdataqueryruntime.CacheRequest{
		Scope: scope, Release: release, IRHash: askdata.HashBytes([]byte("normalized-ir")),
		Snapshots: []askdataqueryruntime.SnapshotVersion{{
			MaterializationID:   "00000000-0000-0000-0000-000000000002",
			SnapshotVersion:     "00000000-0000-0000-0000-000000000003",
			SnapshotCompletedAt: completedAt,
		}},
	}
	cache := askdataqueryruntime.NewResultCache()
	if _, stored, err := cache.Put(request, askdataqueryruntime.CachedResult{
		ResultHash: askdata.HashBytes([]byte("query-result")), RowCount: 1,
		Payload: []byte(`{"rows":[[42]]}`),
	}); err != nil || !stored {
		t.Fatalf("Put() stored=%v err=%v", stored, err)
	}
	if _, hit := cache.Get(request, askdataqueryruntime.LookupOptions{}); !hit {
		t.Fatal("precondition: cache did not hit")
	}
	projector := &SnapshotInvalidationProjector{invalidator: cache}
	payload := fmt.Sprintf(
		`{"tenantId":%q,"materializationId":%q,"snapshotVersion":%q,"qualityStatus":"OK"}`,
		scope.TenantID, request.Snapshots[0].MaterializationID, request.Snapshots[0].SnapshotVersion,
	)
	if removed, err := projector.Project(payload); err != nil || removed != 1 {
		t.Fatalf("Project() removed=%d err=%v", removed, err)
	}
	if _, hit := cache.Get(request, askdataqueryruntime.LookupOptions{}); hit {
		t.Fatal("refresh completion did not immediately invalidate the cached result")
	}
}

func TestSnapshotCompletionRejectsMalformedOrCrossTenantPayload(t *testing.T) {
	invalidator := &recordingSnapshotInvalidator{}
	projector := &SnapshotInvalidationProjector{invalidator: invalidator}
	invalid := []string{
		`not-json`,
		`{"tenantId":"tenant-a"}`,
		`{"tenantId":"00000000-0000-0000-0000-000000000001","materializationId":"00000000-0000-0000-0000-000000000002","snapshotVersion":"00000000-0000-0000-0000-000000000003","qualityStatus":"UNKNOWN"}`,
	}
	for _, payload := range invalid {
		if removed, err := projector.Project(payload); err != ErrInvalidSnapshotCompletion || removed != 0 {
			t.Fatalf("Project(%q) = %d, %v", payload, removed, err)
		}
	}
	if invalidator.calls != 0 {
		t.Fatalf("invalid notification reached cache invalidator %d times", invalidator.calls)
	}
}

type recordingSnapshotInvalidator struct{ calls int }

func (invalidator *recordingSnapshotInvalidator) InvalidateBySnapshot(_, _ string) int {
	invalidator.calls++
	return 0
}
