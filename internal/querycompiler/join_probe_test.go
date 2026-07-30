package querycompiler

import (
	"strings"
	"testing"

	"intelligent-report-generation-system/internal/dataset"
)

func TestCompileJoinProbesUsesPreAggregatedJoinRowset(t *testing.T) {
	document := dataset.AsSourcePreviewExecution(dataset.Document{
		DSLVersion: dataset.DSLVersion,
		Dataset: dataset.Descriptor{
			Code: "dwd_order_item", Name: "订单商品明细事实表",
			Type: "SINGLE_SOURCE", Layer: dataset.LayerDWD,
		},
		Nodes: []dataset.Node{
			{
				ID: "fact", Type: "TABLE", DataSourceID: "source",
				TableID: "order_items", Alias: "fact",
				Projection: []string{"ITEM_ID", "SKU_ID"},
			},
			{
				ID: "sku_dim", Type: "TABLE", DataSourceID: "source",
				TableID: "order_items", Alias: "sku",
				Projection: []string{"SKU_ID", "ITEM_NAME"},
			},
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
							Type: "FIELD_REF", NodeID: "sku_dim", Field: "ITEM_NAME",
						},
					},
				},
			},
			Metrics: []dataset.PreAggregationMetric{{
				Field: "preview_row_count", Function: "COUNT", CountRows: true,
			}},
		}},
		Fields: []dataset.Field{
			{
				ID: "field_item_id", Code: "item_id", Name: "明细ID",
				Role: "IDENTIFIER", CanonicalType: "INTEGER",
				Expression: dataset.Expression{
					Type: "FIELD_REF", NodeID: "fact", Field: "ITEM_ID",
				},
			},
			{
				ID: "field_sku_name", Code: "sku_name", Name: "商品名称",
				Role: "ATTRIBUTE", CanonicalType: "STRING",
				Expression: dataset.Expression{
					Type: "FIELD_REF", NodeID: "sku_dim", Field: "item_name",
				},
			},
		},
		Filters: []dataset.Filter{}, GroupBy: []string{},
		Having: []dataset.Filter{}, Sorts: []dataset.Sort{},
		Parameters: []dataset.Parameter{},
		OutputGrain: dataset.OutputGrain{
			Description: "每行代表一条订单商品明细",
			KeyFields:   []string{"item_id"},
		},
		ExecutionPolicy: dataset.ExecutionPolicy{
			Mode: "REALTIME", TimeoutMS: 1000,
			PreviewLimit: 100, ResultLimit: 1000,
		},
	})
	tables := map[string]TableRef{
		"fact": {
			NodeID: "fact", Schema: "sales", Name: "order_items",
			Columns: map[string]bool{"ITEM_ID": true, "SKU_ID": true},
		},
		"sku_dim": {
			NodeID: "sku_dim", Schema: "sales", Name: "order_items",
			// The logical DIM outputs deliberately differ from the physical
			// ODS names. The probe must target the derived DIM rowset.
			Columns: map[string]bool{"SKU_ID": true, "ITEM_NAME": true},
		},
	}

	probes, err := CompileJoinProbes(JoinProbeInput{
		Document: document, Dialect: MySQL, Tables: tables,
	})
	if err != nil {
		t.Fatalf("compile join probes: %v", err)
	}
	if len(probes) != 1 {
		t.Fatalf("probe count = %d, want 1", len(probes))
	}
	sql := probes[0].Query.SQL
	if !strings.Contains(sql,
		"FROM (SELECT `pre_source`.`SKU_ID` AS `sku_id`, TRIM(`pre_source`.`ITEM_NAME`) AS `item_name`, COUNT(*) AS `preview_row_count` FROM `sales`.`order_items` `pre_source` GROUP BY `pre_source`.`SKU_ID`, TRIM(`pre_source`.`ITEM_NAME`)) `sku`") {
		t.Fatalf("probe does not use the pre-aggregated DIM rowset: %s", sql)
	}
	if strings.Contains(sql,
		"SELECT `sku`.`sku_id` AS `probe_key_0` FROM `sales`.`order_items` `sku`") {
		t.Fatalf("probe bypasses the DIM rowset and scans raw ODS rows: %s", sql)
	}
}

func TestCompileJoinProbesKeepsRawRowsetWithoutPreAggregation(t *testing.T) {
	document := dataset.AsSourcePreviewExecution(dataset.Document{
		DSLVersion: dataset.DSLVersion,
		Dataset: dataset.Descriptor{
			Code: "dwd_orders", Name: "订单事实表",
			Type: "SINGLE_SOURCE", Layer: dataset.LayerDWD,
		},
		Nodes: []dataset.Node{
			{
				ID: "fact", Type: "TABLE", DataSourceID: "source",
				TableID: "orders", Alias: "fact",
				Projection: []string{"ORDER_ID", "MERCHANT_ID"},
			},
			{
				ID: "merchant", Type: "TABLE", DataSourceID: "source",
				TableID: "merchants", Alias: "merchant",
				Projection: []string{"MERCHANT_ID"},
			},
		},
		Joins: []dataset.Join{{
			ID: "join_merchant", LeftNodeID: "fact", RightNodeID: "merchant",
			JoinType: "LEFT", Cardinality: "MANY_TO_ONE",
			ManualConfirmed: true,
			Conditions: []dataset.JoinCondition{{
				Operator: "EQUALS",
				LeftExpression: dataset.Expression{
					Type: "FIELD_REF", NodeID: "fact", Field: "MERCHANT_ID",
				},
				RightExpression: dataset.Expression{
					Type: "FIELD_REF", NodeID: "merchant", Field: "MERCHANT_ID",
				},
			}},
		}},
		Fields: []dataset.Field{{
			ID: "field_order_id", Code: "order_id", Name: "订单ID",
			Role: "IDENTIFIER", CanonicalType: "INTEGER",
			Expression: dataset.Expression{
				Type: "FIELD_REF", NodeID: "fact", Field: "ORDER_ID",
			},
		}},
		Filters: []dataset.Filter{}, GroupBy: []string{},
		Having: []dataset.Filter{}, Sorts: []dataset.Sort{},
		Parameters: []dataset.Parameter{},
		OutputGrain: dataset.OutputGrain{
			Description: "每行代表一条订单", KeyFields: []string{"order_id"},
		},
		ExecutionPolicy: dataset.ExecutionPolicy{
			Mode: "REALTIME", TimeoutMS: 1000,
			PreviewLimit: 100, ResultLimit: 1000,
		},
	})
	tables := map[string]TableRef{
		"fact": {
			NodeID: "fact", Schema: "sales", Name: "orders",
			Columns: map[string]bool{"ORDER_ID": true, "MERCHANT_ID": true},
		},
		"merchant": {
			NodeID: "merchant", Schema: "sales", Name: "merchants",
			Columns: map[string]bool{"MERCHANT_ID": true},
		},
	}

	probes, err := CompileJoinProbes(JoinProbeInput{
		Document: document, Dialect: MySQL, Tables: tables,
	})
	if err != nil {
		t.Fatalf("compile join probes: %v", err)
	}
	if !strings.Contains(probes[0].Query.SQL,
		"SELECT `merchant`.`MERCHANT_ID` AS `probe_key_0` FROM `sales`.`merchants` `merchant`") {
		t.Fatalf("ordinary Join probe no longer uses the physical rowset: %s", probes[0].Query.SQL)
	}
}
