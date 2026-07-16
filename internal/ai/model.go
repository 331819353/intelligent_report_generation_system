package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const (
	// MaxProviderResponseBytes 限制单次模型响应体，避免异常上游耗尽服务内存。
	MaxProviderResponseBytes int64 = 4 << 20
	// MaxMessagesPerRequest 限制单次调用的消息数量，避免无界请求进入上游。
	MaxMessagesPerRequest = 100
	// MaxPartsPerMessage 限制一条消息中的文本和图片片段数量。
	MaxPartsPerMessage = 100
)

// Provider 抽象文本和视觉模型的结构化补全能力。
type Provider interface {
	Name() string
	Model() string
	Configured() bool
	Complete(context.Context, ProviderRequest) (ProviderResult, error)
}

// MessageRole 表示消息在对话中的可信角色。
type MessageRole string

const (
	MessageRoleSystem    MessageRole = "system"
	MessageRoleUser      MessageRole = "user"
	MessageRoleAssistant MessageRole = "assistant"
)

// ContentType 区分文本和远程图片内容。
type ContentType string

const (
	ContentTypeText     ContentType = "text"
	ContentTypeImageURL ContentType = "image_url"
)

// ImageDetail 表示兼容模型可选的图片分析精度。
type ImageDetail string

const (
	ImageDetailAuto ImageDetail = "auto"
	ImageDetailLow  ImageDetail = "low"
	ImageDetailHigh ImageDetail = "high"
)

// ContentPart 保存一段互斥的文本或远程图片内容。
type ContentPart struct {
	Type        ContentType `json:"type"`
	Text        string      `json:"text,omitempty"`
	ImageURL    string      `json:"imageUrl,omitempty"`
	ImageDetail ImageDetail `json:"imageDetail,omitempty"`
}

// Message 由一个角色和一组有序内容片段组成。
type Message struct {
	Role  MessageRole   `json:"role"`
	Parts []ContentPart `json:"parts"`
}

// JSONSchema 描述模型必须严格遵守的结构化输出合同。
type JSONSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Schema      json.RawMessage `json:"schema"`
}

// ProviderRequest 是不包含租户、权限和审计信息的最小模型请求。
type ProviderRequest struct {
	Messages        []Message  `json:"messages"`
	ResponseSchema  JSONSchema `json:"responseSchema"`
	Temperature     *float64   `json:"temperature,omitempty"`
	MaxOutputTokens int        `json:"maxOutputTokens,omitempty"`
}

// Usage 保存模型返回的 Token 计量信息。
type Usage struct {
	PromptTokens     int `json:"promptTokens"`
	CompletionTokens int `json:"completionTokens"`
	TotalTokens      int `json:"totalTokens"`
}

// ProviderResult 保存经过本地解析和 Schema 复核的规范 JSON。
type ProviderResult struct {
	Content      json.RawMessage `json:"content"`
	Model        string          `json:"model"`
	FinishReason string          `json:"finishReason,omitempty"`
	RequestID    string          `json:"requestId,omitempty"`
	Usage        Usage           `json:"usage"`
}

// ErrorCode 是对外稳定、与具体供应商无关的模型错误码。
type ErrorCode string

const (
	ErrorCodeProviderUnavailable ErrorCode = "AI_PROVIDER_UNAVAILABLE"
	ErrorCodeInvalidRequest      ErrorCode = "AI_REQUEST_INVALID"
	ErrorCodeCanceled            ErrorCode = "AI_REQUEST_CANCELED"
	ErrorCodeTimeout             ErrorCode = "AI_PROVIDER_TIMEOUT"
	ErrorCodeRateLimited         ErrorCode = "AI_RATE_LIMITED"
	ErrorCodeAuthentication      ErrorCode = "AI_PROVIDER_AUTH_FAILED"
	ErrorCodeProviderRejected    ErrorCode = "AI_PROVIDER_REJECTED"
	ErrorCodeResponseTooLarge    ErrorCode = "AI_RESPONSE_TOO_LARGE"
	ErrorCodeInvalidResponse     ErrorCode = "AI_INVALID_RESPONSE"
	ErrorCodeRefusal             ErrorCode = "AI_PROVIDER_REFUSAL"
	ErrorCodeInvalidOutput       ErrorCode = "AI_INVALID_OUTPUT"
	ErrorCodeCompletionFailed    ErrorCode = "AI_COMPLETION_FAILED"
)

// ProviderError 保存安全错误说明及上层重试决策所需的信息。
// Message 必须是本地固定说明，不能拼接上游响应正文。
type ProviderError struct {
	Code       ErrorCode     `json:"code"`
	Message    string        `json:"message"`
	StatusCode int           `json:"statusCode,omitempty"`
	Retryable  bool          `json:"retryable"`
	RetryAfter time.Duration `json:"retryAfter,omitempty"`
	Cause      error         `json:"-"`
}

// Error 实现标准错误接口，但不会展开可能包含敏感信息的底层错误。
func (e *ProviderError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Message == "" {
		return string(e.Code)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap 允许调用方使用 errors.Is 和 errors.As 判断底层取消或超时错误。
func (e *ProviderError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// ErrorClassification 是编排层进行重试、切换模型或转人工时的稳定输入。
type ErrorClassification struct {
	Code       ErrorCode
	Retryable  bool
	RetryAfter time.Duration
}

// ClassifyError 将任意错误安全收敛为稳定的模型错误分类。
func ClassifyError(err error) ErrorClassification {
	var providerErr *ProviderError
	if errors.As(err, &providerErr) {
		return ErrorClassification{
			Code:       providerErr.Code,
			Retryable:  providerErr.Retryable,
			RetryAfter: providerErr.RetryAfter,
		}
	}
	return ErrorClassification{Code: ErrorCodeCompletionFailed}
}

// NormalizeProviderError 将编排层遇到的任意错误统一转换为安全的 ProviderError。
func NormalizeProviderError(err error) *ProviderError {
	if err == nil {
		return nil
	}
	var providerErr *ProviderError
	if errors.As(err, &providerErr) {
		return providerErr
	}
	if errors.Is(err, context.Canceled) {
		return newProviderError(ErrorCodeCanceled, "AI request was canceled", 0, false, 0, err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return newProviderError(ErrorCodeTimeout, "AI provider request timed out", 0, true, 0, err)
	}
	return newProviderError(ErrorCodeCompletionFailed, "AI completion failed", 0, false, 0, err)
}

// newProviderError 构造只包含本地固定说明的模型错误。
func newProviderError(code ErrorCode, message string, statusCode int, retryable bool, retryAfter time.Duration, cause error) *ProviderError {
	return &ProviderError{
		Code:       code,
		Message:    message,
		StatusCode: statusCode,
		Retryable:  retryable,
		RetryAfter: retryAfter,
		Cause:      cause,
	}
}
