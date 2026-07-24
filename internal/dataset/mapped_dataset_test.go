package dataset

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
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
	if document.ExecutionPolicy.Mode != "MATERIALIZED_PREFERRED" ||
		!document.ExecutionPolicy.Materialization.Enabled ||
		document.ExecutionPolicy.Materialization.RefreshMode != "ON_DEMAND" {
		t.Fatalf("mapped ODS materialization policy = %#v", document.ExecutionPolicy)
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

func TestBuildMappedDatasetDocumentSupportsSafeUnicodePhysicalColumns(t *testing.T) {
	table := MappedDatasetTable{
		ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", DataSourceID: "source-1", DataSourceName: "销售订单明细表",
		FileVersionID: "file-version-1", TableName: "CSV", BusinessName: "sales_order",
	}
	document, err := BuildMappedDatasetDocument(table, []MappedDatasetColumn{
		{ColumnName: "订单编号", BusinessName: "order_id", BusinessDescription: "订单唯一编号", CanonicalType: "TEXT"},
		{ColumnName: "订单金额", BusinessName: "order_amount", BusinessDescription: "订单金额", CanonicalType: "DECIMAL", SemanticType: "AMOUNT"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if document.Dataset.Name != "销售订单明细表" ||
		!reflect.DeepEqual(document.Nodes[0].Projection, []string{"订单编号", "订单金额"}) ||
		document.Fields[0].Expression.Field != "订单编号" ||
		!reflect.DeepEqual([]string{document.Fields[0].Code, document.Fields[1].Code}, []string{"order_id", "order_amount"}) ||
		!reflect.DeepEqual([]string{document.Fields[0].Name, document.Fields[1].Name}, []string{"订单编号", "订单金额"}) {
		t.Fatalf("document = %#v", document)
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(raw); err != nil {
		t.Fatalf("unicode physical fields did not pass Prepare: %v", err)
	}
}

func TestBuildMappedDatasetDocumentPrefersChineseSheetNameForFileDataset(t *testing.T) {
	document, err := BuildMappedDatasetDocument(MappedDatasetTable{
		ID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", DataSourceID: "source-1", DataSourceName: "经营分析工作簿",
		FileVersionID: "file-version-1", TableName: "销售订单", BusinessName: "sales_order",
	}, []MappedDatasetColumn{{ColumnName: "order_id", BusinessName: "order_id", CanonicalType: "TEXT"}})
	if err != nil {
		t.Fatal(err)
	}
	if document.Dataset.Name != "销售订单" {
		t.Fatalf("dataset name=%q, want Chinese sheet name", document.Dataset.Name)
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

func TestMappedDatasetStateSystemAdvanceRequiresExactPublishedDraft(t *testing.T) {
	state := mappedDatasetState{
		DraftVersionID:       "11111111-1111-4111-8111-111111111111",
		DraftRecordVersion:   7,
		DSLHash:              strings.Repeat("a", 64),
		PlanHash:             strings.Repeat("b", 64),
		PendingApprovalCount: 0,
	}
	publication := mappedDatasetPublicationFence{
		VersionID:                "22222222-2222-4222-8222-222222222222",
		SourceDraftVersionID:     state.DraftVersionID,
		SourceDraftRecordVersion: state.DraftRecordVersion,
		SchemaHash:               state.DSLHash,
		PlanHash:                 state.PlanHash,
	}
	if !state.canSystemAdvance(publication) {
		t.Fatalf("exact published source revision was not eligible: state=%#v publication=%#v", state, publication)
	}

	tests := []struct {
		name   string
		mutate func(*mappedDatasetState, *mappedDatasetPublicationFence)
	}{
		{
			name: "草稿身份已变化",
			mutate: func(state *mappedDatasetState, _ *mappedDatasetPublicationFence) {
				state.DraftVersionID = "33333333-3333-4333-8333-333333333333"
			},
		},
		{
			name: "草稿记录已变化",
			mutate: func(state *mappedDatasetState, _ *mappedDatasetPublicationFence) {
				state.DraftRecordVersion++
			},
		},
		{
			name: "草稿结构已变化",
			mutate: func(state *mappedDatasetState, _ *mappedDatasetPublicationFence) {
				state.DSLHash = strings.Repeat("c", 64)
			},
		},
		{
			name: "草稿计划已变化",
			mutate: func(state *mappedDatasetState, _ *mappedDatasetPublicationFence) {
				state.PlanHash = strings.Repeat("d", 64)
			},
		},
		{
			name: "存在待审批申请",
			mutate: func(state *mappedDatasetState, _ *mappedDatasetPublicationFence) {
				state.PendingApprovalCount = 1
			},
		},
		{
			name: "发布来源草稿缺失",
			mutate: func(_ *mappedDatasetState, publication *mappedDatasetPublicationFence) {
				publication.SourceDraftVersionID = ""
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidateState := state
			candidatePublication := publication
			test.mutate(&candidateState, &candidatePublication)
			if candidateState.canSystemAdvance(candidatePublication) {
				t.Fatalf("unsafe system advance remained eligible: state=%#v publication=%#v",
					candidateState, candidatePublication)
			}
		})
	}
}
