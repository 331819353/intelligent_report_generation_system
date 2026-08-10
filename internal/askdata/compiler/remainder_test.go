package compiler

import (
	"strings"
	"testing"

	"intelligent-report-generation-system/internal/askdata/ir"
	"intelligent-report-generation-system/internal/askdata/registry"
)

func TestCompileOtherReusesAggregationPlannerForEveryAdditivity(t *testing.T) {
	query := remainderQueryFixture()
	compiledLimit := remainderLimitFixture(t, query)
	compiled, err := CompileOther(OtherCompileRequest{
		Query: query, Limit: compiledLimit, GroupColumns: []string{"region"},
		Metrics: remainderMetricsFixture(), RecomputedRemainderRelation: "recomputed_remainder",
	})
	if err != nil {
		t.Fatal(err)
	}
	strategies := map[string]RemainderStrategy{}
	for _, plan := range compiled.MetricPlans {
		strategies[string(plan.MetricVersionID)] = plan.Strategy
	}
	if len(compiled.MetricPlans) != 3 ||
		strategies["metric-sales-v1"] != RemainderTotalMinusTop ||
		strategies["metric-inventory-v1"] != RemainderRecompute ||
		strategies["metric-margin-v1"] != RemainderRecompute {
		t.Fatalf("unexpected strategies: %#v", compiled.MetricPlans)
	}
	if !strings.Contains(compiled.SQL, `SUM("all_rows"."sales")`) ||
		!strings.Contains(compiled.SQL, `SUM("top_rows"."sales")`) ||
		strings.Contains(compiled.SQL, `SUM("all_rows"."inventory")`) ||
		strings.Contains(compiled.SQL, `SUM("all_rows"."margin")`) ||
		!strings.Contains(compiled.SQL, `"recomputed_remainder" AS "recomputed"`) ||
		!strings.Contains(compiled.SQL, `TRUE AS "is_remainder"`) ||
		!strings.Contains(compiled.SQL, `"remainder_member_count"`) {
		t.Fatalf("unexpected remainder SQL:\n%s", compiled.SQL)
	}
}

func remainderQueryFixture() ir.SemanticIR {
	query := topNQueryFixture()
	query.Metrics = []ir.Metric{
		{MetricVersionID: "metric-sales-v1", Alias: "sales"},
		{MetricVersionID: "metric-inventory-v1", Alias: "inventory"},
		{MetricVersionID: "metric-margin-v1", Alias: "margin"},
	}
	query.TimeRange = &ir.TimeRange{
		DimensionVersionID: "dimension-date-v1", Start: "2026-01-01", EndExclusive: "2027-01-01",
		Timezone: "Asia/Shanghai",
	}
	query.OtherPolicy = ir.OtherAggregateRemainder
	return query
}

func remainderLimitFixture(t *testing.T, query ir.SemanticIR) CompiledLimit {
	t.Helper()
	sort := mustCompileSort(t, query)
	limit := 2
	compiled, err := CompileLimit(LimitCompileRequest{
		SourceRelation: "rank_input", OutputColumns: []string{"region", "sales", "inventory", "margin"},
		Sort: sort, Limit: &limit,
	})
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

func remainderMetricsFixture() []RemainderMetric {
	return []RemainderMetric{
		{Contract: MetricContract{
			MetricVersionID: "metric-sales-v1", Additivity: registry.FullyAdditive,
		}, OutputColumn: "sales"},
		{Contract: MetricContract{
			MetricVersionID: "metric-inventory-v1", Additivity: registry.SemiAdditive,
			SemiAdditiveTimeAggregation: registry.SemiAdditivePeriodEnd,
		}, OutputColumn: "inventory", RecomputedColumn: "inventory"},
		{Contract: MetricContract{
			MetricVersionID: "metric-margin-v1", Additivity: registry.NonAdditive,
			AggregationRestriction: registry.PostAggregate,
		}, OutputColumn: "margin", RecomputedColumn: "margin"},
	}
}
