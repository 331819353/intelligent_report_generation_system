package dataset

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestBuildMappedDatasetDocumentCreatesDirectSingleTableGraph(t *testing.T) {
	table := MappedDatasetTable{
		ID:                  "4D0F846D-9DB6-4C6E-8B2F-08C6BD376349",
		DataSourceID:        "11111111-1111-4111-8111-111111111111",
		FileVersionID:       "22222222-2222-4222-8222-222222222222",
		TableName:           "orders",
		BusinessName:        "订单事实表",
		BusinessDescription: "LLM 完善后的订单明细",
	}
	columns := []MappedDatasetColumn{
		{ColumnName: "customer$id", BusinessName: "客户ID", CanonicalType: "TEXT", SemanticType: "IDENTIFIER", Nullable: false},
		{ColumnName: "order$id", BusinessName: "订单编号", BusinessDescription: "业务主键", CanonicalType: "NUMBER", SemanticType: "IDENTIFIER", PrimaryKey: true},
		{ColumnName: "amount", BusinessName: "交易金额", CanonicalType: "DECIMAL", SemanticType: "AMOUNT", Nullable: true},
	}

	document, err := BuildMappedDatasetDocument(table, columns)
	if err != nil {
		t.Fatalf("BuildMappedDatasetDocument() error = %v", err)
	}
	if document.Dataset.Code != "mapped_4d0f846d9db64c6e8b2f08c6bd376349" ||
		document.Dataset.Name != "订单事实表" || document.Dataset.Type != "SINGLE_SOURCE" {
		t.Fatalf("dataset descriptor = %#v", document.Dataset)
	}
	if len(document.Nodes) != 1 {
		t.Fatalf("nodes = %#v", document.Nodes)
	}
	node := document.Nodes[0]
	if node.ID != "node_1" || node.Type != "TABLE" || node.Alias != "t1" ||
		node.TableID != "4d0f846d-9db6-4c6e-8b2f-08c6bd376349" || node.DataSourceID != table.DataSourceID ||
		node.FileVersionID != table.FileVersionID ||
		!reflect.DeepEqual(node.Projection, []string{"customer$id", "order$id", "amount"}) {
		t.Fatalf("node = %#v", node)
	}
	if len(document.Joins) != 0 || len(document.PreAggregations) != 0 || len(document.GroupBy) != 0 {
		t.Fatalf("default mapped dataset contains an intermediate component: joins=%#v groups=%#v groupBy=%#v", document.Joins, document.PreAggregations, document.GroupBy)
	}
	if len(document.Fields) != len(columns) {
		t.Fatalf("fields = %#v", document.Fields)
	}
	if got := []string{document.Fields[0].Code, document.Fields[1].Code, document.Fields[2].Code}; !reflect.DeepEqual(got, []string{"customer_id", "order_id", "amount"}) {
		t.Fatalf("field codes = %v", got)
	}
	if document.Fields[0].CanonicalType != "STRING" || document.Fields[1].CanonicalType != "INTEGER" || document.Fields[2].CanonicalType != "DECIMAL" {
		t.Fatalf("canonical types = %q, %q, %q", document.Fields[0].CanonicalType, document.Fields[1].CanonicalType, document.Fields[2].CanonicalType)
	}
	if document.Fields[1].Name != "订单编号" || document.Fields[1].Description != "业务主键" ||
		document.Fields[1].Expression.Field != "order$id" || document.Fields[1].Role != "IDENTIFIER" {
		t.Fatalf("primary field = %#v", document.Fields[1])
	}
	if !reflect.DeepEqual(document.OutputGrain.KeyFields, []string{"order_id"}) {
		t.Fatalf("output grain = %#v", document.OutputGrain)
	}
	if document.Designer["version"] != "1.0" {
		t.Fatalf("designer version = %#v", document.Designer["version"])
	}
	if joins, ok := document.Designer["joins"].([]map[string]any); !ok || len(joins) != 0 {
		t.Fatalf("designer joins = %#v", document.Designer["joins"])
	}
	if groups, ok := document.Designer["groups"].([]map[string]any); !ok || len(groups) != 0 {
		t.Fatalf("designer groups = %#v", document.Designer["groups"])
	}
	positions := document.Designer["nodePositions"].(map[string]any)
	if _, ok := positions["node_1"]; !ok {
		t.Fatalf("node position = %#v", positions)
	}
	end := document.Designer["end"].(map[string]any)
	input := end["input"].(map[string]any)
	outputs := end["outputs"].([]map[string]any)
	if end["id"] != "end_1" || input["kind"] != "NODE" || input["id"] != "node_1" || len(outputs) != len(document.Fields) {
		t.Fatalf("end = %#v", end)
	}
	for index, output := range outputs {
		if output["key"] != "node_1."+node.Projection[index] || output["code"] != document.Fields[index].Code || output["name"] != document.Fields[index].Name {
			t.Fatalf("end output %d = %#v, field=%#v", index, output, document.Fields[index])
		}
	}

	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := Prepare(raw)
	if err != nil {
		t.Fatalf("generated DSL did not pass Prepare(): %v\n%s", err, raw)
	}
	if prepared.Document.Dataset.Code != document.Dataset.Code || len(prepared.LogicalPlan.Steps) != 1 || prepared.LogicalPlan.Steps[0].Kind != "SCAN" {
		t.Fatalf("prepared = %#v", prepared)
	}
}

func TestBuildMappedDatasetDocumentUsesStableCollisionSuffixesAndFirstColumnGrain(t *testing.T) {
	table := MappedDatasetTable{
		ID: "33333333-3333-4333-8333-333333333333", DataSourceID: "source-1", TableName: "raw_orders",
	}
	columns := []MappedDatasetColumn{
		{ColumnName: "item$id", CanonicalType: "TEXT"},
		{ColumnName: "item#id", CanonicalType: "TEXT", SemanticType: "IDENTIFIER"},
		{ColumnName: "item_id", CanonicalType: "TEXT"},
	}
	first, err := BuildMappedDatasetDocument(table, columns)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildMappedDatasetDocument(table, columns)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("builder output changed for identical input")
	}
	if got := []string{first.Fields[0].Code, first.Fields[1].Code, first.Fields[2].Code}; !reflect.DeepEqual(got, []string{"item_id", "item_id_2", "item_id_3"}) {
		t.Fatalf("collision codes = %v", got)
	}
	if first.Fields[0].ID == first.Fields[1].ID || first.Fields[1].ID == first.Fields[2].ID {
		t.Fatalf("field IDs collided: %#v", first.Fields)
	}
	if !reflect.DeepEqual(first.OutputGrain.KeyFields, []string{"item_id"}) {
		t.Fatalf("fallback grain = %#v", first.OutputGrain)
	}
}

func TestBuildMappedDatasetDocumentUsesAllCompositePrimaryKeyFieldsAsGrain(t *testing.T) {
	document, err := BuildMappedDatasetDocument(MappedDatasetTable{
		ID: "44444444-4444-4444-8444-444444444444", DataSourceID: "source-1", TableName: "order_lines",
	}, []MappedDatasetColumn{
		{ColumnName: "order_id", CanonicalType: "INTEGER", PrimaryKey: true},
		{ColumnName: "line_no", CanonicalType: "INTEGER", PrimaryKey: true},
		{ColumnName: "amount", CanonicalType: "DECIMAL"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(document.OutputGrain.KeyFields, []string{"order_id", "line_no"}) {
		t.Fatalf("composite key grain = %#v", document.OutputGrain)
	}
}

func TestBuildMappedDatasetDocumentRejectsUnexecutablePhysicalColumnWithoutRewritingIt(t *testing.T) {
	_, err := BuildMappedDatasetDocument(MappedDatasetTable{
		ID: "33333333-3333-4333-8333-333333333333", DataSourceID: "source-1", TableName: "raw_orders",
	}, []MappedDatasetColumn{{ColumnName: "unsafe column", CanonicalType: "TEXT"}})
	if !errors.Is(err, errMappedDatasetUnsupportedColumn) {
		t.Fatalf("error = %v, want unsupported physical column", err)
	}
}

func TestMappedDatasetStateDefaultPublicationRequiresPristineUnpublishedDraft(t *testing.T) {
	document, err := BuildMappedDatasetDocument(MappedDatasetTable{
		ID: "55555555-5555-4555-8555-555555555555", DataSourceID: "source-1", TableName: "orders",
	}, []MappedDatasetColumn{{ColumnName: "order_id", CanonicalType: "INTEGER", PrimaryKey: true}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := Prepare(raw)
	if err != nil {
		t.Fatal(err)
	}
	pristine := mappedDatasetState{
		ID: "dataset-1", Status: "DRAFT", Version: 1,
		DraftVersionID: "draft-1", DraftVersionNo: 1, DraftRecordVersion: 1,
		DSLHash: prepared.DSLHash, PlanHash: prepared.PlanHash,
		RevisionCount: 1, ExactCreateCount: 1,
	}
	if !pristine.canDefaultPublish(prepared) {
		t.Fatalf("pristine mapped dataset was not eligible: %#v", pristine)
	}

	tests := []struct {
		name   string
		mutate func(*mappedDatasetState)
	}{
		{name: "已被编辑", mutate: func(state *mappedDatasetState) { state.Version = 2 }},
		{name: "草稿记录已变化", mutate: func(state *mappedDatasetState) { state.DraftRecordVersion = 2 }},
		{name: "已有发布历史", mutate: func(state *mappedDatasetState) { state.PublishedCount = 1 }},
		{name: "存在待审批申请", mutate: func(state *mappedDatasetState) { state.PendingApprovalCount = 1 }},
		{name: "修订不唯一", mutate: func(state *mappedDatasetState) { state.RevisionCount = 2 }},
		{name: "创建修订不匹配", mutate: func(state *mappedDatasetState) { state.ExactCreateCount = 0 }},
		{name: "DSL 已偏离映射默认值", mutate: func(state *mappedDatasetState) { state.DSLHash = "a" }},
		{name: "已停用", mutate: func(state *mappedDatasetState) { state.Status = "DISABLED" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := pristine
			test.mutate(&candidate)
			if candidate.canDefaultPublish(prepared) {
				t.Fatalf("unsafe mapped dataset remained eligible: %#v", candidate)
			}
		})
	}
}
