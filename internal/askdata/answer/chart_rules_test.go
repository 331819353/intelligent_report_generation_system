package answer

import (
	"reflect"
	"testing"

	"intelligent-report-generation-system/internal/report/template"
)

func TestChartRecommendationCoversGovernedRuleRegistry(t *testing.T) {
	registry, err := template.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		input ChartRuleInput
		want  string
		rule  string
	}{
		{"large table has highest priority", ChartRuleInput{MetricCount: 1, RowCount: 501, HasComparison: true}, "data-table", "large-result-table"},
		{"comparison", ChartRuleInput{MetricCount: 1, TimeGrain: "MONTH", RowCount: 12, HasComparison: true}, "bar-comparison", "period-comparison"},
		{"additive share", ChartRuleInput{MetricCount: 1, NonTimeGroupByCount: 1, RowCount: 8, Additive: true, ShareIntent: true}, "pie-donut", "single-metric-share"},
		{"single metric time", ChartRuleInput{MetricCount: 1, TimeGrain: "MONTH", RowCount: 12}, "line-trend", "single-metric-time-series"},
		{"multi metric time", ChartRuleInput{MetricCount: 2, TimeGrain: "MONTH", RowCount: 12}, "line-trend", "multi-metric-time-series"},
		{"one dimension", ChartRuleInput{MetricCount: 1, NonTimeGroupByCount: 1, RowCount: 8}, "bar-horizontal", "single-metric-one-dimension"},
		{"two dimensions", ChartRuleInput{MetricCount: 1, NonTimeGroupByCount: 2, RowCount: 20}, "bar-comparison", "single-metric-two-dimensions"},
		{"metric group", ChartRuleInput{MetricCount: 2, RowCount: 1}, "metric-card", "multi-metric-no-time"},
		{"snapshot", ChartRuleInput{MetricCount: 1, RowCount: 1}, "metric-card", "single-metric-snapshot"},
		{"dense dimensions", ChartRuleInput{MetricCount: 1, NonTimeGroupByCount: 3, RowCount: 20}, "data-table", "dense-dimensional-result"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := RecommendChart(test.input, registry)
			if got.ComponentType != test.want || got.RuleID != test.rule ||
				got.RuleVersion != ChartRuleVersion || got.ComponentVersion == "" {
				t.Fatalf("recommendation=%+v", got)
			}
			if replay := RecommendChart(test.input, registry); !reflect.DeepEqual(replay, got) {
				t.Fatalf("recommendation is not deterministic: %+v != %+v", replay, got)
			}
		})
	}
}

func TestChartRecommendationSkipsUnsafeOrUnavailableRecommendations(t *testing.T) {
	registry, err := template.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	nonAdditive := RecommendChart(ChartRuleInput{
		MetricCount: 1, NonTimeGroupByCount: 1, RowCount: 8,
		ShareIntent: true, Additive: false,
	}, registry)
	if nonAdditive.ComponentType == "pie-donut" || nonAdditive.ComponentType == "area-stacked" {
		t.Fatalf("non-additive recommendation=%+v", nonAdditive)
	}

	var tableOnly []template.Manifest
	for _, manifest := range registry.List() {
		if manifest.Type == "data-table" {
			tableOnly = append(tableOnly, manifest)
		}
	}
	limited, err := template.NewRegistry(tableOnly, nil)
	if err != nil {
		t.Fatal(err)
	}
	fallback := RecommendChart(ChartRuleInput{
		MetricCount: 1, TimeGrain: "MONTH", RowCount: 12, Additive: true,
	}, limited)
	if fallback.ComponentType != "data-table" || fallback.RuleID != "fallback-table" {
		t.Fatalf("unavailable recommendation was not skipped: %+v", fallback)
	}
}
