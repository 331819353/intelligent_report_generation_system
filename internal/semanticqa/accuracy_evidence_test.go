package semanticqa

import (
	"testing"
	"time"

	"intelligent-report-generation-system/internal/dataset"
)

func TestBuildAnswerEvidenceIsStableAndCoversComparisonResult(t *testing.T) {
	verifiedAt := time.Date(2026, 8, 3, 10, 20, 30, 0, time.FixedZone("CST", 8*60*60))
	plan := QueryPlan{
		GraphGenerationID:         "11111111-1111-4111-8111-111111111111",
		GraphGeneration:           7,
		Intent:                    "COMPARISON",
		PathHash:                  hashText("certified-path"),
		SelectedMetricID:          "22222222-2222-4222-8222-222222222222",
		SelectedMetricVersionID:   "33333333-3333-4333-8333-333333333333",
		SelectedDatasetVersionID:  "44444444-4444-4444-8444-444444444444",
		SelectedMaterializationID: "55555555-5555-4555-8555-555555555555",
		Conditions: QueryConditionDocument{
			Domain: "sales", MetricCode: "paid_gmv",
			MetricVersionID:  "33333333-3333-4333-8333-333333333333",
			DatasetVersionID: "44444444-4444-4444-8444-444444444444",
		},
	}
	current := dataset.PreviewResult{
		QueryID: "66666666-6666-4666-8666-666666666666",
		Columns: []string{"paid_gmv"}, Rows: [][]any{{120.0}}, RowCount: 1,
	}
	baseline := dataset.PreviewResult{
		QueryID: "77777777-7777-4777-8777-777777777777",
		Columns: []string{"paid_gmv"}, Rows: [][]any{{100.0}}, RowCount: 1,
	}

	first, err := buildAnswerEvidence(plan, current, &baseline, verifiedAt)
	if err != nil {
		t.Fatalf("build evidence: %v", err)
	}
	second, err := buildAnswerEvidence(plan, current, &baseline, verifiedAt)
	if err != nil {
		t.Fatalf("rebuild evidence: %v", err)
	}
	if first.QueryPlanHash == "" || first.ResultHash == "" {
		t.Fatalf("expected plan and result hashes: %#v", first)
	}
	if first.QueryPlanHash != second.QueryPlanHash || first.ResultHash != second.ResultHash {
		t.Fatalf("evidence hashes must be stable: %#v %#v", first, second)
	}
	if first.SemanticVersion != "semantic-graph-7" ||
		first.VerifiedAt != "2026-08-03T02:20:30Z" ||
		first.QueryTraceID != current.QueryID {
		t.Fatalf("unexpected public evidence identity: %#v", first)
	}
	changedBaseline := baseline
	changedBaseline.Rows = [][]any{{99.0}}
	changed, err := buildAnswerEvidence(plan, current, &changedBaseline, verifiedAt)
	if err != nil {
		t.Fatalf("build changed evidence: %v", err)
	}
	if changed.ResultHash == first.ResultHash {
		t.Fatal("comparison baseline must participate in result hash")
	}
	if len(first.ValidatorChecks) != 7 {
		t.Fatalf("expected named validator checks, got %#v", first.ValidatorChecks)
	}
}
