package compiler

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/ir"
	"intelligent-report-generation-system/internal/askdata/registry"
	"intelligent-report-generation-system/internal/platform/database"
)

func TestAdaptProducesReplaySafeGoldenCompiledQuery(t *testing.T) {
	request, buildArtifact, scope := resolverBuildFixture(t)
	resolver, err := NewResolver(&memoryContractStore{snapshot: metricOnlySnapshot(t, scope.Release)})
	if err != nil {
		t.Fatal(err)
	}
	ctx := database.WithAccessContext(context.Background(), string(scope.ActorID), "sales")
	resolution, err := resolver.Resolve(ctx, ResolveRequest{BuildRequest: request, BuildArtifact: buildArtifact})
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := Adapt(AdaptRequest{
		ResolveRequest: ResolveRequest{BuildRequest: request, BuildArtifact: buildArtifact},
		Resolution:     resolution,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(artifact.Plans) != 1 || artifact.PlanHash.Validate() != nil {
		t.Fatalf("unexpected query artifact: %#v", artifact)
	}
	compiled, ok := artifact.Plans[0].CompiledQuery()
	if !ok {
		t.Fatal("live adapter did not retain the in-process compiled query")
	}
	wantSQL := `SELECT "secure_base"."metric_e767c85023bc65e498835c26d9a550f11b82fb10617f5eb4e7f94778" AS "metric_e767c85023bc65e498835c26d9a550f11b82fb10617f5eb4e7f94778" FROM (SELECT SUM("semantic_model"."net_sales") AS "metric_e767c85023bc65e498835c26d9a550f11b82fb10617f5eb4e7f94778" FROM "warehouse_published"."dws_sales_orders" "semantic_model") "secure_base" LIMIT $1`
	if compiled.SQL != wantSQL || !reflect.DeepEqual(compiled.Args, []any{500}) ||
		compiled.PlanHash != string(artifact.Plans[0].CompiledPlanHash) {
		t.Fatalf("compiled query differs from golden:\nSQL: %s\nargs: %#v\nhash: %s", compiled.SQL, compiled.Args, compiled.PlanHash)
	}

	raw, err := json.Marshal(artifact)
	if err != nil {
		t.Fatal(err)
	}
	serialized := string(raw)
	if strings.Contains(serialized, compiled.SQL) || strings.Contains(serialized, `"args"`) ||
		strings.Contains(serialized, `"sql"`) {
		t.Fatalf("query artifact leaked executable SQL or args: %s", serialized)
	}
	var replayed QueryArtifact
	if err := askdata.DecodeStrictJSON(raw, &replayed); err != nil {
		t.Fatal(err)
	}
	if err := replayed.Validate(); err != nil {
		t.Fatalf("replayed public artifact failed validation: %v", err)
	}
	if _, ok := replayed.Plans[0].CompiledQuery(); ok {
		t.Fatal("serialized artifact unexpectedly retained executable parameter values")
	}

	tampered := replayed
	tampered.Plans[0].Source.PublishedName = "other_view"
	if !errors.Is(tampered.Validate(), ErrInvalidQueryPlan) {
		t.Fatal("tampered physical whitelist must fail closed")
	}
}

func TestComparisonBuildsCurrentAndBaselineWithoutPersistingValues(t *testing.T) {
	semanticIR, resolution := comparisonAdapterFixture(t)
	document, source, currentValues, shapes, err := buildQueryDocument(semanticIR, resolution)
	if err != nil {
		t.Fatal(err)
	}
	missingMemberValue := resolution
	missingMemberValue.memberParameterValues = nil
	if _, _, _, _, err := buildQueryDocument(semanticIR, missingMemberValue); !errors.Is(err, ErrInvalidAdaptRequest) {
		t.Fatalf("missing live member value error = %v", err)
	}
	if document.Fields[0].ID != "order_date" || document.Fields[0].Expression.Type != "CAST" ||
		document.Fields[1].ID != stableDatasetIdentifier("metric", "metric-sales-v1") ||
		len(document.Parameters) != 3 || document.ExecutionPolicy.ResultLimit != 10000 {
		t.Fatalf("generated document did not preserve stable fields/parameters: %#v", document)
	}
	current, err := compileQueryPlan(QueryRoleCurrent, document, source, shapes, currentValues, semanticIR.Limit)
	if err != nil {
		t.Fatal(err)
	}
	baselineValues, err := baselineParameterValues(semanticIR, resolution, currentValues)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := compileQueryPlan(QueryRoleBaseline, document, source, shapes, baselineValues, semanticIR.Limit)
	if err != nil {
		t.Fatal(err)
	}
	currentQuery, _ := current.CompiledQuery()
	baselineQuery, _ := baseline.CompiledQuery()
	if currentQuery.SQL != baselineQuery.SQL || currentQuery.PlanHash != baselineQuery.PlanHash {
		t.Fatal("comparison query shape changed with parameter values")
	}
	if !reflect.DeepEqual(currentQuery.Args, []any{"PAID", nil, "EAST", "2026-01-01", "2027-01-01", 10000}) {
		t.Fatalf("unexpected current args: %#v", currentQuery.Args)
	}
	if !reflect.DeepEqual(baselineQuery.Args, []any{"PAID", nil, "EAST", "2025-01-01", "2026-01-01", 10000}) {
		t.Fatalf("unexpected baseline args: %#v", baselineQuery.Args)
	}
	if !strings.Contains(currentQuery.SQL, `DATE_TRUNC('month', "semantic_model"."order_date")`) ||
		!strings.Contains(currentQuery.SQL, `CASE WHEN ("semantic_model"."order_status" = $1) THEN "semantic_model"."net_sales" ELSE $2 END`) {
		t.Fatalf("compiled query lost time grain or metric default filter: %s", currentQuery.SQL)
	}
	raw, err := json.Marshal([]QueryPlan{current, baseline})
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"EAST", "2026-01-01", "2025-01-01", currentQuery.SQL} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("serialized plans leaked runtime value %q: %s", secret, raw)
		}
	}
}

func TestSemanticASTFailsClosedOnUnknownReferencesAndShape(t *testing.T) {
	_, resolution := comparisonAdapterFixture(t)
	fields := map[askdata.ID]FieldContract{}
	for _, field := range resolution.Model.Fields {
		fields[field.FieldID] = field
	}
	metric := resolution.Metrics[0]
	metric.FormulaAST = json.RawMessage(`{"arguments":[{"measureVersionId":"measure-sales-v1","type":"MEASURE_REF"},{"type":"LITERAL","value":2}],"sql":"DROP TABLE x","type":"DIVIDE"}`)
	if _, _, err := compileMetricExpression(metric, fields); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unsafe semantic AST error = %v", err)
	}
	metric = resolution.Metrics[0]
	metric.FormulaAST = json.RawMessage(`{"measureVersionId":"measure-other-v1","type":"MEASURE_REF"}`)
	if _, _, err := compileMetricExpression(metric, fields); err == nil || !strings.Contains(err.Error(), "not declared") {
		t.Fatalf("unknown measure error = %v", err)
	}
}

func TestComparisonRangeClampsCalendarBoundaries(t *testing.T) {
	shifted, err := shiftComparisonRange(ir.TimeRange{
		DimensionVersionID: "dimension-order-date-v1", Start: "2024-02-29",
		EndExclusive: "2024-04-01", Timezone: "Asia/Shanghai",
	}, ir.Comparison{Type: ir.ComparisonYearOverYear, Periods: 1})
	if err != nil {
		t.Fatal(err)
	}
	if shifted.Start != "2023-02-28" || shifted.EndExclusive != "2023-04-01" {
		t.Fatalf("unexpected clamped comparison range: %#v", shifted)
	}
}

func comparisonAdapterFixture(t *testing.T) (ir.SemanticIR, Resolution) {
	t.Helper()
	month := ir.TimeGrainMonth
	semanticIR := ir.SemanticIR{
		IRVersion: ir.Version, SemanticReleaseID: "release-query-v1",
		SemanticContentHash: hash("release-query-v1"), ModelVersionID: "model-sales-v1",
		Metrics: []ir.Metric{{MetricVersionID: "metric-sales-v1", Alias: "net_sales"}},
		GroupBy: []ir.GroupBy{{DimensionVersionID: "dimension-order-date-v1", Grain: &month}},
		Filters: []ir.Filter{{
			DimensionVersionID: "dimension-region-v1", Operator: ir.FilterEquals,
			MemberVersionIDs: []askdata.ID{"member-east-v1"},
		}},
		TimeRange: &ir.TimeRange{
			DimensionVersionID: "dimension-order-date-v1", Start: "2026-01-01",
			EndExclusive: "2027-01-01", Timezone: "Asia/Shanghai",
		},
		Comparison: &ir.Comparison{Type: ir.ComparisonYearOverYear, Periods: 1},
		Sort: []ir.Sort{{
			TargetType: ir.SortTargetMetric, TargetVersionID: "metric-sales-v1",
			Direction: ir.SortDescending, Nulls: ir.NullsLast,
		}},
		Limit: 10000,
	}
	if err := semanticIR.Validate(); err != nil {
		t.Fatal(err)
	}
	fields := []FieldContract{
		resolvedField(t, "net_sales", "net_sales", "MEASURE", "DECIMAL"),
		resolvedField(t, "order_date", "order_date", "TIME", "DATE"),
		resolvedField(t, "order_status", "order_status", "DIMENSION", "STRING"),
		resolvedField(t, "region_code", "region_code", "DIMENSION", "STRING"),
	}
	resolution := Resolution{
		TimeDimensionVersionID: idPointer("dimension-order-date-v1"),
		Model: ModelContract{
			ModelVersionID: "model-sales-v1", Fields: fields,
			Materialization: MaterializationContract{
				MaterializationID: "materialization-sales-v1", DatasetVersionID: "dataset-sales-v1",
				PublishedSchema: "warehouse_published", PublishedName: "dws_sales_orders",
			},
		},
		Metrics: []MetricContract{{
			MetricVersionID: "metric-sales-v1", FormulaAST: json.RawMessage(`{"measureId":"measure-sales","type":"MEASURE_REF"}`),
			DefaultFilterAST: json.RawMessage(`{"left":{"fieldId":"order_status","type":"FIELD_REF"},"right":{"type":"LITERAL","value":"PAID"},"type":"EQUALS"}`),
			NullPolicy:       "PRESERVE", Measures: []MeasureContract{{
				MeasureID: "measure-sales", MeasureVersionID: "measure-sales-v1", FormulaAST: json.RawMessage(`{"fieldId":"net_sales","type":"FIELD_REF"}`),
				Aggregation: registry.AggregationSum, DataType: registry.NumericDecimal,
			}},
		}},
		Dimensions: []DimensionContract{
			{DimensionVersionID: "dimension-order-date-v1", LogicalFieldID: "order_date", Kind: registry.DimensionTime},
			{DimensionVersionID: "dimension-region-v1", LogicalFieldID: "region_code", Kind: registry.DimensionCategorical},
		},
		memberParameterValues: map[askdata.ID]string{"member-east-v1": "EAST"},
	}
	return semanticIR, resolution
}
