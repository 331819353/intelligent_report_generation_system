package semanticqa

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestApplyChangeOperationsUsesBoundedStructuredPatches(t *testing.T) {
	baseline := json.RawMessage(`{
		"dslVersion":"1.0",
		"dataset":{"code":"orders","name":"Orders","type":"SINGLE_SOURCE","layer":"DWD"},
		"nodes":[],
		"joins":[],
		"fields":[{"code":"order_id"}]
	}`)
	raw, err := applyChangeOperations(baseline, []ChangeOperation{
		{Operation: "REPLACE", Path: "/dataset/name", Value: json.RawMessage(`"Order facts"`)},
		{Operation: "ADD", Path: "/fields/-", Value: json.RawMessage(`{"code":"amount"}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result["dataset"].(map[string]any)["name"] != "Order facts" {
		t.Fatalf("dataset name=%v", result["dataset"])
	}
	if len(result["fields"].([]any)) != 2 {
		t.Fatalf("fields=%v", result["fields"])
	}
}

func TestApplyChangeOperationsRejectsWholeDocumentAndAmbiguousPatches(t *testing.T) {
	baseline := json.RawMessage(`{"dslVersion":"1.0","dataset":{"name":"Orders"}}`)
	tests := [][]ChangeOperation{
		{{Operation: "REPLACE", Path: "/", Value: json.RawMessage(`{}`)}},
		{{Operation: "REPLACE", Path: "/dslVersion", Value: json.RawMessage(`"2.0"`)}},
		{{Operation: "ADD", Path: "/dataset/name", Value: json.RawMessage(`"Again"`)}},
		{
			{Operation: "REPLACE", Path: "/dataset/name", Value: json.RawMessage(`"One"`)},
			{Operation: "REPLACE", Path: "/dataset/name", Value: json.RawMessage(`"Two"`)},
		},
	}
	for index, operations := range tests {
		if _, err := applyChangeOperations(baseline, operations); !errors.Is(err, ErrUnsafeChange) {
			t.Fatalf("case %d error=%v", index, err)
		}
	}
}

func TestComponentDiffProducesDeterministicBoundedPatches(t *testing.T) {
	baseline := json.RawMessage(`{
		"dslVersion":"1.0",
		"dataset":{"code":"orders","name":"Orders"},
		"nodes":[],
		"joins":[],
		"designer":{"version":"1.0","positions":{}}
	}`)
	candidate := json.RawMessage(`{
		"dslVersion":"1.0",
		"dataset":{"code":"orders","name":"Orders enriched"},
		"nodes":[{"id":"fact"}],
		"joins":[],
		"fields":[],
		"designer":{"version":"1.0","positions":{"fact":{"x":10,"y":20}}}
	}`)
	operations, err := componentDiff(baseline, candidate)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(operations))
	for _, operation := range operations {
		got = append(got, operation.Operation+" "+operation.Path)
	}
	want := []string{
		"REPLACE /dataset",
		"REPLACE /nodes",
		"ADD /fields",
		"REPLACE /designer",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("operations=%v, want %v", got, want)
	}
	applied, err := applyChangeOperations(baseline, operations)
	if err != nil {
		t.Fatal(err)
	}
	var appliedValue, candidateValue any
	if err := json.Unmarshal(applied, &appliedValue); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(candidate, &candidateValue); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(appliedValue, candidateValue) {
		t.Fatalf("applied=%s candidate=%s", applied, candidate)
	}
}

func TestTopologicalDatasetOrderIsDeterministic(t *testing.T) {
	nodes := []WarehouseDAGNode{
		{DatasetVersionID: "dws"}, {DatasetVersionID: "dwd_b"},
		{DatasetVersionID: "dwd_a"}, {DatasetVersionID: "ods"},
	}
	edges := []WarehouseDAGEdge{
		{FromDatasetVersionID: "dwd_b", ToDatasetVersionID: "dws"},
		{FromDatasetVersionID: "dwd_a", ToDatasetVersionID: "dws"},
		{FromDatasetVersionID: "ods", ToDatasetVersionID: "dwd_a"},
	}
	order, err := topologicalDatasetOrder(nodes, edges)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"dwd_b", "ods", "dwd_a", "dws"}
	for index := range want {
		if order[index] != want[index] {
			t.Fatalf("order=%v want=%v", order, want)
		}
	}
	_, err = topologicalDatasetOrder(nodes, append(edges, WarehouseDAGEdge{
		FromDatasetVersionID: "dws", ToDatasetVersionID: "dwd_b",
	}))
	if !errors.Is(err, ErrUnprovenPath) {
		t.Fatalf("cycle error=%v", err)
	}
}
