package metadataai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	aiplatform "intelligent-report-generation-system/internal/ai"
)

type providerInvoker struct {
	configured  bool
	result      aiplatform.InvocationResult
	results     []aiplatform.InvocationResult
	invocation  aiplatform.Invocation
	invocations []aiplatform.Invocation
	err         error
	errs        []error
	onInvoke    func(int)
	models      string
	delays      []time.Duration
}

func (i *providerInvoker) Configured() bool   { return i.configured }
func (*providerInvoker) ProviderName() string { return "test-provider" }
func (i *providerInvoker) Model() string {
	if i.models != "" {
		return i.models
	}
	return "test-model"
}
func (i *providerInvoker) Invoke(ctx context.Context, invocation aiplatform.Invocation) (aiplatform.InvocationResult, error) {
	i.invocation = invocation
	i.invocations = append(i.invocations, invocation)
	index := len(i.invocations) - 1
	if i.onInvoke != nil {
		i.onInvoke(index)
	}
	if index < len(i.delays) && i.delays[index] > 0 {
		timer := time.NewTimer(i.delays[index])
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return aiplatform.InvocationResult{}, ctx.Err()
		}
	}
	if index < len(i.results) || index < len(i.errs) {
		var result aiplatform.InvocationResult
		if index < len(i.results) {
			result = i.results[index]
		}
		var err error
		if index < len(i.errs) {
			err = i.errs[index]
		}
		return result, err
	}
	return i.result, i.err
}

func TestOrchestratedProviderBuildsMinimalRequestAndParsesUsage(t *testing.T) {
	input, output := validCompletion()
	input.SourceFormat = SourceFormatCSV
	input.Table.Name = "销售订单"
	input.Columns[0].Name, input.Columns[1].Name = "客户名称", "订单金额"
	input.SampleRows = []map[string]any{{"客户名称": "华东智造有限公司", "订单金额": 16320}}
	content, _ := json.Marshal(output)
	invoker := &providerInvoker{configured: true, result: aiplatform.InvocationResult{ProviderResult: aiplatform.ProviderResult{
		Content: content, Model: "model-v1", Usage: aiplatform.Usage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30},
	}}}
	provider := NewOrchestratedProvider(invoker)
	result, err := provider.Complete(context.Background(), "tenant-1", "actor-1", input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Model != "model-v1" || result.Usage.TotalTokens != 30 {
		t.Fatalf("result = %#v", result)
	}
	if invoker.invocation.TenantID != "tenant-1" || invoker.invocation.ActorID != "actor-1" || invoker.invocation.Purpose != aiplatform.PurposeMetadataCompletion {
		t.Fatalf("调用身份或用途未传入通用编排层: %#v", invoker.invocation)
	}
	if len(invoker.invocation.Request.Messages) != 2 || invoker.invocation.Request.ResponseSchema.Name != "metadata_completion" {
		t.Fatalf("模型请求未保持最小结构化合同: %#v", invoker.invocation.Request)
	}
	if !messageContains(invoker.invocation.Request.Messages[0], "真实表头") || !messageContains(invoker.invocation.Request.Messages[0], "字段业务描述") ||
		!messageContains(invoker.invocation.Request.Messages[0], "sourceFormat=CSV 或 EXCEL") || !messageContains(invoker.invocation.Request.Messages[0], "中文业务名称") ||
		!messageContains(invoker.invocation.Request.Messages[0], "原表头含中文") || !messageContains(invoker.invocation.Request.Messages[0], "原表头为英文") ||
		!messageContains(invoker.invocation.Request.Messages[0], "中文描述") ||
		!messageContains(invoker.invocation.Request.Messages[0], "最多十行") || !messageContains(invoker.invocation.Request.Messages[0], "候选关联键") ||
		!messageContains(invoker.invocation.Request.Messages[0], "适用范围") ||
		!messageContains(invoker.invocation.Request.Messages[0], "semanticType 必须与输入 canonicalType 兼容") {
		t.Fatalf("系统提示未要求结合 Sheet 表头和内容完成映射: %#v", invoker.invocation.Request.Messages[0])
	}
	if !messageContains(invoker.invocation.Request.Messages[1], "客户名称") || !messageContains(invoker.invocation.Request.Messages[1], "华东智造有限公司") {
		t.Fatalf("Sheet 表头或真实内容未进入模型用户消息: %#v", invoker.invocation.Request.Messages[1])
	}
	if err := aiplatform.ValidateProviderRequest(invoker.invocation.Request); err != nil {
		t.Fatalf("元数据输出 Schema 不满足通用严格合同: %v", err)
	}
	if bytes.Contains(invoker.invocation.Request.ResponseSchema.Schema, []byte(`"uniqueItems"`)) {
		t.Fatal("元数据输出 Schema 包含 deepseek-v3 不支持的 uniqueItems")
	}
	for _, fragment := range [][]byte{
		[]byte(`"const":"table-1"`),
		[]byte(`"enum":["column-1","column-2"]`),
		[]byte(`"minItems":2`),
		[]byte(`"maxItems":2`),
		[]byte(`"description":"文件表中文业务名称"`),
		[]byte(`"description":"文件字段中文业务名称：中文表头保持中文含义，英文表头翻译为中文"`),
		[]byte(`"description":"文件字段中文业务描述，可包含 ID、SKU 等英文缩写"`),
	} {
		if !bytes.Contains(invoker.invocation.Request.ResponseSchema.Schema, fragment) {
			t.Fatalf("元数据输出 Schema 缺少动态约束 %s: %s", fragment, invoker.invocation.Request.ResponseSchema.Schema)
		}
	}
}

type batchingProviderInvoker struct {
	mu            sync.Mutex
	active        int
	maxActive     int
	calls         int
	tableCalls    int
	batchSizes    []int
	sampleWidths  []int
	contextWidths []int
	failColumnID  string
	invocationLag time.Duration
}

func (*batchingProviderInvoker) Configured() bool     { return true }
func (*batchingProviderInvoker) ProviderName() string { return "batch-test" }
func (*batchingProviderInvoker) Model() string        { return "batch-model" }

func (i *batchingProviderInvoker) Invoke(
	ctx context.Context,
	invocation aiplatform.Invocation,
) (aiplatform.InvocationResult, error) {
	var input CompletionInput
	if len(invocation.Request.Messages) < 2 ||
		len(invocation.Request.Messages[1].Parts) != 1 ||
		json.Unmarshal(
			[]byte(invocation.Request.Messages[1].Parts[0].Text), &input,
		) != nil {
		return aiplatform.InvocationResult{}, errors.New("invalid batch test input")
	}
	i.mu.Lock()
	i.active++
	if i.active > i.maxActive {
		i.maxActive = i.active
	}
	i.calls++
	if input.TargetTable {
		i.tableCalls++
	}
	i.batchSizes = append(i.batchSizes, len(input.Columns))
	i.contextWidths = append(i.contextWidths, len(input.ContextColumns))
	for _, row := range input.SampleRows {
		i.sampleWidths = append(i.sampleWidths, len(row))
	}
	i.mu.Unlock()
	defer func() {
		i.mu.Lock()
		i.active--
		i.mu.Unlock()
	}()
	if i.invocationLag > 0 {
		timer := time.NewTimer(i.invocationLag)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return aiplatform.InvocationResult{}, ctx.Err()
		}
	}
	for _, column := range input.Columns {
		if column.ID == i.failColumnID {
			return aiplatform.InvocationResult{}, errors.New("batch provider unavailable")
		}
	}
	output := CompletionOutput{
		SchemaVersion: SchemaVersion,
		Columns:       make([]SuggestionValue, 0, len(input.Columns)),
	}
	if input.TargetTable {
		output.Table = &SuggestionValue{
			TargetID: input.Table.ID, BusinessName: "员工信息",
			BusinessDescription: "员工信息明细表",
			Tags:                []string{"领域:企业", "作用:主数据"},
			SensitivityLevel:    "INTERNAL", Confidence: 0.95,
		}
	}
	for _, column := range input.Columns {
		output.Columns = append(output.Columns, SuggestionValue{
			TargetID: column.ID, BusinessName: "字段" + column.ID,
			BusinessDescription: "员工字段业务含义",
			Tags:                []string{"作用:辅助信息"},
			SensitivityLevel:    "INTERNAL", SemanticType: "TEXT",
			Confidence: 0.95,
		})
	}
	content, err := json.Marshal(output)
	if err != nil {
		return aiplatform.InvocationResult{}, err
	}
	return aiplatform.InvocationResult{ProviderResult: aiplatform.ProviderResult{
		Content: content, Model: "batch-model",
		Usage: aiplatform.Usage{
			PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3,
		},
	}}, nil
}

func batchedCompletionInput(columnCount int) CompletionInput {
	input := CompletionInput{
		SchemaVersion: SchemaVersion,
		SourceFormat:  SourceFormatExcel,
		TargetTable:   true,
		Table:         Target{ID: "table-batch", Name: "员工列表"},
		Columns:       make([]Target, 0, columnCount),
		SampleRows:    []map[string]any{{}},
	}
	for index := 1; index <= columnCount; index++ {
		id := fmt.Sprintf("column-%02d", index)
		name := fmt.Sprintf("字段%02d", index)
		input.Columns = append(input.Columns, Target{
			ID: id, Name: name, Kind: "COLUMN", CanonicalType: "TEXT",
		})
		input.SampleRows[0][name] = index
	}
	return input
}

func TestOrchestratedProviderCompletesLargeTableInBoundedParallelBatches(t *testing.T) {
	input := batchedCompletionInput(57)
	invoker := &batchingProviderInvoker{invocationLag: 20 * time.Millisecond}
	provider := NewOrchestratedProvider(invoker)

	result, err := provider.Complete(
		context.Background(), "tenant-1", "actor-1", input,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateOutput(input, result.Output); err != nil {
		t.Fatalf("merged output is invalid: %v", err)
	}
	if len(result.Output.Columns) != 57 ||
		result.Output.Columns[0].TargetID != "column-01" ||
		result.Output.Columns[56].TargetID != "column-57" {
		t.Fatalf("merged column order=%#v", result.Output.Columns)
	}
	if invoker.calls != 12 || invoker.tableCalls != 1 ||
		invoker.maxActive != metadataCompletionBatchConcurrency {
		t.Fatalf(
			"calls=%d tableCalls=%d maxActive=%d",
			invoker.calls, invoker.tableCalls, invoker.maxActive,
		)
	}
	for _, size := range invoker.batchSizes {
		if size < 1 || size > metadataCompletionBatchColumns {
			t.Fatalf("batch size=%d, want 1..%d", size, metadataCompletionBatchColumns)
		}
	}
	for _, width := range invoker.sampleWidths {
		if width > metadataCompletionBatchColumns {
			t.Fatalf(
				"sample width=%d, want <=%d",
				width, metadataCompletionBatchColumns,
			)
		}
	}
	for _, width := range invoker.contextWidths {
		if width != 57 {
			t.Fatalf("context width=%d, want 57", width)
		}
	}
	if result.Usage != (Usage{
		PromptTokens: 12, CompletionTokens: 24, TotalTokens: 36,
	}) {
		t.Fatalf("merged usage=%#v", result.Usage)
	}
}

func TestOrchestratedProviderReturnsSuccessfulBatchesAsPartialOutput(t *testing.T) {
	input := batchedCompletionInput(5)
	invoker := &batchingProviderInvoker{failColumnID: "column-03"}
	provider := NewOrchestratedProvider(invoker)
	provider.batchColumns = 2
	provider.batchConcurrency = 3
	provider.batchSemaphore = make(chan struct{}, 3)

	result, err := provider.Complete(
		context.Background(), "tenant-1", "actor-1", input,
	)
	var partial *PartialOutputError
	if !errors.As(err, &partial) || partial.MissingTargets != 2 {
		t.Fatalf("error=%v, want two missing targets", err)
	}
	if result.Output.Table == nil || len(result.Output.Columns) != 3 {
		t.Fatalf("partial output=%#v", result.Output)
	}
	if err := validatePartialOutput(input, result.Output); err != nil {
		t.Fatalf("partial output cannot be persisted: %v", err)
	}
}

func TestOrchestratedProviderRepairsDuplicateTargetIDWithCorrectionContext(t *testing.T) {
	input, validOutput := validCompletion()
	invalidOutput := validOutput
	invalidOutput.Columns = append([]SuggestionValue(nil), validOutput.Columns...)
	invalidOutput.Columns[1].TargetID = invalidOutput.Columns[0].TargetID
	invalidContent, err := json.Marshal(invalidOutput)
	if err != nil {
		t.Fatal(err)
	}
	validContent, err := json.Marshal(validOutput)
	if err != nil {
		t.Fatal(err)
	}
	invoker := &providerInvoker{
		configured: true,
		results: []aiplatform.InvocationResult{
			{ProviderResult: aiplatform.ProviderResult{Content: invalidContent, Model: "model-v1", Usage: aiplatform.Usage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30}}},
			{ProviderResult: aiplatform.ProviderResult{Content: validContent, Model: "model-v1", Usage: aiplatform.Usage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3}}},
		},
	}

	result, err := NewOrchestratedProvider(invoker).Complete(context.Background(), "tenant-1", "actor-1", input)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateOutput(input, result.Output); err != nil {
		t.Fatalf("修复后的结果仍然非法: %v", err)
	}
	if result.Usage != (Usage{PromptTokens: 11, CompletionTokens: 22, TotalTokens: 33}) {
		t.Fatalf("纠错后的累计 usage=%#v", result.Usage)
	}
	if len(invoker.invocations) != 2 {
		t.Fatalf("调用次数=%d, want 2", len(invoker.invocations))
	}
	firstMessages := invoker.invocations[0].Request.Messages
	secondMessages := invoker.invocations[1].Request.Messages
	if len(secondMessages) != len(firstMessages)+2 {
		t.Fatalf("纠错请求 messages=%#v", secondMessages)
	}
	for index := range firstMessages {
		if !messagesEqual(firstMessages[index], secondMessages[index]) {
			t.Fatalf("纠错请求未保留原始上下文: first=%#v second=%#v", firstMessages, secondMessages)
		}
	}
	invalidMessage := secondMessages[len(firstMessages)]
	correctionMessage := secondMessages[len(firstMessages)+1]
	if invalidMessage.Role != aiplatform.MessageRoleAssistant || len(invalidMessage.Parts) != 1 || invalidMessage.Parts[0].Text != string(invalidContent) {
		t.Fatalf("纠错请求未携带模型的非法原始输出: %#v", invalidMessage)
	}
	if correctionMessage.Role != aiplatform.MessageRoleUser || !messageContains(correctionMessage, "duplicates targetId") {
		t.Fatalf("纠错请求未携带本地校验原因: %#v", correctionMessage)
	}
}

func TestOrchestratedProviderUsesDeepSeekOnlyAfterMiniMaxValidationFailure(t *testing.T) {
	input, validOutput := validCompletion()
	invalidOutput := validOutput
	invalidOutput.Columns = append(
		[]SuggestionValue(nil),
		validOutput.Columns...,
	)
	invalidOutput.Columns[1].TargetID = invalidOutput.Columns[0].TargetID
	invalidContent, err := json.Marshal(invalidOutput)
	if err != nil {
		t.Fatal(err)
	}
	validContent, err := json.Marshal(validOutput)
	if err != nil {
		t.Fatal(err)
	}
	invoker := &providerInvoker{
		configured: true,
		models:     "MiniMax-M2,deepseek-v3",
		results: []aiplatform.InvocationResult{
			{ProviderResult: aiplatform.ProviderResult{
				Content: invalidContent,
				Model:   "MiniMax-M2",
				Usage: aiplatform.Usage{
					PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30,
				},
			}},
			{ProviderResult: aiplatform.ProviderResult{
				Content: validContent,
				Model:   "deepseek-v3",
				Usage: aiplatform.Usage{
					PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3,
				},
			}},
		},
	}

	result, err := NewOrchestratedProviderWithPrimaryFailover(
		invoker,
		time.Second,
	).Complete(context.Background(), "tenant-1", "actor-1", input)
	if err != nil {
		t.Fatal(err)
	}
	if len(invoker.invocations) != 2 ||
		invoker.invocations[0].PreferredModel != "" ||
		invoker.invocations[1].PreferredModel != "deepseek-v3" {
		t.Fatalf("invocations=%#v", invoker.invocations)
	}
	if result.Model != "MiniMax-M2,deepseek-v3" ||
		result.Usage != (Usage{
			PromptTokens: 11, CompletionTokens: 22, TotalTokens: 33,
		}) {
		t.Fatalf("result=%#v", result)
	}
}

func TestOrchestratedProviderFallsBackAfterPrimaryDeadline(t *testing.T) {
	input, output := validCompletion()
	content, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	invoker := &providerInvoker{
		configured: true,
		models:     "MiniMax-M2,deepseek-v3",
		delays:     []time.Duration{time.Second, 0},
		results: []aiplatform.InvocationResult{
			{},
			{ProviderResult: aiplatform.ProviderResult{
				Content: content,
				Model:   "deepseek-v3",
			}},
		},
	}

	result, err := NewOrchestratedProviderWithPrimaryFailover(
		invoker,
		10*time.Millisecond,
	).Complete(context.Background(), "tenant-1", "actor-1", input)
	if err != nil {
		t.Fatal(err)
	}
	if len(invoker.invocations) != 2 ||
		invoker.invocations[1].PreferredModel != "deepseek-v3" {
		t.Fatalf("invocations=%#v", invoker.invocations)
	}
	if len(invoker.invocations[1].Request.Messages) !=
		len(invoker.invocations[0].Request.Messages) {
		t.Fatal("transport failover unexpectedly included repair content")
	}
	if result.Model != "deepseek-v3" {
		t.Fatalf("result model=%q", result.Model)
	}
}

func TestOrchestratedProviderRepairsSchemaInvalidOutputWithSafeValidationContext(t *testing.T) {
	input, validOutput := validCompletion()
	validContent, err := json.Marshal(validOutput)
	if err != nil {
		t.Fatal(err)
	}
	invalidOutput := validOutput
	invalidOutput.Columns = append([]SuggestionValue(nil), validOutput.Columns...)
	invalidOutput.Columns[0].SemanticType = "PHONE"
	invalidContent, err := json.Marshal(invalidOutput)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := json.Marshal(outputSchema(input))
	if err != nil {
		t.Fatal(err)
	}
	_, invalidErr := aiplatform.ValidateStructuredOutput(
		aiplatform.JSONSchema{Name: "metadata_completion", Schema: schema},
		invalidContent,
	)
	if invalidErr == nil {
		t.Fatal("测试前置条件错误：非法语义类型通过了 Schema")
	}
	invoker := &providerInvoker{
		configured: true,
		results: []aiplatform.InvocationResult{
			{},
			{ProviderResult: aiplatform.ProviderResult{Content: validContent, Model: "model-v1"}},
		},
		errs: []error{invalidErr, nil},
	}

	result, err := NewOrchestratedProvider(invoker).Complete(context.Background(), "tenant-1", "actor-1", input)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateOutput(input, result.Output); err != nil {
		t.Fatalf("纠错后的结果仍然非法: %v", err)
	}
	if len(invoker.invocations) != 2 {
		t.Fatalf("调用次数=%d, want 2", len(invoker.invocations))
	}
	firstMessages := invoker.invocations[0].Request.Messages
	secondMessages := invoker.invocations[1].Request.Messages
	if len(secondMessages) != len(firstMessages)+2 || secondMessages[len(firstMessages)].Role != aiplatform.MessageRoleAssistant {
		t.Fatalf("纠错请求未携带内存中的非法原始输出: %#v", secondMessages)
	}
	if secondMessages[len(firstMessages)].Parts[0].Text != string(invalidContent) {
		t.Fatalf("纠错请求非法输出不匹配: %#v", secondMessages[len(firstMessages)])
	}
	correction := secondMessages[len(firstMessages)+1]
	if !messageContains(correction, "$.columns[0].semanticType is outside enum") ||
		!messageContains(correction, "每个 ID 恰好出现一次") {
		t.Fatalf("纠错请求未携带精确校验原因和完整 targetId 约束: %#v", correction)
	}
}

func TestOrchestratedProviderReturnsInvalidOutputWhenRepairIsStillInvalid(t *testing.T) {
	input, output := validCompletion()
	output.Columns[1].TargetID = output.Columns[0].TargetID
	content, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	invoker := &providerInvoker{
		configured: true,
		results: []aiplatform.InvocationResult{
			{ProviderResult: aiplatform.ProviderResult{Content: content}},
			{ProviderResult: aiplatform.ProviderResult{Content: content}},
		},
	}

	_, err = NewOrchestratedProvider(invoker).Complete(context.Background(), "tenant-1", "actor-1", input)
	if !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("error=%v, want ErrInvalidOutput", err)
	}
	if len(invoker.invocations) != 2 {
		t.Fatalf("调用次数=%d, want 2", len(invoker.invocations))
	}
}

func TestOrchestratedProviderReturnsOnlyValidTargetsFromPartialRepair(t *testing.T) {
	input, output := validCompletion()
	invalid := output
	invalid.Columns = append([]SuggestionValue(nil), output.Columns...)
	invalid.Columns[1].TargetID = invalid.Columns[0].TargetID
	invalidContent, err := json.Marshal(invalid)
	if err != nil {
		t.Fatal(err)
	}
	partial := output
	partial.Columns = append([]SuggestionValue(nil), output.Columns[:1]...)
	partialContent, err := json.Marshal(partial)
	if err != nil {
		t.Fatal(err)
	}
	invoker := &providerInvoker{
		configured: true,
		results: []aiplatform.InvocationResult{
			{ProviderResult: aiplatform.ProviderResult{Content: invalidContent}},
			{ProviderResult: aiplatform.ProviderResult{Content: partialContent}},
		},
	}

	result, err := NewOrchestratedProvider(invoker).Complete(
		context.Background(), "tenant-1", "actor-1", input,
	)
	var partialErr *PartialOutputError
	if !errors.As(err, &partialErr) || partialErr.MissingTargets != 1 {
		t.Fatalf("error=%v, want one missing target", err)
	}
	if result.Output.Table == nil || len(result.Output.Columns) != 1 ||
		result.Output.Columns[0].TargetID != input.Columns[0].ID {
		t.Fatalf("partial result=%#v", result.Output)
	}
}

func TestPartialCompletionMergesValidPropertiesThenTargetsOnlyWhatRemains(t *testing.T) {
	input := CompletionInput{
		SchemaVersion: SchemaVersion,
		TargetTable:   false,
		Columns: []Target{{
			ID:                 "column-1",
			Kind:               "COLUMN",
			CanonicalType:      "TEXT",
			CurrentSensitivity: "INTERNAL",
		}},
	}
	firstContent := json.RawMessage(`{
		"schemaVersion":"1.1",
		"columns":[{
			"targetId":"column-1",
			"businessName":"客户类型",
			"businessDescription":"用于区分客户所属业务分类",
			"tags":null,
			"sensitivityLevel":"INTERNAL",
			"semanticType":"CATEGORY",
			"confidence":0.93
		}]
	}`)
	first, missing, ok := decodePartialCompletion(input, firstContent)
	if !ok || missing != 1 || len(first.Columns) != 1 {
		t.Fatalf("first partial ok=%v missing=%d output=%#v", ok, missing, first)
	}
	if first.Columns[0].Complete ||
		first.Columns[0].BusinessName != "客户类型" ||
		first.Columns[0].BusinessDescription == "" ||
		first.Columns[0].SemanticType != "CATEGORY" {
		t.Fatalf("first merged value=%#v", first.Columns[0])
	}

	input.Columns[0].CurrentBusinessName = first.Columns[0].BusinessName
	input.Columns[0].CurrentDescription = first.Columns[0].BusinessDescription
	input.Columns[0].CurrentSensitivity = first.Columns[0].SensitivityLevel
	input.Columns[0].CurrentSemanticType = first.Columns[0].SemanticType
	input.Columns[0].MissingFields = completionMissingFields(
		input, input.Columns[0], suggestionFromTarget(input.Columns[0]), true,
	)
	if len(input.Columns[0].MissingFields) != 1 ||
		input.Columns[0].MissingFields[0] != "tags" {
		t.Fatalf("remaining fields=%#v", input.Columns[0].MissingFields)
	}
	secondContent := json.RawMessage(`{
		"schemaVersion":"1.1",
		"columns":[{
			"targetId":"column-1",
			"businessName":"",
			"businessDescription":"",
			"tags":["作用:维度表"],
			"sensitivityLevel":"",
			"semanticType":"",
			"confidence":0.95
		}]
	}`)
	second, missing, ok := decodePartialCompletion(input, secondContent)
	if !ok || missing != 0 || len(second.Columns) != 1 ||
		!second.Columns[0].Complete {
		t.Fatalf("second partial ok=%v missing=%d output=%#v", ok, missing, second)
	}
	if second.Columns[0].BusinessName != "客户类型" ||
		second.Columns[0].BusinessDescription == "" ||
		second.Columns[0].SemanticType != "CATEGORY" ||
		len(second.Columns[0].Tags) != 1 {
		t.Fatalf("confirmed fields were not preserved: %#v", second.Columns[0])
	}
}

func TestOrchestratedProviderDoesNotRetryValidOutput(t *testing.T) {
	input, output := validCompletion()
	content, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	invoker := &providerInvoker{
		configured: true,
		models:     "MiniMax-M2,deepseek-v3",
		results:    []aiplatform.InvocationResult{{ProviderResult: aiplatform.ProviderResult{Content: content}}},
	}

	result, err := NewOrchestratedProvider(invoker).Complete(context.Background(), "tenant-1", "actor-1", input)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateOutput(input, result.Output); err != nil {
		t.Fatal(err)
	}
	if len(invoker.invocations) != 1 {
		t.Fatalf("有效输出调用次数=%d, want 1", len(invoker.invocations))
	}
	if invoker.invocations[0].PreferredModel != "" {
		t.Fatalf(
			"有效 MiniMax 输出不应触发后备模型: %#v",
			invoker.invocations[0],
		)
	}
}

func TestOrchestratedProviderDoesNotRepairTransportFailures(t *testing.T) {
	input, _ := validCompletion()
	tests := []struct {
		name string
		err  error
		want error
	}{
		{
			name: "Provider 暂时不可用",
			err:  &aiplatform.ProviderError{Code: aiplatform.ErrorCodeProviderUnavailable, Message: "provider unavailable", Retryable: true},
		},
		{
			name: "Provider 超时",
			err:  &aiplatform.ProviderError{Code: aiplatform.ErrorCodeTimeout, Message: "provider timeout", Retryable: true, Cause: context.DeadlineExceeded},
			want: context.DeadlineExceeded,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invoker := &providerInvoker{configured: true, err: test.err}
			_, err := NewOrchestratedProvider(invoker).Complete(context.Background(), "tenant-1", "actor-1", input)
			if !errors.Is(err, test.err) {
				t.Fatalf("error=%v, want 保留原始错误 %v", err, test.err)
			}
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("error=%v, want %v", err, test.want)
			}
			if errors.Is(err, ErrInvalidOutput) {
				t.Fatalf("网络错误被污染为 ErrInvalidOutput: %v", err)
			}
			if len(invoker.invocations) != 1 {
				t.Fatalf("网络错误调用次数=%d, want 1", len(invoker.invocations))
			}
		})
	}
}

func TestOrchestratedProviderPreservesSafePartialOutputWhenRepairTransportFails(t *testing.T) {
	input, output := validCompletion()
	output.Columns[1].TargetID = output.Columns[0].TargetID
	invalidContent, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	upstreamErr := &aiplatform.ProviderError{
		Code: aiplatform.ErrorCodeProviderUnavailable, Message: "provider unavailable", Retryable: true,
	}
	invoker := &providerInvoker{
		configured: true,
		results: []aiplatform.InvocationResult{
			{ProviderResult: aiplatform.ProviderResult{Content: invalidContent}},
			{},
		},
		errs: []error{nil, upstreamErr},
	}

	result, err := NewOrchestratedProvider(invoker).Complete(context.Background(), "tenant-1", "actor-1", input)
	var partial *PartialOutputError
	if !errors.As(err, &partial) || partial.MissingTargets != 2 {
		t.Fatalf("error=%v, want 缺少 2 个目标的部分输出", err)
	}
	if result.Output.Table == nil || len(result.Output.Columns) != 0 {
		t.Fatalf("safe partial output=%#v", result.Output)
	}
	if len(invoker.invocations) != 2 {
		t.Fatalf("调用次数=%d, want 2", len(invoker.invocations))
	}
}

func TestOrchestratedProviderDoesNotRepairAfterContextCancellation(t *testing.T) {
	input, output := validCompletion()
	output.Columns[1].TargetID = output.Columns[0].TargetID
	invalidContent, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	invoker := &providerInvoker{
		configured: true,
		results:    []aiplatform.InvocationResult{{ProviderResult: aiplatform.ProviderResult{Content: invalidContent}}},
		onInvoke: func(index int) {
			if index == 0 {
				cancel()
			}
		},
	}

	_, err = NewOrchestratedProvider(invoker).Complete(ctx, "tenant-1", "actor-1", input)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v, want context.Canceled", err)
	}
	if len(invoker.invocations) != 1 {
		t.Fatalf("context 取消后调用次数=%d, want 1", len(invoker.invocations))
	}
}

func messagesEqual(left, right aiplatform.Message) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func messageContains(message aiplatform.Message, fragment string) bool {
	for _, part := range message.Parts {
		if part.Type == aiplatform.ContentTypeText && bytes.Contains([]byte(part.Text), []byte(fragment)) {
			return true
		}
	}
	return false
}

func TestOutputSchemaSupportsColumnOnlyAndTableOnlyScopes(t *testing.T) {
	input, _ := validCompletion()
	input.TargetTable = false
	input.Columns = input.Columns[:1]
	columnOnly, err := json.Marshal(outputSchema(input))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(columnOnly, []byte(`"table"`)) || !bytes.Contains(columnOnly, []byte(`"enum":["column-1"]`)) {
		t.Fatalf("字段级增量 Schema 范围错误: %s", columnOnly)
	}

	input.TargetTable = true
	input.Columns = []Target{}
	tableOnly, err := json.Marshal(outputSchema(input))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(tableOnly, []byte(`"table"`)) || bytes.Contains(tableOnly, []byte(`"enum":[]`)) {
		t.Fatalf("仅表级 Schema 非法: %s", tableOnly)
	}
	request := aiplatform.ProviderRequest{
		Messages:       []aiplatform.Message{{Role: aiplatform.MessageRoleUser, Parts: []aiplatform.ContentPart{{Type: aiplatform.ContentTypeText, Text: "{}"}}}},
		ResponseSchema: aiplatform.JSONSchema{Name: "metadata_completion", Schema: tableOnly},
	}
	if err := aiplatform.ValidateProviderRequest(request); err != nil {
		t.Fatalf("仅表级 Schema 不满足严格合同: %v", err)
	}
}

func TestOrchestratedProviderRejectsUnknownStructuredFields(t *testing.T) {
	input, _ := validCompletion()
	invoker := &providerInvoker{configured: true, result: aiplatform.InvocationResult{ProviderResult: aiplatform.ProviderResult{
		Content: json.RawMessage(`{"schemaVersion":"1.0","table":{},"columns":[],"invented":true}`),
	}}}
	provider := NewOrchestratedProvider(invoker)
	if _, err := provider.Complete(context.Background(), "tenant-1", "actor-1", input); err == nil {
		t.Fatal("未知结构化字段未被拒绝")
	}
}

func TestOrchestratedProviderReportsUnconfiguredInvoker(t *testing.T) {
	provider := NewOrchestratedProvider(&providerInvoker{})
	input, _ := validCompletion()
	if _, err := provider.Complete(context.Background(), "tenant-1", "actor-1", input); err != ErrProviderUnavailable {
		t.Fatalf("error=%v, want ErrProviderUnavailable", err)
	}
}

func TestOrchestratedProviderPreservesPublishedErrorContract(t *testing.T) {
	input, _ := validCompletion()
	tests := []struct {
		name string
		err  error
		want error
	}{
		{
			name: "超时",
			err:  &aiplatform.ProviderError{Code: aiplatform.ErrorCodeTimeout, Message: "safe timeout"},
			want: context.DeadlineExceeded,
		},
		{
			name: "非法结构化输出",
			err:  &aiplatform.ProviderError{Code: aiplatform.ErrorCodeInvalidOutput, Message: "safe invalid output"},
			want: ErrInvalidOutput,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := NewOrchestratedProvider(&providerInvoker{configured: true, err: test.err})
			_, err := provider.Complete(context.Background(), "tenant-1", "actor-1", input)
			if !errors.Is(err, test.want) || !errors.Is(err, test.err) {
				t.Fatalf("error=%v, want %v 且保留原错误", err, test.want)
			}
		})
	}
}

func TestMergeProviderModelsPreservesDistinctLoadBalancedModels(t *testing.T) {
	model := mergeProviderModels("MiniMax-M2", "deepseek-v3,MiniMax-M2")
	if model != "MiniMax-M2,deepseek-v3" {
		t.Fatalf("model=%q", model)
	}
}
