package policy

import "testing"

func TestCacheKeyIsolationAndStability(t *testing.T) {
	base := CacheKeyInput{TenantID: "tenant-a", UserScope: "user-a", DatasetVersion: "dataset-v1", Parameters: map[string]any{"year": 2026}, RowPolicyVersions: []int64{2, 1}, ColumnPolicyVersions: []int64{4, 3}, DataWatermark: "w1", EngineVersion: "v1"}
	a, err := BuildCacheKey(base)
	if err != nil {
		t.Fatal(err)
	}
	same := base
	same.RowPolicyVersions = []int64{1, 2}
	b, _ := BuildCacheKey(same)
	if a != b {
		t.Fatal("equivalent scopes produced different keys")
	}
	otherTenant := base
	otherTenant.TenantID = "tenant-b"
	c, _ := BuildCacheKey(otherTenant)
	if a == c {
		t.Fatal("cache key crossed tenants")
	}
	otherUser := base
	otherUser.UserScope = "user-b"
	d, _ := BuildCacheKey(otherUser)
	if a == d {
		t.Fatal("cache key crossed users")
	}
	newPolicy := base
	newPolicy.RowPolicyVersions = []int64{1, 3}
	e, _ := BuildCacheKey(newPolicy)
	if a == e {
		t.Fatal("policy version did not invalidate cache key")
	}
}
