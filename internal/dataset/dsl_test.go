package dataset

import (
	"encoding/json"
	"errors"
	"fmt"
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
