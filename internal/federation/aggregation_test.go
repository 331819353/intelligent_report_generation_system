package federation

import (
	"testing"

	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/datasource"
	"intelligent-report-generation-system/internal/policy"
)

func TestPlanSourceAggregationsPushesOnlySafeManySide(t *testing.T) {
	document := crossDocument()
	planned := planSourceAggregations(document, crossPlan(datasource.TypeOracle, datasource.TypeMySQL), nil)
	if len(planned.Projections) != 1 || len(planned.Projections["orders"]) != 1 {
		t.Fatalf("planned aggregations=%#v", planned)
	}
	for alias, projection := range planned.Projections["orders"] {
		if alias != "partial_sum_1" || projection.SourceField != "amount" || projection.Function != "SUM" {
			t.Fatalf("aggregate projection=%s/%#v", alias, projection)
		}
	}
	if len(planned.Projections["customers"]) != 0 {
		t.Fatalf("one-side node was aggregated: %#v", planned)
	}
	if planned.Document.Fields[1].Expression.Argument == nil || planned.Document.Fields[1].Expression.Argument.Field != "partial_sum_1" {
		t.Fatalf("rewritten SUM=%#v", planned.Document.Fields[1].Expression)
	}
}

func TestPlanSourceAggregationsBuildsCountAndAverageStates(t *testing.T) {
	document := crossDocument()
	document.Fields = append(document.Fields, dataset.Field{
		ID: "field_rows", Code: "row_count", Name: "行数", Role: "MEASURE", CanonicalType: "INTEGER",
		Expression: dataset.Expression{Type: "AGGREGATE", Function: "COUNT", Argument: &dataset.Expression{Type: "FIELD_REF", NodeID: "orders", Field: "amount"}},
	}, dataset.Field{
		ID: "field_average", Code: "average_amount", Name: "平均金额", Role: "MEASURE", CanonicalType: "DECIMAL",
		Expression: dataset.Expression{Type: "AGGREGATE", Function: "AVG", Argument: &dataset.Expression{Type: "FIELD_REF", NodeID: "orders", Field: "amount"}},
	})
	planned := planSourceAggregations(document, crossPlan(datasource.TypeOracle, datasource.TypeMySQL), nil)
	functions := map[string]int{}
	for _, projection := range planned.Projections["orders"] {
		functions[projection.Function]++
	}
	if functions["SUM"] != 1 || functions["COUNT"] != 1 || len(planned.Projections["orders"]) != 2 {
		t.Fatalf("partial states=%#v", planned.Projections)
	}
	if planned.Document.Fields[2].Expression.Type != "CAST" || planned.Document.Fields[3].Expression.Type != "CASE" {
		t.Fatalf("rewritten count/avg=%#v / %#v", planned.Document.Fields[2].Expression, planned.Document.Fields[3].Expression)
	}
}

func TestPlanSourceAggregationsFallsBackForUnsafeRowCountSemantics(t *testing.T) {
	document := crossDocument()
	document.Fields = append(document.Fields, dataset.Field{
		ID: "field_rows", Code: "row_count", Name: "行数", Role: "MEASURE", CanonicalType: "INTEGER",
		Expression: dataset.Expression{Type: "AGGREGATE", Function: "COUNT"},
	})
	if planned := planSourceAggregations(document, crossPlan(datasource.TypeOracle, datasource.TypeMySQL), nil); len(planned.Projections) != 0 {
		t.Fatalf("COUNT(*) query should not be pre-aggregated: %#v", planned)
	}
	policies := []policy.ColumnPolicy{{FieldCode: "revenue", PolicyType: "AGGREGATE_ONLY", AllowedAggregations: []string{"SUM"}, MinimumGroupSize: 3}}
	if planned := planSourceAggregations(crossDocument(), crossPlan(datasource.TypeOracle, datasource.TypeMySQL), policies); len(planned.Projections) != 0 {
		t.Fatalf("minimum group size query should not be pre-aggregated: %#v", planned)
	}
	document = crossDocument()
	document.Fields = append(document.Fields, dataset.Field{
		ID: "field_average", Code: "average_amount", Name: "平均金额", Role: "MEASURE", CanonicalType: "DECIMAL",
		Expression: dataset.Expression{Type: "AGGREGATE", Function: "AVG", Argument: &dataset.Expression{Type: "FIELD_REF", NodeID: "orders", Field: "amount"}},
	})
	policies = []policy.ColumnPolicy{{FieldCode: "average_amount", PolicyType: "AGGREGATE_ONLY", AllowedAggregations: []string{"AVG"}}}
	if planned := planSourceAggregations(document, crossPlan(datasource.TypeOracle, datasource.TypeMySQL), policies); len(planned.Projections) != 0 {
		t.Fatalf("direct AVG policy query should not be rewritten: %#v", planned)
	}
	cast := dataset.Expression{Type: "CAST", TargetType: "DECIMAL", Argument: &dataset.Expression{Type: "FIELD_REF", NodeID: "orders", Field: "amount"}}
	document.Fields[len(document.Fields)-1].Expression.Argument = &cast
	if planned := planSourceAggregations(document, crossPlan(datasource.TypeOracle, datasource.TypeMySQL), nil); len(planned.Projections) != 0 {
		t.Fatalf("complex AVG query should not be pre-aggregated: %#v", planned)
	}
}

func TestPlanSourceAggregationsFallsBackWhenMeasureIsReused(t *testing.T) {
	document := crossDocument()
	document.Filters = []dataset.Filter{{
		ID: "cross_amount", Stage: "PRE_AGGREGATION",
		Expression: dataset.Expression{Type: "GT", Left: &dataset.Expression{Type: "FIELD_REF", NodeID: "orders", Field: "amount"}, Right: &dataset.Expression{Type: "LITERAL", Value: 10}},
	}}
	if planned := planSourceAggregations(document, crossPlan(datasource.TypeOracle, datasource.TypeMySQL), nil); len(planned.Projections) != 0 {
		t.Fatalf("reused measure should not be pre-aggregated: %#v", planned)
	}
	document = crossDocument()
	document.Joins[0].Cardinality = "ONE_TO_ONE"
	if planned := planSourceAggregations(document, crossPlan(datasource.TypeOracle, datasource.TypeMySQL), nil); len(planned.Projections) != 0 {
		t.Fatalf("one-side node should not be pre-aggregated: %#v", planned)
	}
}

func TestPlanSourceAggregationsSkipsFileNodes(t *testing.T) {
	if planned := planSourceAggregations(crossDocument(), crossPlan(datasource.TypeExcel, datasource.TypeMySQL), nil); len(planned.Projections) != 0 {
		t.Fatalf("file node should not receive SQL aggregation: %#v", planned)
	}
}

func TestProjectionPruningDoesNotMutateInputDocument(t *testing.T) {
	document := crossDocument()
	pruned := pruneNodeProjections(document)
	if len(document.Nodes[0].Projection) != 3 || len(pruned.Nodes[0].Projection) != 2 {
		t.Fatalf("original=%#v pruned=%#v", document.Nodes[0].Projection, pruned.Nodes[0].Projection)
	}
}

func TestPlanSourceAggregationsAvoidsProjectionAliasCollision(t *testing.T) {
	document := crossDocument()
	document.Nodes[0].Projection = append(document.Nodes[0].Projection, "partial_sum_1")
	planned := planSourceAggregations(document, crossPlan(datasource.TypeOracle, datasource.TypeMySQL), nil)
	if len(planned.Projections["orders"]) != 1 || planned.Projections["orders"]["partial_sum_2"].Function != "SUM" {
		t.Fatalf("collision-safe projections=%#v", planned.Projections)
	}
}
