package semanticqa

import "testing"

func TestApplyToolSelectedDecisionsAcceptsOnlyRetrievedDecisionIDs(t *testing.T) {
	lookups := []QueryDimensionValueLookupTrace{
		{
			Term: "北京", DimensionCode: "city", DimensionName: "城市",
			DecisionCandidates: []QueryDecisionCandidate{
				{
					DecisionID: "decision-city", CanonicalValue: "北京",
					MemberValue: "beijing", PredicateOperator: "EQUALS",
					WhereCondition: "城市 = 北京", CompiledCondition: "city = $1",
				},
				{
					DecisionID: "decision-region", CanonicalValue: "北京区域",
					MemberValue: "north", PredicateOperator: "EQUALS",
				},
			},
		},
	}
	selected, ok := applyToolSelectedDecisions(lookups, []string{"decision-city"})
	if !ok || !selected[0].Selected ||
		selected[0].DecisionID != "decision-city" ||
		len(selected[0].SelectedMemberKeys) != 1 ||
		selected[0].SelectedMemberKeys[0] != "beijing" {
		t.Fatalf("expected the retrieved decision to be selected: %#v", selected)
	}
	if _, ok := applyToolSelectedDecisions(lookups, []string{"invented"}); ok {
		t.Fatal("an invented decision id must be rejected")
	}
	if _, ok := applyToolSelectedDecisions(
		lookups, []string{"decision-city", "decision-region"},
	); ok {
		t.Fatal("two decisions for the same dimension term must be rejected")
	}
}

func TestClearAmbiguousToolSelectionsReturnsChoicesToTheUser(t *testing.T) {
	lookups := []QueryDimensionValueLookupTrace{
		{
			Term: "北京", DimensionCode: "city", Selected: true,
			DecisionID: "decision-city", SelectedMemberKeys: []string{"beijing"},
			DecisionCandidates: []QueryDecisionCandidate{
				{
					DecisionID: "decision-city", CanonicalValue: "北京",
					MemberValue: "beijing", Score: 0.97, Selected: true,
				},
				{
					DecisionID: "decision-region", CanonicalValue: "华北",
					MemberValue: "north", Score: 0.96,
				},
			},
		},
	}
	cleared := clearAmbiguousToolSelections(lookups)
	if cleared[0].Selected || cleared[0].DecisionID != "" ||
		len(cleared[0].SelectedMemberKeys) != 0 ||
		len(unresolvedDimensionDecisionIDs(cleared)) != 2 {
		t.Fatalf("ambiguous decisions must be returned for confirmation: %#v", cleared)
	}
	if !lookups[0].Selected || lookups[0].DecisionID == "" {
		t.Fatal("clarification preparation must not mutate the source lookup")
	}
}

func TestToolDimensionSelectionRejectsTwoDimensionsForTheSameTerm(t *testing.T) {
	lookups := []QueryDimensionValueLookupTrace{
		{
			Term: "北京", DimensionCode: "city",
			DecisionCandidates: []QueryDecisionCandidate{{
				DecisionID: "decision-city", MemberValue: "beijing", Score: 0.97,
			}},
		},
		{
			Term: "北京", DimensionCode: "region",
			DecisionCandidates: []QueryDecisionCandidate{{
				DecisionID: "decision-region", MemberValue: "north", Score: 0.96,
			}},
		},
	}
	if _, ok := applyToolSelectedDecisions(
		lookups, []string{"decision-city", "decision-region"},
	); ok {
		t.Fatal("one user term must not select two different dimensions")
	}

	lookups[0].Selected = true
	lookups[0].DecisionID = "decision-city"
	lookups[0].SelectedMemberKeys = []string{"beijing"}
	cleared := clearAmbiguousToolSelections(lookups)
	if cleared[0].Selected || cleared[0].DecisionID != "" ||
		len(unresolvedDimensionDecisionIDs(cleared)) != 2 {
		t.Fatalf("cross-dimension ambiguity must be returned to the user: %#v", cleared)
	}
}

func TestToolDimensionSelectionRejectsConflictWithExistingSelection(t *testing.T) {
	lookups := []QueryDimensionValueLookupTrace{
		{
			Term: "北京", DimensionCode: "city", Selected: true,
			DecisionID: "decision-city", SelectedMemberKeys: []string{"beijing"},
			DecisionCandidates: []QueryDecisionCandidate{{
				DecisionID: "decision-city", MemberValue: "beijing", Selected: true,
			}},
		},
		{
			Term: "北京", DimensionCode: "region",
			DecisionCandidates: []QueryDecisionCandidate{{
				DecisionID: "decision-region", MemberValue: "north",
			}},
		},
	}
	if _, valid := applyToolSelectedDecisions(
		lookups, []string{"decision-region"},
	); valid {
		t.Fatal("a new selection must not conflict with an existing term selection")
	}
}
