package reporthttp

import (
	"testing"

	"intelligent-report-generation-system/internal/askdata"
	reportmodel "intelligent-report-generation-system/internal/report"
	"intelligent-report-generation-system/internal/report/reportai"
)

func TestBlankReportTemplateSections(t *testing.T) {
	sections := blankReportTemplateSections()
	if sections == nil || len(sections) != 0 {
		t.Fatalf("blank report must not pre-create body sections, got %#v", sections)
	}

	definition := newReportDefinition(
		askdata.ID("11111111-1111-4111-8111-111111111111"), "模板测试", "",
		reportmodel.DataContext{
			ID:               askdata.ID("22222222-2222-4222-8222-222222222222"),
			DatasetID:        askdata.ID("33333333-3333-4333-8333-333333333333"),
			DatasetVersionID: askdata.ID("44444444-4444-4444-8444-444444444444"),
			Alias:            "测试数据", DefaultParameters: []reportmodel.DefaultParameter{},
			QueryPolicy: reportmodel.QueryPolicy{TimeoutMS: 10_000, MaxRows: 5_000, CacheTTLSeconds: 300},
		}, reportmodel.CreatedManually,
	)
	definition.Pages[0].Sections = sections
	if err := definition.Validate(); err != nil {
		t.Fatalf("header-only blank report must be a valid Report Definition: %v", err)
	}
}

func TestApplyCreationPresentationPersistsHeaderAndCreatesGovernedFilters(t *testing.T) {
	context := reportmodel.DataContext{
		ID: askdata.ID("22222222-2222-4222-8222-222222222222"), DatasetID: askdata.ID("33333333-3333-4333-8333-333333333333"),
		DatasetVersionID: askdata.ID("44444444-4444-4444-8444-444444444444"), Alias: "经营数据",
		DefaultParameters: []reportmodel.DefaultParameter{}, QueryPolicy: reportmodel.QueryPolicy{TimeoutMS: 10_000, MaxRows: 5_000, CacheTTLSeconds: 300},
	}
	candidate := reportai.DataContextCandidate{DataContext: context, Fields: []string{"month", "region", "product", "revenue"}, FieldDefinitions: []reportai.FieldDefinition{
		{Code: "month", Name: "月份", CanonicalType: "DATE", SemanticType: "TIME", Role: "TIME"},
		{Code: "region", Name: "区域", CanonicalType: "STRING", SemanticType: "REGION", Role: "DIMENSION"},
		{Code: "product", Name: "产品", CanonicalType: "STRING", SemanticType: "PRODUCT", Role: "DIMENSION"},
		{Code: "revenue", Name: "收入", CanonicalType: "DECIMAL", SemanticType: "CURRENCY", Role: "MEASURE"},
	}}
	base := newReportDefinition(askdata.ID("11111111-1111-4111-8111-111111111111"), "报告头测试", "", context, reportmodel.CreatedManually)
	definition := applyCreationPresentation(base, reportmodel.ReportTypeReport, reportmodel.ReportHeaderStyle02, []reportai.DataContextCandidate{candidate})
	if definition.Metadata.HeaderStyle != reportmodel.ReportHeaderStyle02 {
		t.Fatalf("header style = %q, want 02", definition.Metadata.HeaderStyle)
	}
	if len(definition.GlobalFilters) != 3 {
		t.Fatalf("global filters = %d, want 3", len(definition.GlobalFilters))
	}
	if definition.GlobalFilters[0].Type != reportmodel.FilterDateRange || definition.GlobalFilters[0].FieldRef.Field != "month" {
		t.Fatalf("first filter should be governed time range: %#v", definition.GlobalFilters[0])
	}
	if definition.GlobalFilters[0].Label != "月份" || definition.GlobalFilters[1].Label != "区域" || definition.GlobalFilters[2].Label != "产品" {
		t.Fatalf("filters must persist governed LLM-enriched field names: %#v", definition.GlobalFilters)
	}
	for _, filter := range definition.GlobalFilters {
		if filter.FieldRef.Field == "revenue" {
			t.Fatalf("measure field must not become a default filter: %#v", filter)
		}
	}
	if err := definition.Validate(); err != nil {
		t.Fatalf("definition with report header and default filters must validate: %v", err)
	}
}

func TestDefaultReportFiltersReserveDistinctAdvancedFields(t *testing.T) {
	context := reportmodel.DataContext{
		ID: askdata.ID("22222222-2222-4222-8222-222222222222"), DatasetID: askdata.ID("33333333-3333-4333-8333-333333333333"),
		DatasetVersionID: askdata.ID("44444444-4444-4444-8444-444444444444"), Alias: "经营数据",
		DefaultParameters: []reportmodel.DefaultParameter{}, QueryPolicy: reportmodel.QueryPolicy{TimeoutMS: 10_000, MaxRows: 5_000, CacheTTLSeconds: 300},
	}
	candidate := reportai.DataContextCandidate{DataContext: context, FieldDefinitions: []reportai.FieldDefinition{
		{Code: "period", CanonicalType: "DATE", Role: "TIME"},
		{Code: "region", CanonicalType: "STRING", Role: "DIMENSION"},
		{Code: "product", CanonicalType: "STRING", Role: "DIMENSION"},
		{Code: "channel", CanonicalType: "STRING", Role: "DIMENSION"},
		{Code: "customer", CanonicalType: "STRING", Role: "DIMENSION"},
		{Code: "level", CanonicalType: "STRING", Role: "DIMENSION"},
		{Code: "caliber", CanonicalType: "STRING", Role: "DIMENSION"},
		{Code: "business", CanonicalType: "STRING", Role: "DIMENSION"},
		{Code: "unused", CanonicalType: "STRING", Role: "DIMENSION"},
		{Code: "revenue", CanonicalType: "DECIMAL", Role: "MEASURE"},
	}}
	filters := defaultReportFilters([]reportai.DataContextCandidate{candidate})
	if len(filters) != 8 {
		t.Fatalf("default filters = %d, want four primary and four advanced", len(filters))
	}
	seen := map[string]bool{}
	for _, filter := range filters {
		if seen[filter.FieldRef.Field] {
			t.Fatalf("default filter field %q was duplicated", filter.FieldRef.Field)
		}
		seen[filter.FieldRef.Field] = true
	}
	if seen["unused"] || seen["revenue"] {
		t.Fatalf("filter limit or measure guard failed: %#v", seen)
	}
}
