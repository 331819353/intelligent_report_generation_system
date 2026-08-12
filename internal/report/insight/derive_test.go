package insight

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/shared"
	"intelligent-report-generation-system/internal/report"
)

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func datasetBinding(dimensions, measures []report.FieldBinding) *report.DataBinding {
	id := askdata.ID("00000000-0000-4000-8000-000000000130")
	return &report.DataBinding{
		BindingMode: report.BindingDatasetField, DataContextID: &id,
		Dimensions: dimensions, Measures: measures,
	}
}

func TestBindingRolesOnlyNamesFieldsTheComponentBinds(t *testing.T) {
	roles, err := BindingRoles(datasetBinding(
		[]report.FieldBinding{
			{Role: report.RoleCategory, Field: "channel"},
			{Role: report.RoleSeries, Field: "region"},
		},
		[]report.FieldBinding{
			{Role: report.RoleValue, Field: "revenue"},
			{Role: report.RoleValue, Field: "revenue_prev"},
		},
	))
	if err != nil {
		t.Fatalf("binding roles: %v", err)
	}
	if roles.Dimension != "channel" || roles.Group != "region" ||
		roles.Value != "revenue" || roles.Previous != "revenue_prev" {
		t.Fatalf("unexpected roles: %#v", roles)
	}
}

func TestBindingRolesRefusesWhatItCannotDeriveFrom(t *testing.T) {
	if _, err := BindingRoles(nil); err == nil {
		t.Fatal("an unbound component has no evidence to derive")
	}
	semantic := &report.DataBinding{BindingMode: report.BindingSemanticIR}
	if _, err := BindingRoles(semantic); err == nil {
		t.Fatal("a pinned semantic binding is not derivable through this path")
	}
	noMeasure := datasetBinding([]report.FieldBinding{{Role: report.RoleCategory, Field: "channel"}}, nil)
	if _, err := BindingRoles(noMeasure); err == nil {
		t.Fatal("evidence requires a measure")
	}
}

func TestMethodInputCarriesACellReferenceForEveryValue(t *testing.T) {
	result := ResultTable{
		Columns: []string{"channel", "revenue"},
		Rows: [][]any{
			{"线上", json.Number("120")},
			{"线下", json.Number("80")},
		},
	}
	input, err := BuildMethodInput(result, MethodRoles{Dimension: "channel", Value: "revenue"}, 0)
	if err != nil {
		t.Fatalf("build method input: %v", err)
	}
	if len(input.Values) != 2 {
		t.Fatalf("expected one value per row, got %d", len(input.Values))
	}
	for _, value := range input.Values {
		// Traceability is the whole point: a fact must point at the cell it came
		// from, or the verifier cannot check a claim against real data.
		if value.CellRef.Validate() != nil {
			t.Fatalf("value %q has no usable cell reference: %#v", value.Key, value.CellRef)
		}
		if value.CellRef.ColumnKey != "revenue" {
			t.Fatalf("cell reference must name the measure column, got %q", value.CellRef.ColumnKey)
		}
		// Row coordinates use the shared percent-encoded group-by form.
		parts, err := shared.ParseRowKey(value.CellRef.RowKey)
		if err != nil || len(parts) != 1 || parts[0].Key != "channel" {
			t.Fatalf("row key %q is not a governed coordinate: %v", value.CellRef.RowKey, err)
		}
	}
	if input.Values[0].Key != "线上" || input.Values[0].Value != 120 {
		t.Fatalf("unexpected first value: %#v", input.Values[0])
	}
	// The row key carries the dimension value, so a fact survives a reordering.
	parts, err := shared.ParseRowKey(input.Values[0].CellRef.RowKey)
	if err != nil || parts[0].Value != "线上" {
		t.Fatalf("row key must carry the dimension value, got %q", input.Values[0].CellRef.RowKey)
	}
}

func TestMethodInputMarksUnparseableCellsMissingRatherThanZero(t *testing.T) {
	result := ResultTable{
		Columns: []string{"channel", "revenue"},
		Rows:    [][]any{{"线上", nil}, {"线下", "not a number"}, {"工程", json.Number("5")}},
	}
	input, err := BuildMethodInput(result, MethodRoles{Dimension: "channel", Value: "revenue"}, 0)
	if err != nil {
		t.Fatalf("build method input: %v", err)
	}
	if !input.Values[0].Missing || !input.Values[1].Missing || input.Values[2].Missing {
		t.Fatalf("missing flags are wrong: %#v", input.Values)
	}
	// A missing input must not be presented to an analysis method as measured 0.
	if input.Values[0].Value != 0 || !input.Values[0].Missing {
		t.Fatalf("a missing cell must be flagged, not silently zero: %#v", input.Values[0])
	}
}

func TestMethodInputRejectsAResultItCannotAnalyse(t *testing.T) {
	empty := ResultTable{Columns: []string{"channel", "revenue"}}
	if _, err := BuildMethodInput(empty, MethodRoles{Dimension: "channel", Value: "revenue"}, 0); err == nil {
		t.Fatal("an empty result yields no evidence")
	}
	missingColumn := ResultTable{Columns: []string{"channel"}, Rows: [][]any{{"线上"}}}
	if _, err := BuildMethodInput(missingColumn, MethodRoles{Dimension: "channel", Value: "revenue"}, 0); err == nil {
		t.Fatal("a measure column that is not in the result must be rejected")
	}
}

// Evidence derived this way must satisfy the frozen bundle contract.
func TestDerivedEvidenceValidatesAgainstTheBundleContract(t *testing.T) {
	result := ResultTable{
		Columns: []string{"channel", "revenue"},
		Rows:    [][]any{{"线上", json.Number("120")}, {"线下", json.Number("80")}},
	}
	input, err := BuildMethodInput(result, MethodRoles{Dimension: "channel", Value: "revenue"}, 3)
	if err != nil {
		t.Fatalf("build method input: %v", err)
	}
	bundle, err := BuildEvidence(NewRegistry(), EvidenceRequest{
		SourceType:               SourceDatasetQuery,
		DatasetVersionID:         askdata.ID("00000000-0000-4000-8000-000000000132"),
		DataSnapshotVersion:      "snapshot-1",
		QueryPlanHash:            askdata.ContentHash(strings.Repeat("a", 64)),
		FilterHash:               askdata.ContentHash(strings.Repeat("b", 64)),
		AsOf:                     mustTime(t, "2026-08-12T00:00:00Z"),
		ResolvedTimeRange:        ResolvedTimeRange{Start: "2025-08-12T00:00:00Z", EndExclusive: "2026-08-12T00:00:00Z", Timezone: "UTC"},
		MetricVersionID:          askdata.ID("00000000-0000-4000-8000-000000000132"),
		Unit:                     "CNY",
		Method:                   AnalysisTopN,
		EvidenceAlgorithmVersion: "report-evidence-derive-1.0.0",
		Input:                    input,
	}, mustTime(t, "2026-08-12T00:00:01Z"))
	if err != nil {
		t.Fatalf("build evidence: %v", err)
	}
	if err := bundle.Validate(); err != nil {
		t.Fatalf("derived evidence must satisfy its own contract: %v", err)
	}
	if len(bundle.Facts) == 0 {
		t.Fatal("derived evidence must contain facts")
	}
	for _, fact := range bundle.Facts {
		if len(fact.CellRefs) == 0 {
			t.Fatalf("fact %q lost its cell references", fact.ID)
		}
	}
	if _, err := bundle.Hash(); err != nil {
		t.Fatalf("derived evidence must be hashable: %v", err)
	}
}
