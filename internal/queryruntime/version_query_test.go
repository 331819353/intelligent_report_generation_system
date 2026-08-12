package queryruntime

import (
	"testing"

	"intelligent-report-generation-system/internal/dataset"
)

func TestVersionPredicateContractIsClosedAndBounded(t *testing.T) {
	valid := []VersionPredicate{
		{FieldCode: "region", Operator: "EQUALS", Value: "east"},
		{FieldCode: "amount", Operator: "GTE", Value: 10.5},
		{FieldCode: "region", Operator: "IN", Value: []string{"east", "west"}},
	}
	for _, predicate := range valid {
		if !validVersionPredicate(predicate) {
			t.Fatalf("valid predicate rejected: %#v", predicate)
		}
	}
	invalid := []VersionPredicate{
		{FieldCode: "", Operator: "EQUALS", Value: "east"},
		{FieldCode: "region", Operator: "LIKE", Value: "%"},
		{FieldCode: "region", Operator: "IN", Value: []string{}},
		{FieldCode: "region", Operator: "EQUALS", Value: []string{"east"}},
		{FieldCode: "region", Operator: "EQUALS", Value: map[string]any{"sql": "select 1"}},
	}
	for _, predicate := range invalid {
		if validVersionPredicate(predicate) {
			t.Fatalf("invalid predicate accepted: %#v", predicate)
		}
	}
}

func TestExpressionContainsAggregateRecursesThroughClosedAST(t *testing.T) {
	expression := dataset.Expression{
		Type: "DIVIDE",
		Left: &dataset.Expression{Type: "COALESCE", Arguments: []dataset.Expression{{
			Type: "AGGREGATE", Function: "SUM",
		}}},
		Right: &dataset.Expression{Type: "LITERAL", Value: 2},
	}
	if !expressionContainsAggregate(expression) {
		t.Fatal("nested aggregate was not classified as post-aggregation")
	}
	if expressionContainsAggregate(dataset.Expression{Type: "FIELD_REF", Field: "amount"}) {
		t.Fatal("plain field was classified as aggregate")
	}
}

func TestReportVersionQueryRowCapMatchesTheDatasetDSLPreviewContract(t *testing.T) {
	if MaxVersionQueryRows != 5_000 {
		t.Fatalf("MaxVersionQueryRows = %d, want 5000", MaxVersionQueryRows)
	}
}
