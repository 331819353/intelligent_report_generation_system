package policy

import (
	"errors"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
)

func TestSemanticAccessSnapshotIsCanonicalAndScopeBound(t *testing.T) {
	scope := policyTestScope(t, "tenant-a", "sales", "analyst")
	request := SemanticAccessRequest{
		Scope: scope, DomainID: "sales", Projection: SemanticProjectionRegistry,
		Objects: mustPolicyRefs(t,
			SemanticObjectRef{DomainID: "sales", ObjectType: "DIMENSION", ObjectID: "dimension-1", ObjectVersionID: "dimension-version-1"},
			SemanticObjectRef{DomainID: "sales", ObjectType: "METRIC", ObjectID: "metric-1", ObjectVersionID: "metric-version-1"},
		),
	}
	snapshot, err := NewSemanticAccessSnapshot(request, request.Objects)
	if err != nil || snapshot.ValidateAgainst(request) != nil {
		t.Fatalf("NewSemanticAccessSnapshot() = %#v, %v", snapshot, err)
	}

	driftedScope := policyTestScope(t, "tenant-a", "sales", "viewer")
	drifted := request
	drifted.Scope = driftedScope
	if !errors.Is(snapshot.ValidateAgainst(drifted), ErrInvalidSemanticAccess) {
		t.Fatal("snapshot accepted a different role scope")
	}
	tampered := snapshot
	tampered.Objects = append([]SemanticObjectRef(nil), snapshot.Objects...)
	tampered.Objects[0].ObjectVersionID = "dimension-version-2"
	if !errors.Is(tampered.ValidateAgainst(request), ErrInvalidSemanticAccess) {
		t.Fatal("snapshot accepted a tampered object set")
	}
}

func TestCanonicalSemanticObjectRefsRejectsDuplicates(t *testing.T) {
	ref := SemanticObjectRef{
		DomainID: "sales", ObjectType: "METRIC", ObjectID: "metric-1", ObjectVersionID: "metric-version-1",
	}
	if _, err := CanonicalSemanticObjectRefs([]SemanticObjectRef{ref, ref}); !errors.Is(err, ErrInvalidSemanticAccess) {
		t.Fatalf("duplicate error = %v", err)
	}
}

func TestBuildAskDataCacheKeyBindsPolicyReleaseIRAndWarehouseState(t *testing.T) {
	base := AskDataCacheKeyInput{
		Scope:  policyTestScope(t, "tenant-a", "sales", "analyst"),
		IRHash: askdata.HashBytes([]byte("ir")), SnapshotHash: askdata.HashBytes([]byte("snapshot")),
		FreshnessHash: askdata.HashBytes([]byte("freshness")), EngineVersion: "adapter-v2",
	}
	first, err := BuildAskDataCacheKey(base)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildAskDataCacheKey(base)
	if err != nil || first != second {
		t.Fatalf("stable key = %q, %q, %v", first, second, err)
	}

	variants := []AskDataCacheKeyInput{base, base, base, base}
	variants[0].Scope = policyTestScope(t, "tenant-b", "sales", "analyst")
	variants[1].Scope = policyTestScope(t, "tenant-a", "sales", "viewer")
	variants[2].IRHash = askdata.HashBytes([]byte("other ir"))
	variants[3].SnapshotHash = askdata.HashBytes([]byte("other snapshot"))
	for index, variant := range variants {
		key, err := BuildAskDataCacheKey(variant)
		if err != nil || key == first {
			t.Fatalf("variant[%d] key = %q, %v", index, key, err)
		}
	}
}

func policyTestScope(t *testing.T, tenantID, domainID, roleID askdata.ID) askdata.PolicyScope {
	t.Helper()
	scope, err := askdata.NewPolicyScope(
		tenantID, "actor-1", []askdata.ID{domainID}, []askdata.ID{roleID},
		askdata.ReleaseRef{ReleaseID: "release-1", ContentHash: askdata.HashBytes([]byte("release"))},
	)
	if err != nil {
		t.Fatal(err)
	}
	return scope
}

func mustPolicyRefs(t *testing.T, values ...SemanticObjectRef) []SemanticObjectRef {
	t.Helper()
	result, err := CanonicalSemanticObjectRefs(values)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
