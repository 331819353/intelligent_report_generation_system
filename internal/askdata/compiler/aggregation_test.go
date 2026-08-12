package compiler

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/ir"
	"intelligent-report-generation-system/internal/askdata/registry"
)

func TestPlanMetricReturnsStableAdditivityErrors(t *testing.T) {
	query := ir.SemanticIR{TimeRange: &ir.TimeRange{DimensionVersionID: "dimension-date-v1"}}
	tests := []struct {
		name   string
		metric MetricContract
		code   string
	}{
		{name: "missing", metric: MetricContract{MetricVersionID: "metric-a", Unit: "COUNT"}, code: AdditivityMissingCode},
		{name: "semi declaration", metric: MetricContract{
			MetricVersionID: "metric-a", Unit: "COUNT", Additivity: registry.SemiAdditive,
		}, code: SemiAdditiveTimeAggregationMissingCode},
		{name: "post aggregate", metric: MetricContract{
			MetricVersionID: "metric-a", Unit: "PERCENT", Additivity: registry.NonAdditive,
		}, code: NonAdditiveSumAttemptCode},
		{name: "collapsed dimension", metric: MetricContract{
			MetricVersionID: "metric-a", Unit: "COUNT", Additivity: registry.NonAdditive,
			AggregationRestriction: registry.PostAggregate,
			NonAdditiveDimensions:  []string{"dimension-region-v1"},
		}, code: NonAdditiveDimensionCollapsedCode},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := PlanMetric(test.metric, query)
			var failure *AggregationPlanError
			if !errors.As(err, &failure) || failure.Code != test.code || !errors.Is(err, ErrInvalidAggregationPlan) {
				t.Fatalf("error = %#v", err)
			}
		})
	}
}

func TestCheckUnitCompatibilityRejectsMixedUnitAndCurrency(t *testing.T) {
	tests := [][]MetricContract{
		{
			{MetricVersionID: "metric-a", Unit: "COUNT", Currency: ""},
			{MetricVersionID: "metric-b", Unit: "PERCENT", Currency: ""},
		},
		{
			{MetricVersionID: "metric-a", Unit: "CURRENCY", Currency: "CNY"},
			{MetricVersionID: "metric-b", Unit: "CURRENCY", Currency: "USD"},
		},
	}
	for _, metrics := range tests {
		var failure *AggregationPlanError
		if err := CheckUnitCompatibility(metrics); !errors.As(err, &failure) || failure.Code != IncompatibleUnitCode {
			t.Fatalf("unit error = %#v", err)
		}
	}
}

func TestNonAdditiveRatioAlwaysAggregatesOperandsBeforeDivision(t *testing.T) {
	for mask := 0; mask < 8; mask++ {
		semanticIR, resolution := ratioAggregationFixture(t)
		semanticIR.GroupBy = aggregationGroups(mask)
		document, _, _, _, err := buildQueryDocument(semanticIR, resolution)
		if err != nil {
			t.Fatalf("mask %d: %v", mask, err)
		}
		expression := document.Fields[len(document.Fields)-1].Expression
		if expression.Type != "DIVIDE" || len(expression.Arguments) != 2 ||
			expression.Arguments[0].Type != "AGGREGATE" || expression.Arguments[0].Function != "SUM" ||
			expression.Arguments[1].Type != "NULLIF" || len(expression.Arguments[1].Arguments) != 2 ||
			expression.Arguments[1].Arguments[0].Type != "AGGREGATE" {
			t.Fatalf("mask %d unsafe metric expression: %#v", mask, expression)
		}
		compiled := compileAggregationDocument(t, semanticIR, resolution)
		if !strings.Contains(compiled, "SUM(") || !strings.Contains(compiled, "/ NULLIF(SUM(") ||
			strings.Contains(compiled, "AVG(") {
			t.Fatalf("mask %d unsafe SQL: %s", mask, compiled)
		}
	}
}

func TestCountDistinctIsRecomputedAtRequestedGrain(t *testing.T) {
	semanticIR, resolution := baseAggregationFixture(t)
	semanticIR.GroupBy = aggregationGroups(2)
	resolution.Metrics = []MetricContract{{
		MetricVersionID: "metric-customers-v1", ModelVersionID: "model-sales-v1",
		FormulaAST:       json.RawMessage(`{"measureVersionId":"measure-customers-v1","type":"MEASURE_REF"}`),
		DefaultFilterAST: json.RawMessage(`{"type":"TRUE"}`), Unit: "COUNT",
		Additivity: registry.NonAdditive, AggregationRestriction: registry.PostAggregate,
		ZeroDenominatorPolicy: registry.ZeroDenominatorNull, NullPolicy: "PRESERVE",
		Measures: []MeasureContract{{
			MeasureID: "measure-customers", MeasureVersionID: "measure-customers-v1",
			FormulaAST:  json.RawMessage(`{"fieldId":"customer_id","type":"FIELD_REF"}`),
			Aggregation: registry.AggregationCountDistinct, Additivity: registry.NonAdditive,
			AggregationRestriction: registry.PostAggregate, DataType: registry.NumericInteger,
			Unit: "COUNT", ZeroDenominatorPolicy: registry.ZeroDenominatorNull,
		}},
	}}
	semanticIR.Metrics = []ir.Metric{{MetricVersionID: "metric-customers-v1", Alias: "customers"}}
	sql := compileAggregationDocument(t, semanticIR, resolution)
	if !strings.Contains(sql, `COUNT(DISTINCT "semantic_model"."customer_id")`) ||
		strings.Contains(sql, "SUM(COUNT") {
		t.Fatalf("distinct metric was not recomputed: %s", sql)
	}
}

func TestSemiAdditiveStrategiesUseTwoStageTimeReduction(t *testing.T) {
	tests := []struct {
		name        string
		strategy    registry.SemiAdditiveTimeAggregation
		grain       ir.TimeGrain
		groupByTime bool
		want        string
	}{
		{name: "period end by month", strategy: registry.SemiAdditivePeriodEnd, grain: ir.TimeGrainMonth, groupByTime: true, want: " DESC NULLS LAST))[1]"},
		{name: "period begin by quarter", strategy: registry.SemiAdditivePeriodBegin, grain: ir.TimeGrainQuarter, groupByTime: true, want: " ASC NULLS LAST))[1]"},
		{name: "period average across years", strategy: registry.SemiAdditivePeriodAverage, want: "AVG(\"semantic_model\".\"am_"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			semanticIR, resolution := semiAdditiveFixture(t, test.strategy, test.grain)
			if !test.groupByTime {
				semanticIR.GroupBy = []ir.GroupBy{{DimensionVersionID: "dimension-region-v1"}}
				semanticIR.TimeRange.Start = "2024-01-01"
				semanticIR.TimeRange.EndExclusive = "2026-01-01"
			}
			document, _, _, _, err := buildQueryDocument(semanticIR, resolution)
			if err != nil {
				t.Fatal(err)
			}
			if len(document.PreAggregations) != 1 || len(document.Filters) != 0 ||
				len(document.Nodes[0].SourceFilters) != 2 {
				t.Fatalf("semi-additive stages are incomplete: %#v", document)
			}
			sql := compileAggregationDocument(t, semanticIR, resolution)
			if !strings.Contains(sql, "FROM (SELECT") || !strings.Contains(sql, test.want) {
				t.Fatalf("semi-additive SQL is unsafe: %s", sql)
			}
			if test.groupByTime && !strings.Contains(sql, `DATE_TRUNC('`+strings.ToLower(string(test.grain))+`'`) {
				t.Fatalf("semi-additive time bucket is missing: %s", sql)
			}
			if !test.groupByTime && strings.Contains(sql, "DATE_TRUNC(") {
				t.Fatalf("cross-time total unexpectedly retained a time bucket: %s", sql)
			}
		})
	}
}

func TestZeroDenominatorPolicyUsesNullifAndOptionalZero(t *testing.T) {
	semanticIR, resolution := ratioAggregationFixture(t)
	resolution.Metrics[0].NullPolicy = "ZERO"
	document, _, _, _, err := buildQueryDocument(semanticIR, resolution)
	if err != nil {
		t.Fatal(err)
	}
	if expression := document.Fields[len(document.Fields)-1].Expression; expression.Type != "DIVIDE" ||
		expression.Arguments[1].Type != "NULLIF" {
		t.Fatalf("NULL zero-denominator policy was overridden by null policy: %#v", expression)
	}

	resolution.Metrics[0].ZeroDenominatorPolicy = registry.ZeroDenominatorZero
	resolution.Metrics[0].NullPolicy = "PRESERVE"
	document, _, _, _, err = buildQueryDocument(semanticIR, resolution)
	if err != nil {
		t.Fatal(err)
	}
	expression := document.Fields[len(document.Fields)-1].Expression
	if expression.Type != "COALESCE" || expression.Arguments[0].Type != "DIVIDE" ||
		expression.Arguments[0].Arguments[1].Type != "NULLIF" {
		t.Fatalf("zero denominator policy = %#v", expression)
	}
	if sql := compileAggregationDocument(t, semanticIR, resolution); !strings.Contains(sql, "COALESCE(") || !strings.Contains(sql, "NULLIF(") {
		t.Fatalf("zero denominator SQL = %s", sql)
	}
}

func compileAggregationDocument(t *testing.T, semanticIR ir.SemanticIR, resolution Resolution) string {
	t.Helper()
	document, source, values, shapes, err := buildQueryDocument(semanticIR, resolution)
	if err != nil {
		t.Fatal(err)
	}
	placement, err := placeJoinedModels(resolution)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := compileQueryPlan(
		QueryRoleCurrent, document, source, placement.sources, shapes, values, semanticIR.Limit,
	)
	if err != nil {
		t.Fatal(err)
	}
	compiled, ok := plan.CompiledQuery()
	if !ok {
		t.Fatal("compiled query is unavailable")
	}
	return compiled.SQL
}

func ratioAggregationFixture(t *testing.T) (ir.SemanticIR, Resolution) {
	t.Helper()
	semanticIR, resolution := baseAggregationFixture(t)
	resolution.Metrics = []MetricContract{{
		MetricVersionID: "metric-margin-v1", ModelVersionID: "model-sales-v1",
		FormulaAST:       json.RawMessage(`{"arguments":[{"measureVersionId":"measure-profit-v1","type":"MEASURE_REF"},{"measureVersionId":"measure-revenue-v1","type":"MEASURE_REF"}],"type":"DIVIDE"}`),
		DefaultFilterAST: json.RawMessage(`{"type":"TRUE"}`), Unit: "PERCENT",
		Additivity: registry.NonAdditive, AggregationRestriction: registry.PostAggregate,
		ZeroDenominatorPolicy: registry.ZeroDenominatorNull, NullPolicy: "PRESERVE",
		Measures: []MeasureContract{
			{MeasureID: "measure-profit", MeasureVersionID: "measure-profit-v1", FormulaAST: json.RawMessage(`{"fieldId":"gross_profit","type":"FIELD_REF"}`), Aggregation: registry.AggregationSum, Additivity: registry.FullyAdditive, DataType: registry.NumericDecimal, Unit: "CNY", ZeroDenominatorPolicy: registry.ZeroDenominatorNull},
			{MeasureID: "measure-revenue", MeasureVersionID: "measure-revenue-v1", FormulaAST: json.RawMessage(`{"fieldId":"net_sales","type":"FIELD_REF"}`), Aggregation: registry.AggregationSum, Additivity: registry.FullyAdditive, DataType: registry.NumericDecimal, Unit: "CNY", ZeroDenominatorPolicy: registry.ZeroDenominatorNull},
		},
	}}
	semanticIR.Metrics = []ir.Metric{{MetricVersionID: "metric-margin-v1", Alias: "gross_margin_rate"}}
	return semanticIR, resolution
}

func semiAdditiveFixture(t *testing.T, strategy registry.SemiAdditiveTimeAggregation, grain ir.TimeGrain) (ir.SemanticIR, Resolution) {
	t.Helper()
	semanticIR, resolution := baseAggregationFixture(t)
	semanticIR.GroupBy = []ir.GroupBy{{DimensionVersionID: "dimension-date-v1", Grain: &grain}, {DimensionVersionID: "dimension-region-v1"}}
	resolution.Metrics = []MetricContract{{
		MetricVersionID: "metric-inventory-v1", ModelVersionID: "model-sales-v1",
		FormulaAST:       json.RawMessage(`{"measureVersionId":"measure-inventory-v1","type":"MEASURE_REF"}`),
		DefaultFilterAST: json.RawMessage(`{"type":"TRUE"}`), Unit: "COUNT",
		Additivity: registry.SemiAdditive, SemiAdditiveTimeAggregation: strategy,
		ZeroDenominatorPolicy: registry.ZeroDenominatorNull, NullPolicy: "PRESERVE",
		Measures: []MeasureContract{{
			MeasureID: "measure-inventory", MeasureVersionID: "measure-inventory-v1",
			FormulaAST:  json.RawMessage(`{"fieldId":"inventory_qty","type":"FIELD_REF"}`),
			Aggregation: registry.AggregationSum, Additivity: registry.SemiAdditive,
			SemiAdditiveTimeAggregation: strategy, DataType: registry.NumericDecimal,
			Unit: "COUNT", ZeroDenominatorPolicy: registry.ZeroDenominatorNull,
		}},
	}}
	semanticIR.Metrics = []ir.Metric{{MetricVersionID: "metric-inventory-v1", Alias: "inventory_qty"}}
	return semanticIR, resolution
}

func baseAggregationFixture(t *testing.T) (ir.SemanticIR, Resolution) {
	t.Helper()
	dateField := resolvedField(t, "order_date", "order_date", "TIME", "DATE")
	regionField := resolvedField(t, "region_code", "region_code", "DIMENSION", "STRING")
	productField := resolvedField(t, "product_code", "product_code", "DIMENSION", "STRING")
	fields := []FieldContract{
		dateField, regionField, productField,
		resolvedField(t, "net_sales", "net_sales", "MEASURE", "DECIMAL"),
		resolvedField(t, "gross_profit", "gross_profit", "MEASURE", "DECIMAL"),
		resolvedField(t, "inventory_qty", "inventory_qty", "MEASURE", "DECIMAL"),
		resolvedField(t, "customer_id", "customer_id", "IDENTIFIER", "STRING"),
	}
	semanticIR := ir.SemanticIR{
		IRVersion: ir.Version, SemanticReleaseID: "release-additivity-v1",
		SemanticContentHash: hash("release-additivity-v1"), DomainID: "sales", ModelVersionID: "model-sales-v1",
		Metrics: []ir.Metric{{MetricVersionID: "metric-placeholder-v1", Alias: "metric_value"}},
		TimeRange: &ir.TimeRange{
			DimensionVersionID: "dimension-date-v1", Start: "2025-01-01",
			EndExclusive: "2026-01-01", Timezone: "Asia/Shanghai",
		},
		Limit: 1000,
	}
	primaryTime := askdata.ID("order_date")
	resolution := Resolution{
		TimeDimensionVersionID: idPointer("dimension-date-v1"),
		Model: ModelContract{
			ModelVersionID: "model-sales-v1", PrimaryTimeFieldID: &primaryTime, Fields: fields,
			Materialization: MaterializationContract{
				MaterializationID: "materialization-sales-v1", DatasetVersionID: "dataset-sales-v1",
				PublishedSchema: "warehouse_published", PublishedName: "dws_sales_orders",
			},
		},
		Dimensions: []DimensionContract{
			{DimensionVersionID: "dimension-date-v1", LogicalFieldID: "order_date", Kind: registry.DimensionTime},
			{DimensionVersionID: "dimension-region-v1", LogicalFieldID: "region_code", Kind: registry.DimensionCategorical},
			{DimensionVersionID: "dimension-product-v1", LogicalFieldID: "product_code", Kind: registry.DimensionCategorical},
		},
	}
	return semanticIR, resolution
}

func aggregationGroups(mask int) []ir.GroupBy {
	grain := ir.TimeGrainMonth
	values := []ir.GroupBy{
		{DimensionVersionID: "dimension-date-v1", Grain: &grain},
		{DimensionVersionID: "dimension-region-v1"},
		{DimensionVersionID: "dimension-product-v1"},
	}
	result := make([]ir.GroupBy, 0, len(values))
	for index, value := range values {
		if mask&(1<<index) != 0 {
			result = append(result, value)
		}
	}
	return result
}
