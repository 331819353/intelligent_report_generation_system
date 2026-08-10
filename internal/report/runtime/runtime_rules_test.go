package runtime

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/report"
)

func TestBuildExecutionPlanAppliesAllLazyLoadingModes(t *testing.T) {
	definition := runtimeDefinition(t, "multi-page-report.json")
	policy := string(askdata.HashBytes([]byte("viewer policy")))
	firstPage := definition.Pages[0]
	secondPage := definition.Pages[1]

	plan, err := BuildExecutionPlan(definition, PlanRequest{PageID: firstPage.ID, VisibleBlockIDs: []askdata.ID{"block_region_chart"}, PolicyScopeHash: policy})
	if err != nil || len(plan.Components) != 1 || plan.Components[0].PageID != firstPage.ID || plan.Components[0].BlockID != "block_region_chart" {
		t.Fatalf("viewport plan = %#v, %v", plan, err)
	}

	plan, err = BuildExecutionPlan(definition, PlanRequest{PageID: secondPage.ID, PolicyScopeHash: policy})
	if err != nil || len(plan.Components) != 2 {
		t.Fatalf("current page plan = %#v, %v", plan, err)
	}
	for _, component := range plan.Components {
		if component.PageID != secondPage.ID {
			t.Fatalf("non-current page leaked into plan: %#v", component)
		}
	}

	// Add a hidden secondary mobile slot to the PRIMARY_ONLY block. The
	// compiler-produced mobile layout must exclude it without querying.
	secondary := definition.Components[0]
	secondary.ID = "component_mobile_secondary"
	definition.Components = append(definition.Components, secondary)
	block := &definition.Pages[0].Sections[0].Blocks[1]
	block.Zones[0].Slots = append(block.Zones[0].Slots, report.Slot{
		ID: "slot_mobile_secondary", Grid: report.SlotGrid{X: 0, Y: 1, W: 1, H: 1}, ComponentID: secondary.ID,
	})
	plan, err = BuildExecutionPlan(definition, PlanRequest{PageID: firstPage.ID, Mobile: true, PolicyScopeHash: policy})
	if err != nil {
		t.Fatal(err)
	}
	for _, component := range plan.Components {
		if component.ComponentID == secondary.ID {
			t.Fatal("PRIMARY_ONLY secondary component was queried")
		}
	}

	exportPlan, err := BuildExecutionPlan(definition, PlanRequest{PageID: firstPage.ID, VisibleBlockIDs: []askdata.ID{"block_region_chart"}, Export: true, PolicyScopeHash: policy})
	if err != nil || len(exportPlan.Components) != len(definition.Components) {
		t.Fatalf("export plan components=%d want=%d err=%v", len(exportPlan.Components), len(definition.Components), err)
	}
}

func TestResolveDefaultFilterValuesEvaluatesRelativeMonthInBusinessTimezone(t *testing.T) {
	definition := runtimeDefinition(t, "multi-page-report.json")
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	values, err := ResolveDefaultFilterValues(definition, time.Date(2026, 8, 10, 15, 30, 0, 0, time.UTC), location)
	if err != nil {
		t.Fatal(err)
	}
	window, ok := values["filter_month"].(RelativeTimeWindow)
	if !ok || !window.Start.Equal(time.Date(2026, 8, 1, 0, 0, 0, 0, location)) ||
		!window.EndExclusive.Equal(time.Date(2026, 9, 1, 0, 0, 0, 0, location)) {
		t.Fatalf("relative month window = %#v", values["filter_month"])
	}
}

func TestFilterValueContractRoundTripsAllEightControls(t *testing.T) {
	tests := []struct {
		filterType report.FilterType
		raw        string
	}{
		{report.FilterSingleSelect, `"east"`},
		{report.FilterMultiSelect, `["east","west"]`},
		{report.FilterDate, `"2026-08-10"`},
		{report.FilterDateRange, `{"start":"2026-08-01","endExclusive":"2026-09-01"}`},
		{report.FilterRelativeTime, `{"unit":"month","offset":-1}`},
		{report.FilterNumberRange, `{"minimum":10,"maximum":20}`},
		{report.FilterSearchSelect, `"customer-42"`},
		{report.FilterParameterInput, `12.5`},
	}
	for _, test := range tests {
		t.Run(string(test.filterType), func(t *testing.T) {
			value, err := ParseFilterValue(test.filterType, json.RawMessage(test.raw))
			if err != nil {
				t.Fatal(err)
			}
			raw, err := SerializeFilterValue(test.filterType, value)
			if err != nil {
				t.Fatal(err)
			}
			roundTrip, err := ParseFilterValue(test.filterType, raw)
			if err != nil || !reflect.DeepEqual(value, roundTrip) {
				t.Fatalf("round trip = %#v, %v; want %#v", roundTrip, err, value)
			}
		})
	}
}

func TestFilterValueContractRejectsMalformedValues(t *testing.T) {
	tests := []struct {
		filterType report.FilterType
		raw        string
	}{
		{report.FilterSingleSelect, `""`},
		{report.FilterMultiSelect, `["east","east"]`},
		{report.FilterDate, `"2026-02-30"`},
		{report.FilterDateRange, `{"start":"2026-09-01","endExclusive":"2026-08-01"}`},
		{report.FilterRelativeTime, `{"unit":"hour","offset":0}`},
		{report.FilterNumberRange, `{"minimum":20,"maximum":10}`},
		{report.FilterSearchSelect, `null`},
		{report.FilterParameterInput, `{"sql":"select 1"}`},
	}
	for _, test := range tests {
		if _, err := ParseFilterValue(test.filterType, json.RawMessage(test.raw)); err == nil {
			t.Fatalf("%s accepted malformed value %s", test.filterType, test.raw)
		}
	}
}

func TestResolveFiltersUsesExplicitScopePrecedenceAndTemporaryOverride(t *testing.T) {
	definition := runtimeDefinition(t, "multi-page-report.json")
	contextID := definition.DataContexts[0].ID
	field := "region_name"
	definition.GlobalFilters = []report.GlobalFilter{
		{
			ID: "filter_report_region", Type: report.FilterSingleSelect,
			FieldRef: report.FieldReference{DataContextID: contextID, Field: field},
			Scope:    report.FilterScope{Type: report.FilterScopeReport, TargetIDs: []askdata.ID{}},
		},
		{
			ID: "filter_component_region", Type: report.FilterSingleSelect,
			FieldRef: report.FieldReference{DataContextID: contextID, Field: field},
			Scope:    report.FilterScope{Type: report.FilterScopeComponent, TargetIDs: []askdata.ID{"component_region_chart"}},
		},
	}
	values := map[askdata.ID]any{"filter_report_region": "all", "filter_component_region": "east"}
	filters, err := ResolveFilters(definition, definition.Pages[0].ID, definition.Pages[0].Sections[0].ID,
		"block_region_chart", "component_region_chart", values, nil)
	if err != nil || len(filters) != 1 || filters[0].ID != "filter_component_region" || filters[0].Value != "east" {
		t.Fatalf("local override = %#v, %v", filters, err)
	}

	temporary := ResolvedFilter{ID: "runtime_click", DataContextID: contextID, Field: field, Value: "west"}
	filters, err = ResolveFilters(definition, definition.Pages[0].ID, definition.Pages[0].Sections[0].ID,
		"block_region_chart", "component_region_chart", values, []ResolvedFilter{temporary})
	if err != nil || len(filters) != 1 || filters[0].ID != temporary.ID || filters[0].Value != "west" || !filters[0].Temporary {
		t.Fatalf("temporary override = %#v, %v", filters, err)
	}

	outside, err := ResolveFilters(definition, definition.Pages[0].ID, definition.Pages[0].Sections[0].ID,
		"block_region_table", "component_region_table", map[askdata.ID]any{"filter_component_region": "east"}, nil)
	if err != nil || len(outside) != 0 {
		t.Fatalf("component-scoped filter propagated outside scope: %#v, %v", outside, err)
	}
}

func TestTemporaryFilterSnapshotRestoresWithoutTouchingDefinition(t *testing.T) {
	definition := runtimeDefinition(t, "multi-page-report.json")
	before, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	store := NewTemporaryFilterStore()
	store.Set(ResolvedFilter{
		ID: "runtime_region", DataContextID: definition.DataContexts[0].ID,
		Field: "region_name", Value: "east",
	})
	snapshot := store.Snapshot()
	restored := NewTemporaryFilterStore()
	if err := restored.Restore(snapshot); err != nil {
		t.Fatal(err)
	}
	if got := restored.Snapshot(); !reflect.DeepEqual(got, snapshot) {
		t.Fatalf("restored snapshot = %#v; want %#v", got, snapshot)
	}
	after, err := json.Marshal(definition)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("temporary filter changed definition: %v", err)
	}
	if err := restored.Restore([]ResolvedFilter{{ID: "bad"}}); err == nil {
		t.Fatal("invalid temporary snapshot was accepted")
	}
}

func TestHTTPPlanInputParsesTypedValuesAndAppliesComponentScope(t *testing.T) {
	definition := runtimeDefinition(t, "multi-page-report.json")
	definition.GlobalFilters = []report.GlobalFilter{{
		ID: "filter_region", Type: report.FilterSingleSelect,
		FieldRef: report.FieldReference{
			DataContextID: definition.DataContexts[0].ID, Field: "region_name",
		},
		Scope: report.FilterScope{
			Type: report.FilterScopeComponent, TargetIDs: []askdata.ID{"component_region_chart"},
		},
	}}
	asOf := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	request, err := (HTTPPlanInput{
		PageID: definition.Pages[0].ID,
		FilterValues: map[askdata.ID]json.RawMessage{
			"filter_region": json.RawMessage(`"east"`),
		},
	}).Resolve(definition, asOf, time.UTC, string(askdata.HashBytes([]byte("viewer"))))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildExecutionPlan(definition, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Components) != 2 {
		t.Fatalf("component count = %d", len(plan.Components))
	}
	for _, component := range plan.Components {
		if component.Query == nil {
			continue
		}
		if component.ComponentID == "component_region_chart" {
			if len(component.Query.Filters) != 1 || component.Query.Filters[0].Value != "east" {
				t.Fatalf("component-scoped filters = %#v", component.Query.Filters)
			}
		} else if len(component.Query.Filters) != 0 {
			t.Fatalf("filter leaked to %s: %#v", component.ComponentID, component.Query.Filters)
		}
	}

	_, err = (HTTPPlanInput{
		PageID: definition.Pages[0].ID,
		FilterValues: map[askdata.ID]json.RawMessage{
			"filter_unknown": json.RawMessage(`"east"`),
		},
	}).Resolve(definition, asOf, time.UTC, string(askdata.HashBytes([]byte("viewer"))))
	if err == nil {
		t.Fatal("unknown public filter was accepted")
	}
}

func TestRuntimeTimezoneRejectsConflictingPinnedContracts(t *testing.T) {
	definition := runtimeDefinition(t, "ask-data-report.json")
	copy := definition.Components[0]
	copy.ID = "component_semantic_conflict"
	binding := *copy.DataBinding
	reference := *binding.SemanticQueryRef
	binding.SemanticQueryRef = &reference
	copy.DataBinding = &binding
	if reference.ResolvedTimeSpec != nil {
		resolved := *reference.ResolvedTimeSpec
		resolved.Timezone = "UTC"
		reference.ResolvedTimeSpec = &resolved
	} else {
		timeRange := *reference.SemanticIR.TimeRange
		timeRange.Timezone = "UTC"
		reference.SemanticIR.TimeRange = &timeRange
	}
	definition.Components = append(definition.Components, copy)
	if _, err := RuntimeTimezone(definition); err == nil {
		t.Fatal("conflicting immutable runtime timezones were accepted")
	}
}

func TestQueryStateReportsPartialWithoutTreatingItAsFailure(t *testing.T) {
	state, code := queryState(context.Background(), QueryResult{Rows: [][]any{{1}}, Partial: true}, nil)
	if state != StatePartial || code != "" {
		t.Fatalf("partial state = %s, %q", state, code)
	}
}
