package dataset

import (
	"encoding/json"
	"testing"
)

func TestBuildDWSThemeCandidateCreatesReviewableDraft(t *testing.T) {
	sourceDocument := Document{
		DSLVersion: DSLVersion,
		Dataset: Descriptor{
			Code: "dwd_order_item", Name: "订单明细", Type: "SINGLE_SOURCE",
			Layer: LayerDWD, Domain: "供应链", Subject: "订单",
			SemanticContractVersion: "1.0",
		},
		Nodes: []Node{{
			ID: "source", Type: "DATASET",
			DatasetVersionID: "4dc4d116-133a-4f3e-961a-d5d140d93a26",
			Alias:            "source", Projection: []string{"order_id", "order_date", "region", "amount"},
			SourceFilters: []SourceFilter{},
		}},
		Joins: []Join{},
		Fields: []Field{
			{ID: "field_order_id", Code: "order_id", Name: "订单", Role: "IDENTIFIER", CanonicalType: "STRING", Expression: Expression{Type: "FIELD_REF", NodeID: "source", Field: "order_id"}},
			{ID: "field_order_date", Code: "order_date", Name: "下单日期", Role: "TIME", CanonicalType: "DATE", Expression: Expression{Type: "FIELD_REF", NodeID: "source", Field: "order_date"}},
			{ID: "field_region", Code: "region", Name: "区域", Role: "DIMENSION", CanonicalType: "STRING", Expression: Expression{Type: "FIELD_REF", NodeID: "source", Field: "region"}},
			{ID: "field_amount", Code: "amount", Name: "金额", Role: "MEASURE", CanonicalType: "DECIMAL", Expression: Expression{Type: "FIELD_REF", NodeID: "source", Field: "amount"}},
		},
		Filters: []Filter{}, GroupBy: []string{}, Having: []Filter{}, Sorts: []Sort{}, Parameters: []Parameter{},
		OutputGrain: OutputGrain{Description: "每行一条订单明细", KeyFields: []string{"order_id"}, TimeField: "order_date"},
		FactContract: &FactContract{
			BusinessAction: "下单", GrainKeyFields: []string{"order_id"}, EventTimeField: "order_date",
			AtomicMeasures: []AtomicMeasureContract{{Field: "amount", Additivity: "ADDITIVE", DefaultAggregation: "SUM", NullPolicy: "ZERO"}},
		},
		ExecutionPolicy: ExecutionPolicy{
			Mode: "MATERIALIZED_PREFERRED", TimeoutMS: 30000, PreviewLimit: 10,
			ResultLimit: 100000, CacheTTLSeconds: 300,
			Materialization: MaterializationPolicy{Enabled: true, RefreshMode: "MANUAL"},
		},
	}
	raw, err := json.Marshal(sourceDocument)
	if err != nil {
		t.Fatal(err)
	}
	sourcePrepared, err := Prepare(raw)
	if err != nil {
		t.Fatalf("prepare source: %v", err)
	}
	source := Record{Code: "dwd_order_item", Name: "订单明细", DSL: sourcePrepared.DSLJSON}
	prepared, err := buildDWSThemeCandidate(
		source,
		"1472fc03-7e4c-431d-a8ac-fb9aa61a961b",
		sourcePrepared.Document,
		dwsModelingPlan{
			Name: "订单主题汇总", Description: "订单金额按日期和区域汇总",
			Subject: "订单分析", DimensionCodes: []string{"region"}, MetricCodes: []string{"amount"},
		},
	)
	if err != nil {
		if validation, ok := err.(*ValidationError); ok {
			t.Fatalf("build DWS validation: %#v", validation.Issues)
		}
		t.Fatalf("build DWS: %v", err)
	}
	if prepared.Document.Dataset.Layer != LayerDWS ||
		len(prepared.Document.Fields) != 3 ||
		prepared.Document.ExecutionPolicy.PreviewLimit != 10 {
		t.Fatalf("unexpected DWS document: %#v", prepared.Document)
	}
}
