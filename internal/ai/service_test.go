package ai

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

type serviceStore struct {
	startInput StartRequest
	startErr   error
	completed  *CompletionRecord
	failed     *FailureRecord
}

func (s *serviceStore) Start(_ context.Context, input StartRequest) (RequestRecord, error) {
	s.startInput = input
	if s.startErr != nil {
		return RequestRecord{}, s.startErr
	}
	return RequestRecord{ID: "request-1"}, nil
}
func (s *serviceStore) Complete(_ context.Context, _, _ string, record CompletionRecord) error {
	s.completed = &record
	return nil
}
func (s *serviceStore) Fail(_ context.Context, _, _ string, record FailureRecord) error {
	s.failed = &record
	return nil
}

type serviceProvider struct {
	results  []ProviderResult
	errors   []error
	requests []ProviderRequest
	complete func(context.Context, ProviderRequest) (ProviderResult, error)
}

func (*serviceProvider) Name() string     { return "test-provider" }
func (*serviceProvider) Model() string    { return "test-model" }
func (*serviceProvider) Configured() bool { return true }
func (p *serviceProvider) Complete(ctx context.Context, request ProviderRequest) (ProviderResult, error) {
	p.requests = append(p.requests, request)
	if p.complete != nil {
		return p.complete(ctx, request)
	}
	index := len(p.requests) - 1
	var result ProviderResult
	if index < len(p.results) {
		result = p.results[index]
	}
	if index < len(p.errors) {
		return result, p.errors[index]
	}
	return result, nil
}

func TestServiceRedactsInputAndSettlesUsageCost(t *testing.T) {
	store := &serviceStore{}
	provider := &serviceProvider{results: []ProviderResult{{
		Content: json.RawMessage(`{}`), Model: "model-v2", RequestID: "upstream-1",
		Usage: Usage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120},
	}}}
	service := newTestService(t, store, provider)
	secret := "sk-" + strings.Repeat("A", 20)
	invocation := testInvocation(`请分析 {"password":"secret-value","name":"企业"}，临时密钥 ` + secret)
	invocation.Request.ResponseSchema.Description = "响应合同临时密钥 " + secret
	result, err := service.Invoke(context.Background(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	if result.RequestID != "request-1" || result.RedactionCount != 3 || result.CostMicros != 140 {
		t.Fatalf("result=%#v", result)
	}
	if len(provider.requests) != 1 || provider.requests[0].Messages[0].Parts[0].Text != `请分析 {"password":"[已脱敏]","name":"企业"}，临时密钥 [已脱敏密钥]` {
		t.Fatalf("Provider 收到未脱敏输入: %#v", provider.requests)
	}
	if provider.requests[0].ResponseSchema.Description != "响应合同临时密钥 [已脱敏密钥]" {
		t.Fatalf("Provider 收到未脱敏 Schema 说明: %#v", provider.requests[0].ResponseSchema)
	}
	if store.startInput.InputHash == "" || store.startInput.RedactionCount != 3 || store.startInput.ReservedTokens <= 128 || store.startInput.ReservedCostMicros <= result.CostMicros {
		t.Fatalf("配额预留摘要无效: %#v", store.startInput)
	}
	if store.completed == nil || store.completed.ProviderRequestID != hashOptionalAuditIdentifier("upstream-1") || store.completed.ProviderModel != "test-model" || store.completed.CostMicros != 140 || store.failed != nil {
		t.Fatalf("成功审计未正确收口: complete=%#v fail=%#v", store.completed, store.failed)
	}
	if result.ProviderResult.Model != "test-model" || result.ProviderResult.RequestID != hashOptionalAuditIdentifier("upstream-1") {
		t.Fatalf("上游可控审计摘要未被收敛: %#v", result.ProviderResult)
	}
}

func TestServiceRetriesOnlyRetryableProviderErrors(t *testing.T) {
	store := &serviceStore{}
	provider := &serviceProvider{
		results: []ProviderResult{{}, {Content: json.RawMessage(`{}`), Usage: Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12}}},
		errors:  []error{newProviderError(ErrorCodeRateLimited, "模型限流", 429, true, time.Second, nil), nil},
	}
	service := newTestService(t, store, provider)
	service.wait = func(context.Context, time.Duration) error { return nil }
	result, err := service.Invoke(context.Background(), testInvocation("生成报告"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Attempts != 2 || len(provider.requests) != 2 || store.completed == nil || store.completed.Attempts != 2 {
		t.Fatalf("有限重试结果无效: result=%#v calls=%d completed=%#v", result, len(provider.requests), store.completed)
	}
}

func TestServiceRejectsMissingProviderUsageAndKeepsReservation(t *testing.T) {
	store := &serviceStore{}
	provider := &serviceProvider{results: []ProviderResult{{Content: json.RawMessage(`{}`)}}}
	service := newTestService(t, store, provider)
	_, err := service.Invoke(context.Background(), testInvocation("生成报告"))
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) || providerErr.Code != ErrorCodeInvalidResponse {
		t.Fatalf("error=%v", err)
	}
	if store.failed == nil || store.completed != nil || store.startInput.ReservedTokens <= 0 {
		t.Fatalf("缺失 Usage 未保留失败关闭预留: start=%#v failed=%#v completed=%#v", store.startInput, store.failed, store.completed)
	}
}

func TestServiceRedactsUntrustedProviderAuditFields(t *testing.T) {
	store := &serviceStore{}
	provider := &serviceProvider{results: []ProviderResult{{
		Content: json.RawMessage(`{}`), Model: "模型内嵌敏感正文", RequestID: "Bearer provider-secret-value",
		FinishReason: "secret finish reason", Usage: Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12},
	}}}
	service := newTestService(t, store, provider)
	result, err := service.Invoke(context.Background(), testInvocation("生成报告"))
	if err != nil {
		t.Fatal(err)
	}
	if result.ProviderResult.Model != "test-model" || result.ProviderResult.FinishReason != "other" ||
		result.ProviderResult.RequestID != hashOptionalAuditIdentifier("Bearer provider-secret-value") {
		t.Fatalf("审计字段未安全规范化: %#v", result.ProviderResult)
	}
}

func TestServiceRecordsStableFailureWithoutRetryingRejectedRequest(t *testing.T) {
	store := &serviceStore{}
	provider := &serviceProvider{errors: []error{newProviderError(ErrorCodeProviderRejected, "模型拒绝请求", 400, false, 0, nil)}}
	service := newTestService(t, store, provider)
	result, err := service.Invoke(context.Background(), testInvocation("生成报告"))
	if err == nil || result.Attempts != 1 || len(provider.requests) != 1 {
		t.Fatalf("result=%#v error=%v", result, err)
	}
	if store.failed == nil || store.failed.ErrorCode != string(ErrorCodeProviderRejected) || store.completed != nil {
		t.Fatalf("失败审计未正确收口: %#v", store.failed)
	}
}

func TestServiceRecordsCanceledRequestWithStableCode(t *testing.T) {
	store := &serviceStore{}
	provider := &serviceProvider{errors: []error{context.Canceled}}
	service := newTestService(t, store, provider)
	_, err := service.Invoke(context.Background(), testInvocation("生成报告"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	if store.failed == nil || store.failed.ErrorCode != string(ErrorCodeCanceled) || store.completed != nil {
		t.Fatalf("取消审计未正确收口: failed=%#v completed=%#v", store.failed, store.completed)
	}
}

func TestServiceRejectsLateSuccessAfterAttemptTimeout(t *testing.T) {
	store := &serviceStore{}
	provider := &serviceProvider{complete: func(ctx context.Context, _ ProviderRequest) (ProviderResult, error) {
		<-ctx.Done()
		return ProviderResult{
			Content: json.RawMessage(`{}`),
			Usage:   Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12},
		}, nil
	}}
	service := newTestService(t, store, provider)
	service.options.AttemptTimeout = 10 * time.Millisecond
	service.options.MaxAttempts = 1
	_, err := service.Invoke(context.Background(), testInvocation("生成报告"))
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) || providerErr.Code != ErrorCodeTimeout {
		t.Fatalf("error=%v", err)
	}
	if store.failed == nil || store.completed != nil {
		t.Fatalf("超时后的伪成功被结算: failed=%#v completed=%#v", store.failed, store.completed)
	}
}

func TestServiceStopsBeforeProviderWhenTenantQuotaIsExceeded(t *testing.T) {
	store := &serviceStore{startErr: ErrQuotaExceeded}
	provider := &serviceProvider{}
	service := newTestService(t, store, provider)
	_, err := service.Invoke(context.Background(), testInvocation("生成报告"))
	if !errors.Is(err, ErrQuotaExceeded) || len(provider.requests) != 0 {
		t.Fatalf("error=%v providerCalls=%d", err, len(provider.requests))
	}
}

func TestServiceRejectsExtremeProviderUsageAndClosesAudit(t *testing.T) {
	store := &serviceStore{}
	provider := &serviceProvider{results: []ProviderResult{{
		Content: json.RawMessage(`{}`), Usage: Usage{
			PromptTokens:     int(maxPostgresInteger) + 1,
			CompletionTokens: 1,
			TotalTokens:      int(maxPostgresInteger) + 2,
		},
	}}}
	service := newTestService(t, store, provider)
	_, err := service.Invoke(context.Background(), testInvocation("生成报告"))
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) || providerErr.Code != ErrorCodeInvalidResponse {
		t.Fatalf("error=%v", err)
	}
	if store.failed == nil || store.failed.ErrorCode != string(ErrorCodeInvalidResponse) || store.completed != nil {
		t.Fatalf("极端用量未以稳定失败审计收口: failed=%#v completed=%#v", store.failed, store.completed)
	}
}

func TestCalculateCostMicrosSaturatesWithoutOverflow(t *testing.T) {
	options := DefaultServiceOptions()
	options.InputCostMicrosPerMTokens = math.MaxInt64
	options.OutputCostMicrosPerMTokens = math.MaxInt64
	if got := calculateCostMicros(math.MaxInt, math.MaxInt, options); got != math.MaxInt64 {
		t.Fatalf("极端成本未饱和到 MaxInt64: %d", got)
	}
}

func TestServiceRejectsUnsafeImageURLAndOversizedSchema(t *testing.T) {
	store := &serviceStore{}
	provider := &serviceProvider{}
	service := newTestService(t, store, provider)
	input := testInvocation("分析图片")
	input.Request.Messages[0].Parts = []ContentPart{{Type: ContentTypeImageURL, ImageURL: "http://user:password@example.test/a.png"}}
	if _, err := service.Invoke(context.Background(), input); err == nil {
		t.Fatal("不安全图片地址未被拒绝")
	}
	input = testInvocation("生成报告")
	input.Request.ResponseSchema.Schema = json.RawMessage(`{"type":"array"}`)
	if _, err := service.Invoke(context.Background(), input); err == nil {
		t.Fatal("非对象结构化输出 Schema 未被拒绝")
	}
	if len(provider.requests) != 0 || store.startInput.InputHash != "" {
		t.Fatal("非法输入不应进入配额或 Provider")
	}
}

func newTestService(t *testing.T, store Store, provider Provider) *Service {
	t.Helper()
	options := DefaultServiceOptions()
	options.Timeout = time.Second
	options.AttemptTimeout = 500 * time.Millisecond
	options.BaseRetryDelay = time.Millisecond
	options.MaxRetryDelay = time.Second
	options.MaxAttempts = 3
	options.InputCostMicrosPerMTokens = 1_000_000
	options.OutputCostMicrosPerMTokens = 2_000_000
	service, err := NewService(store, provider, options)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func testInvocation(text string) Invocation {
	temperature := 0.0
	return Invocation{
		TenantID: "tenant-1", ActorID: "actor-1", Purpose: PurposeReportGeneration,
		PromptVersion: "report-v1", ResourceType: "REPORT", ResourceID: "report-1",
		Request: ProviderRequest{
			Messages:       []Message{{Role: MessageRoleUser, Parts: []ContentPart{{Type: ContentTypeText, Text: text}}}},
			ResponseSchema: JSONSchema{Name: "report", Schema: json.RawMessage(`{"type":"object","properties":{},"required":[],"additionalProperties":false}`)},
			Temperature:    &temperature, MaxOutputTokens: 128,
		},
	}
}
