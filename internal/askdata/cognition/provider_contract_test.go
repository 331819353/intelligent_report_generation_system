package cognition

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/ai"
)

type auditStoreFixture struct {
	StartRecord      ai.StartRequest
	CompletionRecord ai.CompletionRecord
	FailureRecord    ai.FailureRecord
}

func (store *auditStoreFixture) Start(_ context.Context, input ai.StartRequest) (ai.RequestRecord, error) {
	store.StartRecord = input
	return ai.RequestRecord{ID: "ai-request-fixture-1"}, nil
}

func (store *auditStoreFixture) Complete(_ context.Context, _, _ string, input ai.CompletionRecord) error {
	store.CompletionRecord = input
	return nil
}

func (store *auditStoreFixture) Fail(_ context.Context, _, _ string, input ai.FailureRecord) error {
	store.FailureRecord = input
	return nil
}

func TestProviderContractPinsPreferredGLMAndDropsReasoningContent(t *testing.T) {
	actionRaw, err := json.Marshal(validBlockAction(StageUnderstanding))
	if err != nil {
		t.Fatal(err)
	}
	deepSeekCalls := 0
	deepSeek := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		deepSeekCalls++
	}))
	defer deepSeek.Close()

	var glmWire map[string]any
	glm := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&glmWire); err != nil {
			t.Errorf("decode GLM wire request: %v", err)
			response.WriteHeader(http.StatusBadRequest)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(map[string]any{
			"id": "provider-glm-fixture-1", "model": "untrusted-upstream-model",
			"choices": []any{map[string]any{
				"finish_reason": "stop",
				"message": map[string]any{
					"content": string(actionRaw), "reasoning_content": "hidden reasoning must be ignored",
					"refusal": nil,
				},
			}},
			"usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 25, "total_tokens": 125},
		})
	}))
	defer glm.Close()

	provider := ai.NewMultiEndpointProviderPool([]ai.ProviderEndpoint{
		{Name: "deepseek", BaseURL: deepSeek.URL, APIKey: "fixture-key", Models: []string{"deepseek-v4-flash"}},
		{
			Name: "glm", BaseURL: glm.URL, APIKey: "fixture-key", Models: []string{"glm-5.2"},
			ThinkingEnabled: true, ResponseFormat: "json_object", MaxOutputTokens: 65_536,
		},
	}, ai.ProviderSelectionRoundRobin, http.DefaultClient)
	store := &auditStoreFixture{}
	service, err := ai.NewService(store, provider, ai.ServiceOptions{
		Timeout: 5 * time.Second, AttemptTimeout: 2 * time.Second, MaxAttempts: 1,
		BaseRetryDelay: time.Millisecond, MaxRetryDelay: time.Millisecond,
		MaxInputBytes: 256 << 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	executor := newTestExecutor(t, service)
	request := validRoundRequest(StageUnderstanding)
	request.PreferredModel = "glm-5.2"
	result, err := executor.Execute(context.Background(), request)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if deepSeekCalls != 0 {
		t.Fatalf("DeepSeek calls = %d, want preferred GLM only", deepSeekCalls)
	}
	if result.ProviderModel != "glm-5.2" || store.StartRecord.Model != "glm-5.2" || store.StartRecord.Purpose != ai.PurposeSemanticQuestion {
		t.Fatalf("result/store routing = %#v / %#v", result, store.StartRecord)
	}
	if store.CompletionRecord.FinishReason != "stop" || store.CompletionRecord.ProviderRequestID == "provider-glm-fixture-1" {
		t.Fatalf("completion audit was not normalized: %#v", store.CompletionRecord)
	}
	serializedAudit, err := json.Marshal(store)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(serializedAudit), "hidden reasoning") || strings.Contains(string(serializedAudit), string(actionRaw)) {
		t.Fatal("audit store received model content or hidden reasoning")
	}
	format := glmWire["response_format"].(map[string]any)
	thinking := glmWire["thinking"].(map[string]any)
	if format["type"] != "json_object" || thinking["type"] != "enabled" {
		t.Fatalf("GLM wire extensions = format %#v thinking %#v", format, thinking)
	}
	if glmWire["max_tokens"] != float64(defaultMaxOutputTokens) {
		t.Fatalf("GLM max_tokens = %#v, want reserved cognition budget", glmWire["max_tokens"])
	}
}

func TestProviderContractRefusalFailsClosedWithoutEchoingBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(map[string]any{
			"id": "provider-refusal-1", "model": "deepseek-v4-flash",
			"choices": []any{map[string]any{
				"finish_reason": "stop",
				"message": map[string]any{
					"content": "", "refusal": "sensitive upstream refusal body",
				},
			}},
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 1, "total_tokens": 11},
		})
	}))
	defer server.Close()
	provider := ai.NewOpenAICompatibleProvider(server.URL, "fixture-key", "deepseek-v4-flash", http.DefaultClient)
	stageSchema, err := SchemaForStage(readActionSchema(t), StageUnderstanding)
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Complete(context.Background(), ai.ProviderRequest{
		Messages: []ai.Message{{
			Role:  ai.MessageRoleUser,
			Parts: []ai.ContentPart{{Type: ai.ContentTypeText, Text: "识别销售额"}},
		}},
		ResponseSchema: stageSchema, MaxOutputTokens: 8_192,
	})
	var providerErr *ai.ProviderError
	if !errors.As(err, &providerErr) || providerErr.Code != ai.ErrorCodeRefusal {
		t.Fatalf("Complete() error = %v", err)
	}
	if strings.Contains(err.Error(), "sensitive upstream") {
		t.Fatal("provider refusal body was echoed")
	}
}
