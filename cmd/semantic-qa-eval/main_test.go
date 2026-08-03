package main

import (
	"encoding/json"
	"testing"
)

func TestBuildEvaluationCasesHasBroadDeterministicCoverage(t *testing.T) {
	cases := buildEvaluationCases()
	if len(cases) != 302 {
		t.Fatalf("case count = %d, want 302", len(cases))
	}
	seen := map[string]bool{}
	classes := map[string]int{}
	for _, item := range cases {
		if seen[item.ID] {
			t.Fatalf("duplicate case id %q", item.ID)
		}
		seen[item.ID] = true
		classes[item.Class]++
	}
	for _, class := range []string{
		"DIRECT_METRIC", "SUPERSEDED_ALIAS", "CITY_FILTER", "DATE_FILTER",
		"ORDER_STATUS_FILTER", "PAYMENT_METHOD_FILTER", "ITEM_CATEGORY_FILTER",
		"DELIVERY_EVENT_FILTER", "RANKING", "DISTRIBUTION",
	} {
		if classes[class] == 0 {
			t.Fatalf("class %q has no cases", class)
		}
	}
}

func TestWilsonLowerRequiresEvidenceBeyondPointAccuracy(t *testing.T) {
	if lower := wilsonLower(302, 302); lower < 0.98 {
		t.Fatalf("perfect 302-case lower bound = %v", lower)
	}
	if lower := wilsonLower(19, 20); lower >= 0.95 {
		t.Fatalf("small 95%% sample must not pass confidence gate: %v", lower)
	}
}

func TestCompareRowsChecksTextAndNumbers(t *testing.T) {
	actual := [][]json.RawMessage{{json.RawMessage(`"Suzhou"`), json.RawMessage(`26052.0`)}}
	if failure := compareRows([][]any{{"Suzhou", float64(26052)}}, actual); failure != "" {
		t.Fatal(failure)
	}
	if failure := compareRows([][]any{{"Beijing", float64(26052)}}, actual); failure == "" {
		t.Fatal("different dimension member must fail")
	}
}

func TestFilterEvaluationCasesSelectsRequestedClasses(t *testing.T) {
	cases := []evaluationCase{
		{ID: "1", Class: "DIRECT_METRIC"},
		{ID: "2", Class: "PAYMENT_METHOD_FILTER"},
		{ID: "3", Class: "DELIVERY_EVENT_FILTER"},
	}
	filtered, err := filterEvaluationCases(
		cases, " payment_method_filter,DELIVERY_EVENT_FILTER ",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 2 || filtered[0].ID != "2" || filtered[1].ID != "3" {
		t.Fatalf("unexpected filtered cases: %+v", filtered)
	}
}
