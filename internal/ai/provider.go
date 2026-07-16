package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// OpenAICompatibleProvider 通过兼容 Chat Completions 的协议调用文本或视觉模型。
type OpenAICompatibleProvider struct {
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
}

// NewOpenAICompatibleProvider 创建不持有业务上下文的通用模型提供方。
func NewOpenAICompatibleProvider(baseURL, apiKey, model string, client *http.Client) *OpenAICompatibleProvider {
	if client == nil {
		client = http.DefaultClient
	}
	// 复制调用方客户端并禁用重定向，防止 Authorization 和请求正文被转发到其他端点。
	securedClient := *client
	securedClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &OpenAICompatibleProvider{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		apiKey:  strings.TrimSpace(apiKey),
		model:   strings.TrimSpace(model),
		http:    &securedClient,
	}
}

// Name 返回稳定且不包含供应商地址的提供方标识。
func (p *OpenAICompatibleProvider) Name() string { return "openai-compatible" }

// Model 返回当前配置的模型名称。
func (p *OpenAICompatibleProvider) Model() string { return p.model }

// Configured 校验端点、密钥和模型是否满足最小调用条件。
func (p *OpenAICompatibleProvider) Configured() bool {
	if p == nil || p.http == nil || p.apiKey == "" || p.model == "" {
		return false
	}
	parsed, err := url.Parse(p.baseURL)
	return err == nil &&
		secureProviderScheme(parsed) &&
		parsed.Host != "" && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == "" && parsed.Opaque == ""
}

// secureProviderScheme 生产端点只允许 HTTPS；HTTP 仅供本机测试或本地代理。
func secureProviderScheme(parsed *url.URL) bool {
	if parsed == nil {
		return false
	}
	if parsed.Scheme == "https" {
		return true
	}
	if parsed.Scheme != "http" {
		return false
	}
	host := strings.TrimSpace(parsed.Hostname())
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Complete 请求严格 JSON Schema 输出，并返回经过本地复核的规范 JSON。
func (p *OpenAICompatibleProvider) Complete(ctx context.Context, request ProviderRequest) (ProviderResult, error) {
	if !p.Configured() {
		return ProviderResult{}, newProviderError(
			ErrorCodeProviderUnavailable,
			"AI provider is not configured",
			0,
			false,
			0,
			nil,
		)
	}
	normalized, schemaRoot, err := normalizeProviderRequest(request)
	if err != nil {
		return ProviderResult{}, err
	}
	payload, err := json.Marshal(newWireRequest(p.model, normalized))
	if err != nil {
		return ProviderResult{}, invalidRequest(err)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return ProviderResult{}, invalidRequest(err)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")

	response, err := p.http.Do(httpRequest)
	if err != nil {
		return ProviderResult{}, classifyTransportError(ctx, err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		// 错误正文可能包含提示词或供应商内部信息，只做有界丢弃且绝不拼入错误。
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, MaxProviderResponseBytes+1))
		return ProviderResult{}, classifyHTTPError(response.StatusCode, response.Header.Get("Retry-After"), time.Now())
	}
	body, err := readProviderResponse(response)
	if err != nil {
		if ctx.Err() != nil {
			return ProviderResult{}, classifyTransportError(ctx, ctx.Err())
		}
		return ProviderResult{}, err
	}
	envelope, err := decodeProviderEnvelope(body)
	if err != nil {
		return ProviderResult{}, err
	}
	choice := envelope.Choices[0]
	if hasRefusal(choice.Message.Refusal) {
		return ProviderResult{}, newProviderError(ErrorCodeRefusal, "AI provider refused the request", 0, false, 0, nil)
	}
	if strings.TrimSpace(choice.Message.Content) == "" {
		return ProviderResult{}, newProviderError(ErrorCodeInvalidResponse, "AI provider did not return structured content", 0, false, 0, nil)
	}
	content, err := validateStructuredOutput(schemaRoot, []byte(choice.Message.Content))
	if err != nil {
		return ProviderResult{}, err
	}
	if envelope.Usage == nil {
		return ProviderResult{}, newProviderError(ErrorCodeInvalidResponse, "AI provider did not return token usage", 0, false, 0, nil)
	}
	usage := Usage{
		PromptTokens:     envelope.Usage.PromptTokens,
		CompletionTokens: envelope.Usage.CompletionTokens,
		TotalTokens:      envelope.Usage.TotalTokens,
	}
	if err := validateProviderUsage(usage); err != nil {
		return ProviderResult{}, err
	}
	requestID := firstNonBlank(response.Header.Get("x-request-id"), response.Header.Get("request-id"), envelope.ID)
	return ProviderResult{
		Content:      content,
		Model:        firstNonBlank(envelope.Model, p.model),
		FinishReason: strings.TrimSpace(choice.FinishReason),
		RequestID:    requestID,
		Usage:        usage,
	}, nil
}

type wireRequest struct {
	Model           string             `json:"model"`
	Messages        []wireMessage      `json:"messages"`
	ResponseFormat  wireResponseFormat `json:"response_format"`
	Temperature     *float64           `json:"temperature,omitempty"`
	MaxOutputTokens int                `json:"max_tokens,omitempty"`
}

type wireMessage struct {
	Role    MessageRole `json:"role"`
	Content any         `json:"content"`
}

type wireContentPart struct {
	Type     ContentType   `json:"type"`
	Text     string        `json:"text,omitempty"`
	ImageURL *wireImageURL `json:"image_url,omitempty"`
}

type wireImageURL struct {
	URL    string      `json:"url"`
	Detail ImageDetail `json:"detail,omitempty"`
}

type wireResponseFormat struct {
	Type       string         `json:"type"`
	JSONSchema wireJSONSchema `json:"json_schema"`
}

type wireJSONSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Strict      bool            `json:"strict"`
	Schema      json.RawMessage `json:"schema"`
}

// newWireRequest 把领域消息转换成兼容协议，不传递审计和租户字段。
func newWireRequest(model string, request ProviderRequest) wireRequest {
	messages := make([]wireMessage, len(request.Messages))
	for i, message := range request.Messages {
		messages[i] = wireMessage{Role: message.Role, Content: wireMessageContent(message.Parts)}
	}
	return wireRequest{
		Model:           model,
		Messages:        messages,
		Temperature:     request.Temperature,
		MaxOutputTokens: request.MaxOutputTokens,
		ResponseFormat: wireResponseFormat{
			Type: "json_schema",
			JSONSchema: wireJSONSchema{
				Name:        request.ResponseSchema.Name,
				Description: request.ResponseSchema.Description,
				Strict:      true,
				Schema:      request.ResponseSchema.Schema,
			},
		},
	}
}

// wireMessageContent 让单段纯文本兼容旧模型，视觉消息使用多模态片段数组。
func wireMessageContent(parts []ContentPart) any {
	if len(parts) == 1 && parts[0].Type == ContentTypeText {
		return parts[0].Text
	}
	content := make([]wireContentPart, len(parts))
	for i, part := range parts {
		if part.Type == ContentTypeText {
			content[i] = wireContentPart{Type: ContentTypeText, Text: part.Text}
			continue
		}
		content[i] = wireContentPart{
			Type: ContentTypeImageURL,
			ImageURL: &wireImageURL{
				URL:    part.ImageURL,
				Detail: part.ImageDetail,
			},
		}
	}
	return content
}

type providerEnvelope struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Content string          `json:"content"`
			Refusal json.RawMessage `json:"refusal"`
		} `json:"message"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// readProviderResponse 在多读一个字节后精确识别超过 4 MiB 的响应。
func readProviderResponse(response *http.Response) ([]byte, error) {
	if response.ContentLength > MaxProviderResponseBytes {
		return nil, newProviderError(ErrorCodeResponseTooLarge, "AI provider response exceeds 4 MiB", response.StatusCode, false, 0, nil)
	}
	limited := &io.LimitedReader{R: response.Body, N: MaxProviderResponseBytes + 1}
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, newProviderError(ErrorCodeProviderUnavailable, "AI provider response could not be read", response.StatusCode, true, 0, err)
	}
	if int64(len(body)) > MaxProviderResponseBytes {
		return nil, newProviderError(ErrorCodeResponseTooLarge, "AI provider response exceeds 4 MiB", response.StatusCode, false, 0, nil)
	}
	return body, nil
}

// decodeProviderEnvelope 校验响应信封是单个对象且只包含一个候选结果。
func decodeProviderEnvelope(body []byte) (providerEnvelope, error) {
	value, err := decodeSingleJSONValue(body)
	if err != nil {
		return providerEnvelope{}, newProviderError(ErrorCodeInvalidResponse, "AI provider returned an invalid response", 0, false, 0, err)
	}
	if _, ok := value.(map[string]any); !ok {
		return providerEnvelope{}, newProviderError(ErrorCodeInvalidResponse, "AI provider returned an invalid response", 0, false, 0, nil)
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return providerEnvelope{}, newProviderError(ErrorCodeInvalidResponse, "AI provider returned an invalid response", 0, false, 0, err)
	}
	var envelope providerEnvelope
	if err := json.Unmarshal(canonical, &envelope); err != nil {
		return providerEnvelope{}, newProviderError(ErrorCodeInvalidResponse, "AI provider returned an invalid response", 0, false, 0, err)
	}
	if len(envelope.Choices) != 1 {
		return providerEnvelope{}, newProviderError(ErrorCodeInvalidResponse, "AI provider did not return one completion", 0, false, 0, nil)
	}
	return envelope, nil
}

// hasRefusal 兼容 null、空字符串和非空拒答字段，但不回显拒答正文。
func hasRefusal(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) && !bytes.Equal(trimmed, []byte(`""`))
}

// classifyTransportError 区分调用方取消、超时和可重试网络故障。
func classifyTransportError(ctx context.Context, err error) *ProviderError {
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return newProviderError(ErrorCodeCanceled, "AI request was canceled", 0, false, 0, err)
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return newProviderError(ErrorCodeTimeout, "AI provider request timed out", 0, true, 0, err)
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return newProviderError(ErrorCodeTimeout, "AI provider request timed out", 0, true, 0, err)
	}
	return newProviderError(ErrorCodeProviderUnavailable, "AI provider request failed", 0, true, 0, err)
}

// classifyHTTPError 使用状态码和 Retry-After 生成与供应商无关的重试决策。
func classifyHTTPError(statusCode int, retryAfterHeader string, now time.Time) *ProviderError {
	retryAfter := parseRetryAfter(retryAfterHeader, now)
	switch statusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return newProviderError(ErrorCodeAuthentication, "AI provider authentication failed", statusCode, false, 0, nil)
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return newProviderError(ErrorCodeTimeout, "AI provider request timed out", statusCode, true, retryAfter, nil)
	case http.StatusTooManyRequests:
		return newProviderError(ErrorCodeRateLimited, "AI provider rate limit was reached", statusCode, true, retryAfter, nil)
	case http.StatusTooEarly, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable:
		return newProviderError(ErrorCodeProviderUnavailable, "AI provider is temporarily unavailable", statusCode, true, retryAfter, nil)
	default:
		if statusCode >= 500 {
			return newProviderError(ErrorCodeProviderUnavailable, "AI provider is temporarily unavailable", statusCode, true, retryAfter, nil)
		}
		return newProviderError(ErrorCodeProviderRejected, "AI provider rejected the request", statusCode, false, 0, nil)
	}
}

// parseRetryAfter 同时支持秒数和 HTTP 日期格式，并拒绝负数与溢出值。
func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds <= 0 {
			return 0
		}
		if seconds > math.MaxInt64/int64(time.Second) {
			return time.Duration(math.MaxInt64)
		}
		return time.Duration(seconds) * time.Second
	}
	date, err := http.ParseTime(value)
	if err != nil || !date.After(now) {
		return 0
	}
	return date.Sub(now)
}

// firstNonBlank 返回第一个非空白字符串，并统一清理首尾空白。
func firstNonBlank(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

var _ Provider = (*OpenAICompatibleProvider)(nil)
