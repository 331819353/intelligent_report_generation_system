package answer

import (
	"sort"

	"intelligent-report-generation-system/internal/report/template"
)

const ChartRuleVersion = "1.0.0"

type ChartRuleInput struct {
	MetricCount         int    `json:"metricCount"`
	TimeGrain           string `json:"timeGrain,omitempty"`
	NonTimeGroupByCount int    `json:"nonTimeGroupByCount"`
	RowCount            int    `json:"rowCount"`
	Additive            bool   `json:"additive"`
	ShareIntent         bool   `json:"shareIntent"`
	HasComparison       bool   `json:"hasComparison"`
}

type ChartRecommendation struct {
	ComponentType    string `json:"componentType"`
	ComponentVersion string `json:"componentVersion"`
	RuleID           string `json:"ruleId"`
	RuleVersion      string `json:"ruleVersion"`
	Priority         int    `json:"priority"`
}

type chartRule struct {
	id        string
	priority  int
	match     func(ChartRuleInput) bool
	recommend []string
}

var defaultChartRules = []chartRule{
	{id: "large-result-table", priority: 1000, match: func(input ChartRuleInput) bool { return input.RowCount > 500 }, recommend: []string{"data-table"}},
	{id: "period-comparison", priority: 950, match: func(input ChartRuleInput) bool { return input.HasComparison && input.MetricCount >= 1 }, recommend: []string{"bar-comparison", "data-table"}},
	{id: "single-metric-share", priority: 900, match: func(input ChartRuleInput) bool {
		return input.MetricCount == 1 && input.NonTimeGroupByCount == 1 && input.ShareIntent && input.Additive
	}, recommend: []string{"pie-donut", "bar-horizontal"}},
	{id: "single-metric-time-series", priority: 850, match: func(input ChartRuleInput) bool {
		return input.MetricCount == 1 && input.TimeGrain != "" && input.NonTimeGroupByCount == 0 && input.RowCount >= 2
	}, recommend: []string{"line-trend", "bar-comparison"}},
	{id: "multi-metric-time-series", priority: 800, match: func(input ChartRuleInput) bool { return input.MetricCount > 1 && input.TimeGrain != "" }, recommend: []string{"line-trend", "area-stacked"}},
	{id: "single-metric-one-dimension", priority: 750, match: func(input ChartRuleInput) bool { return input.MetricCount == 1 && input.NonTimeGroupByCount == 1 }, recommend: []string{"bar-horizontal", "data-table"}},
	{id: "single-metric-two-dimensions", priority: 700, match: func(input ChartRuleInput) bool { return input.MetricCount == 1 && input.NonTimeGroupByCount == 2 }, recommend: []string{"bar-comparison", "data-table"}},
	{id: "multi-metric-no-time", priority: 650, match: func(input ChartRuleInput) bool {
		return input.MetricCount > 1 && input.TimeGrain == "" && input.NonTimeGroupByCount == 0
	}, recommend: []string{"metric-card", "data-table"}},
	{id: "single-metric-snapshot", priority: 600, match: func(input ChartRuleInput) bool {
		return input.MetricCount == 1 && input.TimeGrain == "" && input.NonTimeGroupByCount == 0 && input.RowCount <= 1
	}, recommend: []string{"metric-card"}},
	{id: "dense-dimensional-result", priority: 550, match: func(input ChartRuleInput) bool { return input.NonTimeGroupByCount > 2 }, recommend: []string{"data-table"}},
}

func RecommendChart(input ChartRuleInput, registry *template.Registry) ChartRecommendation {
	rules := append([]chartRule(nil), defaultChartRules...)
	sort.SliceStable(rules, func(i, j int) bool { return rules[i].priority > rules[j].priority })
	for _, rule := range rules {
		if !rule.match(input) {
			continue
		}
		for index, componentType := range rule.recommend {
			manifest, exists := manifestForType(registry, componentType)
			if !exists || !manifestAcceptsShape(manifest, input) {
				continue
			}
			if manifest.StackingRequiresAdditive && !input.Additive {
				continue
			}
			return ChartRecommendation{ComponentType: componentType, ComponentVersion: manifest.Version, RuleID: rule.id, RuleVersion: ChartRuleVersion, Priority: index + 1}
		}
	}
	manifest, exists := manifestForType(registry, "data-table")
	if !exists {
		return ChartRecommendation{ComponentType: "data-table", ComponentVersion: "1.0.0", RuleID: "fallback-table", RuleVersion: ChartRuleVersion, Priority: 1}
	}
	return ChartRecommendation{ComponentType: manifest.Type, ComponentVersion: manifest.Version, RuleID: "fallback-table", RuleVersion: ChartRuleVersion, Priority: 1}
}

func manifestForType(registry *template.Registry, componentType string) (template.Manifest, bool) {
	if registry == nil {
		return template.Manifest{}, false
	}
	var result template.Manifest
	found := false
	for _, manifest := range registry.List() {
		if manifest.Type == componentType {
			result, found = manifest, true
		}
	}
	return result, found
}

func manifestAcceptsShape(manifest template.Manifest, input ChartRuleInput) bool {
	dimensions := input.NonTimeGroupByCount
	if input.TimeGrain != "" {
		dimensions++
	}
	return input.MetricCount >= manifest.DataContract.Measures.Min && input.MetricCount <= manifest.DataContract.Measures.Max &&
		dimensions >= manifest.DataContract.Dimensions.Min && dimensions <= manifest.DataContract.Dimensions.Max &&
		(!manifest.DataContract.TimeField.Required || input.TimeGrain != "")
}
