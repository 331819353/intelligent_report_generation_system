package datasetai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	aiplatform "intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/asset"
	"intelligent-report-generation-system/internal/assetembedding"
)

type fakeCatalog struct {
	tables       []asset.Table
	columns      map[string][]asset.Column
	total        int
	getHash      map[string]string
	searchErr    error
	listErrorFor string
	searches     []asset.Search
}

func (f *fakeCatalog) SearchTables(_ context.Context, _ string, search asset.Search) ([]asset.Table, int, error) {
	f.searches = append(f.searches, search)
	if f.searchErr != nil {
		return nil, 0, f.searchErr
	}
	total := f.total
	if total == 0 {
		total = len(f.tables)
	}
	start := min(max(search.Offset, 0), len(f.tables))
	limit := search.Limit
	if limit <= 0 {
		limit = len(f.tables)
	}
	end := min(start+limit, len(f.tables))
	return append([]asset.Table(nil), f.tables[start:end]...), total, nil
}

func (f *fakeCatalog) GetTable(_ context.Context, _ string, id string) (asset.Table, error) {
	for _, table := range f.tables {
		if table.ID != id {
			continue
		}
		if hash, ok := f.getHash[id]; ok {
			table.StructureHash = hash
		}
		return table, nil
	}
	return asset.Table{}, errors.New("not found")
}

func (f *fakeCatalog) ListColumns(_ context.Context, _ string, id string) ([]asset.Column, error) {
	if f.listErrorFor == id {
		return nil, errors.New("column query failed")
	}
	return append([]asset.Column(nil), f.columns[id]...), nil
}

type fakeInvoker struct {
	configured bool
	results    []aiplatform.InvocationResult
	errors     []error
	inputs     []aiplatform.Invocation
	invoke     func(context.Context, aiplatform.Invocation, int) (aiplatform.InvocationResult, error)
}

type fakeAssetRetriever struct {
	result assetembedding.RetrievalResult
	calls  int
}

func (f *fakeAssetRetriever) Retrieve(_ context.Context, _, _ string, _ []string, _, _ int) (assetembedding.RetrievalResult, error) {
	f.calls++
	return f.result, nil
}

func (f *fakeInvoker) Configured() bool     { return f.configured }
func (f *fakeInvoker) ProviderName() string { return "fake" }
func (f *fakeInvoker) Model() string        { return "fake-model" }
func (f *fakeInvoker) Invoke(ctx context.Context, input aiplatform.Invocation) (aiplatform.InvocationResult, error) {
	f.inputs = append(f.inputs, input)
	index := len(f.inputs) - 1
	if f.invoke != nil {
		return f.invoke(ctx, input, index)
	}
	var result aiplatform.InvocationResult
	if index < len(f.results) {
		result = f.results[index]
	}
	if index < len(f.errors) {
		return result, f.errors[index]
	}
	return result, nil
}

func plannerCatalog() *fakeCatalog {
	return &fakeCatalog{
		tables: []asset.Table{
			{ID: "table-customers", DataSourceID: "source-1", DataSourceName: "CRM", DataSourceType: "MYSQL", SchemaName: "demo", TableName: "customers", BusinessName: "客户", BusinessDescription: "客户主数据", AssetStatus: "ACTIVE", ManagementStatus: "ENABLED", EnrichmentStatus: "SUCCEEDED", StructureHash: "hash-customers"},
			{ID: "table-orders", DataSourceID: "source-1", DataSourceName: "CRM", DataSourceType: "MYSQL", SchemaName: "demo", TableName: "orders", BusinessName: "订单", BusinessDescription: "订单事实", AssetStatus: "ACTIVE", ManagementStatus: "ENABLED", EnrichmentStatus: "SUCCEEDED", StructureHash: "hash-orders"},
		},
		columns: map[string][]asset.Column{
			"table-customers": {
				{TableID: "table-customers", ColumnName: "customer_id", OrdinalPosition: 1, CanonicalType: "STRING", BusinessName: "客户编号", SemanticType: "IDENTIFIER", AssetStatus: "ACTIVE"},
				{TableID: "table-customers", ColumnName: "region", OrdinalPosition: 2, CanonicalType: "STRING", BusinessName: "地区", AssetStatus: "ACTIVE"},
			},
			"table-orders": {
				{TableID: "table-orders", ColumnName: "customer_id", OrdinalPosition: 1, CanonicalType: "STRING", BusinessName: "客户编号", SemanticType: "IDENTIFIER", AssetStatus: "ACTIVE"},
				{TableID: "table-orders", ColumnName: "amount", OrdinalPosition: 2, CanonicalType: "DECIMAL", BusinessName: "订单金额", SemanticType: "AMOUNT", AssetStatus: "ACTIVE"},
			},
		},
	}
}

func monthlyRegionalOrderCountAssetCatalog() *fakeCatalog {
	return &fakeCatalog{
		tables: []asset.Table{
			{ID: "table-orders", DataSourceID: "source-1", DataSourceName: "ERP", DataSourceType: "MYSQL", SchemaName: "demo", TableName: "orders", BusinessName: "订单事实表", BusinessDescription: "订单事实", AssetStatus: "ACTIVE", ManagementStatus: "ENABLED", EnrichmentStatus: "SUCCEEDED", StructureHash: "hash-orders"},
			{ID: "table-customers", DataSourceID: "source-1", DataSourceName: "ERP", DataSourceType: "MYSQL", SchemaName: "demo", TableName: "customers", BusinessName: "客户信息表", BusinessDescription: "客户信息", AssetStatus: "ACTIVE", ManagementStatus: "ENABLED", EnrichmentStatus: "SUCCEEDED", StructureHash: "hash-customers"},
		},
		columns: map[string][]asset.Column{
			"table-orders": {
				{TableID: "table-orders", ColumnName: "ORDER_ID", OrdinalPosition: 1, CanonicalType: "NUMBER", SemanticType: "IDENTIFIER", Nullable: false, AssetStatus: "ACTIVE"},
				{TableID: "table-orders", ColumnName: "CUSTOMER_ID", OrdinalPosition: 2, CanonicalType: "NUMBER", SemanticType: "IDENTIFIER", Nullable: false, AssetStatus: "ACTIVE"},
				{TableID: "table-orders", ColumnName: "CREATED_AT", OrdinalPosition: 3, CanonicalType: "DATETIME", SemanticType: "DATETIME", Nullable: false, AssetStatus: "ACTIVE"},
			},
			"table-customers": {
				{TableID: "table-customers", ColumnName: "customer_id", OrdinalPosition: 1, CanonicalType: "NUMBER", SemanticType: "IDENTIFIER", Nullable: false, AssetStatus: "ACTIVE"},
				{TableID: "table-customers", ColumnName: "region_code", OrdinalPosition: 2, CanonicalType: "STRING", SemanticType: "DIMENSION", Nullable: false, AssetStatus: "ACTIVE"},
			},
		},
	}
}

func plannerResult(t *testing.T, proposal Proposal, requestID string) aiplatform.InvocationResult {
	t.Helper()
	payload, err := json.Marshal(plannerProposalOutput{
		SchemaVersion: proposal.SchemaVersion,
		Mode:          proposal.Mode,
		Summary:       proposal.Summary,
		Assumptions:   proposal.Assumptions,
		Warnings:      proposal.Warnings,
		Plan:          proposal.Plan,
	})
	if err != nil {
		t.Fatal(err)
	}
	return aiplatform.InvocationResult{RequestID: requestID, ProviderResult: aiplatform.ProviderResult{Content: payload}}
}

func intentResult(t *testing.T, intent ChangeIntent, requestID string) aiplatform.InvocationResult {
	t.Helper()
	payload, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	return aiplatform.InvocationResult{RequestID: requestID, ProviderResult: aiplatform.ProviderResult{Content: payload}}
}

func readyIntent(operations ...ChangeOperation) ChangeIntent {
	if operations == nil {
		operations = []ChangeOperation{}
	}
	return ChangeIntent{Status: "READY", Question: "", Candidates: []ComponentRef{}, ChangeSet: ChangeSet{Operations: operations, FieldChanges: []FieldChange{}}}
}

func changeOperation(action, kind, id, name string, fields []string, inputChanges []InputChange, description string) ChangeOperation {
	if fields == nil {
		fields = []string{}
	}
	if inputChanges == nil {
		inputChanges = []InputChange{}
	}
	return ChangeOperation{Action: action, ComponentKind: kind, ComponentID: id, ComponentName: name, Fields: fields, InputChanges: inputChanges, Description: description}
}

func singleTableProposal(tableID, column string) Proposal {
	return Proposal{
		SchemaVersion: SchemaVersion, Mode: "CREATE", Summary: "保留当前单表流程", Assumptions: []string{}, Warnings: []string{},
		Plan: GraphPlan{
			Dataset: PlanDataset{Name: "单表数据集", Description: "单表流程"},
			Nodes:   []PlanNode{{ID: "node_1", TableID: tableID, Alias: "source", SelectedColumns: []string{column}}},
			Joins:   []PlanJoin{}, Groups: []PlanGroup{},
			End: PlanEnd{Name: "最终输出", Input: PlanInput{Kind: "NODE", ID: "node_1"}, Outputs: []PlanOutput{{NodeID: "node_1", Column: column, Name: column, Code: "output_id"}}},
		},
	}
}

func proposalWithGroupsBeforeAndAfterJoin() Proposal {
	return Proposal{
		SchemaVersion: SchemaVersion, Mode: "MODIFY", Summary: "保留关联前汇总并按地区输出", Assumptions: []string{}, Warnings: []string{},
		Plan: GraphPlan{
			Dataset: PlanDataset{Name: "客户订单汇总", Description: "关联前和关联后都有分组"},
			Nodes: []PlanNode{
				{ID: "node_1", TableID: "table-customers", Alias: "customers", SelectedColumns: []string{"customer_id", "region"}},
				{ID: "node_2", TableID: "table-orders", Alias: "orders", SelectedColumns: []string{"customer_id", "amount"}},
			},
			Joins: []PlanJoin{{
				ID: "join_1", Name: "客户订单关联", Left: PlanInput{Kind: "NODE", ID: "node_1"}, Right: PlanInput{Kind: "GROUP", ID: "group_before"}, JoinType: "LEFT",
				Conditions: []PlanJoinCondition{{LeftNodeID: "node_1", LeftColumn: "customer_id", RightNodeID: "node_2", RightColumn: "customer_id"}},
			}},
			Groups: []PlanGroup{
				{
					ID: "group_before", Name: "关联前订单汇总", Input: PlanInput{Kind: "NODE", ID: "node_2"},
					Dimensions: []PlanDimension{{NodeID: "node_2", Column: "customer_id", Grouping: ""}},
					Metrics:    []PlanMetric{{NodeID: "node_2", Column: "amount", Aggregation: "SUM"}},
				},
				{
					ID: "group_after", Name: "关联后地区汇总", Input: PlanInput{Kind: "JOIN", ID: "join_1"},
					Dimensions: []PlanDimension{{NodeID: "node_1", Column: "region", Grouping: ""}},
					Metrics:    []PlanMetric{{NodeID: "node_2", Column: "amount", Aggregation: "SUM"}},
				},
			},
			End: PlanEnd{Name: "最终输出", Input: PlanInput{Kind: "GROUP", ID: "group_after"}, Outputs: []PlanOutput{
				{NodeID: "node_1", Column: "region", Name: "地区", Code: "region"},
				{NodeID: "node_2", Column: "amount", Name: "订单金额", Code: "order_amount"},
			}},
		},
	}
}

func proposalAfterRemovingPostJoinGroup() Proposal {
	proposal := proposalWithGroupsBeforeAndAfterJoin()
	proposal.Summary = "仅删除关联后分组"
	proposal.Plan.Groups = proposal.Plan.Groups[:1]
	proposal.Plan.End.Input = PlanInput{Kind: "JOIN", ID: "join_1"}
	return proposal
}

func proposalAfterRemovingPreJoinGroup() Proposal {
	proposal := proposalWithGroupsBeforeAndAfterJoin()
	proposal.Summary = "仅删除关联前分组"
	proposal.Plan.Groups = proposal.Plan.Groups[1:]
	proposal.Plan.Joins[0].Right = PlanInput{Kind: "NODE", ID: "node_2"}
	return proposal
}

func proposalAfterRemovingAllGroups() Proposal {
	proposal := proposalWithGroupsBeforeAndAfterJoin()
	proposal.Summary = "删除全部分组"
	proposal.Plan.Groups = []PlanGroup{}
	proposal.Plan.Joins[0].Right = PlanInput{Kind: "NODE", ID: "node_2"}
	proposal.Plan.End.Input = PlanInput{Kind: "JOIN", ID: "join_1"}
	return proposal
}

func removePostJoinGroupIntent() ChangeIntent {
	return readyIntent(
		changeOperation("REMOVE", "GROUP", "group_after", "关联后地区汇总", nil, nil, "删除关联后的地区汇总"),
		changeOperation("UPDATE", "END", "end_1", "最终输出", []string{"input"}, []InputChange{{
			Field: "input", From: PlanInput{Kind: "GROUP", ID: "group_after"}, To: PlanInput{Kind: "JOIN", ID: "join_1"},
		}}, "输出直接连接客户订单关联"),
	)
}

func removePreJoinGroupIntent() ChangeIntent {
	return readyIntent(
		changeOperation("REMOVE", "GROUP", "group_before", "关联前订单汇总", nil, nil, "删除关联前的订单汇总"),
		changeOperation("UPDATE", "JOIN", "join_1", "客户订单关联", []string{"right"}, []InputChange{{
			Field: "right", From: PlanInput{Kind: "GROUP", ID: "group_before"}, To: PlanInput{Kind: "NODE", ID: "node_2"},
		}}, "关联右侧直接连接订单节点"),
	)
}

func removeAllGroupsIntent() ChangeIntent {
	return readyIntent(
		changeOperation("REMOVE", "GROUP", "group_before", "关联前订单汇总", nil, nil, "删除关联前订单汇总"),
		changeOperation("REMOVE", "GROUP", "group_after", "关联后地区汇总", nil, nil, "删除关联后地区汇总"),
		changeOperation("UPDATE", "JOIN", "join_1", "客户订单关联", []string{"right"}, []InputChange{{Field: "right", From: PlanInput{Kind: "GROUP", ID: "group_before"}, To: PlanInput{Kind: "NODE", ID: "node_2"}}}, "关联右侧直接连接订单节点"),
		changeOperation("UPDATE", "END", "end_1", "最终输出", []string{"input"}, []InputChange{{Field: "input", From: PlanInput{Kind: "GROUP", ID: "group_after"}, To: PlanInput{Kind: "JOIN", ID: "join_1"}}}, "输出直接连接关联结果"),
	)
}

func requestEnvelope(t *testing.T, invocation aiplatform.Invocation) plannerPromptEnvelope {
	t.Helper()
	var envelope plannerPromptEnvelope
	if err := json.Unmarshal([]byte(invocation.Request.Messages[1].Parts[0].Text), &envelope); err != nil {
		t.Fatalf("decode prompt envelope: %v", err)
	}
	return envelope
}

func intentRequestEnvelope(t *testing.T, invocation aiplatform.Invocation) intentPromptEnvelope {
	t.Helper()
	var envelope intentPromptEnvelope
	if err := json.Unmarshal([]byte(invocation.Request.Messages[1].Parts[0].Text), &envelope); err != nil {
		t.Fatalf("decode intent prompt envelope: %v", err)
	}
	return envelope
}

func TestServicePlanBuildsAuditedProposalWithoutSamples(t *testing.T) {
	invoker := &fakeInvoker{configured: true, results: []aiplatform.InvocationResult{plannerResult(t, testProposal(), "request-1")}}
	service := NewService(plannerCatalog(), invoker)
	result, err := service.Plan(context.Background(), "tenant-1", "actor-1", "", PlanRequest{Instruction: "关联客户和订单，按地区汇总金额"})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if result.RequestID != "request-1" || result.Proposal.Plan.Dataset.Name != "客户订单汇总" {
		t.Fatalf("Plan() result = %#v", result)
	}
	if len(invoker.inputs) != 1 {
		t.Fatalf("Invoke() count = %d", len(invoker.inputs))
	}
	input := invoker.inputs[0]
	if input.Purpose != aiplatform.PurposeDatasetDAGGeneration || input.ResourceType != "" || input.ResourceID != "" {
		t.Fatalf("invocation audit envelope = %#v", input)
	}
	if err := aiplatform.ValidateProviderRequest(input.Request); err != nil {
		t.Fatalf("planner provider request is invalid: %v", err)
	}
	if bytes.Contains(input.Request.ResponseSchema.Schema, []byte(`"uniqueItems"`)) {
		t.Fatal("dataset proposal schema contains deepseek-v3 unsupported uniqueItems")
	}
	systemText := input.Request.Messages[0].Parts[0].Text
	for _, expected := range []string{
		"数据节点 → 源字段处理 → 关联前分组 → 关联 → 关联后分组 → 输出字段处理 → 结束节点",
		"每个阶段可以是 0 个、1 个或多个组件",
		"必须在 GROUP 前生成独立 DATE_FORMAT TRANSFORM",
		"从 END 逆向遍历到每个 NODE",
	} {
		if !strings.Contains(systemText, expected) {
			t.Fatalf("staged planner prompt does not contain %q: %s", expected, systemText)
		}
	}
	userText := input.Request.Messages[1].Parts[0].Text
	if strings.Contains(userText, "sampleRows") || strings.Contains(userText, "连接凭据") {
		t.Fatalf("planner prompt leaked disallowed context: %s", userText)
	}
}

func TestServicePlanUsesCurrentGraphAndObjectAuditForModification(t *testing.T) {
	proposal := testProposal()
	proposal.Mode = "CREATE" // trusted service must overwrite this model value.
	current := proposal.Plan
	invoker := &fakeInvoker{configured: true, results: []aiplatform.InvocationResult{
		intentResult(t, readyIntent(), "intent-2"),
		plannerResult(t, proposal, "request-2"),
	}}
	service := NewService(plannerCatalog(), invoker)
	result, err := service.Plan(context.Background(), "tenant-1", "actor-1", "dataset-1", PlanRequest{Instruction: "改成内连接", Current: &current})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if result.Proposal.Mode != "MODIFY" {
		t.Fatalf("proposal mode = %q", result.Proposal.Mode)
	}
	if len(invoker.inputs) != 2 || invoker.inputs[0].PromptVersion != IntentPromptVersion || invoker.inputs[1].PromptVersion != PromptVersion {
		t.Fatalf("two-stage invocations = %#v", invoker.inputs)
	}
	if err := aiplatform.ValidateProviderRequest(invoker.inputs[0].Request); err != nil {
		t.Fatalf("intent provider request is invalid: %v", err)
	}
	intentSchema := string(invoker.inputs[0].Request.ResponseSchema.Schema)
	for _, expected := range []string{`"required":["nodeId","tableId","column"]`, `"tableId"`, `"SELECTED_ONLY"`, `"maxItems":1024`} {
		if !strings.Contains(intentSchema, expected) {
			t.Fatalf("intent schema does not contain %q: %s", expected, intentSchema)
		}
	}
	if invoker.inputs[0].Request.MaxOutputTokens != 32768 {
		t.Fatalf("intent output token allowance = %d", invoker.inputs[0].Request.MaxOutputTokens)
	}
	input := invoker.inputs[1]
	if input.ResourceType != "DATASET" || input.ResourceID != "dataset-1" {
		t.Fatalf("resource audit = %q/%q", input.ResourceType, input.ResourceID)
	}
	plannerEnvelope := requestEnvelope(t, input)
	if plannerEnvelope.Instruction != "" || len(plannerEnvelope.ChangeSet.Operations) != 0 {
		t.Fatalf("planner reinterprets natural language or changed locked scope: %#v", plannerEnvelope)
	}
}

func TestServicePlanRepairsOverBroadPostJoinGroupRemoval(t *testing.T) {
	currentProposal := proposalWithGroupsBeforeAndAfterJoin()
	current := currentProposal.Plan
	invoker := &fakeInvoker{configured: true, results: []aiplatform.InvocationResult{
		intentResult(t, removePostJoinGroupIntent(), "intent-post-group"),
		plannerResult(t, proposalAfterRemovingAllGroups(), "request-over-broad"),
		plannerResult(t, proposalAfterRemovingPostJoinGroup(), "request-precise"),
	}}
	service := NewService(plannerCatalog(), invoker)

	result, err := service.Plan(context.Background(), "tenant-1", "actor-1", "dataset-1", PlanRequest{
		Instruction: "取消关联后的分组，关联前的保持不变",
		Current:     &current,
	})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if result.RequestID != "request-precise" || len(invoker.inputs) != 3 {
		t.Fatalf("repair result = %#v, calls = %d", result, len(invoker.inputs))
	}
	if len(result.Proposal.Plan.Groups) != 1 || result.Proposal.Plan.Groups[0].ID != "group_before" {
		t.Fatalf("groups after precise repair = %#v", result.Proposal.Plan.Groups)
	}
	if result.Proposal.Plan.Joins[0].Right != (PlanInput{Kind: "GROUP", ID: "group_before"}) || result.Proposal.Plan.End.Input != (PlanInput{Kind: "JOIN", ID: "join_1"}) {
		t.Fatalf("repaired topology = join %#v, end %#v", result.Proposal.Plan.Joins[0], result.Proposal.Plan.End.Input)
	}
	intentPrompt := invoker.inputs[0].Request.Messages[1].Parts[0].Text
	for _, expected := range []string{"BEFORE_JOIN", "AFTER_JOIN"} {
		if !strings.Contains(intentPrompt, expected) {
			t.Fatalf("intent prompt does not contain %q: %s", expected, intentPrompt)
		}
	}
	prompt := invoker.inputs[1].Request.Messages[1].Parts[0].Text
	for _, expected := range []string{`"componentId":"group_after"`, `"componentId":"end_1"`, `"fields":["input"]`} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("planner prompt does not contain %q: %s", expected, prompt)
		}
	}
	if strings.Contains(prompt, "取消关联后的分组") {
		t.Fatalf("planner prompt retained natural-language instruction: %s", prompt)
	}
	repairMessage := invoker.inputs[2].Request.Messages[len(invoker.inputs[2].Request.Messages)-1].Parts[0].Text
	if !strings.Contains(repairMessage, "group_before") {
		t.Fatalf("repair message does not identify undeclared change: %s", repairMessage)
	}
	if len(result.Proposal.ChangeSet.Operations) != 2 {
		t.Fatalf("canonical change review = %#v", result.Proposal.ChangeSet)
	}
}

func TestServicePlanDiscardsUnrequestedDatasetMetadataRewrite(t *testing.T) {
	current := proposalWithGroupsBeforeAndAfterJoin().Plan
	changed := proposalAfterRemovingPostJoinGroup()
	changed.Plan.Dataset.Name = "模型顺手重命名"
	changed.Plan.Dataset.Description = "模型顺手改写说明"
	invoker := &fakeInvoker{configured: true, results: []aiplatform.InvocationResult{
		intentResult(t, removePostJoinGroupIntent(), "intent-valid"),
		plannerResult(t, changed, "plan-valid"),
	}}

	result, err := NewService(plannerCatalog(), invoker).Plan(context.Background(), "tenant-1", "actor-1", "dataset-1", PlanRequest{
		Instruction: "取消关联后的分组，其他配置保持不变",
		Current:     &current,
	})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if len(invoker.inputs) != 2 || result.Proposal.Plan.Dataset != current.Dataset {
		t.Fatalf("protected dataset metadata = %#v, calls = %d", result.Proposal.Plan.Dataset, len(invoker.inputs))
	}
}

func TestPreserveProtectedDatasetMetadataKeepsGeneratedNameForUnnamedDraft(t *testing.T) {
	current := proposalWithGroupsBeforeAndAfterJoin().Plan
	current.Dataset.Name = ""
	proposal := cloneGraphPlan(current)
	proposal.Dataset.Name = "按地区统计客户数量"
	proposal.Dataset.Description = "模型生成的说明"

	protected := preserveProtectedDatasetMetadata(current, proposal, ChangeSet{Operations: []ChangeOperation{}, FieldChanges: []FieldChange{}})
	if protected.Dataset.Name != "按地区统计客户数量" || protected.Dataset.Description != current.Dataset.Description {
		t.Fatalf("unnamed draft metadata = %#v", protected.Dataset)
	}
}

func TestServicePlanAllowsGeneratedNameForUnnamedDraftWithoutExpandingChangeSet(t *testing.T) {
	current := proposalWithGroupsBeforeAndAfterJoin().Plan
	current.Dataset.Name = ""
	changed := proposalAfterRemovingPostJoinGroup()
	changed.Plan.Dataset.Name = "按地区统计客户数量"
	invoker := &fakeInvoker{configured: true, results: []aiplatform.InvocationResult{
		intentResult(t, removePostJoinGroupIntent(), "intent-valid"),
		plannerResult(t, changed, "plan-valid"),
	}}

	result, err := NewService(plannerCatalog(), invoker).Plan(context.Background(), "tenant-1", "actor-1", "dataset-1", PlanRequest{
		Instruction: "取消关联后的分组，其他配置保持不变",
		Current:     &current,
	})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if result.Proposal.Plan.Dataset.Name != "按地区统计客户数量" || len(result.Proposal.ChangeSet.Operations) != 2 {
		t.Fatalf("unnamed draft result = %#v", result.Proposal)
	}
	for _, operation := range result.Proposal.ChangeSet.Operations {
		if operation.ComponentKind == "DATASET" {
			t.Fatalf("generated draft name expanded changeSet: %#v", result.Proposal.ChangeSet)
		}
	}
}

func TestServicePlanRepairsFieldAddedOnlyToUpstreamHalf(t *testing.T) {
	catalog := plannerCatalog()
	catalog.columns["table-orders"] = append(catalog.columns["table-orders"], asset.Column{
		TableID: "table-orders", ColumnName: "order_date", OrdinalPosition: 3,
		CanonicalType: "DATE", BusinessName: "订单日期", AssetStatus: "ACTIVE",
	})
	currentProposal := proposalWithGroupsBeforeAndAfterJoin()
	current := currentProposal.Plan

	partial := currentProposal
	partial.Summary = "仅在关联前加入订单日期"
	partial.Plan = cloneGraphPlan(current)
	partial.Plan.Nodes[1].SelectedColumns = append(partial.Plan.Nodes[1].SelectedColumns, "order_date")
	partial.Plan.Groups[0].Dimensions = append(partial.Plan.Groups[0].Dimensions, PlanDimension{
		NodeID: "node_2", Column: "order_date", Grouping: "",
	})

	complete := partial
	complete.Summary = "订单日期贯穿前后分组并加入最终输出"
	complete.Plan = cloneGraphPlan(partial.Plan)
	complete.Plan.Groups[1].Dimensions = append(complete.Plan.Groups[1].Dimensions, PlanDimension{
		NodeID: "node_2", Column: "order_date", Grouping: "",
	})
	complete.Plan.End.Outputs = append(complete.Plan.End.Outputs, PlanOutput{
		NodeID: "node_2", Column: "order_date", Name: "订单日期", Code: "order_date",
	})

	intent := readyIntent(
		changeOperation("UPDATE", "NODE", "node_2", "orders", []string{"selectedColumns"}, nil, "选择订单日期"),
		changeOperation("UPDATE", "GROUP", "group_before", "关联前订单汇总", []string{"dimensions"}, nil, "关联前保留原始订单日期"),
		changeOperation("UPDATE", "GROUP", "group_after", "关联后地区汇总", []string{"dimensions"}, nil, "关联后继续保留订单日期"),
		changeOperation("UPDATE", "END", endComponentID, "最终输出", []string{"outputs"}, nil, "在最终结果展示订单日期"),
	)
	intent.ChangeSet.FieldChanges = []FieldChange{{
		Field: FieldBinding{NodeID: "node_2", TableID: "table-orders", Column: "order_date"}, SelectionAction: "ADD", Purpose: "FINAL_OUTPUT",
		GroupUses: []FieldGroupUse{
			{GroupID: "group_before", Role: "DIMENSION", Grouping: ""},
			{GroupID: "group_after", Role: "DIMENSION", Grouping: ""},
		},
		JoinUses:   []FieldJoinUse{},
		OutputUses: []FieldOutputUse{{EndID: endComponentID, Name: "订单日期", Code: "order_date"}},
	}}

	invoker := &fakeInvoker{configured: true, results: []aiplatform.InvocationResult{
		intentResult(t, intent, "intent-add-date"),
		plannerResult(t, partial, "request-upstream-only"),
		plannerResult(t, complete, "request-complete"),
	}}
	result, err := NewService(catalog, invoker).Plan(context.Background(), "tenant-1", "actor-1", "dataset-1", PlanRequest{
		Instruction: "新增订单日期字段，作为原始日期维度并展示到最终结果",
		Current:     &current,
	})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if result.RequestID != "request-complete" || len(invoker.inputs) != 3 {
		t.Fatalf("repair result/calls = %#v/%d", result, len(invoker.inputs))
	}
	intentEnvelope := intentRequestEnvelope(t, invoker.inputs[0])
	foundIntentColumn := false
	for _, table := range intentEnvelope.Assets {
		if table.ID != "table-orders" {
			continue
		}
		for _, column := range table.Columns {
			if column.Name == "order_date" {
				foundIntentColumn = true
			}
		}
	}
	if !foundIntentColumn {
		t.Fatalf("intent assets omitted unselected requested field order_date: %#v", intentEnvelope.Assets)
	}
	intentUserMessage := strings.ToLower(invoker.inputs[0].Request.Messages[1].Parts[0].Text)
	for _, forbidden := range []string{"samplerows", "sample_rows", "credential", "password", "select *", "连接凭据", "样例数据"} {
		if strings.Contains(intentUserMessage, forbidden) {
			t.Fatalf("intent prompt leaked %q: %s", forbidden, intentUserMessage)
		}
	}
	for _, groupID := range []string{"group_before", "group_after"} {
		found := false
		for _, group := range result.Proposal.Plan.Groups {
			if group.ID != groupID {
				continue
			}
			for _, dimension := range group.Dimensions {
				if dimension.NodeID == "node_2" && dimension.Column == "order_date" && dimension.Grouping == "" {
					found = true
				}
			}
		}
		if !found {
			t.Fatalf("order_date missing from %s: %#v", groupID, result.Proposal.Plan.Groups)
		}
	}
	foundOutput := false
	for _, output := range result.Proposal.Plan.End.Outputs {
		if output.NodeID == "node_2" && output.Column == "order_date" {
			foundOutput = true
		}
	}
	if !foundOutput {
		t.Fatalf("order_date missing from END: %#v", result.Proposal.Plan.End.Outputs)
	}
	if !reflect.DeepEqual(result.Proposal.Plan.Joins, current.Joins) {
		t.Fatalf("join changed while repairing field propagation: %#v", result.Proposal.Plan.Joins)
	}
	if len(result.Proposal.ChangeSet.FieldChanges) != 1 || result.Proposal.ChangeSet.FieldChanges[0].Purpose != "FINAL_OUTPUT" {
		t.Fatalf("canonical fieldChanges = %#v", result.Proposal.ChangeSet.FieldChanges)
	}
}

func TestServicePlanRepairsOverBroadPreJoinGroupRemoval(t *testing.T) {
	currentProposal := proposalWithGroupsBeforeAndAfterJoin()
	current := currentProposal.Plan
	invoker := &fakeInvoker{configured: true, results: []aiplatform.InvocationResult{
		intentResult(t, removePreJoinGroupIntent(), "intent-pre-group"),
		plannerResult(t, proposalAfterRemovingAllGroups(), "request-over-broad"),
		plannerResult(t, proposalAfterRemovingPreJoinGroup(), "request-precise"),
	}}
	service := NewService(plannerCatalog(), invoker)

	result, err := service.Plan(context.Background(), "tenant-1", "actor-1", "dataset-1", PlanRequest{Instruction: "删除关联前的分组", Current: &current})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if len(invoker.inputs) != 3 || len(result.Proposal.Plan.Groups) != 1 || result.Proposal.Plan.Groups[0].ID != "group_after" {
		t.Fatalf("repair result = %#v, calls = %d", result, len(invoker.inputs))
	}
	if result.Proposal.Plan.Joins[0].Right != (PlanInput{Kind: "NODE", ID: "node_2"}) {
		t.Fatalf("pre-join group removal did not rewire its consumer: %#v", result.Proposal.Plan.Joins[0])
	}
}

func TestServicePlanFailsClosedWhenRepairStillDeletesProtectedGroup(t *testing.T) {
	currentProposal := proposalWithGroupsBeforeAndAfterJoin()
	current := currentProposal.Plan
	invoker := &fakeInvoker{configured: true, results: []aiplatform.InvocationResult{
		intentResult(t, removePostJoinGroupIntent(), "intent-post-group"),
		plannerResult(t, proposalAfterRemovingAllGroups(), "request-over-broad-1"),
		plannerResult(t, proposalAfterRemovingAllGroups(), "request-over-broad-2"),
	}}
	service := NewService(plannerCatalog(), invoker)

	_, err := service.Plan(context.Background(), "tenant-1", "actor-1", "dataset-1", PlanRequest{Instruction: "移除关联后汇总", Current: &current})
	if !errors.Is(err, ErrInvalidOutput) || len(invoker.inputs) != 3 {
		t.Fatalf("Plan() error = %v, calls = %d; want closed failure after one repair", err, len(invoker.inputs))
	}
}

func TestServicePlanAllowsExplicitRemovalOfAllGroups(t *testing.T) {
	currentProposal := proposalWithGroupsBeforeAndAfterJoin()
	current := currentProposal.Plan
	invoker := &fakeInvoker{configured: true, results: []aiplatform.InvocationResult{
		intentResult(t, removeAllGroupsIntent(), "intent-remove-all"),
		plannerResult(t, proposalAfterRemovingAllGroups(), "request-remove-all"),
	}}
	service := NewService(plannerCatalog(), invoker)

	result, err := service.Plan(context.Background(), "tenant-1", "actor-1", "dataset-1", PlanRequest{Instruction: "删除所有分组", Current: &current})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if len(invoker.inputs) != 2 || len(result.Proposal.Plan.Groups) != 0 {
		t.Fatalf("remove-all result = %#v, calls = %d", result, len(invoker.inputs))
	}
}

func TestServicePlanStopsForAmbiguousModificationBeforePlanner(t *testing.T) {
	current := proposalWithGroupsBeforeAndAfterJoin().Plan
	clarify := ChangeIntent{
		Status:   "CLARIFY",
		Question: "当前有多个分组，请说明要删除 group_before 还是 group_after。",
		Candidates: []ComponentRef{
			{ComponentKind: "GROUP", ComponentID: "group_before"},
			{ComponentKind: "GROUP", ComponentID: "group_after"},
		},
		ChangeSet: ChangeSet{Operations: []ChangeOperation{}},
	}
	invoker := &fakeInvoker{configured: true, results: []aiplatform.InvocationResult{
		intentResult(t, clarify, "intent-clarify"),
	}}
	_, err := NewService(plannerCatalog(), invoker).Plan(context.Background(), "tenant-1", "actor-1", "dataset-1", PlanRequest{
		Instruction: "删除那个汇总",
		Current:     &current,
	})
	var clarificationErr *ClarificationRequiredError
	if !errors.As(err, &clarificationErr) || clarificationErr.Question != clarify.Question || len(invoker.inputs) != 1 {
		t.Fatalf("clarification error/calls = %v/%d", err, len(invoker.inputs))
	}
}

func TestServicePlanRepairsInvalidChangeIntentOnce(t *testing.T) {
	current := proposalWithGroupsBeforeAndAfterJoin().Plan
	invalidIntent := removePostJoinGroupIntent()
	invalidIntent.ChangeSet.Operations[0].ComponentID = "group_missing"
	invoker := &fakeInvoker{configured: true, results: []aiplatform.InvocationResult{
		intentResult(t, invalidIntent, "intent-invalid"),
		intentResult(t, removePostJoinGroupIntent(), "intent-repaired"),
		plannerResult(t, proposalAfterRemovingPostJoinGroup(), "plan-valid"),
	}}

	result, err := NewService(plannerCatalog(), invoker).Plan(context.Background(), "tenant-1", "actor-1", "dataset-1", PlanRequest{
		Instruction: "取消关联后的分组，关联前的保持不变",
		Current:     &current,
	})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if result.RequestID != "plan-valid" || len(invoker.inputs) != 3 {
		t.Fatalf("intent repair result/calls = %#v/%d", result, len(invoker.inputs))
	}
	if invoker.inputs[0].PromptVersion != IntentPromptVersion || invoker.inputs[1].PromptVersion != IntentPromptVersion || invoker.inputs[2].PromptVersion != PromptVersion {
		t.Fatalf("intent repair prompt versions = %#v", invoker.inputs)
	}
	repairMessages := invoker.inputs[1].Request.Messages
	repairText := repairMessages[len(repairMessages)-1].Parts[0].Text
	for _, expected := range []string{InvalidOutputReasonGroup, InvalidOutputStageIntentValidation, "group_missing", "READY 仍不能可靠成立时返回 CLARIFY"} {
		if !strings.Contains(repairText, expected) {
			t.Fatalf("intent repair message does not contain %q: %s", expected, repairText)
		}
	}
}

func TestServicePlanReportsIntentRepairMetadataWhenSecondIntentStillInvalid(t *testing.T) {
	current := proposalWithGroupsBeforeAndAfterJoin().Plan
	invalidIntent := removePostJoinGroupIntent()
	invalidIntent.ChangeSet.Operations[0].ComponentID = "group_missing"
	invoker := &fakeInvoker{configured: true, results: []aiplatform.InvocationResult{
		intentResult(t, invalidIntent, "intent-invalid-1"),
		intentResult(t, invalidIntent, "intent-invalid-2"),
	}}

	_, err := NewService(plannerCatalog(), invoker).Plan(context.Background(), "tenant-1", "actor-1", "dataset-1", PlanRequest{
		Instruction: "取消关联后的分组",
		Current:     &current,
	})
	var invalid *InvalidOutputError
	if !errors.As(err, &invalid) || invalid.ReasonCode != InvalidOutputReasonGroup || invalid.Stage != InvalidOutputStageIntentValidation || !invalid.RepairAttempted || invalid.RequestID != "intent-invalid-2" {
		t.Fatalf("intent repair metadata = %#v, error = %v", invalid, err)
	}
	if len(invoker.inputs) != 2 {
		t.Fatalf("intent repair calls = %d", len(invoker.inputs))
	}
}

func TestServicePlanRejectsExistingDatasetModificationWithoutCurrentBaseline(t *testing.T) {
	invoker := &fakeInvoker{configured: true}
	_, err := NewService(plannerCatalog(), invoker).Plan(context.Background(), "tenant-1", "actor-1", "dataset-1", PlanRequest{Instruction: "删除分组"})
	if !errors.Is(err, ErrInvalidRequest) || len(invoker.inputs) != 0 {
		t.Fatalf("missing baseline error/calls = %v/%d", err, len(invoker.inputs))
	}
}

func TestDecodePlannerResultRejectsPlannerChangeSetOverride(t *testing.T) {
	proposal := testProposal()
	proposal.ChangeSet = ChangeSet{Operations: []ChangeOperation{
		changeOperation("REMOVE", "GROUP", "group_1", "地区订单汇总", nil, nil, "扩大修改范围"),
	}}
	payload, err := json.Marshal(proposal)
	if err != nil {
		t.Fatal(err)
	}
	_, err = decodePlannerResult(aiplatform.InvocationResult{ProviderResult: aiplatform.ProviderResult{Content: payload}}, "MODIFY", testCatalog(), nil)
	if !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("planner changeSet override error = %v", err)
	}
}

func TestServicePlanRepairsDomainInvalidOutputOnce(t *testing.T) {
	invalid := testProposal()
	invalid.Plan.Nodes[0].TableID = "unknown"
	invoker := &fakeInvoker{configured: true, results: []aiplatform.InvocationResult{
		plannerResult(t, invalid, "request-invalid"),
		plannerResult(t, testProposal(), "request-repaired"),
	}}
	service := NewService(plannerCatalog(), invoker)
	result, err := service.Plan(context.Background(), "tenant-1", "actor-1", "", PlanRequest{Instruction: "生成客户订单汇总"})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if result.RequestID != "request-repaired" || len(invoker.inputs) != 2 {
		t.Fatalf("repair result = %#v, calls = %d", result, len(invoker.inputs))
	}
	messages := invoker.inputs[1].Request.Messages
	if messages[len(messages)-1].Role != aiplatform.MessageRoleUser || !strings.Contains(messages[len(messages)-1].Parts[0].Text, "可信边界校验") {
		t.Fatalf("repair message = %#v", messages[len(messages)-1])
	}
}

func TestServicePlanRepairsSyntheticCountFieldUsingExactAuthorizedIdentifier(t *testing.T) {
	invalid := monthlyRegionalOrderCountProposal()
	invalid.Plan.Nodes[0].SelectedColumns[0] = "COUNT(*)"
	invalid.Plan.Groups[0].Metrics[0].Column = "COUNT(*)"
	invalid.Plan.End.Outputs[2].Column = "COUNT(*)"
	valid := monthlyRegionalOrderCountProposal()
	invoker := &fakeInvoker{configured: true, results: []aiplatform.InvocationResult{
		plannerResult(t, invalid, "request-count-invalid"),
		plannerResult(t, valid, "request-count-repaired"),
	}}
	hints := &PlanHints{
		PreferredTableIDs: []string{"table-orders", "table-customers"},
		Aggregation:       "COUNT",
		MeasureFields:     []PlanFieldHint{{TableID: "table-orders", Column: "ORDER_ID"}},
		TimeField:         &PlanFieldHint{TableID: "table-orders", Column: "CREATED_AT"},
		DimensionFields:   []PlanFieldHint{{TableID: "table-customers", Column: "region_code"}},
		TimeGrain:         "MONTH",
	}
	result, err := NewService(monthlyRegionalOrderCountAssetCatalog(), invoker).Plan(context.Background(), "tenant-1", "actor-1", "", PlanRequest{
		Instruction: "基于订单表和客户表创建月度各区域交易量",
		Hints:       hints,
	})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if result.RequestID != "request-count-repaired" || len(invoker.inputs) != 2 || result.Proposal.Plan.Groups[0].Metrics[0] != (PlanMetric{NodeID: "node_1", Column: "ORDER_ID", Aggregation: "COUNT"}) {
		t.Fatalf("repair result/calls = %#v/%d", result, len(invoker.inputs))
	}
	repairMessage := invoker.inputs[1].Request.Messages[len(invoker.inputs[1].Request.Messages)-1].Parts[0].Text
	for _, expected := range []string{InvalidOutputReasonAggregationField, "禁止 *", "非空 IDENTIFIER"} {
		if !strings.Contains(repairMessage, expected) {
			t.Fatalf("repair message does not contain %q: %s", expected, repairMessage)
		}
	}
	envelope := requestEnvelope(t, invoker.inputs[0])
	if envelope.Hints == nil || !reflect.DeepEqual(*envelope.Hints, *hints) {
		t.Fatalf("provider hints = %#v, want %#v", envelope.Hints, hints)
	}
	schema := string(invoker.inputs[0].Request.ResponseSchema.Schema)
	for _, exact := range []string{`"oneOf"`, `"const":"table-orders"`, `"ORDER_ID"`, `"customer_id"`, `"region_code"`} {
		if !strings.Contains(schema, exact) {
			t.Fatalf("proposal schema does not contain %q: %s", exact, schema)
		}
	}
}

func TestServicePlanReturnsFinalRepairClassificationAndRequestID(t *testing.T) {
	first := monthlyRegionalOrderCountProposal()
	first.Plan.Nodes[0].SelectedColumns[0] = "COUNT(*)"
	first.Plan.Groups[0].Metrics[0].Column = "COUNT(*)"
	first.Plan.End.Outputs[2].Column = "COUNT(*)"
	second := monthlyRegionalOrderCountProposal()
	second.Plan.Nodes[0].SelectedColumns[0] = "order_id"
	second.Plan.Groups[0].Metrics[0].Column = "order_id"
	second.Plan.End.Outputs[2].Column = "order_id"
	invoker := &fakeInvoker{configured: true, results: []aiplatform.InvocationResult{
		plannerResult(t, first, "request-count-invalid"),
		plannerResult(t, second, "request-case-invalid"),
	}}
	_, err := NewService(monthlyRegionalOrderCountAssetCatalog(), invoker).Plan(context.Background(), "tenant-1", "actor-1", "", PlanRequest{Instruction: "统计月度各区域订单量"})
	var invalid *InvalidOutputError
	if !errors.As(err, &invalid) {
		t.Fatalf("Plan() error = %v, want InvalidOutputError", err)
	}
	if invalid.ReasonCode != InvalidOutputReasonFieldCaseMismatch || invalid.Stage != InvalidOutputStagePlanValidation || !invalid.RepairAttempted || invalid.RequestID != "request-case-invalid" {
		t.Fatalf("final invalid output metadata = %#v", invalid)
	}
}

func TestServicePlanRevalidatesHintedTablesAndFields(t *testing.T) {
	tests := []struct {
		name  string
		hints *PlanHints
	}{
		{name: "unknown table", hints: &PlanHints{PreferredTableIDs: []string{"not-authorized"}}},
		{name: "wrong field case", hints: &PlanHints{MeasureFields: []PlanFieldHint{{TableID: "table-orders", Column: "order_id"}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invoker := &fakeInvoker{configured: true}
			_, err := NewService(monthlyRegionalOrderCountAssetCatalog(), invoker).Plan(context.Background(), "tenant-1", "actor-1", "", PlanRequest{Instruction: "统计订单量", Hints: test.hints})
			if !errors.Is(err, ErrInvalidRequest) || len(invoker.inputs) != 0 {
				t.Fatalf("Plan() error/calls = %v/%d, want rejected before model invoke", err, len(invoker.inputs))
			}
		})
	}
}

func TestServicePlanRejectsStaleAssetContext(t *testing.T) {
	catalog := plannerCatalog()
	catalog.getHash = map[string]string{"table-orders": "changed"}
	invoker := &fakeInvoker{configured: true, results: []aiplatform.InvocationResult{plannerResult(t, testProposal(), "request-3")}}
	service := NewService(catalog, invoker)
	_, err := service.Plan(context.Background(), "tenant-1", "actor-1", "", PlanRequest{Instruction: "生成客户订单汇总"})
	if !errors.Is(err, ErrContextStale) {
		t.Fatalf("Plan() error = %v, want ErrContextStale", err)
	}
}

func TestServicePlanFailsClosedWithoutProviderOrAssets(t *testing.T) {
	service := NewService(plannerCatalog(), &fakeInvoker{configured: false})
	if _, err := service.Plan(context.Background(), "tenant-1", "actor-1", "", PlanRequest{Instruction: "生成"}); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("Plan() error = %v, want ErrProviderUnavailable", err)
	}

	service = NewService(&fakeCatalog{}, &fakeInvoker{configured: true})
	if _, err := service.Plan(context.Background(), "tenant-1", "actor-1", "", PlanRequest{Instruction: "生成"}); !errors.Is(err, ErrNoAssets) {
		t.Fatalf("Plan() error = %v, want ErrNoAssets", err)
	}
}

func TestServicePlanDirectLoadsCurrentTableBeyondFirstSearchPage(t *testing.T) {
	tables := make([]asset.Table, 201)
	columns := make(map[string][]asset.Column, len(tables))
	for index := range tables {
		id := fmt.Sprintf("table-%03d", index)
		tables[index] = asset.Table{
			ID: id, DataSourceID: "source-1", DataSourceName: "source", DataSourceType: "MYSQL",
			SchemaName: "demo", TableName: fmt.Sprintf("table_%03d", index), BusinessName: fmt.Sprintf("表%03d", index),
			AssetStatus: "ACTIVE", ManagementStatus: "ENABLED", EnrichmentStatus: "SUCCEEDED", StructureHash: "hash-" + id,
		}
		columns[id] = []asset.Column{{TableID: id, ColumnName: "id", OrdinalPosition: 1, CanonicalType: "INTEGER", AssetStatus: "ACTIVE"}}
	}
	for index := 0; index < 200; index++ {
		columns["table-200"] = append(columns["table-200"], asset.Column{TableID: "table-200", ColumnName: fmt.Sprintf("unused_%03d", index), OrdinalPosition: index + 2, CanonicalType: "STRING", AssetStatus: "ACTIVE"})
	}
	columns["table-200"] = append(columns["table-200"], asset.Column{TableID: "table-200", ColumnName: "late_required_field", OrdinalPosition: 202, CanonicalType: "INTEGER", AssetStatus: "ACTIVE"})
	catalog := &fakeCatalog{tables: tables, columns: columns}
	proposal := singleTableProposal("table-200", "late_required_field")
	current := proposal.Plan
	invoker := &fakeInvoker{configured: true, results: []aiplatform.InvocationResult{
		intentResult(t, readyIntent(), "intent-page-201"),
		plannerResult(t, proposal, "request-page-201"),
	}}
	service := NewService(catalog, invoker)

	result, err := service.Plan(context.Background(), "tenant-1", "actor-1", "dataset-1", PlanRequest{Instruction: "保留当前流程", Current: &current})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if result.Proposal.Mode != "MODIFY" || len(catalog.searches) < 2 {
		t.Fatalf("mode/search pages = %q/%d", result.Proposal.Mode, len(catalog.searches))
	}
	envelope := requestEnvelope(t, invoker.inputs[1])
	found := false
	for _, table := range envelope.Assets {
		if table.ID == "table-200" {
			for _, column := range table.Columns {
				if column.Name == "late_required_field" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatalf("current table was omitted from prompt assets: %#v", envelope.Assets)
	}
}

func TestServicePlanPrunesCatalogToProviderByteBudget(t *testing.T) {
	catalog := plannerCatalog()
	for tableIndex := 0; tableIndex < 24; tableIndex++ {
		id := fmt.Sprintf("table-extra-%02d", tableIndex)
		catalog.tables = append(catalog.tables, asset.Table{
			ID: id, DataSourceID: "source-1", DataSourceName: "CRM", DataSourceType: "MYSQL", SchemaName: "demo",
			TableName: fmt.Sprintf("extra_%02d", tableIndex), BusinessName: fmt.Sprintf("扩展表%02d", tableIndex),
			BusinessDescription: strings.Repeat("业务说明", 30), AssetStatus: "ACTIVE", ManagementStatus: "ENABLED", EnrichmentStatus: "SUCCEEDED", StructureHash: "hash-" + id,
		})
		for columnIndex := 0; columnIndex < 80; columnIndex++ {
			catalog.columns[id] = append(catalog.columns[id], asset.Column{
				TableID: id, ColumnName: fmt.Sprintf("field_%03d", columnIndex), OrdinalPosition: columnIndex + 1,
				CanonicalType: "STRING", BusinessDescription: strings.Repeat("字段业务说明", 20), AssetStatus: "ACTIVE",
			})
		}
	}
	const budget = 64 << 10
	invoker := &fakeInvoker{configured: true, results: []aiplatform.InvocationResult{plannerResult(t, testProposal(), "request-budget")}}
	service := NewService(catalog, invoker, ServiceOptions{Timeout: time.Second, MaxProviderInputBytes: budget})

	result, err := service.Plan(context.Background(), "tenant-1", "actor-1", "", PlanRequest{Instruction: "关联客户和订单，按地区汇总金额"})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	payload, err := json.Marshal(invoker.inputs[0].Request)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload)*providerRedactionHeadroom > budget {
		t.Fatalf("provider request bytes with headroom = %d, budget = %d", len(payload)*providerRedactionHeadroom, budget)
	}
	envelope := requestEnvelope(t, invoker.inputs[0])
	if len(envelope.Assets) >= len(catalog.tables) || catalogColumnCount(envelope.Assets) >= 24*80 {
		t.Fatalf("catalog was not pruned: tables=%d columns=%d", len(envelope.Assets), catalogColumnCount(envelope.Assets))
	}
	if len(result.Proposal.Warnings) == 0 || strings.Contains(strings.Join(result.Proposal.Warnings, " "), "写出表名") {
		t.Fatalf("bounded catalog warning = %#v", result.Proposal.Warnings)
	}
}

func TestServicePlanRejectsCurrentContextThatCannotFitBudget(t *testing.T) {
	catalog := plannerCatalog()
	table := catalog.tables[0]
	selected := make([]string, 0, 100)
	catalog.columns[table.ID] = nil
	for index := 0; index < 100; index++ {
		name := fmt.Sprintf("required_field_%03d_%s", index, strings.Repeat("x", 70))
		selected = append(selected, name)
		catalog.columns[table.ID] = append(catalog.columns[table.ID], asset.Column{TableID: table.ID, ColumnName: name, OrdinalPosition: index + 1, CanonicalType: "STRING", AssetStatus: "ACTIVE"})
	}
	current := GraphPlan{
		Dataset: PlanDataset{Name: "大字段集"}, Nodes: []PlanNode{{ID: "node_1", TableID: table.ID, Alias: "source", SelectedColumns: selected}},
		Joins: []PlanJoin{}, Groups: []PlanGroup{}, End: PlanEnd{Name: "最终输出", Input: PlanInput{Kind: "NODE", ID: "node_1"}, Outputs: []PlanOutput{}},
	}
	invoker := &fakeInvoker{configured: true}
	service := NewService(catalog, invoker, ServiceOptions{Timeout: time.Second, MaxProviderInputBytes: 12 << 10})
	_, err := service.Plan(context.Background(), "tenant-1", "actor-1", "dataset-1", PlanRequest{Instruction: "保留全部字段", Current: &current})
	if !errors.Is(err, ErrInvalidRequest) || len(invoker.inputs) != 0 {
		t.Fatalf("Plan() error/calls = %v/%d, want oversized current rejected before invoke", err, len(invoker.inputs))
	}
}

func TestServicePlanUsesOneDeadlineAcrossRepair(t *testing.T) {
	invalid := testProposal()
	invalid.Plan.Nodes[0].TableID = "unknown"
	invalidResult := plannerResult(t, invalid, "request-invalid")
	invoker := &fakeInvoker{configured: true}
	invoker.invoke = func(ctx context.Context, _ aiplatform.Invocation, index int) (aiplatform.InvocationResult, error) {
		if index == 0 {
			select {
			case <-time.After(30 * time.Millisecond):
				return invalidResult, nil
			case <-ctx.Done():
				return aiplatform.InvocationResult{}, ctx.Err()
			}
		}
		<-ctx.Done()
		return aiplatform.InvocationResult{}, ctx.Err()
	}
	service := NewService(plannerCatalog(), invoker, ServiceOptions{Timeout: 50 * time.Millisecond, MaxProviderInputBytes: defaultProviderInputBytes})
	started := time.Now()
	_, err := service.Plan(context.Background(), "tenant-1", "actor-1", "", PlanRequest{Instruction: "生成客户订单汇总"})
	if !errors.Is(err, context.DeadlineExceeded) || len(invoker.inputs) != 2 {
		t.Fatalf("Plan() error/calls = %v/%d", err, len(invoker.inputs))
	}
	if elapsed := time.Since(started); elapsed > 200*time.Millisecond {
		t.Fatalf("overall planner deadline was not shared, elapsed=%s", elapsed)
	}
}

func TestServicePlanOmitsRawInvalidBodyWhenRepairWouldExceedBudget(t *testing.T) {
	invalid := testProposal()
	invalid.Summary = strings.Repeat("x", 12000)
	invoker := &fakeInvoker{configured: true, results: []aiplatform.InvocationResult{
		plannerResult(t, invalid, "request-invalid"),
		plannerResult(t, testProposal(), "request-repaired"),
	}}
	// The staged workflow checklist intentionally makes the trusted system prompt larger.
	// Keep enough room for the base request while still proving the 12 KiB invalid body is omitted.
	service := NewService(plannerCatalog(), invoker, ServiceOptions{Timeout: time.Second, MaxProviderInputBytes: 48 << 10})
	if _, err := service.Plan(context.Background(), "tenant-1", "actor-1", "", PlanRequest{Instruction: "生成客户订单汇总"}); err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	for _, message := range invoker.inputs[1].Request.Messages {
		if message.Role == aiplatform.MessageRoleAssistant {
			t.Fatal("repair request retained raw invalid provider body despite byte budget")
		}
	}
}

func TestServicePlanCapsServerCatalogWarning(t *testing.T) {
	catalog := plannerCatalog()
	catalog.total = 999
	proposal := testProposal()
	proposal.Warnings = make([]string, maxProposalWarnings)
	for index := range proposal.Warnings {
		proposal.Warnings[index] = fmt.Sprintf("模型警告 %d", index+1)
	}
	invoker := &fakeInvoker{configured: true, results: []aiplatform.InvocationResult{plannerResult(t, proposal, "request-warning")}}
	result, err := NewService(catalog, invoker).Plan(context.Background(), "tenant-1", "actor-1", "", PlanRequest{Instruction: "生成客户订单汇总"})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if len(result.Proposal.Warnings) != maxProposalWarnings || !strings.Contains(result.Proposal.Warnings[maxProposalWarnings-1], "控制模型上下文") {
		t.Fatalf("warnings = %#v", result.Proposal.Warnings)
	}
}

func TestHybridCatalogOrderingAndTags(t *testing.T) {
	tables := []asset.Table{
		{ID: "orders", TableName: "orders", BusinessName: "销售订单", Tags: []string{"销售", "订单"}},
		{ID: "regions", TableName: "regions", BusinessName: "区域", Tags: []string{"区域", "维度"}},
		{ID: "stores", TableName: "stores", BusinessName: "门店", Tags: []string{"门店", "区域"}},
	}
	ranked := rankCatalogTablesByRetrieval(tables, []string{"regions", "stores", "orders"}, nil, "每个月各区域销售")
	if got := []string{ranked[0].ID, ranked[1].ID, ranked[2].ID}; !reflect.DeepEqual(got, []string{"regions", "stores", "orders"}) {
		t.Fatalf("hybrid order=%#v", got)
	}
	tokenSet := map[string]bool{}
	for _, token := range meaningfulTokens("每个月各区域的销售情况") {
		tokenSet[token] = true
	}
	if !tokenSet["区域"] || !tokenSet["销售"] {
		t.Fatalf("Chinese retrieval tokens=%#v", tokenSet)
	}
	candidate := catalogCandidate{table: tables[0], columns: []asset.Column{{ID: "amount", ColumnName: "amount", Tags: []string{"销售额", "金额"}}}}
	catalog := catalogTable(candidate, 1)
	if !reflect.DeepEqual(catalog.Tags, tables[0].Tags) || !reflect.DeepEqual(catalog.Columns[0].Tags, candidate.columns[0].Tags) {
		t.Fatalf("catalog tags were not propagated: %#v", catalog)
	}
}

func TestServiceWiresHybridRetrieverIntoCatalogLoading(t *testing.T) {
	retriever := &fakeAssetRetriever{result: assetembedding.RetrievalResult{
		TableIDs: []string{"table-orders", "table-customers"}, TableScores: map[string]float64{"table-orders": 0.03},
		ColumnScores: map[string]float64{}, EmbeddingReady: true,
	}}
	invoker := &fakeInvoker{configured: true, results: []aiplatform.InvocationResult{
		plannerResult(t, singleTableProposal("table-orders", "amount"), "request-hybrid"),
	}}
	service := NewService(plannerCatalog(), invoker, ServiceOptions{
		Timeout: time.Second, MaxProviderInputBytes: defaultProviderInputBytes,
		Retriever: retriever, RetrievalMode: "HYBRID",
	})
	if _, err := service.Plan(context.Background(), "tenant-1", "actor-1", "", PlanRequest{Instruction: "生成订单金额数据集"}); err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if retriever.calls != 1 {
		t.Fatalf("retriever calls = %d, want 1", retriever.calls)
	}
	envelope := requestEnvelope(t, invoker.inputs[0])
	if len(envelope.Assets) == 0 || envelope.Assets[0].ID != "table-orders" {
		t.Fatalf("hybrid catalog order = %#v", envelope.Assets)
	}
}
