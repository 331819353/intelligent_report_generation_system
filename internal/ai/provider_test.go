package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestOpenAICompatibleProviderSendsVisionAndStrictSchema(t *testing.T) {
	request := validProviderRequest()
	request.Messages = []Message{
		{Role: MessageRoleSystem, Parts: []ContentPart{{Type: ContentTypeText, Text: "只返回报告提纲"}}},
		{Role: MessageRoleUser, Parts: []ContentPart{
			{Type: ContentTypeText, Text: "分析图片"},
			{Type: ContentTypeImageURL, ImageURL: "https://assets.example.test/chart.png", ImageDetail: ImageDetailHigh},
		}},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if body["model"] != "configured-model" || body["max_tokens"] != float64(512) {
			t.Errorf("request body = %#v", body)
		}
		messages, _ := body["messages"].([]any)
		if len(messages) != 2 || messages[0].(map[string]any)["content"] != "只返回报告提纲" {
			t.Errorf("messages = %#v", messages)
		}
		parts, _ := messages[1].(map[string]any)["content"].([]any)
		if len(parts) != 2 {
			t.Errorf("vision parts = %#v", parts)
		} else {
			imagePart := parts[1].(map[string]any)
			imageURL := imagePart["image_url"].(map[string]any)
			if imagePart["type"] != "image_url" || imageURL["url"] != "https://assets.example.test/chart.png" || imageURL["detail"] != "high" {
				t.Errorf("image part = %#v", imagePart)
			}
		}
		format := body["response_format"].(map[string]any)
		contract := format["json_schema"].(map[string]any)
		if format["type"] != "json_schema" || contract["strict"] != true || contract["name"] != "report_outline" {
			t.Errorf("response format = %#v", format)
		}
		schema := contract["schema"].(map[string]any)
		if schema["additionalProperties"] != false {
			t.Errorf("schema = %#v", schema)
		}
		w.Header().Set("x-request-id", " request-123 ")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":    "completion-ignored",
			"model": "provider-model-v2",
			"choices": []any{map[string]any{
				"finish_reason": "stop",
				"message": map[string]any{
					"content": `{"tags":["a"],"name":"月报","count":2}`,
					"refusal": nil,
				},
			}},
			"usage": map[string]int{"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18},
		})
	}))
	defer server.Close()

	provider := NewOpenAICompatibleProvider(server.URL+"/v1", "secret", "configured-model", server.Client())
	result, err := provider.Complete(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Model != "provider-model-v2" || result.RequestID != "request-123" || result.FinishReason != "stop" {
		t.Fatalf("result = %#v", result)
	}
	if result.Usage != (Usage{PromptTokens: 11, CompletionTokens: 7, TotalTokens: 18}) {
		t.Fatalf("usage = %#v", result.Usage)
	}
	if got, want := string(result.Content), `{"count":2,"name":"月报","tags":["a"]}`; got != want {
		t.Fatalf("content = %s, want %s", got, want)
	}
}

func TestOpenAICompatibleProviderClassifiesHTTPFailuresWithoutLeakingBody(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		retryAfter string
		code       ErrorCode
		retryable  bool
		wait       time.Duration
	}{
		{name: "限流", status: http.StatusTooManyRequests, retryAfter: "7", code: ErrorCodeRateLimited, retryable: true, wait: 7 * time.Second},
		{name: "暂时不可用", status: http.StatusServiceUnavailable, retryAfter: "2", code: ErrorCodeProviderUnavailable, retryable: true, wait: 2 * time.Second},
		{name: "认证失败", status: http.StatusUnauthorized, retryAfter: "9", code: ErrorCodeAuthentication, retryable: false},
		{name: "请求被拒", status: http.StatusBadRequest, code: ErrorCodeProviderRejected, retryable: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Retry-After", test.retryAfter)
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, `{"error":"secret upstream body sk-sensitive-value"}`)
			}))
			defer server.Close()
			provider := NewOpenAICompatibleProvider(server.URL, "secret", "model", server.Client())
			_, err := provider.Complete(context.Background(), validProviderRequest())
			providerErr := requireProviderError(t, err, test.code)
			if providerErr.StatusCode != test.status || providerErr.Retryable != test.retryable || providerErr.RetryAfter != test.wait {
				t.Fatalf("provider error = %#v", providerErr)
			}
			if strings.Contains(err.Error(), "secret upstream") || strings.Contains(err.Error(), "sk-sensitive") {
				t.Fatalf("error leaked upstream body: %v", err)
			}
		})
	}
}

func TestOpenAICompatibleProviderRejectsRefusalAndInvalidOutput(t *testing.T) {
	tests := []struct {
		name    string
		message map[string]any
		code    ErrorCode
	}{
		{
			name:    "拒答",
			message: map[string]any{"content": "", "refusal": "不能处理此请求"},
			code:    ErrorCodeRefusal,
		},
		{
			name:    "结构越界",
			message: map[string]any{"content": `{"name":"月报","count":2,"tags":["a"],"invented":true}`},
			code:    ErrorCodeInvalidOutput,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"choices": []any{map[string]any{"message": test.message}},
				})
			}))
			defer server.Close()
			provider := NewOpenAICompatibleProvider(server.URL, "secret", "model", server.Client())
			_, err := provider.Complete(context.Background(), validProviderRequest())
			requireProviderError(t, err, test.code)
		})
	}
}

func TestReadProviderResponseEnforcesExactFourMiBBoundary(t *testing.T) {
	exact := bytes.Repeat([]byte{'a'}, int(MaxProviderResponseBytes))
	response := &http.Response{StatusCode: http.StatusOK, ContentLength: -1, Body: io.NopCloser(bytes.NewReader(exact))}
	body, err := readProviderResponse(response)
	if err != nil || len(body) != len(exact) {
		t.Fatalf("exact boundary len=%d err=%v", len(body), err)
	}
	oversized := append(exact, 'b')
	response = &http.Response{StatusCode: http.StatusOK, ContentLength: -1, Body: io.NopCloser(bytes.NewReader(oversized))}
	_, err = readProviderResponse(response)
	requireProviderError(t, err, ErrorCodeResponseTooLarge)
}

func TestOpenAICompatibleProviderRejectsInvalidEnvelopeAndTransportTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[]} {"second":true}`)
	}))
	provider := NewOpenAICompatibleProvider(server.URL, "secret", "model", server.Client())
	_, err := provider.Complete(context.Background(), validProviderRequest())
	requireProviderError(t, err, ErrorCodeInvalidResponse)
	server.Close()

	provider = NewOpenAICompatibleProvider("https://provider.example.test/v1", "secret", "model", &http.Client{
		Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return nil, context.DeadlineExceeded
		}),
	})
	_, err = provider.Complete(context.Background(), validProviderRequest())
	providerErr := requireProviderError(t, err, ErrorCodeTimeout)
	if !providerErr.Retryable || !errors.Is(providerErr, context.DeadlineExceeded) {
		t.Fatalf("timeout = %#v", providerErr)
	}
}

func TestOpenAICompatibleProviderRejectsSuccessfulResponseWithoutUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{
				"content": `{"name":"月报","count":2,"tags":["a"]}`,
			}}},
		})
	}))
	defer server.Close()
	provider := NewOpenAICompatibleProvider(server.URL, "secret", "model", server.Client())
	_, err := provider.Complete(context.Background(), validProviderRequest())
	requireProviderError(t, err, ErrorCodeInvalidResponse)
}

func TestOpenAICompatibleProviderDoesNotFollowRedirects(t *testing.T) {
	var redirected atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirected.Store(true)
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.URL+"/stolen")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	provider := NewOpenAICompatibleProvider(source.URL, "secret", "model", source.Client())
	_, err := provider.Complete(context.Background(), validProviderRequest())
	requireProviderError(t, err, ErrorCodeProviderRejected)
	if redirected.Load() {
		t.Fatal("Provider 重定向被跟随，请求正文可能泄露")
	}
}

func TestProviderConfigurationAndRetryAfterDate(t *testing.T) {
	if NewOpenAICompatibleProvider("https://user:pass@provider.example.test/v1", "secret", "model", nil).Configured() {
		t.Fatal("base URL user information was accepted")
	}
	if NewOpenAICompatibleProvider("file:///tmp/provider", "secret", "model", nil).Configured() {
		t.Fatal("file provider URL was accepted")
	}
	if NewOpenAICompatibleProvider("http://provider.example.test/v1", "secret", "model", nil).Configured() {
		t.Fatal("非本机明文 HTTP Provider 地址被接受")
	}
	if !NewOpenAICompatibleProvider("http://127.0.0.1:11434/v1", "secret", "model", nil).Configured() {
		t.Fatal("本机 HTTP Provider 地址被拒绝")
	}
	if !NewOpenAICompatibleProvider("https://provider.example.test/v1", "secret", "model", nil).Configured() {
		t.Fatal("valid provider configuration was rejected")
	}
	now := time.Date(2026, time.July, 16, 8, 0, 0, 0, time.UTC)
	if got := parseRetryAfter(now.Add(5*time.Second).Format(http.TimeFormat), now); got != 5*time.Second {
		t.Fatalf("retry after = %s", got)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}
