package queryruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"testing"
	"testing/quick"
	"time"

	"intelligent-report-generation-system/internal/askdata"
)

func TestBuildKeyUsesFixedSegmentsAndSortedSnapshots(t *testing.T) {
	scope := cacheScope(t, "actor-1", "role-1")
	release := scope.Release
	irHash := askdata.HashBytes([]byte("normalized-ir"))
	snapshots := []SnapshotVersion{
		{MaterializationID: "materialization-b", SnapshotVersion: "snapshot-2"},
		{MaterializationID: "materialization-a", SnapshotVersion: "snapshot-1"},
	}
	payload := fmt.Sprintf("%s\x1f%s\x1f%s\x1f%s\x1f%s",
		scope.TenantID, scope.PolicyHash, release.ContentHash, irHash,
		"materialization-a:snapshot-1,materialization-b:snapshot-2")
	sum := sha256.Sum256([]byte(payload))
	want := hex.EncodeToString(sum[:])
	if got := BuildKey(scope, release, irHash, snapshots); got != want {
		t.Fatalf("BuildKey() = %q, want %q", got, want)
	}
	if reversed := BuildKey(scope, release, irHash, []SnapshotVersion{snapshots[1], snapshots[0]}); reversed != want {
		t.Fatalf("snapshot ordering changed key: %q", reversed)
	}
}

func TestPolicyScopePropertyNeverSharesKeyOrEntry(t *testing.T) {
	now := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	property := func(left, right uint32) bool {
		if left == right {
			right++
		}
		leftScope := cacheScope(t, "actor-shared", askdata.ID(fmt.Sprintf("role-%d", left)))
		rightScope := cacheScope(t, "actor-shared", askdata.ID(fmt.Sprintf("role-%d", right)))
		request := cacheRequest(leftScope, now)
		other := cacheRequest(rightScope, now)
		leftKey := BuildKey(request.Scope, request.Release, request.IRHash, request.Snapshots)
		rightKey := BuildKey(other.Scope, other.Release, other.IRHash, other.Snapshots)
		if leftKey == "" || rightKey == "" || leftKey == rightKey {
			return false
		}
		cache := newResultCache(DefaultResultCacheTTL, func() time.Time { return now })
		if _, stored, err := cache.Put(request, cacheResult()); err != nil || !stored {
			return false
		}
		_, hit := cache.Get(other, LookupOptions{})
		return !hit
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 256}); err != nil {
		t.Fatal(err)
	}
}

func TestCacheStoresMetadataAsOfAndIsolatesPayload(t *testing.T) {
	now := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	cache := newResultCache(DefaultResultCacheTTL, func() time.Time { return now })
	request := cacheRequest(cacheScope(t, "actor-1", "role-1"), now)
	request.Snapshots = append(request.Snapshots, SnapshotVersion{
		MaterializationID: "materialization-b", SnapshotVersion: "snapshot-b",
		SnapshotCompletedAt: now.Add(-10 * time.Minute),
	})
	entry, stored, err := cache.Put(request, cacheResult())
	if err != nil || !stored {
		t.Fatalf("Put() = %#v, %v, %v", entry, stored, err)
	}
	if entry.AsOf != now.Add(-10*time.Minute) || entry.CreatedAt != now ||
		entry.TTL != time.Hour || entry.RowCount != 1 {
		t.Fatalf("unexpected cache metadata: %#v", entry)
	}
	first := entry.Result()
	first[0] = '['
	hit, ok := cache.Get(request, LookupOptions{})
	if !ok || string(hit.Result()) != `{"rows":[[42]]}` || hit.AsOf != entry.AsOf {
		t.Fatalf("Get() = %#v, %v, payload=%s", hit, ok, hit.Result())
	}
}

func TestSnapshotChangeAndForceFreshBypassOldEntry(t *testing.T) {
	now := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	cache := newResultCache(DefaultResultCacheTTL, func() time.Time { return now })
	request := cacheRequest(cacheScope(t, "actor-1", "role-1"), now)
	if _, stored, err := cache.Put(request, cacheResult()); err != nil || !stored {
		t.Fatalf("Put() stored=%v err=%v", stored, err)
	}
	current := cloneSnapshots(request.Snapshots)
	current[0].SnapshotVersion = "snapshot-v2"
	current[0].SnapshotCompletedAt = now.Add(time.Minute)
	changedRequest := request
	changedRequest.Snapshots = current
	if _, hit := cache.Get(changedRequest, LookupOptions{}); hit {
		t.Fatal("changed snapshot unexpectedly hit the old cache key")
	}
	if _, hit := cache.Get(request, LookupOptions{
		ForceFresh: true, CurrentSnapshots: current,
	}); hit {
		t.Fatal("forceFresh unexpectedly returned a stale cached result")
	}
	if _, hit := cache.Get(request, LookupOptions{ForceFresh: true}); hit {
		t.Fatal("forceFresh without current snapshot proof must fail closed")
	}
	if _, hit := cache.Get(request, LookupOptions{
		ForceFresh: true, CurrentSnapshots: request.Snapshots,
	}); !hit {
		t.Fatal("forceFresh with the unchanged current snapshot should hit")
	}
	if _, hit := cache.Get(request, LookupOptions{}); !hit {
		t.Fatal("ordinary pinned-snapshot lookup should still hit before invalidation")
	}
}

func TestMissingScopeOrSnapshotsDoesNotWrite(t *testing.T) {
	now := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	cache := newResultCache(DefaultResultCacheTTL, func() time.Time { return now })
	request := cacheRequest(cacheScope(t, "actor-1", "role-1"), now)
	tests := []CacheRequest{request, request}
	tests[0].Scope.PolicyHash = ""
	tests[1].Snapshots = nil
	for _, invalid := range tests {
		if entry, stored, err := cache.Put(invalid, cacheResult()); err != nil || stored || !reflect.DeepEqual(entry, CacheEntry{}) {
			t.Fatalf("unsafe Put() = %#v, %v, %v", entry, stored, err)
		}
	}
	if len(cache.entries) != 0 || len(cache.index.bySnapshot) != 0 {
		t.Fatalf("unsafe write changed cache/index: %d/%d", len(cache.entries), len(cache.index.bySnapshot))
	}
}

func TestReverseIndexStaysConsistentOnDeleteInvalidationAndExpiry(t *testing.T) {
	now := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	cache := newResultCache(DefaultResultCacheTTL, func() time.Time { return now })
	scope := cacheScope(t, "actor-1", "role-1")
	request := cacheRequest(scope, now)
	request.Snapshots = append(request.Snapshots, SnapshotVersion{
		MaterializationID: "materialization-b", SnapshotVersion: "snapshot-b",
		SnapshotCompletedAt: now,
	})
	if _, stored, err := cache.Put(request, cacheResult()); err != nil || !stored {
		t.Fatalf("Put() stored=%v err=%v", stored, err)
	}
	key := BuildKey(scope, scope.Release, request.IRHash, request.Snapshots)
	if len(cache.index.bySnapshot) != 2 || !cache.Delete(key) ||
		len(cache.entries) != 0 || len(cache.index.bySnapshot) != 0 {
		t.Fatalf("Delete left inconsistent cache/index: %d/%d", len(cache.entries), len(cache.index.bySnapshot))
	}
	if _, stored, err := cache.Put(request, cacheResult()); err != nil || !stored {
		t.Fatal(err)
	}
	if removed := cache.InvalidateBySnapshot(string(scope.TenantID), "materialization-a"); removed != 1 ||
		len(cache.entries) != 0 || len(cache.index.bySnapshot) != 0 {
		t.Fatalf("Invalidate removed=%d cache/index=%d/%d", removed, len(cache.entries), len(cache.index.bySnapshot))
	}
	if _, stored, err := cache.Put(request, cacheResult()); err != nil || !stored {
		t.Fatal(err)
	}
	now = now.Add(DefaultResultCacheTTL)
	if _, hit := cache.Get(request, LookupOptions{}); hit ||
		len(cache.entries) != 0 || len(cache.index.bySnapshot) != 0 {
		t.Fatalf("expiry left inconsistent cache/index: hit=%v %d/%d", hit, len(cache.entries), len(cache.index.bySnapshot))
	}
}

func TestInvalidResultDoesNotOverwriteExistingEntry(t *testing.T) {
	now := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	cache := newResultCache(DefaultResultCacheTTL, func() time.Time { return now })
	request := cacheRequest(cacheScope(t, "actor-1", "role-1"), now)
	original := cacheResult()
	if _, stored, err := cache.Put(request, original); err != nil || !stored {
		t.Fatal(err)
	}
	invalid := original
	invalid.Payload = []byte("not-json")
	if _, stored, err := cache.Put(request, invalid); !errorsIs(err, ErrInvalidCacheResult) || stored {
		t.Fatalf("invalid Put() stored=%v err=%v", stored, err)
	}
	entry, hit := cache.Get(request, LookupOptions{})
	if !hit || !reflect.DeepEqual(entry.Result(), original.Payload) {
		t.Fatal("invalid replacement destroyed the previous safe entry")
	}
}

func cacheScope(t *testing.T, actorID, roleID askdata.ID) askdata.PolicyScope {
	t.Helper()
	release := askdata.ReleaseRef{
		ReleaseID: "release-v1", ContentHash: askdata.HashBytes([]byte("release-v1")),
	}
	scope, err := askdata.NewPolicyScope(
		"00000000-0000-0000-0000-000000000001", actorID,
		[]askdata.ID{"domain-sales"}, []askdata.ID{roleID}, release,
	)
	if err != nil {
		t.Fatal(err)
	}
	return scope
}

func cacheRequest(scope askdata.PolicyScope, completedAt time.Time) CacheRequest {
	return CacheRequest{
		Scope: scope, Release: scope.Release,
		IRHash: askdata.HashBytes([]byte("normalized-ir")),
		Snapshots: []SnapshotVersion{{
			MaterializationID: "materialization-a", SnapshotVersion: "snapshot-v1",
			SnapshotCompletedAt: completedAt,
		}},
	}
}

func cacheResult() CachedResult {
	payload := []byte(`{"rows":[[42]]}`)
	return CachedResult{
		ResultHash: askdata.HashBytes([]byte("query-result")), RowCount: 1, Payload: payload,
	}
}

func errorsIs(err, target error) bool {
	return err == target
}
