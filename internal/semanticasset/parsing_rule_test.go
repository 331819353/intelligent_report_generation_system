package semanticasset

import "testing"

func TestValidParsingRuleInputRequiresTypeSpecificShape(t *testing.T) {
	valid := []ParsingRuleInput{
		{
			RuleType: "METRIC_NAME_SUFFIX", Pattern: "总量",
			MatchMode: "SUFFIX", Action: "STRIP_SUFFIX",
			MinimumLength: 2, Priority: 100,
		},
		{
			RuleType: "ADMIN_REGION_SUFFIX", Pattern: "市",
			MatchMode: "SUFFIX", Action: "MAP_ADMIN_REGION",
			OutputName: "城市", OutputCode: "city",
			MinimumLength: 2, MaximumLength: 12, Priority: 100,
		},
		{
			RuleType: "QUERY_RESIDUAL_TERM", Pattern: "帮我",
			MatchMode: "EXACT", Action: "ALLOW_DETERMINISTIC",
			Priority: 100,
		},
		{
			RuleType: "BROAD_METRIC_PHRASE", Pattern: "经营情况",
			MatchMode: "CONTAINS", Action: "REQUIRE_METRIC_CONFIRMATION",
			Priority: 100,
		},
	}
	for _, input := range valid {
		if !validParsingRuleInput(input) {
			t.Fatalf("expected valid input: %#v", input)
		}
	}

	invalid := append([]ParsingRuleInput(nil), valid...)
	invalid[0].MaximumLength = 12
	invalid[1].OutputCode = "城市 编码"
	invalid[2].MatchMode = "CONTAINS"
	invalid[3].Action = "ALLOW_DETERMINISTIC"
	for _, input := range invalid {
		if validParsingRuleInput(input) {
			t.Fatalf("expected invalid input: %#v", input)
		}
	}
}
