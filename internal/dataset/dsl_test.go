package dataset

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestPrepareIsDeterministic(t *testing.T) {
	raw := readExample(t)
	first, err := Prepare(raw)
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	second, err := Prepare(first.DSLJSON)
	if err != nil {
		t.Fatalf("Prepare(normalized) error = %v", err)
	}
	if first.DSLHash != second.DSLHash || first.PlanHash != second.PlanHash {
		t.Fatalf("hashes are not stable: dsl %s/%s plan %s/%s", first.DSLHash, second.DSLHash, first.PlanHash, second.PlanHash)
	}
	if string(first.DSLJSON) != string(second.DSLJSON) || string(first.LogicalPlanJSON) != string(second.LogicalPlanJSON) {
		t.Fatal("normalized DSL or logical plan changed after a second preparation")
	}
	wantKinds := []string{"SCAN", "FILTER", "AGGREGATE", "SORT"}
	var gotKinds []string
	for _, step := range first.LogicalPlan.Steps {
		gotKinds = append(gotKinds, step.Kind)
	}
	if !reflect.DeepEqual(gotKinds, wantKinds) {
		t.Fatalf("plan kinds = %v, want %v", gotKinds, wantKinds)
	}
}

func TestDecodeAndNormalizeMigratesLegacyGrain(t *testing.T) {
	var input map[string]any
	if err := json.Unmarshal(readExample(t), &input); err != nil {
		t.Fatal(err)
	}
	input["dslVersion"] = "0.9"
	datasetObject := input["dataset"].(map[string]any)
	datasetObject["grain"] = input["outputGrain"]
	delete(input, "outputGrain")
	raw, _ := json.Marshal(input)

	document, err := DecodeAndNormalize(raw)
	if err != nil {
		t.Fatalf("DecodeAndNormalize() error = %v", err)
	}
	if document.DSLVersion != DSLVersion || document.Dataset.Grain != nil {
		t.Fatalf("legacy document was not migrated: %#v", document.Dataset)
	}
	if document.OutputGrain.Description == "" || len(document.OutputGrain.KeyFields) != 1 {
		t.Fatalf("output grain was lost: %#v", document.OutputGrain)
	}
}

func TestPrepareRejectsUnknownField(t *testing.T) {
	var input map[string]any
	if err := json.Unmarshal(readExample(t), &input); err != nil {
		t.Fatal(err)
	}
	input["compiledSql"] = "select * from secrets"
	raw, _ := json.Marshal(input)
	if _, err := Prepare(raw); err == nil {
		t.Fatal("Prepare() accepted an unknown field")
	}
}

func TestPreparePersistsDesignerWithoutChangingLogicalPlan(t *testing.T) {
	baseline, err := Prepare(readExample(t))
	if err != nil {
		t.Fatal(err)
	}
	var input map[string]any
	if err := json.Unmarshal(readExample(t), &input); err != nil {
		t.Fatal(err)
	}
	designer := map[string]any{
		"version": "1.0",
		"components": []any{
			map[string]any{
				"id": "node_orders", "kind": "DATA", "name": "订单数据",
				"position": map[string]any{"x": 42, "y": 58},
				"outputs":  []any{map[string]any{"id": "orders_customer_id", "name": "客户ID"}},
			},
			map[string]any{
				"id": "output_1", "kind": "OUTPUT", "name": "最终结果",
				"position": map[string]any{"x": 640, "y": 58},
			},
		},
		"edges": []any{map[string]any{"id": "edge_1", "sourceId": "node_orders", "targetId": "output_1"}},
	}
	input["designer"] = designer
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	first, err := Prepare(raw)
	if err != nil {
		t.Fatal(err)
	}
	if first.Document.Designer == nil || !strings.Contains(string(first.DSLJSON), `"name":"客户ID"`) {
		t.Fatalf("designer metadata was not preserved: %s", first.DSLJSON)
	}
	if first.DSLHash == baseline.DSLHash {
		t.Fatal("adding designer metadata did not change dslHash")
	}
	if first.PlanHash != baseline.PlanHash || string(first.LogicalPlanJSON) != string(baseline.LogicalPlanJSON) {
		t.Fatal("designer metadata changed the executable logical plan")
	}

	// 整理后只移动一个组件：设计修订必须变化，执行计划必须保持不变。
	position := designer["components"].([]any)[0].(map[string]any)["position"].(map[string]any)
	position["x"] = 360
	movedRaw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	moved, err := Prepare(movedRaw)
	if err != nil {
		t.Fatal(err)
	}
	if moved.DSLHash == first.DSLHash || moved.PlanHash != first.PlanHash {
		t.Fatalf("layout hashes dsl=%s/%s plan=%s/%s", first.DSLHash, moved.DSLHash, first.PlanHash, moved.PlanHash)
	}
	second, err := Prepare(moved.DSLJSON)
	if err != nil {
		t.Fatal(err)
	}
	if string(second.DSLJSON) != string(moved.DSLJSON) || second.DSLHash != moved.DSLHash {
		t.Fatal("designer metadata was not stable after normalization round-trip")
	}
}

func TestValidateDesignerMetadataBoundariesAndLegacyCompatibility(t *testing.T) {
	prepared, err := Prepare(readExample(t))
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Document.Designer != nil {
		t.Fatalf("legacy DSL unexpectedly gained designer metadata: %#v", prepared.Document.Designer)
	}
	fixedGraph := map[string]any{
		"version":       "1.0",
		"nodePositions": map[string]any{"orders": map[string]any{"x": 42, "y": 48}},
		"nodeNames":     map[string]any{"orders": "订单数据"},
		"joins": []any{
			map[string]any{"id": "join_1", "name": "订单关联", "position": map[string]any{"x": 342, "y": 48}},
		},
		"groups": []any{
			map[string]any{"id": "group_1", "name": "客户汇总", "position": map[string]any{"x": 642, "y": 48}},
		},
		"end": map[string]any{"id": "end_1", "name": "最终输出", "position": map[string]any{"x": 942, "y": 48}},
	}
	document := prepared.Document
	document.Designer = fixedGraph
	if err := Validate(document); err != nil {
		t.Fatalf("fixed designer graph was rejected: %v", err)
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	withGraph, err := Prepare(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(withGraph.DSLJSON), `"nodePositions":{"orders":{"x":42,"y":48}}`) ||
		!strings.Contains(string(withGraph.DSLJSON), `"id":"end_1"`) {
		t.Fatalf("fixed designer graph was not preserved: %s", withGraph.DSLJSON)
	}
	tests := []struct {
		name     string
		designer map[string]any
		reason   string
	}{
		{name: "版本", designer: map[string]any{"version": "2.0"}, reason: "必须为 1.0"},
		{name: "负坐标", designer: map[string]any{"components": []any{map[string]any{"id": "node_1", "kind": "DATA", "position": map[string]any{"x": -1, "y": 0}}}}, reason: "有限的非负数"},
		{name: "非法组件", designer: map[string]any{"components": []any{map[string]any{"id": "1 bad", "kind": "SCRIPT", "position": map[string]any{"x": 0, "y": 0}}}}, reason: "合法且非空的组件标识"},
		{name: "非有限坐标", designer: map[string]any{"components": []any{map[string]any{"id": "node_1", "kind": "NODE", "position": map[string]any{"x": math.Inf(1), "y": 0}}}}, reason: "有限的非负数"},
		{name: "固定图节点负坐标", designer: map[string]any{"nodePositions": map[string]any{"orders": map[string]any{"x": 0, "y": -1}}}, reason: "有限的非负数"},
		{name: "固定图节点标识", designer: map[string]any{"nodePositions": map[string]any{"1 bad": map[string]any{"x": 0, "y": 0}}}, reason: "节点标识不合法"},
		{name: "固定图组件重复", designer: map[string]any{
			"joins":  []any{map[string]any{"id": "shared_1", "position": map[string]any{"x": 0, "y": 0}}},
			"groups": []any{map[string]any{"id": "shared_1", "position": map[string]any{"x": 100, "y": 0}}},
		}, reason: "画布组件标识重复"},
		{name: "固定图循环依赖", designer: map[string]any{
			"joins": []any{
				map[string]any{"id": "join_a", "position": map[string]any{"x": 0, "y": 0}, "left": map[string]any{"kind": "JOIN", "id": "join_b"}},
				map[string]any{"id": "join_b", "position": map[string]any{"x": 100, "y": 0}, "left": map[string]any{"kind": "JOIN", "id": "join_a"}},
			},
		}, reason: "循环依赖"},
		{name: "固定图失效引用", designer: map[string]any{
			"groups": []any{map[string]any{"id": "group_1", "position": map[string]any{"x": 0, "y": 0}, "input": map[string]any{"kind": "NODE", "id": "deleted_node"}}},
		}, reason: "不存在或已被删除"},
		{name: "固定图结束节点缺少坐标", designer: map[string]any{"end": map[string]any{"id": "end_1"}}, reason: "必须提供坐标"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := prepared.Document
			document.Designer = test.designer
			if err := Validate(document); !validationHasReason(err, test.reason) {
				t.Fatalf("Validate() error=%v, want reason containing %q", err, test.reason)
			}
		})
	}
}

func TestValidateReturnsPreciseReferencePaths(t *testing.T) {
	prepared, err := Prepare(readExample(t))
	if err != nil {
		t.Fatal(err)
	}
	prepared.Document.GroupBy = []string{"missing_field"}
	prepared.Document.OutputGrain.KeyFields = []string{"missing_code"}
	err = Validate(prepared.Document)
	var validation *ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("Validate() error = %v, want ValidationError", err)
	}
	paths := map[string]bool{}
	for _, issue := range validation.Issues {
		paths[issue.Path] = true
	}
	if !paths["groupBy[0]"] || !paths["outputGrain.keyFields[0]"] {
		t.Fatalf("validation paths = %#v", paths)
	}
}

func TestValidateAllowsMultipleTablesFromOneSource(t *testing.T) {
	prepared, err := Prepare(readExample(t))
	if err != nil {
		t.Fatal(err)
	}
	document := prepared.Document
	second := document.Nodes[0]
	second.ID, second.Alias, second.TableID = "customers", "c", "33333333-3333-4333-8333-333333333333"
	document.Nodes = append(document.Nodes, second)
	document.Joins = []Join{{ID: "join_orders_customers", LeftNodeID: "orders", RightNodeID: "customers", JoinType: "INNER", Cardinality: "MANY_TO_ONE", Conditions: []JoinCondition{{LeftExpression: Expression{Type: "FIELD_REF", NodeID: "orders", Field: "order_date"}, Operator: "EQUALS", RightExpression: Expression{Type: "FIELD_REF", NodeID: "customers", Field: "order_date"}}}, ManualConfirmed: true}}
	if err := Validate(document); err != nil {
		t.Fatalf("same-source multi-table DSL was rejected: %v", err)
	}
	document.Nodes[1].DataSourceID = "another-source"
	if err := Validate(document); err == nil {
		t.Fatal("SINGLE_SOURCE accepted nodes from different data sources")
	}
}

func TestValidateAllowsRepeatedPhysicalTableAndUnknownCardinality(t *testing.T) {
	prepared, err := Prepare(readExample(t))
	if err != nil {
		t.Fatal(err)
	}
	document := prepared.Document
	second := document.Nodes[0]
	second.ID, second.Alias = "orders_parent", "op"
	document.Nodes = append(document.Nodes, second)
	document.Joins = []Join{{ID: "join_orders_parent", LeftNodeID: "orders", RightNodeID: "orders_parent", JoinType: "LEFT", Cardinality: "UNKNOWN", Conditions: []JoinCondition{{LeftExpression: Expression{Type: "FIELD_REF", NodeID: "orders", Field: "order_date"}, Operator: "EQUALS", RightExpression: Expression{Type: "FIELD_REF", NodeID: "orders_parent", Field: "order_date"}}}, ManualConfirmed: true}}
	if err := Validate(document); err != nil {
		t.Fatalf("repeated physical table with unknown cardinality was rejected: %v", err)
	}
}

func TestPreparePersistsPreJoinAggregationBeforeJoin(t *testing.T) {
	prepared, err := Prepare(readExample(t))
	if err != nil {
		t.Fatal(err)
	}
	document := prepared.Document
	document.Parameters, document.Filters, document.Having, document.Sorts = nil, nil, nil, nil
	document.Nodes[0].SourceFilters = nil
	second := document.Nodes[0]
	second.ID, second.Alias, second.TableID = "customers", "c", "table-customers"
	document.Nodes = append(document.Nodes, second)
	document.Joins = []Join{{ID: "join_1", LeftNodeID: "orders", RightNodeID: "customers", JoinType: "LEFT", Cardinality: "UNKNOWN", ManualConfirmed: true, Conditions: []JoinCondition{{LeftExpression: Expression{Type: "FIELD_REF", NodeID: "orders", Field: "order_date"}, Operator: "EQUALS", RightExpression: Expression{Type: "FIELD_REF", NodeID: "customers", Field: "order_date"}}}}}
	document.PreAggregations = []PreAggregation{{ID: "group_1", NodeID: "orders", JoinID: "join_1", JoinSide: "LEFT", GroupBy: []PreAggregationGroup{{Field: "order_date", Unit: "MONTH"}}, Metrics: []PreAggregationMetric{{Field: "order_amount", Function: "SUM"}}}}
	document.Fields = []Field{
		{ID: "field_order_date", Code: "order_date", Name: "订单日期", Role: "DIMENSION", Expression: Expression{Type: "FIELD_REF", NodeID: "orders", Field: "order_date"}, CanonicalType: "DATE", Nullable: false},
		{ID: "field_order_amount", Code: "order_amount", Name: "订单金额", Role: "MEASURE", Expression: Expression{Type: "FIELD_REF", NodeID: "orders", Field: "order_amount"}, CanonicalType: "DECIMAL", Nullable: true},
	}
	document.GroupBy = nil
	document.OutputGrain = OutputGrain{Description: "每行一个月份", KeyFields: []string{"order_date"}}
	raw, _ := json.Marshal(document)

	result, err := Prepare(raw)
	if err != nil {
		t.Fatal(err)
	}
	kinds := []string{}
	for _, step := range result.LogicalPlan.Steps {
		kinds = append(kinds, step.Kind)
	}
	if !reflect.DeepEqual(kinds, []string{"SCAN", "SCAN", "PRE_AGGREGATE", "JOIN_LEFT"}) {
		t.Fatalf("plan kinds=%v", kinds)
	}
	if len(result.Document.PreAggregations) != 1 || result.Document.PreAggregations[0].Metrics[0].Function != "SUM" {
		t.Fatalf("pre-aggregation was not persisted: %#v", result.Document.PreAggregations)
	}
}

func TestValidateRejectsUnboundedNodeFanout(t *testing.T) {
	prepared, err := Prepare(readExample(t))
	if err != nil {
		t.Fatal(err)
	}
	document := prepared.Document
	for index := 1; index <= MaxNodes; index++ {
		node := document.Nodes[0]
		node.ID, node.Alias, node.TableID = fmt.Sprintf("orders_%d", index), fmt.Sprintf("o_%d", index), fmt.Sprintf("table-%d", index)
		document.Nodes = append(document.Nodes, node)
	}
	err = Validate(document)
	var validation *ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("Validate() error=%v, want ValidationError", err)
	}
	for _, issue := range validation.Issues {
		if issue.Path == "nodes" && issue.Reason == "最多允许 16 个节点" {
			return
		}
	}
	t.Fatalf("node limit issue is missing: %#v", validation.Issues)
}

func TestValidateRejectsUnsafeSourceFilterExpression(t *testing.T) {
	prepared, err := Prepare(readExample(t))
	if err != nil {
		t.Fatal(err)
	}
	document := prepared.Document
	document.Nodes[0].SourceFilters = []SourceFilter{{Expression: &Expression{
		Type: "EQUALS", Left: &Expression{Type: "FIELD_REF", NodeID: "another_node", Field: "order_status"}, Right: &Expression{Type: "LITERAL", Value: "VALID"},
	}}}
	if err := Validate(document); !validationHasReason(err, "源端过滤只能引用当前节点") {
		t.Fatalf("cross-node source filter error=%v", err)
	}
	document.Nodes[0].SourceFilters = []SourceFilter{{Expression: &Expression{
		Type: "GT", Left: &Expression{Type: "AGGREGATE", Function: "COUNT"}, Right: &Expression{Type: "LITERAL", Value: 1},
	}}}
	if err := Validate(document); !validationHasReason(err, "源过滤不能包含聚合表达式") {
		t.Fatalf("aggregate source filter error=%v", err)
	}
}

func TestSchemaAndExampleAreValidJSON(t *testing.T) {
	for _, path := range []string{"../../api/schemas/dataset-dsl-v1.schema.json", "../../api/examples/dataset-dsl-v1.json"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			t.Fatalf("%s is invalid JSON: %v", path, err)
		}
	}
}

func readExample(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("../../api/examples/dataset-dsl-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func validationHasReason(err error, reason string) bool {
	var validation *ValidationError
	if !errors.As(err, &validation) {
		return false
	}
	for _, issue := range validation.Issues {
		if strings.Contains(issue.Reason, reason) {
			return true
		}
	}
	return false
}
