package registry

import (
	"encoding/json"
	"strings"
)

const (
	AdditivityRuleFormulaDivide       = "FORMULA_ROOT_DIVIDE"
	AdditivityRuleCountDistinct       = "COUNT_DISTINCT"
	AdditivityRuleRatioLexicon        = "RATIO_LEXICON"
	AdditivityRuleSnapshotLexicon     = "SNAPSHOT_LEXICON"
	AdditivityRuleSnapshotGrain       = "SNAPSHOT_GRAIN"
	AdditivityRuleSumAmountOrQuantity = "SUM_AMOUNT_OR_QUANTITY"
	AdditivityRuleNeedsHuman          = "NEEDS_HUMAN"
)

// AdditivitySuggestion is advisory. In particular, Value must never be copied
// into MetricVersion.Additivity without an explicit human confirmation.
type AdditivitySuggestion struct {
	Value                       Additivity                  `json:"value,omitempty"`
	SemiAdditiveTimeAggregation SemiAdditiveTimeAggregation `json:"semiAdditiveTimeAggregation,omitempty"`
	AggregationRestriction      AggregationRestriction      `json:"aggregationRestriction,omitempty"`
	RuleID                      string                      `json:"ruleId"`
	NeedsHuman                  bool                        `json:"needsHuman"`
}

// AdditivitySuggestionInput carries the catalog facts not embedded directly
// in MetricVersion. Names and aliases are data, not executable instructions.
type AdditivitySuggestionInput struct {
	Metric             MetricVersion
	Model              SemanticModel
	MetricName         string
	MetricCode         string
	Aliases            []string
	DefaultAggregation Aggregation
}

// SuggestAdditivity provides the contract's simple signature. Catalog callers
// should use SuggestAdditivityWithContext so name, alias and aggregation rules
// can be evaluated from authoritative rows.
func SuggestAdditivity(metric MetricVersion, model SemanticModel) AdditivitySuggestion {
	return SuggestAdditivityWithContext(AdditivitySuggestionInput{
		Metric: metric, Model: model,
	}, DefaultAdditivityLexicon())
}

// SuggestAdditivityWithContext applies the documented ordered rule set. The
// first match wins, making the result deterministic and easy to audit.
func SuggestAdditivityWithContext(
	input AdditivitySuggestionInput,
	lexicon AdditivityLexicon,
) AdditivitySuggestion {
	if formulaRootIsDivide(input.Metric.FormulaAST) {
		return nonAdditiveSuggestion(AdditivityRuleFormulaDivide)
	}
	aggregation := input.DefaultAggregation
	if aggregation == "" {
		aggregation = formulaAggregation(input.Metric.FormulaAST)
	}
	if aggregation == AggregationCountDistinct {
		return nonAdditiveSuggestion(AdditivityRuleCountDistinct)
	}
	labels := append([]string{input.MetricName, input.MetricCode}, input.Aliases...)
	if containsLexiconTerm(labels, lexicon.RatioTerms) {
		return nonAdditiveSuggestion(AdditivityRuleRatioLexicon)
	}
	if containsLexiconTerm(labels, lexicon.SnapshotTerms) {
		return semiAdditiveSuggestion(AdditivityRuleSnapshotLexicon)
	}
	if grainIsSnapshot(input.Model.GrainContract) {
		return semiAdditiveSuggestion(AdditivityRuleSnapshotGrain)
	}
	if aggregation == AggregationSum && amountOrQuantityUnit(input.Metric.Unit) {
		return AdditivitySuggestion{
			Value: FullyAdditive, RuleID: AdditivityRuleSumAmountOrQuantity,
		}
	}
	return AdditivitySuggestion{RuleID: AdditivityRuleNeedsHuman, NeedsHuman: true}
}

func nonAdditiveSuggestion(ruleID string) AdditivitySuggestion {
	return AdditivitySuggestion{
		Value: NonAdditive, AggregationRestriction: PostAggregate, RuleID: ruleID,
	}
}

func semiAdditiveSuggestion(ruleID string) AdditivitySuggestion {
	return AdditivitySuggestion{
		Value: SemiAdditive, SemiAdditiveTimeAggregation: SemiAdditivePeriodEnd,
		RuleID: ruleID,
	}
}

func formulaRootIsDivide(raw json.RawMessage) bool {
	var node map[string]any
	if json.Unmarshal(raw, &node) != nil {
		return false
	}
	for _, key := range []string{"type", "op", "operator"} {
		if strings.EqualFold(strings.TrimSpace(stringValue(node[key])), "DIVIDE") {
			return true
		}
	}
	return false
}

func formulaAggregation(raw json.RawMessage) Aggregation {
	var node map[string]any
	if json.Unmarshal(raw, &node) != nil {
		return ""
	}
	for _, key := range []string{"aggregation", "defaultAggregation"} {
		value := Aggregation(strings.ToUpper(strings.TrimSpace(stringValue(node[key]))))
		switch value {
		case AggregationSum, AggregationAverage, AggregationMinimum, AggregationMaximum,
			AggregationCount, AggregationCountDistinct:
			return value
		}
	}
	return ""
}

func grainIsSnapshot(raw json.RawMessage) bool {
	var document map[string]any
	if json.Unmarshal(raw, &document) != nil {
		return false
	}
	value, ok := document["snapshot"].(bool)
	return ok && value
}

func containsLexiconTerm(values, terms []string) bool {
	for _, raw := range values {
		value := strings.ToLower(strings.TrimSpace(raw))
		for _, rawTerm := range terms {
			term := strings.ToLower(strings.TrimSpace(rawTerm))
			if value != "" && term != "" && strings.Contains(value, term) {
				return true
			}
		}
	}
	return false
}

func amountOrQuantityUnit(raw string) bool {
	unit := strings.ToUpper(strings.TrimSpace(raw))
	if unit == "" {
		return false
	}
	if unit == "CURRENCY" || unit == "AMOUNT" || unit == "QUANTITY" || unit == "COUNT" {
		return true
	}
	for _, token := range []string{"金额", "数量", "元", "万元", "件", "个", "吨", "KG"} {
		if strings.Contains(unit, strings.ToUpper(token)) {
			return true
		}
	}
	return false
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}
