package semanticqa

import (
	"os"
	"testing"

	"intelligent-report-generation-system/internal/dataset"
)

func TestMarketAnalysisTemplatesAreBoundedLogicalCandidates(t *testing.T) {
	templates := MarketAnalysisTemplates()
	if len(templates) < 10 {
		t.Fatalf("template count=%d", len(templates))
	}
	seen := map[string]bool{}
	for _, item := range templates {
		if item.Code == "" || item.Intent == "" || seen[item.Code] ||
			item.MaterializationPolicy != "SUGGESTION_ONLY" ||
			len(item.SafetyRules) == 0 || len(item.NotApplicableWhen) == 0 {
			t.Fatalf("invalid template=%#v", item)
		}
		seen[item.Code] = true
	}
	if !seen["TREND"] || !seen["FUNNEL"] || !seen["RETENTION"] ||
		!seen["MULTI_FACT_COMPARISON"] {
		t.Fatalf("market coverage=%v", seen)
	}
}

func TestBuildSingleFactDWSCandidatesStayReviewOnlyAndValid(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/semantic-qa/dwd-multi-dimension.json")
	if err != nil {
		t.Fatal(err)
	}
	sourcePrepared, err := dataset.Prepare(raw)
	if err != nil {
		t.Fatal(err)
	}
	source := dataset.Record{
		ID:    "80000000-0000-4000-8000-000000000001",
		Code:  sourcePrepared.Document.Dataset.Code,
		Name:  sourcePrepared.Document.Dataset.Name,
		Layer: dataset.LayerDWD,
		DSL:   sourcePrepared.DSLJSON,
	}
	codes := autoEligibleTemplateCodes(sourcePrepared.Document)
	if len(codes) < 5 {
		t.Fatalf("auto eligible templates=%v", codes)
	}
	for _, code := range codes {
		prepared, err := buildSingleFactDWSCandidate(
			source, "30000000-0000-4000-8000-000000000010", code,
		)
		if err != nil {
			t.Fatalf("%s candidate: %v", code, err)
		}
		if prepared.Document.Dataset.Layer != dataset.LayerDWS ||
			prepared.Document.Dataset.SemanticContractVersion != "1.0" ||
			prepared.Document.AnalysisContract == nil ||
			prepared.Document.ExecutionPolicy.Materialization.RefreshMode != "MANUAL" {
			t.Fatalf("%s candidate=%#v", code, prepared.Document)
		}
	}
}

func TestValidatedTemplateSelectionCannotEscapeBoundedCandidates(t *testing.T) {
	selected := validatedTemplateSelection(
		[]string{" trend ", "DROP TABLE", "RANKING", "TREND", "FUNNEL"},
		[]string{"TREND", "RANKING", "DISTRIBUTION"},
	)
	if len(selected) != 2 || selected[0] != "TREND" || selected[1] != "RANKING" {
		t.Fatalf("selection escaped catalog boundary: %v", selected)
	}
}
