package dataset

import (
	"encoding/json"
	"testing"
)

func TestSourcePreviewExecutionExceptionIsNotPersisted(t *testing.T) {
	document := Document{
		DSLVersion: DSLVersion,
		Dataset: Descriptor{
			Code: "dwd_preview_test", Name: "预览测试",
			Type: "SINGLE_SOURCE", Layer: LayerDWD,
		},
		Nodes: []Node{
			{
				ID: "fact", Type: "TABLE", DataSourceID: "source",
				TableID: "fact_table", Alias: "fact",
				Projection: []string{"fact_id"}, SourceFilters: []SourceFilter{},
			},
			{
				ID: "dimension", Type: "TABLE", DataSourceID: "source",
				TableID: "dimension_table", Alias: "dimension",
				Projection: []string{"dimension_id"}, SourceFilters: []SourceFilter{},
			},
		},
		Joins: []Join{{
			ID: "join_dimension", LeftNodeID: "fact",
			RightNodeID: "dimension", JoinType: "LEFT",
			Cardinality: "MANY_TO_ONE", ManualConfirmed: true,
			Conditions: []JoinCondition{{
				Operator: "EQUALS",
				LeftExpression: Expression{
					Type: "FIELD_REF", NodeID: "fact", Field: "fact_id",
				},
				RightExpression: Expression{
					Type: "FIELD_REF", NodeID: "dimension",
					Field: "dimension_id",
				},
			}},
		}},
		PreAggregations: []PreAggregation{{
			ID: "preview_dimension", NodeID: "dimension",
			JoinID: "join_dimension", JoinSide: "RIGHT",
			GroupBy: []PreAggregationGroup{{Field: "dimension_id"}},
			Metrics: []PreAggregationMetric{{
				Field: "preview_row_count", Function: "COUNT",
				CountRows: true,
			}},
		}},
		Fields: []Field{
			{
				ID: "field_fact_id", Code: "fact_id", Name: "事实ID",
				Role: "IDENTIFIER", CanonicalType: "INTEGER",
				Expression: Expression{
					Type: "FIELD_REF", NodeID: "fact", Field: "fact_id",
				},
			},
			{
				ID: "field_dimension_id", Code: "dimension_id", Name: "维度ID",
				Role: "ATTRIBUTE", CanonicalType: "INTEGER",
				Expression: Expression{
					Type: "FIELD_REF", NodeID: "dimension",
					Field: "dimension_id",
				},
			},
		},
		Filters: []Filter{}, GroupBy: []string{}, Having: []Filter{},
		Sorts: []Sort{}, Parameters: []Parameter{},
		OutputGrain: OutputGrain{
			Description: "每行代表一条事实", KeyFields: []string{"fact_id"},
		},
		ExecutionPolicy: ExecutionPolicy{
			Mode: "REALTIME", TimeoutMS: 1000,
			PreviewLimit: 100, ResultLimit: 1000,
		},
	}

	if err := Validate(document); err == nil {
		t.Fatal("ordinary DWD unexpectedly accepted pre-join aggregation")
	}
	preview := AsSourcePreviewExecution(document)
	if err := Validate(preview); err != nil {
		if validation, ok := err.(*ValidationError); ok {
			t.Fatalf("source preview validation: %#v", validation.Issues)
		}
		t.Fatalf("source preview validation: %v", err)
	}
	raw, err := json.Marshal(preview)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeAndNormalize(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(decoded); err == nil {
		t.Fatal("serialized source preview retained its private exception")
	}
}
