package semanticqa

import (
	"errors"
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
		if prepared.Document.Dataset.Name !=
			MarketAnalysisTemplateName(code)+" · "+source.Name {
			t.Fatalf("%s business dataset name=%q", code, prepared.Document.Dataset.Name)
		}
	}
}

func TestGeneratedDWSCodeCarriesPhysicalSourcePrefix(t *testing.T) {
	code := generatedDWSCode(
		"TREND",
		"dwd_operations_business_analysis_order_item",
	)
	if code != "dws_operations_business_analysis_order_item_trend" {
		t.Fatalf("DWS physical code=%q", code)
	}
}

func TestBuildMultiFactDWSCandidateAggregatesFactsAtSharedGrain(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/semantic-qa/dwd-multi-dimension.json")
	if err != nil {
		t.Fatal(err)
	}
	sourcePrepared, err := dataset.Prepare(raw)
	if err != nil {
		t.Fatal(err)
	}
	first := dwsPlanningAsset{
		Record: dataset.Record{
			ID:    "80000000-0000-4000-8000-000000000001",
			Code:  sourcePrepared.Document.Dataset.Code,
			Name:  sourcePrepared.Document.Dataset.Name,
			Layer: dataset.LayerDWD,
		},
		VersionID: "30000000-0000-4000-8000-000000000001",
		Document:  sourcePrepared.Document,
	}
	second := first
	second.Record.ID = "80000000-0000-4000-8000-000000000002"
	second.Record.Code = "dwd_operations_business_analysis_delivery"
	second.Record.Name = "配送履约事实"
	second.VersionID = "30000000-0000-4000-8000-000000000002"

	prepared, err := buildMultiFactDWSCandidate(
		[]dwsPlanningAsset{first, second},
		dwsModelingScope{
			GroupKey: "operations:business", DomainCode: "operations",
			SubjectCode: "business", SubjectName: "经营分析",
		},
		"MULTI_FACT_COMPARISON",
	)
	if err != nil {
		var validationError *dataset.ValidationError
		if errors.As(err, &validationError) {
			t.Fatalf("%v: %#v", err, validationError.Issues)
		}
		t.Fatal(err)
	}
	document := prepared.Document
	if document.Dataset.Layer != dataset.LayerDWS ||
		document.Dataset.Code != "dws_operations_business_analysis_multi_fact_summary" ||
		document.AnalysisContract == nil ||
		document.AnalysisContract.InputMode != "MULTI_FACT" {
		t.Fatalf("multi-fact descriptor=%#v contract=%#v", document.Dataset, document.AnalysisContract)
	}
	if len(document.Nodes) != 2 || len(document.PreAggregations) != 2 ||
		len(document.Joins) != 1 || len(document.AnalysisContract.Measures) != 2 {
		t.Fatalf(
			"multi-fact shape nodes=%d preaggregations=%d joins=%d measures=%d",
			len(document.Nodes), len(document.PreAggregations), len(document.Joins),
			len(document.AnalysisContract.Measures),
		)
	}
	if document.PreAggregations[0].GroupBy[0].Field != "stat_month" ||
		document.PreAggregations[1].GroupBy[0].Field != "stat_month" ||
		document.Joins[0].Cardinality != "ONE_TO_ONE" {
		t.Fatalf("multi-fact common grain was not proven: %#v", document)
	}
}

func TestMultiFactEligibilityUsesTemporalGrainAndSkipsTimelessFacts(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/semantic-qa/dwd-multi-dimension.json")
	if err != nil {
		t.Fatal(err)
	}
	sourcePrepared, err := dataset.Prepare(raw)
	if err != nil {
		t.Fatal(err)
	}
	temporal := sourcePrepared.Document
	temporal.FactContract.EventTimeField = ""
	temporal.OutputGrain.TimeField = ""
	temporal.Fields = append(temporal.Fields, dataset.Field{
		ID: "field_metric_date", Code: "metric_date", Name: "统计日期",
		Role: "IDENTIFIER", CanonicalType: "DATE", Nullable: false,
		Expression: dataset.Expression{
			Type: "FIELD_REF", NodeID: temporal.Nodes[0].ID, Field: "metric_date",
		},
	})
	temporal.OutputGrain.KeyFields = append(
		[]string{"metric_date"}, temporal.OutputGrain.KeyFields...,
	)
	if effectiveFactTimeField(temporal) != "metric_date" {
		t.Fatalf("temporal grain was not used: %#v", temporal.OutputGrain)
	}

	timeless := temporal
	timeless.Fields = append([]dataset.Field(nil), sourcePrepared.Document.Fields...)
	timeless.OutputGrain = sourcePrepared.Document.OutputGrain
	timeless.OutputGrain.TimeField = ""
	timeless.FactContract = &dataset.FactContract{}
	eligible := multiFactEligibleSources([]dwsPlanningAsset{
		{Document: temporal},
		{Document: timeless},
	})
	if len(eligible) != 1 ||
		effectiveFactTimeField(eligible[0].Document) != "metric_date" {
		t.Fatalf("eligible sources=%#v", eligible)
	}
}

func MarketAnalysisTemplateName(code string) string {
	for _, template := range MarketAnalysisTemplates() {
		if template.Code == code {
			return template.Name
		}
	}
	return ""
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
