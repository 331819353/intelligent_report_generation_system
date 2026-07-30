package metriccandidate

import (
	"testing"

	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/metric"
)

func TestBuildCandidateUsesExactDWSCalculationContract(t *testing.T) {
	document := dataset.Document{
		Dataset: dataset.Descriptor{
			Code: "dws_entity_count",
			Name: "实体数量 · 配送区域维度",
		},
		Fields: []dataset.Field{
			{
				ID: "field_city", Code: "city", Name: "城市", Role: "DIMENSION",
				CanonicalType: "STRING",
				Expression:    dataset.Expression{Type: "FIELD_REF", NodeID: "dimension", Field: "city"},
			},
			{
				ID: "field_entity_count", Code: "entity_count", Name: "实体数量",
				Description: "按实体键统计的实体数量", Role: "MEASURE", CanonicalType: "INTEGER",
				Expression: dataset.Expression{
					Type: "AGGREGATE", Function: "COUNT_DISTINCT",
					Argument: &dataset.Expression{Type: "FIELD_REF", NodeID: "dimension", Field: "zone_id"},
				},
			},
		},
		AnalysisContract: &dataset.AnalysisContract{
			Measures: []dataset.AnalysisMeasureContract{{
				Field: "entity_count", Aggregation: "COUNT_DISTINCT",
				Additivity: "NON_ADDITIVE", ValueBehavior: "NON_ADDITIVE",
				TimeAggregation: "NONE",
			}},
		},
	}
	rules := deriveCandidateRules(document, false)
	if len(rules) != 1 {
		t.Fatalf("expected one governed metric rule, got %d", len(rules))
	}
	candidate, err := buildCandidate(
		dataset.VersionRecord{
			ID: "11111111-1111-4111-8111-111111111111", DatasetID: "22222222-2222-4222-8222-222222222222",
			DSLHash: "dsl-hash",
		},
		document,
		rules[0],
		[]metric.Dimension{{
			FieldID: "field_city", Name: "城市", HierarchyFieldIDs: []string{},
			SortDirection: "ASC", NullLabel: "未分类",
		}},
		"", "NONE", nil, nil,
	)
	if err != nil {
		t.Fatalf("build candidate: %v", err)
	}
	if candidate.Status != CandidateStatusReady {
		t.Fatalf("expected READY candidate, got %s: %v", candidate.Status, candidate.Warnings)
	}
	definition := candidate.Definition
	if definition.Aggregation != "NONE" {
		t.Fatalf("query layer must not aggregate a DWS aggregate output, got %s", definition.Aggregation)
	}
	if definition.SourceCalculation == nil {
		t.Fatal("expected exact source calculation")
	}
	if got := definition.SourceCalculation.Aggregation; got != "COUNT_DISTINCT" {
		t.Fatalf("expected COUNT_DISTINCT source aggregation, got %s", got)
	}
	if got := definition.SourceCalculation.Formula; got != "COUNT_DISTINCT(dimension.zone_id)" {
		t.Fatalf("unexpected source formula %q", got)
	}
	if definition.Additivity != "NON_ADDITIVE" {
		t.Fatalf("expected contract additivity, got %s", definition.Additivity)
	}
	if definition.DecimalScale != 0 || definition.NumberFormat != "#,##0" {
		t.Fatalf("unexpected integer presentation: %s / %d", definition.NumberFormat, definition.DecimalScale)
	}
}

func TestBuildCandidatePreservesSemiAdditiveTimeContract(t *testing.T) {
	document := dataset.Document{
		Dataset: dataset.Descriptor{Code: "dws_daily_balance", Name: "账户日余额"},
		Fields: []dataset.Field{
			{
				ID: "field_stat_date", Code: "stat_date", Name: "统计日期", Role: "TIME",
				CanonicalType: "DATE",
				Expression:    dataset.Expression{Type: "FIELD_REF", NodeID: "fact", Field: "stat_date"},
			},
			{
				ID: "field_balance", Code: "balance", Name: "账户余额", Role: "MEASURE",
				CanonicalType: "INTEGER",
				Expression: dataset.Expression{
					Type: "AGGREGATE", Function: "SUM",
					Argument: &dataset.Expression{Type: "FIELD_REF", NodeID: "fact", Field: "balance"},
				},
			},
		},
		OutputGrain: dataset.OutputGrain{TimeField: "stat_date", DefaultTimeGrain: "DAY"},
		AnalysisContract: &dataset.AnalysisContract{
			TimeField: "stat_date", TimeGrain: "DAY",
			Measures: []dataset.AnalysisMeasureContract{{
				Field: "balance", Aggregation: "SUM", Additivity: "SEMI_ADDITIVE",
				ValueBehavior: "POINT_IN_TIME", TimeAggregation: "LAST", Unit: "元",
			}},
		},
	}
	dimensions := extractDimensions(document.Fields)
	timeFieldID, timeGrain, warnings := extractTimeSemantics(document, dimensions)
	candidate, err := buildCandidate(
		dataset.VersionRecord{
			ID: "33333333-3333-4333-8333-333333333333", DatasetID: "44444444-4444-4444-8444-444444444444",
			DSLHash: "dsl-hash",
		},
		document, deriveCandidateRules(document, false)[0], dimensions,
		timeFieldID, timeGrain, nil, warnings,
	)
	if err != nil {
		t.Fatalf("build candidate: %v", err)
	}
	if candidate.Definition.Additivity != "SEMI_ADDITIVE" {
		t.Fatalf("expected SEMI_ADDITIVE, got %s", candidate.Definition.Additivity)
	}
	if len(candidate.Definition.NonAdditiveDimensionFieldIDs) != 1 ||
		candidate.Definition.NonAdditiveDimensionFieldIDs[0] != "field_stat_date" {
		t.Fatalf("unexpected non-additive dimensions: %v", candidate.Definition.NonAdditiveDimensionFieldIDs)
	}
	if candidate.Definition.SourceCalculation == nil ||
		candidate.Definition.SourceCalculation.TimeAggregation != "LAST" {
		t.Fatalf("expected LAST source time aggregation: %+v", candidate.Definition.SourceCalculation)
	}
}

func TestContractMismatchAndUnknownDecimalAreNotReady(t *testing.T) {
	document := dataset.Document{
		Dataset: dataset.Descriptor{Code: "dws_ratio", Name: "比例分析"},
		Fields: []dataset.Field{{
			ID: "field_rate", Code: "rate", Name: "完成率", Role: "MEASURE",
			CanonicalType: "DECIMAL",
			Expression: dataset.Expression{
				Type: "AGGREGATE", Function: "AVG",
				Argument: &dataset.Expression{Type: "FIELD_REF", NodeID: "fact", Field: "rate"},
			},
		}},
		AnalysisContract: &dataset.AnalysisContract{
			Measures: []dataset.AnalysisMeasureContract{{
				Field: "rate", Aggregation: "SUM", Additivity: "ADDITIVE",
				ValueBehavior: "FLOW", TimeAggregation: "SUM",
			}},
		},
	}
	rule := deriveCandidateRules(document, false)[0]
	candidate, err := buildCandidate(
		dataset.VersionRecord{
			ID: "55555555-5555-4555-8555-555555555555", DatasetID: "66666666-6666-4666-8666-666666666666",
			DSLHash: "dsl-hash",
		},
		document, rule, nil, "", "NONE", nil, nil,
	)
	if err != nil {
		t.Fatalf("build candidate: %v", err)
	}
	if candidate.Status == CandidateStatusReady {
		t.Fatalf("conflicting aggregation and unknown decimal scale must not be READY: %+v", candidate)
	}
}
