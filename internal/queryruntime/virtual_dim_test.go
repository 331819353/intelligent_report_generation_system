package queryruntime

import (
	"testing"

	"intelligent-report-generation-system/internal/dataset"
)

func TestExpandVirtualDIMDocumentPreservesCleaningInDWDPreview(t *testing.T) {
	dwd := dataset.Document{
		Dataset: dataset.Descriptor{
			Layer: dataset.LayerDWD, Type: "SINGLE_SOURCE",
		},
		Nodes: []dataset.Node{
			{
				ID: "fact", Type: "TABLE", DataSourceID: "source",
				TableID: "orders", Alias: "orders",
				Projection: []string{"MERCHANT_ID"},
			},
			{
				ID: "merchant_dim", Type: "DATASET",
				DatasetVersionID: "dim_version", Alias: "merchant",
				Projection: []string{"merchant_id", "merchant_name"},
			},
		},
		Joins: []dataset.Join{{
			ID: "join_1", LeftNodeID: "fact", RightNodeID: "merchant_dim",
			Conditions: []dataset.JoinCondition{{
				Operator: "EQUALS",
				LeftExpression: dataset.Expression{
					Type: "FIELD_REF", NodeID: "fact", Field: "MERCHANT_ID",
				},
				RightExpression: dataset.Expression{
					Type: "FIELD_REF", NodeID: "merchant_dim", Field: "merchant_id",
				},
			}},
		}},
		Fields: []dataset.Field{{
			ID: "field_name", Code: "merchant_name", Name: "商户名称",
			Expression: dataset.Expression{
				Type: "FIELD_REF", NodeID: "merchant_dim", Field: "merchant_name",
			},
		}},
	}
	dim := dataset.Document{
		Dataset:  dataset.Descriptor{Layer: dataset.LayerDIM},
		Distinct: true,
		Nodes: []dataset.Node{{
			ID: "dim_source", Type: "DATASET",
			DatasetVersionID: "ods_version",
			Projection:       []string{"MERCHANT_ID", "MERCHANT_NAME"},
		}},
		Fields: []dataset.Field{
			{
				Code: "merchant_id",
				Expression: dataset.Expression{
					Type: "FIELD_REF", NodeID: "dim_source", Field: "MERCHANT_ID",
				},
			},
			{
				Code: "merchant_name",
				Expression: dataset.Expression{
					Type: "TRIM",
					Argument: &dataset.Expression{
						Type: "FIELD_REF", NodeID: "dim_source", Field: "MERCHANT_NAME",
					},
				},
			},
		},
	}
	ods := dataset.Document{
		Dataset: dataset.Descriptor{Layer: dataset.LayerODS},
		Nodes: []dataset.Node{{
			ID: "physical", Type: "TABLE", DataSourceID: "dimension_source",
			TableID: "merchant_table", Alias: "merchant",
			Projection: []string{"MERCHANT_ID", "MERCHANT_NAME"},
		}},
		Fields: []dataset.Field{
			{
				Code: "MERCHANT_ID", CanonicalType: "INTEGER",
				Expression: dataset.Expression{
					Type: "FIELD_REF", NodeID: "physical", Field: "MERCHANT_ID",
				},
			},
			{
				Code: "MERCHANT_NAME", CanonicalType: "STRING",
				Expression: dataset.Expression{
					Type: "FIELD_REF", NodeID: "physical", Field: "MERCHANT_NAME",
				},
			},
		},
	}

	expanded, overrides, err := expandVirtualDIMDocument(
		dwd,
		map[string]dataset.Document{"merchant_dim": dim},
		map[string]dataset.Document{"merchant_dim": ods},
	)
	if err != nil {
		t.Fatalf("expand virtual DIM: %v", err)
	}
	if expanded.Nodes[1].Type != "TABLE" ||
		expanded.Nodes[1].TableID != "merchant_table" ||
		expanded.Nodes[1].ID != "merchant_dim" {
		t.Fatalf("DIM node was not rebound to its physical ODS source: %#v", expanded.Nodes[1])
	}
	right := expanded.Joins[0].Conditions[0].RightExpression
	if right.Type != "FIELD_REF" ||
		right.NodeID != "merchant_dim" ||
		right.Field != "merchant_id" {
		t.Fatalf("DIM join key no longer targets the distinct DIM output: %#v", right)
	}
	output := expanded.Fields[0].Expression
	if output.Type != "FIELD_REF" ||
		output.NodeID != "merchant_dim" ||
		output.Field != "merchant_name" {
		t.Fatalf("DWD output no longer targets the distinct DIM output: %#v", output)
	}
	if len(expanded.PreAggregations) != 1 ||
		len(expanded.PreAggregations[0].GroupBy) != 2 ||
		len(expanded.PreAggregations[0].Metrics) != 1 ||
		expanded.PreAggregations[0].JoinID != "join_1" ||
		expanded.PreAggregations[0].JoinSide != "RIGHT" {
		t.Fatalf("DIM DISTINCT was not preserved: %#v", expanded.PreAggregations)
	}
	if expanded.Dataset.Layer != dataset.LayerDWD {
		t.Fatalf("execution layer = %q, want DWD", expanded.Dataset.Layer)
	}
	nameExpression := expanded.PreAggregations[0].GroupBy[1].Expression
	if nameExpression == nil || nameExpression.Type != "TRIM" ||
		nameExpression.Argument == nil ||
		nameExpression.Argument.NodeID != "merchant_dim" ||
		nameExpression.Argument.Field != "MERCHANT_NAME" {
		t.Fatalf("DIM cleaning expression was not preserved: %#v", nameExpression)
	}
	if overrides["merchant_dim"]["MERCHANT_NAME"] != "STRING" {
		t.Fatalf("physical type overrides = %#v", overrides)
	}
	normalizeVirtualExecutionType(&expanded)
	if expanded.Dataset.Type != "CROSS_SOURCE" {
		t.Fatalf("execution type = %q, want CROSS_SOURCE", expanded.Dataset.Type)
	}
}
