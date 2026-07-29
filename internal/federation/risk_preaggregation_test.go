package federation

import (
	"context"
	"testing"

	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/filequery"
)

func TestJoinRisksInspectPreAggregatedDIMRowset(t *testing.T) {
	document := preAggregatedDIMRiskDocument()
	raw := map[string]filequery.NodeTableData{
		"fact": {
			Columns: []string{"SKU_ID"},
			Rows:    [][]any{{int64(1)}, {int64(1)}, {int64(2)}},
		},
		"sku_dim": {
			Columns: []string{"SKU_ID", "ITEM_NAME"},
			Rows: [][]any{
				{int64(1), "商品一"},
				{int64(1), "商品一"},
				{int64(2), "商品二"},
			},
		},
	}

	joinedRowsets, err := filequery.ApplyPreAggregations(
		context.Background(), document, raw, nil,
	)
	if err != nil {
		t.Fatalf("apply DIM rowset: %v", err)
	}
	if rows := len(joinedRowsets["sku_dim"].Rows); rows != 2 {
		t.Fatalf("DIM row count = %d, want 2", rows)
	}
	warnings, err := analyzeJoinRisks(
		context.Background(), document, joinedRowsets,
	)
	if err != nil {
		t.Fatalf("analyze Join risks: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("stable DIM DISTINCT contract produced false warnings: %#v", warnings)
	}
}

func TestJoinRisksStillRejectAttributeVariantsAtSameDIMKey(t *testing.T) {
	document := preAggregatedDIMRiskDocument()
	raw := map[string]filequery.NodeTableData{
		"fact": {
			Columns: []string{"SKU_ID"},
			Rows:    [][]any{{int64(1)}, {int64(1)}},
		},
		"sku_dim": {
			Columns: []string{"SKU_ID", "ITEM_NAME"},
			Rows: [][]any{
				{int64(1), "商品一"},
				{int64(1), "商品一（旧名）"},
			},
		},
	}

	joinedRowsets, err := filequery.ApplyPreAggregations(
		context.Background(), document, raw, nil,
	)
	if err != nil {
		t.Fatalf("apply DIM rowset: %v", err)
	}
	warnings, err := analyzeJoinRisks(
		context.Background(), document, joinedRowsets,
	)
	if err != nil {
		t.Fatalf("analyze Join risks: %v", err)
	}
	codes := map[string]bool{}
	for _, warning := range warnings {
		codes[warning.Code] = true
	}
	if !codes["JOIN_CARDINALITY_MISMATCH"] {
		t.Fatalf("true DIM key duplication was not detected: %#v", warnings)
	}
	if !codes["JOIN_FANOUT_RISK"] {
		t.Fatalf("true Join fanout was not detected: %#v", warnings)
	}
}

func preAggregatedDIMRiskDocument() dataset.Document {
	return dataset.Document{
		Nodes: []dataset.Node{
			{ID: "fact", Projection: []string{"SKU_ID"}},
			{ID: "sku_dim", Projection: []string{"SKU_ID", "ITEM_NAME"}},
		},
		Joins: []dataset.Join{{
			ID: "join_sku", LeftNodeID: "fact", RightNodeID: "sku_dim",
			JoinType: "LEFT", Cardinality: "MANY_TO_ONE",
			ManualConfirmed: true,
			Conditions: []dataset.JoinCondition{{
				Operator: "EQUALS",
				LeftExpression: dataset.Expression{
					Type: "FIELD_REF", NodeID: "fact", Field: "SKU_ID",
				},
				RightExpression: dataset.Expression{
					Type: "FIELD_REF", NodeID: "sku_dim", Field: "sku_id",
				},
			}},
		}},
		PreAggregations: []dataset.PreAggregation{{
			ID: "preview_dim_sku", NodeID: "sku_dim",
			JoinID: "join_sku", JoinSide: "RIGHT",
			GroupBy: []dataset.PreAggregationGroup{
				{
					Field: "sku_id",
					Expression: &dataset.Expression{
						Type: "FIELD_REF", NodeID: "sku_dim", Field: "SKU_ID",
					},
				},
				{
					Field: "item_name",
					Expression: &dataset.Expression{
						Type: "TRIM",
						Argument: &dataset.Expression{
							Type: "FIELD_REF", NodeID: "sku_dim",
							Field: "ITEM_NAME",
						},
					},
				},
			},
			Metrics: []dataset.PreAggregationMetric{{
				Field: "preview_row_count", Function: "COUNT", CountRows: true,
			}},
		}},
	}
}
