package ai

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strings"
	"time"
)

const (
	PurposeMetadataCompletion   = "METADATA_COMPLETION"
	PurposeReportGeneration     = "REPORT_GENERATION"
	PurposeBlockEdit            = "BLOCK_EDIT"
	PurposeConclusion           = "CONCLUSION_GENERATION"
	PurposeDatasetDAGGeneration = "DATASET_DAG_GENERATION"
	PurposeMetricAuthoring      = "METRIC_AUTHORING"
)

var (
	ErrInvalidInvocation = errors.New("AI invocation is invalid")
	ErrTenantAIForbidden = errors.New("AI is disabled for the tenant or purpose")
	ErrQuotaExceeded     = errors.New("AI tenant quota exceeded")
	ErrRequestNotFound   = errors.New("AI request not found")
	ErrRequestConflict   = errors.New("AI request state conflict")
)

// Invocation 是领域服务提交给通用 AI 编排层的最小调用信封。
// 原始提示词只会发送给 Provider，不会进入数据库或日志。
type Invocation struct {
	TenantID      string
	ActorID       string
	Purpose       string
	PromptVersion string
	ResourceType  string
	ResourceID    string
	Request       ProviderRequest
}

// InvocationResult 返回结构化模型结果和可审计但不含业务正文的运行摘要。
type InvocationResult struct {
	RequestID      string
	ProviderResult ProviderResult
	Attempts       int
	CostMicros     int64
	RedactionCount int
}

// StartRequest 保存配额预留和不可逆摘要，不保存提示词、图片地址或响应正文。
type StartRequest struct {
	TenantID, ActorID          string
	Purpose, PromptVersion     string
	Provider, Model            string
	InputHash                  string
	ResourceType, ResourceID   string
	InputBytes, RedactionCount int
	ReservedTokens             int
	ReservedCostMicros         int64
	MaxAttempts                int
}

type RequestRecord struct{ ID string }

// CompletionRecord 是成功调用后用于结算配额的可信 Provider 用量。
type CompletionRecord struct {
	ProviderModel, ProviderRequestID, FinishReason        string
	Attempts, PromptTokens, CompletionTokens, TotalTokens int
	CostMicros, LatencyMS                                 int64
}

// FailureRecord 只持久化稳定错误码，不持久化上游响应或错误正文。
type FailureRecord struct {
	Attempts  int
	ErrorCode string
	LatencyMS int64
}

// Store 在同一租户事务中完成授权、配额预留和审计状态收口。
type Store interface {
	Start(context.Context, StartRequest) (RequestRecord, error)
	Complete(context.Context, string, string, CompletionRecord) error
	Fail(context.Context, string, string, FailureRecord) error
}

// ServiceOptions 约束重试、超时、输入大小和模型成本换算。
type ServiceOptions struct {
	Timeout                    time.Duration
	AttemptTimeout             time.Duration
	MaxAttempts                int
	BaseRetryDelay             time.Duration
	MaxRetryDelay              time.Duration
	MaxInputBytes              int
	InputCostMicrosPerMTokens  int64
	OutputCostMicrosPerMTokens int64
}

// DefaultServiceOptions 提供保守且适合交互请求的默认边界。
func DefaultServiceOptions() ServiceOptions {
	return ServiceOptions{
		Timeout: 25 * time.Second, AttemptTimeout: 10 * time.Second, MaxAttempts: 3,
		BaseRetryDelay: 100 * time.Millisecond, MaxRetryDelay: 2 * time.Second,
		MaxInputBytes: 256 << 10,
	}
}

type Service struct {
	store    Store
	provider Provider
	options  ServiceOptions
	now      func() time.Time
	wait     func(context.Context, time.Duration) error
}

// NewService 创建可被元数据补全、报告生成和分块修改共用的模型编排服务。
func NewService(store Store, provider Provider, options ServiceOptions) (*Service, error) {
	if store == nil || provider == nil {
		return nil, fmt.Errorf("%w: store 和 provider 不能为空", ErrInvalidInvocation)
	}
	options = normalizeServiceOptions(options)
	if err := validateServiceOptions(options); err != nil {
		return nil, err
	}
	return &Service{store: store, provider: provider, options: options, now: time.Now, wait: waitContext}, nil
}

func (s *Service) Configured() bool { return s != nil && s.provider != nil && s.provider.Configured() }
func (s *Service) ProviderName() string {
	if s == nil || s.provider == nil {
		return ""
	}
	return s.provider.Name()
}
func (s *Service) Model() string {
	if s == nil || s.provider == nil {
		return ""
	}
	return s.provider.Model()
}

// Invoke 在发送前最小化并脱敏输入，然后执行租户配额预留、有限重试和审计收口。
func (s *Service) Invoke(ctx context.Context, input Invocation) (InvocationResult, error) {
	if !s.Configured() {
		return InvocationResult{}, newProviderError(ErrorCodeProviderUnavailable, "AI Provider 未配置", 0, false, 0, nil)
	}
	input.Purpose = strings.ToUpper(strings.TrimSpace(input.Purpose))
	input.PromptVersion = strings.TrimSpace(input.PromptVersion)
	if strings.TrimSpace(input.TenantID) == "" || strings.TrimSpace(input.ActorID) == "" || !allowedPurpose(input.Purpose) || input.PromptVersion == "" || len(input.PromptVersion) > 128 {
		return InvocationResult{}, ErrInvalidInvocation
	}

	request, redactionCount, inputBytes, err := sanitizeProviderRequest(input.Request, s.options.MaxInputBytes)
	if err != nil {
		return InvocationResult{}, err
	}
	inputHash, err := hashProviderRequest(request)
	if err != nil {
		return InvocationResult{}, err
	}
	estimatedInputTokens := estimateInputTokens(inputBytes, request)
	perAttemptTokens := estimatedInputTokens + request.MaxOutputTokens
	// 配额按最大尝试次数预留，避免重试把同一份预算重复消费。
	reservedTokens := saturatingMultiplyInt(perAttemptTokens, s.options.MaxAttempts)
	perAttemptCost := calculateCostMicros(estimatedInputTokens, request.MaxOutputTokens, s.options)
	reservedCost := saturatingMultiplyInt64(perAttemptCost, int64(s.options.MaxAttempts))
	record, err := s.store.Start(ctx, StartRequest{
		TenantID: input.TenantID, ActorID: input.ActorID, Purpose: input.Purpose,
		PromptVersion: input.PromptVersion, Provider: s.provider.Name(), Model: s.provider.Model(),
		InputHash: inputHash, ResourceType: strings.TrimSpace(input.ResourceType), ResourceID: strings.TrimSpace(input.ResourceID),
		InputBytes: inputBytes, RedactionCount: redactionCount, ReservedTokens: reservedTokens, ReservedCostMicros: reservedCost,
		MaxAttempts: s.options.MaxAttempts,
	})
	if err != nil {
		return InvocationResult{}, err
	}

	started := s.now()
	callCtx, cancel := context.WithTimeout(ctx, s.options.Timeout)
	defer cancel()
	var result ProviderResult
	var callErr error
	attempts := 0
	for attempts < s.options.MaxAttempts {
		attempts++
		attemptCtx, attemptCancel := context.WithTimeout(callCtx, s.options.AttemptTimeout)
		result, callErr = s.provider.Complete(attemptCtx, request)
		attemptErr := attemptCtx.Err()
		attemptCancel()
		if callErr == nil && attemptErr != nil {
			// 即使异常 Provider 返回 nil，也不能把已经超时或取消的尝试结算为成功。
			callErr = attemptErr
		}
		if callErr == nil {
			break
		}
		classified := NormalizeProviderError(callErr)
		if errors.Is(attemptErr, context.DeadlineExceeded) && !errors.Is(callCtx.Err(), context.DeadlineExceeded) {
			classified = newProviderError(ErrorCodeTimeout, "AI Provider 单次调用超时", 0, true, 0, context.DeadlineExceeded)
		}
		callErr = classified
		if !classified.Retryable || attempts >= s.options.MaxAttempts || callCtx.Err() != nil {
			break
		}
		delay := retryDelay(attempts, classified.RetryAfter, s.options)
		if err := s.wait(callCtx, delay); err != nil {
			callErr = NormalizeProviderError(err)
			break
		}
	}
	if callErr == nil {
		result, callErr = validateProviderResult(request, result, s.provider.Model())
	}
	latency := nonNegativeMilliseconds(s.now().Sub(started))
	if callErr != nil {
		classified := NormalizeProviderError(callErr)
		persistErr := s.persistFailure(ctx, input.TenantID, record.ID, FailureRecord{Attempts: attempts, ErrorCode: string(classified.Code), LatencyMS: latency})
		if persistErr != nil {
			return InvocationResult{RequestID: record.ID, Attempts: attempts, RedactionCount: redactionCount}, errors.Join(callErr, persistErr)
		}
		return InvocationResult{RequestID: record.ID, Attempts: attempts, RedactionCount: redactionCount}, callErr
	}
	usage := result.Usage
	cost := calculateCostMicros(usage.PromptTokens, usage.CompletionTokens, s.options)
	completion := CompletionRecord{
		ProviderModel: result.Model, ProviderRequestID: result.RequestID, FinishReason: result.FinishReason,
		Attempts: attempts, PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens,
		TotalTokens: usage.TotalTokens, CostMicros: cost, LatencyMS: latency,
	}
	if err := s.persistCompletion(ctx, input.TenantID, record.ID, completion); err != nil {
		return InvocationResult{RequestID: record.ID, ProviderResult: result, Attempts: attempts, CostMicros: cost, RedactionCount: redactionCount}, err
	}
	result.Usage = usage
	return InvocationResult{RequestID: record.ID, ProviderResult: result, Attempts: attempts, CostMicros: cost, RedactionCount: redactionCount}, nil
}

func (s *Service) persistCompletion(ctx context.Context, tenantID, requestID string, record CompletionRecord) error {
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return s.store.Complete(persistCtx, tenantID, requestID, record)
}

func (s *Service) persistFailure(ctx context.Context, tenantID, requestID string, record FailureRecord) error {
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return s.store.Fail(persistCtx, tenantID, requestID, record)
}

func normalizeServiceOptions(options ServiceOptions) ServiceOptions {
	defaults := DefaultServiceOptions()
	if options.Timeout == 0 {
		options.Timeout = defaults.Timeout
	}
	if options.AttemptTimeout == 0 {
		options.AttemptTimeout = defaults.AttemptTimeout
	}
	if options.MaxAttempts == 0 {
		options.MaxAttempts = defaults.MaxAttempts
	}
	if options.BaseRetryDelay == 0 {
		options.BaseRetryDelay = defaults.BaseRetryDelay
	}
	if options.MaxRetryDelay == 0 {
		options.MaxRetryDelay = defaults.MaxRetryDelay
	}
	if options.MaxInputBytes == 0 {
		options.MaxInputBytes = defaults.MaxInputBytes
	}
	return options
}

func validateServiceOptions(options ServiceOptions) error {
	if options.Timeout <= 0 || options.AttemptTimeout <= 0 || options.AttemptTimeout > options.Timeout || options.MaxAttempts < 1 || options.MaxAttempts > 5 || options.BaseRetryDelay <= 0 || options.MaxRetryDelay < options.BaseRetryDelay || options.MaxInputBytes < 1024 || options.MaxInputBytes > 4<<20 || options.InputCostMicrosPerMTokens < 0 || options.OutputCostMicrosPerMTokens < 0 {
		return fmt.Errorf("%w: AI 编排配置超出安全边界", ErrInvalidInvocation)
	}
	return nil
}

func allowedPurpose(purpose string) bool {
	switch purpose {
	case PurposeMetadataCompletion, PurposeReportGeneration, PurposeBlockEdit, PurposeConclusion, PurposeDatasetDAGGeneration, PurposeMetricAuthoring:
		return true
	default:
		return false
	}
}

func hashProviderRequest(request ProviderRequest) (string, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

func estimateInputTokens(inputBytes int, request ProviderRequest) int {
	// 文本按最坏 1 字节/Token 预留；视觉输入另留固定高水位预算。
	tokens := inputBytes
	for _, message := range request.Messages {
		for _, part := range message.Parts {
			if part.Type == ContentTypeImageURL {
				tokens += 16_384
			}
		}
	}
	return max(tokens, 1)
}

func saturatingMultiplyInt(value, factor int) int {
	if value <= 0 || factor <= 0 {
		return 0
	}
	if value > math.MaxInt/factor {
		return math.MaxInt
	}
	return value * factor
}

func saturatingMultiplyInt64(value, factor int64) int64 {
	if value <= 0 || factor <= 0 {
		return 0
	}
	if value > math.MaxInt64/factor {
		return math.MaxInt64
	}
	return value * factor
}

func calculateCostMicros(promptTokens, completionTokens int, options ServiceOptions) int64 {
	if promptTokens < 0 {
		promptTokens = 0
	}
	if completionTokens < 0 {
		completionTokens = 0
	}
	input := ceilDivCost(int64(promptTokens), options.InputCostMicrosPerMTokens)
	output := ceilDivCost(int64(completionTokens), options.OutputCostMicrosPerMTokens)
	if input > math.MaxInt64-output {
		return math.MaxInt64
	}
	return input + output
}

func ceilDivCost(tokens, rate int64) int64 {
	if tokens <= 0 || rate <= 0 {
		return 0
	}
	// 使用大整数只处理配置与用量的极端乘法，避免乘积及向上取整加法溢出。
	product := new(big.Int).Mul(big.NewInt(tokens), big.NewInt(rate))
	product.Add(product, big.NewInt(1_000_000-1))
	product.Div(product, big.NewInt(1_000_000))
	if !product.IsInt64() {
		return math.MaxInt64
	}
	return product.Int64()
}

func validateProviderResult(request ProviderRequest, result ProviderResult, configuredModel string) (ProviderResult, error) {
	content, err := ValidateStructuredOutput(request.ResponseSchema, result.Content)
	if err != nil {
		return ProviderResult{}, err
	}
	usage := result.Usage
	if err := validateProviderUsage(usage); err != nil {
		return ProviderResult{}, err
	}
	configuredModel = strings.TrimSpace(configuredModel)
	if !boundedRequired(configuredModel, 256) {
		return ProviderResult{}, newProviderError(ErrorCodeInvalidResponse, "AI Provider 模型配置无效", 0, false, 0, nil)
	}
	result.Content = content
	// 上游可控摘要不直接入库：模型采用本地配置，请求 ID 哈希，结束原因收敛到固定枚举。
	result.Model = configuredModel
	result.RequestID = hashOptionalAuditIdentifier(result.RequestID)
	result.FinishReason = normalizeFinishReason(result.FinishReason)
	return result, nil
}

func validateProviderUsage(usage Usage) error {
	if usage.PromptTokens <= 0 || usage.CompletionTokens <= 0 || usage.TotalTokens <= 0 ||
		int64(usage.PromptTokens) > maxPostgresInteger || int64(usage.CompletionTokens) > maxPostgresInteger || int64(usage.TotalTokens) > maxPostgresInteger {
		return newProviderError(ErrorCodeInvalidResponse, "AI Provider 返回的 Token 用量无效", 0, false, 0, nil)
	}
	minimum := int64(usage.PromptTokens) + int64(usage.CompletionTokens)
	if int64(usage.TotalTokens) < minimum {
		return newProviderError(ErrorCodeInvalidResponse, "AI Provider 返回的 Token 合计不一致", 0, false, 0, nil)
	}
	return nil
}

func hashOptionalAuditIdentifier(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func normalizeFinishReason(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 128 {
		return "other"
	}
	switch strings.ToLower(value) {
	case "", "stop", "length", "content_filter", "tool_calls", "function_call":
		return strings.ToLower(value)
	default:
		return "other"
	}
}

func retryDelay(attempt int, providerDelay time.Duration, options ServiceOptions) time.Duration {
	if providerDelay > 0 {
		if providerDelay > options.MaxRetryDelay {
			return options.MaxRetryDelay
		}
		return providerDelay
	}
	delay := options.BaseRetryDelay << (attempt - 1)
	if delay > options.MaxRetryDelay {
		return options.MaxRetryDelay
	}
	return delay
}

func waitContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func nonNegativeMilliseconds(duration time.Duration) int64 {
	if duration < 0 {
		return 0
	}
	return duration.Milliseconds()
}
