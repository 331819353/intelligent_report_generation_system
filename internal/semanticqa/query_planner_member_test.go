package semanticqa

import (
	"encoding/json"
	"testing"
)

func TestQueryConditionDimensionClauseAllowsGroupingWithoutMember(t *testing.T) {
	node := resolvedGraphNode{Payload: json.RawMessage(
		`{"dimensionId":"dimension-1","code":"zone_city"}`,
	)}

	clause, err := queryConditionDimensionClause(node)
	if err != nil {
		t.Fatalf("build grouping dimension clause: %v", err)
	}
	if clause.DimensionID != "dimension-1" ||
		clause.DimensionCode != "zone_city" ||
		clause.MemberKey != "" || len(clause.MemberKeys) != 0 {
		t.Fatalf("unexpected grouping dimension clause: %+v", clause)
	}
}

func TestSelectMetricScopedMemberMatchesPrefersLongestGovernedPhrase(t *testing.T) {
	matches := []scopedMemberMatch{
		{
			MemberValue: "bank", DimensionID: "payment-dimension",
			DimensionCode: "payment_method", DimensionName: "支付方式",
			MatchedValue: "bank", MatchMethod: "EXACT_MEMBER_PHRASE",
		},
		{
			MemberValue: "bank_card", DimensionID: "payment-dimension",
			DimensionCode: "payment_method", DimensionName: "支付方式",
			MatchedValue: "bank_card", MatchMethod: "EXACT_MEMBER_PHRASE",
		},
	}

	selected, ambiguous := selectMetricScopedMemberMatches(
		matches, "BANK_CARD的净支付金额是多少？",
	)
	if ambiguous || len(selected) != 1 || selected[0].MemberValue != "bank_card" {
		t.Fatalf("expected longest governed phrase, got ambiguous=%v selected=%+v", ambiguous, selected)
	}
}
