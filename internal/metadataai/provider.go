package metadataai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type Provider interface {
	Name() string
	Model() string
	Configured() bool
	Complete(context.Context, CompletionInput) (ProviderResult, error)
}

type OpenAICompatibleProvider struct {
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
}

// NewOpenAICompatibleProvider 创建兼容 Chat Completions 协议的模型提供方。
func NewOpenAICompatibleProvider(baseURL, apiKey, model string, client *http.Client) *OpenAICompatibleProvider {
	if client == nil {
		client = http.DefaultClient
	}
	return &OpenAICompatibleProvider{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		apiKey:  strings.TrimSpace(apiKey),
		model:   strings.TrimSpace(model),
		http:    client,
	}
}

// Name 返回稳定的提供方标识。
func (p *OpenAICompatibleProvider) Name() string { return "openai-compatible" }

// Model 返回当前配置的模型名称。
func (p *OpenAICompatibleProvider) Model() string { return p.model }

// Configured 检查地址、密钥和模型是否满足最小调用条件。
func (p *OpenAICompatibleProvider) Configured() bool {
	parsed, err := url.Parse(p.baseURL)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != "" && p.apiKey != "" && p.model != ""
}

// Complete 请求严格 JSON Schema 输出，并将提供方响应转换为领域结果。
func (p *OpenAICompatibleProvider) Complete(ctx context.Context, input CompletionInput) (ProviderResult, error) {
	if !p.Configured() {
		return ProviderResult{}, ErrProviderUnavailable
	}
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return ProviderResult{}, err
	}
	body := map[string]any{
		"model":       p.model,
		"temperature": 0,
		"messages": []map[string]string{
			{"role": "system", "content": "你是企业数据资产元数据补全器。只能依据给定技术元数据生成结果，不得虚构资产或返回未请求的字段。必须严格遵守 JSON Schema 和标签枚举。"},
			{"role": "user", "content": string(inputJSON)},
		},
		"response_format": map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   "metadata_completion",
				"strict": true,
				"schema": OutputSchema,
			},
		},
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return ProviderResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return ProviderResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	req.Header.Set("Content-Type", "application/json")
	response, err := p.http.Do(req)
	if err != nil {
		return ProviderResult{}, fmt.Errorf("call AI provider: %w", err)
	}
	defer response.Body.Close()
	// 限制响应体大小，避免异常上游耗尽服务内存。
	limited := io.LimitReader(response.Body, 4<<20)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, limited)
		return ProviderResult{}, fmt.Errorf("AI provider returned status %d", response.StatusCode)
	}
	var envelope struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
				Refusal string `json:"refusal"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	decoder := json.NewDecoder(limited)
	if err := decoder.Decode(&envelope); err != nil {
		return ProviderResult{}, fmt.Errorf("decode AI provider response: %w", err)
	}
	if len(envelope.Choices) != 1 || envelope.Choices[0].Message.Refusal != "" || strings.TrimSpace(envelope.Choices[0].Message.Content) == "" {
		return ProviderResult{}, errors.New("AI provider did not return one structured completion")
	}
	var output CompletionOutput
	content := json.NewDecoder(strings.NewReader(envelope.Choices[0].Message.Content))
	// 即使提供方声明遵守 Schema，也再次拒绝未知字段和尾随 JSON。
	content.DisallowUnknownFields()
	if err := content.Decode(&output); err != nil {
		return ProviderResult{}, fmt.Errorf("decode AI structured output: %w", err)
	}
	if err := ensureJSONEOF(content); err != nil {
		return ProviderResult{}, err
	}
	return ProviderResult{
		Output: output,
		Model:  firstNonBlank(envelope.Model, p.model),
		Usage: Usage{
			PromptTokens:     envelope.Usage.PromptTokens,
			CompletionTokens: envelope.Usage.CompletionTokens,
			TotalTokens:      envelope.Usage.TotalTokens,
		},
	}, nil
}

// ensureJSONEOF 确保结构化输出后不存在第二个 JSON 值。
func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("AI structured output contains trailing JSON")
		}
		return fmt.Errorf("decode trailing AI structured output: %w", err)
	}
	return nil
}

// firstNonBlank 返回第一个非空白字符串。
func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
