package metadataai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

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
}

func (i *providerInvoker) Configured() bool   { return i.configured }
func (*providerInvoker) ProviderName() string { return "test-provider" }
func (*providerInvoker) Model() string        { return "test-model" }
func (i *providerInvoker) Invoke(_ context.Context, invocation aiplatform.Invocation) (aiplatform.InvocationResult, error) {
	i.invocation = invocation
	i.invocations = append(i.invocations, invocation)
	index := len(i.invocations) - 1
	if i.onInvoke != nil {
		i.onInvoke(index)
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
	} {
		if !bytes.Contains(invoker.invocation.Request.ResponseSchema.Schema, fragment) {
			t.Fatalf("元数据输出 Schema 缺少动态约束 %s: %s", fragment, invoker.invocation.Request.ResponseSchema.Schema)
		}
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

func TestOrchestratedProviderRepairsSchemaInvalidOutputWithoutRawAssistantMessage(t *testing.T) {
	input, validOutput := validCompletion()
	validContent, err := json.Marshal(validOutput)
	if err != nil {
		t.Fatal(err)
	}
	invalidErr := &aiplatform.ProviderError{Code: aiplatform.ErrorCodeInvalidOutput, Message: "safe invalid output"}
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
	if len(secondMessages) != len(firstMessages)+1 || secondMessages[len(firstMessages)].Role != aiplatform.MessageRoleUser {
		t.Fatalf("无可用原始输出时不应伪造 assistant 消息: %#v", secondMessages)
	}
	if !messageContains(secondMessages[len(firstMessages)], "每个 ID 恰好出现一次") {
		t.Fatalf("纠错请求未明确完整 targetId 约束: %#v", secondMessages[len(firstMessages)])
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

func TestOrchestratedProviderDoesNotRetryValidOutput(t *testing.T) {
	input, output := validCompletion()
	content, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	invoker := &providerInvoker{
		configured: true,
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

func TestOrchestratedProviderReturnsRepairTransportFailureWithoutThirdAttempt(t *testing.T) {
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

	_, err = NewOrchestratedProvider(invoker).Complete(context.Background(), "tenant-1", "actor-1", input)
	if !errors.Is(err, upstreamErr) {
		t.Fatalf("error=%v, want 第二次调用错误 %v", err, upstreamErr)
	}
	if errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("第二次网络错误被首次校验错误污染: %v", err)
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
