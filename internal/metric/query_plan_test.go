package metric

import (
	"encoding/json"
	"testing"

	"intelligent-report-generation-system/internal/dataset"
)

func TestBuildQueryCandidateDetachesSourceAnalysisContract(t *testing.T) {
	document := dataset.Document{
		DSLVersion: dataset.DSLVersion,
		Dataset: dataset.Descriptor{
			Code: "dws_sales_summary", Name: "销售主题汇总",
			Type: "SINGLE_SOURCE", Layer: dataset.LayerDWS,
			Domain: "sales", Subject: "orders",
			SemanticContractVersion: "1.0",
		},
		Nodes: []dataset.Node{
			{
				ID: "fact_1", Type: "DATASET", Alias: "fact_1",
				DatasetVersionID: "11111111-1111-4111-8111-111111111111",
				Projection:       []string{"event_at", "event_count"},
				SourceFilters:    []dataset.SourceFilter{},
			},
			{
				ID: "fact_2", Type: "DATASET", Alias: "fact_2",
				DatasetVersionID: "66666666-6666-4666-8666-666666666666",
				Projection:       []string{"ordered_at", "gross_amount"},
				SourceFilters:    []dataset.SourceFilter{},
			},
		},
		Joins: []dataset.Join{{
			ID: "join_1", LeftNodeID: "fact_1", RightNodeID: "fact_2",
			JoinType: "LEFT", Cardinality: "ONE_TO_ONE",
			FanoutPolicy: "SAFE", ManualConfirmed: true,
			Conditions: []dataset.JoinCondition{{
				Operator: "EQUALS",
				LeftExpression: dataset.Expression{
					Type: "FIELD_REF", NodeID: "fact_1", Field: "stat_month",
				},
				RightExpression: dataset.Expression{
					Type: "FIELD_REF", NodeID: "fact_2", Field: "stat_month",
				},
			}},
		}},
		PreAggregations: []dataset.PreAggregation{
			{
				ID: "preagg_1", NodeID: "fact_1",
				JoinID: "join_1", JoinSide: "LEFT",
				GroupBy: []dataset.PreAggregationGroup{{
					Field: "stat_month", Unit: "MONTH",
					Expression: expressionPointer(dataset.Expression{
						Type: "FIELD_REF", NodeID: "fact_1", Field: "event_at",
					}),
				}},
				Metrics: []dataset.PreAggregationMetric{{
					Field: "event_count", Function: "SUM",
					Expression: expressionPointer(dataset.Expression{
						Type: "FIELD_REF", NodeID: "fact_1", Field: "event_count",
					}),
				}},
			},
			{
				ID: "preagg_2", NodeID: "fact_2",
				JoinID: "join_1", JoinSide: "RIGHT",
				GroupBy: []dataset.PreAggregationGroup{{
					Field: "stat_month", Unit: "MONTH",
					Expression: expressionPointer(dataset.Expression{
						Type: "FIELD_REF", NodeID: "fact_2", Field: "ordered_at",
					}),
				}},
				Metrics: []dataset.PreAggregationMetric{{
					Field: "gross_amount", Function: "SUM",
					Expression: expressionPointer(dataset.Expression{
						Type: "FIELD_REF", NodeID: "fact_2", Field: "gross_amount",
					}),
				}},
			},
		},
		Fields: []dataset.Field{
			{
				ID: "field_stat_month", Code: "stat_month", Name: "统计月份",
				Role: "TIME", CanonicalType: "DATE",
				Expression: dataset.Expression{
					Type: "FIELD_REF", NodeID: "fact_1", Field: "stat_month",
				},
			},
			{
				ID: "field_gross_amount", Code: "gross_amount", Name: "订单总金额",
				Role: "MEASURE", CanonicalType: "DECIMAL", Nullable: true,
				Expression: dataset.Expression{
					Type: "FIELD_REF", NodeID: "fact_2", Field: "gross_amount",
				},
			},
		},
		Filters: []dataset.Filter{}, GroupBy: []string{}, Having: []dataset.Filter{},
		Sorts: []dataset.Sort{}, Parameters: []dataset.Parameter{},
		OutputGrain: dataset.OutputGrain{
			Description: "每行一个统计月份",
			KeyFields:   []string{"stat_month"}, TimeField: "stat_month",
			DefaultTimeGrain: "MONTH",
		},
		ExecutionPolicy: dataset.ExecutionPolicy{
			Mode: "MATERIALIZED_PREFERRED", TimeoutMS: 5000,
			PreviewLimit: 100, ResultLimit: 10000, CacheTTLSeconds: 300,
			Materialization: dataset.MaterializationPolicy{
				Enabled: true, RefreshMode: "MANUAL",
			},
		},
		AnalysisContract: &dataset.AnalysisContract{
			Intent: "MULTI_FACT_COMPARISON", InputMode: "MULTI_FACT",
			CommonGrainFields:   []string{"stat_month"},
			ConformedDimensions: []string{"stat_month"},
			TimeField:           "stat_month", TimeGrain: "MONTH",
			Measures: []dataset.AnalysisMeasureContract{{
				Field: "gross_amount", SourceNodeIDs: []string{"fact_2"},
				Aggregation: "SUM", Additivity: "ADDITIVE",
			}},
		},
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	preparedDataset, err := dataset.Prepare(raw)
	if err != nil {
		t.Fatalf("prepare DWS fixture: %#v", err)
	}
	definition := Definition{
		SchemaVersion: DefinitionVersion,
		Metric: Descriptor{
			Code: "gross_revenue", Name: "订单总金额",
			Description: "订单总金额", Type: "ATOMIC",
		},
		DatasetID:        "22222222-2222-4222-8222-222222222222",
		DatasetVersionID: "33333333-3333-4333-8333-333333333333",
		Expression:       Expression{Type: "FIELD_REF", FieldID: "field_gross_amount"},
		Aggregation:      "SUM", Unit: "", NumberFormat: "#,##0.00",
		TimeFieldID: "field_stat_month", TimeGrain: "MONTH",
		Additivity: "ADDITIVE", NonAdditiveDimensionFieldIDs: []string{},
		AllowedDimensions: []Dimension{{
			FieldID: "field_stat_month", Name: "统计月份",
			HierarchyFieldIDs: []string{}, SortDirection: "ASC", NullLabel: "未知",
		}},
		DecimalScale: 2, RoundingMode: "HALF_UP",
		NullHandling: "IGNORE", DivisionByZero: "NULL",
	}
	preparedMetric, err := Prepare(definitionJSON(t, definition))
	if err != nil {
		t.Fatalf("prepare metric: %v", err)
	}
	candidate, _, err := buildQueryCandidate(
		"44444444-4444-4444-8444-444444444444",
		"55555555-5555-4555-8555-555555555555",
		validatedDefinition{
			prepared:        preparedMetric,
			datasetDocument: preparedDataset.Document,
		},
		nil, nil, "",
	)
	if err != nil {
		t.Fatalf("build DWS metric query candidate: %v", err)
	}
	derived, err := dataset.DecodeAndNormalize(candidate.DSL)
	if err != nil {
		t.Fatal(err)
	}
	if derived.Dataset.Layer != dataset.LayerDWS ||
		derived.Dataset.SemanticContractVersion != "" ||
		derived.AnalysisContract != nil ||
		derived.FactContract != nil ||
		len(derived.Fields) != 1 ||
		derived.Fields[0].Code != definition.Metric.Code {
		t.Fatalf("unexpected derived metric document: %#v", derived)
	}
}

func TestBuildQueryCandidateReadsDistinctDIMAsOuterAggregate(t *testing.T) {
	document := dataset.Document{
		DSLVersion: dataset.DSLVersion,
		Dataset: dataset.Descriptor{
			Code: "dim_courier", Name: "骑手维度表",
			Type: "SINGLE_SOURCE", Layer: dataset.LayerDIM,
			Domain: "operations", Subject: "courier",
		},
		Nodes: []dataset.Node{{
			ID: "source", Type: "DATASET", Alias: "source",
			DatasetVersionID: "11111111-1111-4111-8111-111111111111",
			Projection:       []string{"courier_id", "hire_date"},
			SourceFilters:    []dataset.SourceFilter{},
		}},
		Fields: []dataset.Field{
			{
				ID: "field_courier_id", Code: "courier_id", Name: "骑手ID",
				Role: "IDENTIFIER", CanonicalType: "INTEGER",
				Expression: dataset.Expression{
					Type: "FIELD_REF", NodeID: "source", Field: "courier_id",
				},
			},
			{
				ID: "field_hire_date", Code: "hire_date", Name: "入职日期",
				Role: "TIME", CanonicalType: "DATE",
				Expression: dataset.Expression{
					Type: "FIELD_REF", NodeID: "source", Field: "hire_date",
				},
			},
		},
		Distinct: true,
		Filters:  []dataset.Filter{}, GroupBy: []string{}, Having: []dataset.Filter{},
		Sorts: []dataset.Sort{}, Parameters: []dataset.Parameter{},
		OutputGrain: dataset.OutputGrain{
			Description: "每行一个骑手", KeyFields: []string{"courier_id"},
		},
		ExecutionPolicy: dataset.ExecutionPolicy{
			Mode: "MATERIALIZED_PREFERRED", TimeoutMS: 5000,
			PreviewLimit: 100, ResultLimit: 10000, CacheTTLSeconds: 300,
			Materialization: dataset.MaterializationPolicy{
				Enabled: true, RefreshMode: "MANUAL",
			},
		},
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	preparedDataset, err := dataset.Prepare(raw)
	if err != nil {
		t.Fatalf("prepare DIM fixture: %v", err)
	}
	definition := Definition{
		SchemaVersion: DefinitionVersion,
		Metric: Descriptor{
			Code: "courier_count", Name: "骑手总数",
			Description: "骑手ID去重数", Type: "ATOMIC",
		},
		DatasetID:        "22222222-2222-4222-8222-222222222222",
		DatasetVersionID: "33333333-3333-4333-8333-333333333333",
		Expression: Expression{
			Type: "FIELD_REF", FieldID: "field_courier_id",
		},
		Aggregation: "COUNT_DISTINCT", Unit: "个", NumberFormat: "#,##0",
		TimeFieldID: "field_hire_date", TimeGrain: "DAY",
		Additivity: "NON_ADDITIVE", NonAdditiveDimensionFieldIDs: []string{},
		AllowedDimensions: []Dimension{{
			FieldID: "field_hire_date", Name: "入职日期",
			HierarchyFieldIDs: []string{}, SortDirection: "ASC", NullLabel: "未知",
		}},
		DecimalScale: 0, RoundingMode: "HALF_UP",
		NullHandling: "IGNORE", DivisionByZero: "NULL",
	}
	preparedMetric, err := Prepare(definitionJSON(t, definition))
	if err != nil {
		t.Fatal(err)
	}
	candidate, _, err := buildQueryCandidate(
		"44444444-4444-4444-8444-444444444444",
		"55555555-5555-4555-8555-555555555555",
		validatedDefinition{
			prepared: preparedMetric, datasetDocument: preparedDataset.Document,
		},
		nil, nil, "",
	)
	if err != nil {
		t.Fatalf("build DIM metric query candidate: %v", err)
	}
	derived, err := dataset.DecodeAndNormalize(candidate.DSL)
	if err != nil {
		t.Fatal(err)
	}
	if derived.Dataset.Layer != dataset.LayerDWS || derived.Distinct ||
		len(derived.Fields) != 1 ||
		derived.Fields[0].Expression.Type != "AGGREGATE" ||
		derived.Fields[0].Expression.Function != "COUNT_DISTINCT" {
		t.Fatalf("unexpected DIM metric document: %#v", derived)
	}
}

func expressionPointer(expression dataset.Expression) *dataset.Expression {
	return &expression
}

func TestPortableMetricOutputCodePreservesPostgresIdentifierBoundary(t *testing.T) {
	code := "metric_dws_operations_general_multi_fact_summary_op_052de03bde0b"
	if len(code) != 64 {
		t.Fatalf("fixture length=%d", len(code))
	}
	output := portableMetricOutputCode(code)
	if len(output) != 63 || output == code ||
		output != portableMetricOutputCode(code) {
		t.Fatalf("portable output code=%q length=%d", output, len(output))
	}
}
