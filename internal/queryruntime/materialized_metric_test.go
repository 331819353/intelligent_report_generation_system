package queryruntime

import (
	"testing"

	"intelligent-report-generation-system/internal/dataset"
)

func TestMaterializedFieldReferenceKeepsNonDecomposableExactGrainValue(t *testing.T) {
	field := dataset.Field{
		Code: "entity_count",
		Role: "MEASURE",
		Expression: dataset.Expression{
			Type: "AGGREGATE", Function: "COUNT_DISTINCT",
			Argument: &dataset.Expression{
				Type: "FIELD_REF", NodeID: "dimension", Field: "zone_id",
			},
		},
	}

	expression, err := materializedFieldReference(
		field, dataset.LayerDWS, true,
	)
	if err != nil {
		t.Fatalf("exact-grain materialized value rejected: %v", err)
	}
	if expression.Type != "AGGREGATE" || expression.Function != "MAX" ||
		expression.Argument == nil ||
		expression.Argument.Type != "FIELD_REF" ||
		expression.Argument.NodeID != materializedMetricNodeID ||
		expression.Argument.Field != field.Code {
		t.Fatalf("unexpected exact-grain expression: %#v", expression)
	}
}

func TestMaterializedFieldReferenceRejectsNonDecomposableRollup(t *testing.T) {
	field := dataset.Field{
		Code: "entity_count",
		Role: "MEASURE",
		Expression: dataset.Expression{
			Type: "AGGREGATE", Function: "COUNT_DISTINCT",
			Argument: &dataset.Expression{
				Type: "FIELD_REF", NodeID: "dimension", Field: "zone_id",
			},
		},
	}

	if _, err := materializedFieldReference(
		field, dataset.LayerDWS, false,
	); err == nil {
		t.Fatal("non-decomposable coarser-grain rollup should be rejected")
	}
}

func TestMaterializedFieldReferenceRollsUpEntityCountContract(t *testing.T) {
	field := dataset.Field{
		Code: "entity_count",
		Role: "MEASURE",
		Expression: dataset.Expression{
			Type: "AGGREGATE", Function: "COUNT_DISTINCT",
			Argument: &dataset.Expression{
				Type: "FIELD_REF", NodeID: "dimension", Field: "courier_id",
			},
		},
	}
	contract := &dataset.AnalysisContract{
		Intent: "ENTITY_COUNT",
		Measures: []dataset.AnalysisMeasureContract{
			{Field: "entity_count", Aggregation: "COUNT_DISTINCT"},
		},
	}
	if !isEntityCountContractMeasure(contract, field) {
		t.Fatal("expected generated ENTITY_COUNT measure to be recognized")
	}
	expression, err := materializedFieldReferenceForContract(
		field, dataset.LayerDWS, false, true,
	)
	if err != nil {
		t.Fatalf("entity-count contract rollup rejected: %v", err)
	}
	if expression.Type != "AGGREGATE" ||
		expression.Function != "SUM" ||
		expression.Argument == nil ||
		expression.Argument.NodeID != materializedMetricNodeID ||
		expression.Argument.Field != "entity_count" {
		t.Fatalf("unexpected entity-count rollup: %#v", expression)
	}
}
