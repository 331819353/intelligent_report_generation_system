package querycompiler

import (
	"strings"
	"testing"

	"intelligent-report-generation-system/internal/dataset"
)

func TestCompileAllowsCrossSourcePlanInsideGovernedPostgreSQLWarehouse(t *testing.T) {
	visible := true
	document := dataset.Document{
		DSLVersion: dataset.DSLVersion,
		Dataset: dataset.Descriptor{
			Code: "dws_merchant_distribution", Name: "商户分布",
			Type: "CROSS_SOURCE", Layer: dataset.LayerDWS,
		},
		Nodes: []dataset.Node{{
			ID: "fact", Type: "DATASET", DatasetVersionID: "dwd_version",
			Alias: "fact", Projection: []string{"merchant_name", "order_count"},
		}},
		Fields: []dataset.Field{
			{
				ID: "field_merchant_name", Code: "merchant_name",
				Name: "商户名称", Role: "DIMENSION", CanonicalType: "STRING",
				Visible: &visible,
				Expression: dataset.Expression{
					Type: "FIELD_REF", NodeID: "fact", Field: "merchant_name",
				},
			},
			{
				ID: "field_order_count", Code: "order_count",
				Name: "订单数", Role: "MEASURE", CanonicalType: "INTEGER",
				Visible: &visible,
				Expression: dataset.Expression{
					Type: "AGGREGATE", Function: "SUM",
					Argument: &dataset.Expression{
						Type: "FIELD_REF", NodeID: "fact", Field: "order_count",
					},
				},
			},
		},
		Filters: []dataset.Filter{}, GroupBy: []string{"field_merchant_name"},
		Having: []dataset.Filter{}, Sorts: []dataset.Sort{},
		Parameters: []dataset.Parameter{},
		OutputGrain: dataset.OutputGrain{
			Description: "每行代表一个商户",
			KeyFields:   []string{"merchant_name"},
		},
		ExecutionPolicy: dataset.ExecutionPolicy{
			Mode: "MATERIALIZED_PREFERRED", TimeoutMS: 5000,
			PreviewLimit: 100, ResultLimit: 10000,
			Materialization: dataset.MaterializationPolicy{
				Enabled: true, RefreshMode: "MANUAL",
			},
		},
	}
	tables := map[string]TableRef{
		"fact": {
			NodeID: "fact", Schema: "warehouse_published",
			Name: "dwd_merchant_daily",
			Columns: map[string]bool{
				"merchant_name": true,
				"order_count":   true,
			},
		},
	}

	compiled, err := Compile(Input{
		Document: document, Dialect: PostgreSQL,
		Tables: tables, Parameters: map[string]any{}, MaxRows: 5,
	})
	if err != nil {
		t.Fatalf("compile governed cross-source preview: %v", err)
	}
	if !strings.Contains(
		compiled.SQL,
		`FROM "warehouse_published"."dwd_merchant_daily" "fact"`,
	) {
		t.Fatalf("compiled SQL does not use the governed relation: %s", compiled.SQL)
	}

	if _, err := Compile(Input{
		Document: document, Dialect: MySQL,
		Tables: tables, Parameters: map[string]any{}, MaxRows: 5,
	}); err == nil {
		t.Fatal("source-plane compiler accepted a cross-source document")
	}
}
