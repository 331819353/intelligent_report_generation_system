package registry

import (
	"encoding/json"
	"testing"
)

func TestSuggestAdditivityOrderedRules(t *testing.T) {
	base := AdditivitySuggestionInput{
		Metric: MetricVersion{FormulaAST: json.RawMessage(`{"type":"MEASURE_REF"}`), Unit: "COUNT"},
		Model:  SemanticModel{GrainContract: json.RawMessage(`{"snapshot":false}`)},
	}
	tests := []struct {
		name string
		edit func(*AdditivitySuggestionInput)
		rule string
		want Additivity
	}{
		{name: "divide wins over sum", edit: func(value *AdditivitySuggestionInput) {
			value.Metric.FormulaAST = json.RawMessage(`{"type":"DIVIDE"}`)
			value.DefaultAggregation = AggregationSum
		}, rule: AdditivityRuleFormulaDivide, want: NonAdditive},
		{name: "count distinct", edit: func(value *AdditivitySuggestionInput) {
			value.DefaultAggregation = AggregationCountDistinct
		}, rule: AdditivityRuleCountDistinct, want: NonAdditive},
		{name: "ratio name", edit: func(value *AdditivitySuggestionInput) {
			value.MetricName = "订单完成率"
		}, rule: AdditivityRuleRatioLexicon, want: NonAdditive},
		{name: "snapshot alias", edit: func(value *AdditivitySuggestionInput) {
			value.Aliases = []string{"期末库存"}
		}, rule: AdditivityRuleSnapshotLexicon, want: SemiAdditive},
		{name: "snapshot grain", edit: func(value *AdditivitySuggestionInput) {
			value.Model.GrainContract = json.RawMessage(`{"snapshot":true}`)
		}, rule: AdditivityRuleSnapshotGrain, want: SemiAdditive},
		{name: "sum amount", edit: func(value *AdditivitySuggestionInput) {
			value.DefaultAggregation = AggregationSum
			value.Metric.Unit = "CURRENCY"
		}, rule: AdditivityRuleSumAmountOrQuantity, want: FullyAdditive},
		{name: "needs human", edit: func(value *AdditivitySuggestionInput) {
			value.Metric.Unit = "SCORE"
		}, rule: AdditivityRuleNeedsHuman, want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := base
			test.edit(&input)
			got := SuggestAdditivityWithContext(input, DefaultAdditivityLexicon())
			if got.RuleID != test.rule || got.Value != test.want {
				t.Fatalf("suggestion = %+v, want rule=%s value=%s", got, test.rule, test.want)
			}
		})
	}
}

func TestSuggestAdditivityUsesInjectedLexicon(t *testing.T) {
	input := AdditivitySuggestionInput{
		Metric:     MetricVersion{FormulaAST: json.RawMessage(`{"type":"MEASURE_REF"}`), Unit: "SCORE"},
		Model:      SemanticModel{GrainContract: json.RawMessage(`{}`)},
		MetricName: "健康指数",
	}
	if got := SuggestAdditivityWithContext(input, DefaultAdditivityLexicon()); got.Value != "" {
		t.Fatalf("default suggestion = %+v, want NEEDS_HUMAN", got)
	}
	custom := DefaultAdditivityLexicon()
	custom.RatioTerms = append(custom.RatioTerms, "指数")
	if got := SuggestAdditivityWithContext(input, custom); got.RuleID != AdditivityRuleRatioLexicon {
		t.Fatalf("custom suggestion = %+v, want ratio rule", got)
	}
}
