package reporthttp

import (
	"context"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
	reportmodel "intelligent-report-generation-system/internal/report"
	"intelligent-report-generation-system/internal/report/reportai"
	"intelligent-report-generation-system/internal/report/runtime"
	"intelligent-report-generation-system/internal/report/store"
)

type filterOptionLoaderFunc func(context.Context, runtime.FilterOptionRequest) (runtime.FilterOptionResult, error)

func (function filterOptionLoaderFunc) LoadFilterOptions(ctx context.Context, request runtime.FilterOptionRequest) (runtime.FilterOptionResult, error) {
	return function(ctx, request)
}

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

func TestNewReportHydratesSelectFiltersFromGovernedDistinctValues(t *testing.T) {
	dataContext := reportmodel.DataContext{
		ID: askdata.ID("22222222-2222-4222-8222-222222222222"), DatasetID: askdata.ID("33333333-3333-4333-8333-333333333333"),
		DatasetVersionID: askdata.ID("44444444-4444-4444-8444-444444444444"), Alias: "经营数据",
		DefaultParameters: []reportmodel.DefaultParameter{}, QueryPolicy: reportmodel.QueryPolicy{TimeoutMS: 10_000, MaxRows: 5_000, CacheTTLSeconds: 300},
	}
	definition := newReportDefinition(askdata.ID("11111111-1111-4111-8111-111111111111"), "候选值测试", "", dataContext, reportmodel.CreatedManually)
	definition.GlobalFilters = []reportmodel.GlobalFilter{
		{ID: askdata.ID("55555555-5555-4555-8555-555555555555"), Type: reportmodel.FilterSingleSelect, FieldRef: reportmodel.FieldReference{DataContextID: dataContext.ID, Field: "return_flag"}, Scope: reportmodel.FilterScope{Type: reportmodel.FilterScopeReport, TargetIDs: []askdata.ID{}}},
		{ID: askdata.ID("66666666-6666-4666-8666-666666666666"), Type: reportmodel.FilterDateRange, FieldRef: reportmodel.FieldReference{DataContextID: dataContext.ID, Field: "order_date"}, Scope: reportmodel.FilterScope{Type: reportmodel.FilterScopeReport, TargetIDs: []askdata.ID{}}},
	}
	calls := 0
	handler := Handler{ai: AIOptions{FilterOptions: filterOptionLoaderFunc(func(_ context.Context, request runtime.FilterOptionRequest) (runtime.FilterOptionResult, error) {
		calls++
		if request.DatasetID != dataContext.DatasetID || request.DatasetVersionID != dataContext.DatasetVersionID || request.Field != "return_flag" {
			t.Fatalf("filter option request = %#v", request)
		}
		return runtime.FilterOptionResult{Values: []string{"0", "1"}}, nil
	})}}
	identity := store.Identity{
		TenantID: askdata.ID("77777777-7777-4777-8777-777777777777"), ActorID: askdata.ID("88888888-8888-4888-8888-888888888888"),
		DomainID: askdata.ID("99999999-9999-4999-8999-999999999999"),
	}
	hydrated := handler.hydrateDefaultFilterOptions(context.Background(), identity, definition)
	if calls != 1 || len(hydrated.GlobalFilters[0].Options) != 2 || hydrated.GlobalFilters[1].Options != nil {
		t.Fatalf("hydrated filters = %#v, calls = %d", hydrated.GlobalFilters, calls)
	}
}
