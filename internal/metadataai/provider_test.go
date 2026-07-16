package metadataai

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	aiplatform "intelligent-report-generation-system/internal/ai"
)

type providerInvoker struct {
	configured bool
	result     aiplatform.InvocationResult
	invocation aiplatform.Invocation
	err        error
}

func (i *providerInvoker) Configured() bool   { return i.configured }
func (*providerInvoker) ProviderName() string { return "test-provider" }
func (*providerInvoker) Model() string        { return "test-model" }
func (i *providerInvoker) Invoke(_ context.Context, invocation aiplatform.Invocation) (aiplatform.InvocationResult, error) {
	i.invocation = invocation
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
