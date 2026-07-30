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
