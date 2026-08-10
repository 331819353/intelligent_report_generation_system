package registry

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"intelligent-report-generation-system/internal/askdata"
)

func TestEvaluateReleasePreflightPassesCanonicalQualityRule(t *testing.T) {
	object := lifecycleReleaseObject(t, ReleaseObjectQualityRule, map[string]any{
		"severity": "ERROR", "ruleAst": map[string]any{"type": "NOT_NULL"},
	})
	manifest, err := BuildReleaseManifest([]ReleaseObject{object})
	if err != nil {
		t.Fatal(err)
	}
	result := EvaluateReleasePreflight(uuid.NewString(), manifest.ContentHash, manifest.Objects)
	if !result.Passed || len(result.Issues) != 0 || result.ObjectCount != 1 {
		t.Fatalf("preflight = %#v", result)
	}
}

func TestEvaluateReleasePreflightRejectsRestrictedVectorAndHashDrift(t *testing.T) {
	object := lifecycleReleaseObject(t, ReleaseObjectDimension, map[string]any{
		"sensitivity": "RESTRICTED", "memberIndexPolicy": "FULL",
	})
	object.Sensitivity = SensitivityRestricted
	manifest, err := BuildReleaseManifest([]ReleaseObject{object})
	if err != nil {
		t.Fatal(err)
	}
	result := EvaluateReleasePreflight(uuid.NewString(), askdata.HashBytes([]byte("wrong release")), manifest.Objects)
	if result.Passed || len(result.Issues) != 2 ||
		result.Issues[0].Code != "RELEASE_MANIFEST_HASH_MISMATCH" ||
		result.Issues[1].Code != "RELEASE_RESTRICTED_VECTOR_POLICY" {
		t.Fatalf("preflight = %#v", result)
	}
}

func TestEvaluateReleasePreflightRejectsUnsafeFanout(t *testing.T) {
	object := lifecycleReleaseObject(t, ReleaseObjectRelationship, map[string]any{
		"cardinality": "MANY_TO_MANY", "fanoutPolicy": "SAFE",
	})
	manifest, err := BuildReleaseManifest([]ReleaseObject{object})
	if err != nil {
		t.Fatal(err)
	}
	result := EvaluateReleasePreflight(uuid.NewString(), manifest.ContentHash, manifest.Objects)
	if result.Passed || len(result.Issues) != 1 || result.Issues[0].Code != "RELEASE_RELATIONSHIP_FANOUT_UNSAFE" {
		t.Fatalf("preflight = %#v", result)
	}
}

func lifecycleReleaseObject(t *testing.T, objectType ReleaseObjectType, contract any) ReleaseObject {
	t.Helper()
	canonical, err := CanonicalValue(contract)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(canonical) {
		t.Fatal("contract is not JSON")
	}
	return ReleaseObject{
		Type: objectType, ObjectID: uuid.NewString(), ObjectVersionID: uuid.NewString(),
		ContentHash: askdata.HashBytes(canonical), Sensitivity: SensitivityInternal,
		Contract: canonical,
	}
}
