package shared

import (
	"testing"

	"intelligent-report-generation-system/internal/askdata"
)

func TestIsStaleComparesEverySharedFactorAndFailsClosed(t *testing.T) {
	base := Provenance{
		DatasetVersionID: "dataset:v1", DataSnapshotVersion: "snapshot-v1",
		QueryHash: askdata.HashBytes([]byte("query")), FilterHash: askdata.HashBytes([]byte("filter")),
		AnalysisMethodVersion: "1.2.0", EvidenceAlgorithmVersion: "1.1.0",
		PromptVersion: "answer-v3", ModelPolicy: "narrative-standard",
		VerifierVersion: "1.0.0", PolicyWordlistVersion: "1.0.0",
		EvidenceHash: askdata.HashBytes([]byte("evidence")), ResultHash: askdata.HashBytes([]byte("result")),
		SemanticReleaseID: "release:v1", ChartRuleVersion: "1.0.0",
	}
	if IsStale(base, base) {
		t.Fatal("identical provenance is stale")
	}
	changed := base
	changed.DataSnapshotVersion = "snapshot-v2"
	if !IsStale(base, changed) {
		t.Fatal("changed provenance was current")
	}
	invalid := base
	invalid.QueryHash = "invalid"
	if !IsStale(base, invalid) {
		t.Fatal("invalid current provenance was current")
	}
}
