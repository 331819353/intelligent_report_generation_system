package runtime

import (
	"encoding/json"
	"errors"
	"testing"

	"intelligent-report-generation-system/internal/queryruntime"
	"intelligent-report-generation-system/internal/report"
)

func dimension(field string) report.FieldBinding {
	return report.FieldBinding{Role: report.RoleCategory, Field: field}
}

func measure(field string) report.FieldBinding {
	return report.FieldBinding{Role: report.RoleValue, Field: field}
}

func additiveContract(grain []string, aggregations map[string]string) queryruntime.VersionRollupContract {
	contract := queryruntime.VersionRollupContract{
		GrainKeyFields: grain, Measures: map[string]queryruntime.VersionMeasure{},
	}
	for field, aggregation := range aggregations {
		contract.Measures[field] = queryruntime.VersionMeasure{
			Field: field, Aggregation: aggregation, Additivity: "ADDITIVE", Declared: true,
		}
	}
	return contract
}

func TestNeedsRollupOnlyWhenTheBoundDimensionsLeaveTheGrainIncomplete(t *testing.T) {
	grain := []string{"channel", "month"}
	if NeedsRollup([]report.FieldBinding{dimension("channel"), dimension("month")}, grain) {
		t.Fatal("a projection that keeps every grain key field must pass through untouched")
	}
	if !NeedsRollup([]report.FieldBinding{dimension("channel")}, grain) {
		t.Fatal("dropping a grain key field must require a roll-up")
	}
	if NeedsRollup([]report.FieldBinding{dimension("channel")}, nil) {
		t.Fatal("a version without a declared grain cannot be rolled up")
	}
}

func TestRollUpSumsAdditiveMeasuresPerBoundDimension(t *testing.T) {
	source := QueryResult{
		Columns: []string{"channel", "revenue"},
		Rows: [][]any{
			{"线上", json.Number("100.25")},
			{"线下", json.Number("50")},
			{"线上", json.Number("20.75")},
		},
	}
	result, err := RollUp(source,
		[]report.FieldBinding{dimension("channel")}, []report.FieldBinding{measure("revenue")},
		additiveContract([]string{"channel", "month"}, map[string]string{"revenue": "SUM"}))
	if err != nil {
		t.Fatalf("roll-up failed: %v", err)
	}
	if len(result.Rows) != 2 {
		t.Fatalf("expected one row per channel, got %d", len(result.Rows))
	}
	totals := measuresByDimension(result)
	// 精确十进制求和，不经过 float64：金额合计不能出现二进制浮点误差。
	if got := totals["线上"]; got != json.Number("121") {
		t.Fatalf("线上 revenue = %v, want 121", got)
	}
	if got := totals["线下"]; got != json.Number("50") {
		t.Fatalf("线下 revenue = %v, want 50", got)
	}
	if result.Columns[0] != "channel" || result.Columns[1] != "revenue" {
		t.Fatalf("columns must keep the bound order, got %v", result.Columns)
	}
	if result.Hash == "" {
		t.Fatal("a rolled-up result still needs a content hash")
	}
}

func TestRollUpIsDeterministicAcrossRowOrder(t *testing.T) {
	contract := additiveContract([]string{"channel", "month"}, map[string]string{"revenue": "SUM"})
	forward := QueryResult{Columns: []string{"channel", "revenue"}, Rows: [][]any{
		{"a", json.Number("1")}, {"b", json.Number("2")}, {"a", json.Number("3")},
	}}
	reversed := QueryResult{Columns: []string{"channel", "revenue"}, Rows: [][]any{
		{"a", json.Number("3")}, {"b", json.Number("2")}, {"a", json.Number("1")},
	}}
	first, err := RollUp(forward, []report.FieldBinding{dimension("channel")}, []report.FieldBinding{measure("revenue")}, contract)
	if err != nil {
		t.Fatalf("roll-up failed: %v", err)
	}
	second, err := RollUp(reversed, []report.FieldBinding{dimension("channel")}, []report.FieldBinding{measure("revenue")}, contract)
	if err != nil {
		t.Fatalf("roll-up failed: %v", err)
	}
	if first.Hash != second.Hash {
		t.Fatalf("row order changed the result hash: %s vs %s", first.Hash, second.Hash)
	}
}

func TestRollUpAppliesMinAndMaxRatherThanSumming(t *testing.T) {
	source := QueryResult{Columns: []string{"channel", "peak", "floor"}, Rows: [][]any{
		{"线上", json.Number("10"), json.Number("4")},
		{"线上", json.Number("7"), json.Number("2")},
	}}
	contract := additiveContract([]string{"channel", "month"}, map[string]string{"peak": "MAX", "floor": "MIN"})
	result, err := RollUp(source, []report.FieldBinding{dimension("channel")},
		[]report.FieldBinding{measure("peak"), measure("floor")}, contract)
	if err != nil {
		t.Fatalf("roll-up failed: %v", err)
	}
	if result.Rows[0][1] != json.Number("10") || result.Rows[0][2] != json.Number("2") {
		t.Fatalf("expected MAX=10 and MIN=2, got %v", result.Rows[0])
	}
}

func TestRollUpRefusesMeasuresThatCannotBeRecombined(t *testing.T) {
	source := QueryResult{Columns: []string{"channel", "rate"}, Rows: [][]any{
		{"线上", json.Number("0.4")}, {"线上", json.Number("0.6")},
	}}
	for _, testCase := range []struct {
		name     string
		measure  queryruntime.VersionMeasure
		wantCode string
	}{
		{"average", queryruntime.VersionMeasure{Field: "rate", Aggregation: "AVG", Additivity: "ADDITIVE", Declared: true}, CodeRollupNonAdditive},
		{"distinct count", queryruntime.VersionMeasure{Field: "rate", Aggregation: "COUNT_DISTINCT", Declared: true}, CodeRollupNonAdditive},
		{"non additive ratio", queryruntime.VersionMeasure{Field: "rate", Aggregation: "SUM", Additivity: "NON_ADDITIVE", Declared: true}, CodeRollupNonAdditive},
		{"semi additive balance", queryruntime.VersionMeasure{Field: "rate", Aggregation: "SUM", Additivity: "SEMI_ADDITIVE", Declared: true}, CodeRollupNonAdditive},
		{"undeclared", queryruntime.VersionMeasure{Field: "rate"}, CodeRollupUndeclared},
		{"unsupported aggregation", queryruntime.VersionMeasure{Field: "rate", Aggregation: "MEDIAN", Declared: true}, CodeRollupUndeclared},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			contract := queryruntime.VersionRollupContract{
				GrainKeyFields: []string{"channel", "month"},
				Measures:       map[string]queryruntime.VersionMeasure{"rate": testCase.measure},
			}
			_, err := RollUp(source, []report.FieldBinding{dimension("channel")}, []report.FieldBinding{measure("rate")}, contract)
			var rollupErr *RollupError
			if !errors.As(err, &rollupErr) || rollupErr.Code() != testCase.wantCode {
				t.Fatalf("expected %s, got %v", testCase.wantCode, err)
			}
		})
	}
}

func TestRollUpRefusesTruncatedSourceRows(t *testing.T) {
	source := QueryResult{
		Columns: []string{"channel", "revenue"},
		Rows:    [][]any{{"线上", json.Number("1")}},
		Partial: true,
	}
	_, err := RollUp(source, []report.FieldBinding{dimension("channel")}, []report.FieldBinding{measure("revenue")},
		additiveContract([]string{"channel", "month"}, map[string]string{"revenue": "SUM"}))
	var rollupErr *RollupError
	if !errors.As(err, &rollupErr) || rollupErr.Code() != CodeRollupTruncated {
		t.Fatalf("an aggregate over truncated rows must fail closed, got %v", err)
	}
}

func TestRollUpKeepsMissingMeasuresNullInsteadOfZero(t *testing.T) {
	source := QueryResult{Columns: []string{"channel", "revenue"}, Rows: [][]any{
		{"线上", nil}, {"线下", json.Number("5")},
	}}
	result, err := RollUp(source, []report.FieldBinding{dimension("channel")}, []report.FieldBinding{measure("revenue")},
		additiveContract([]string{"channel", "month"}, map[string]string{"revenue": "SUM"}))
	if err != nil {
		t.Fatalf("roll-up failed: %v", err)
	}
	totals := measuresByDimension(result)
	// 没有任何可用输入的分组保持为 NULL，避免把「没有数据」显示成「金额为 0」。
	if totals["线上"] != nil {
		t.Fatalf("expected NULL for a group with no numeric input, got %v", totals["线上"])
	}
	if totals["线下"] != json.Number("5") {
		t.Fatalf("expected 5, got %v", totals["线下"])
	}
}

// measuresByDimension indexes a single-dimension roll-up by its dimension value
// so assertions do not depend on the deterministic-but-incidental row order.
func measuresByDimension(result QueryResult) map[string]any {
	values := make(map[string]any, len(result.Rows))
	for _, row := range result.Rows {
		values[row[0].(string)] = row[1]
	}
	return values
}
