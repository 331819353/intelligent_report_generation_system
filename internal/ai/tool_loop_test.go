package ai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type toolLoopTestStore struct {
	start      StartRequest
	completion CompletionRecord
	failure    FailureRecord
}

func (store *toolLoopTestStore) Start(
	_ context.Context,
	input StartRequest,
) (RequestRecord, error) {
	store.start = input
	return RequestRecord{ID: "request-id"}, nil
}

func (store *toolLoopTestStore) Complete(
	_ context.Context,
	_, _ string,
	input CompletionRecord,
) error {
	store.completion = input
	return nil
}

func (store *toolLoopTestStore) Fail(
	_ context.Context,
	_, _ string,
	input FailureRecord,
) error {
	store.failure = input
	return nil
}

type toolLoopTestProvider struct {
	model     string
	requests  []ToolProviderRequest
	responses []ToolProviderResult
}

func (provider *toolLoopTestProvider) Name() string { return "test" }
func (provider *toolLoopTestProvider) Model() string {
	if provider.model != "" {
		return provider.model
	}
	return "glm-test"
}
func (provider *toolLoopTestProvider) Configured() bool { return true }
func (provider *toolLoopTestProvider) Complete(
	context.Context,
	ProviderRequest,
) (ProviderResult, error) {
	return ProviderResult{}, errors.New("structured completion is not used")
}
func (provider *toolLoopTestProvider) CompleteWithTools(
	_ context.Context,
	request ToolProviderRequest,
) (ToolProviderResult, error) {
	provider.requests = append(provider.requests, request)
	if len(provider.responses) == 0 {
		return ToolProviderResult{}, errors.New("missing response")
	}
	result := provider.responses[0]
	provider.responses = provider.responses[1:]
	return result, nil
}

type toolLoopTestExecutor struct{}

func (toolLoopTestExecutor) ExecuteTool(
	_ context.Context,
	execution ToolExecution,
) (ToolExecutionResult, error) {
	switch execution.Name {
	case "search_metrics":
		return ToolExecutionResult{
			Content: json.RawMessage(
				`{"candidates":[{"code":"metric_sales"}]}`,
			),
			EvidenceIDs: []string{"metric:metric_sales@v1"},
		}, nil
	case "submit_metric_selection":
		return ToolExecutionResult{
			Content: execution.Arguments, Terminal: true,
		}, nil
	default:
		return ToolExecutionResult{}, errors.New("unknown tool")
	}
}

type repeatedEvidenceToolLoopExecutor struct{}

func (repeatedEvidenceToolLoopExecutor) ExecuteTool(
	_ context.Context,
	execution ToolExecution,
) (ToolExecutionResult, error) {
	return ToolExecutionResult{
		Content:     json.RawMessage(`{"candidates":[]}`),
		EvidenceIDs: []string{"metric-search:fixed"},
	}, nil
}

func TestServiceExposesEveryConfiguredFallbackModel(t *testing.T) {
	pool := NewPrimaryFallbackProvider(
		&toolLoopTestProvider{model: "deepseek-test"},
		&toolLoopTestProvider{model: "glm-test"},
		&toolLoopTestProvider{model: "minimax-test"},
	)
	service, err := NewService(
		&toolLoopTestStore{}, pool,
		ServiceOptions{
			Timeout: time.Second, AttemptTimeout: time.Second,
			MaxAttempts: 1, BaseRetryDelay: time.Millisecond,
			MaxRetryDelay: time.Millisecond, MaxInputBytes: 64 << 10,
		},
	)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	models := service.FallbackModels()
	if len(models) != 2 || models[0] != "glm-test" ||
		models[1] != "minimax-test" {
		t.Fatalf("fallback models = %#v", models)
	}
}

func TestInvokeToolLoopReplaysReasoningAndStopsAtTerminalTool(t *testing.T) {
	store := &toolLoopTestStore{}
	provider := &toolLoopTestProvider{responses: []ToolProviderResult{
		{
			Message: ToolMessage{
				Role:             MessageRoleAssistant,
				ReasoningContent: "先检索候选",
				ToolCalls: []ToolCall{{
					ID: "call-search", Name: "search_metrics",
					Arguments: json.RawMessage(`{"query":"销售额"}`),
				}},
			},
			Model: "glm-test", FinishReason: "tool_calls",
			RequestID: "provider-1",
			Usage: Usage{
				PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15,
			},
		},
		{
			Message: ToolMessage{
				Role:             MessageRoleAssistant,
				ReasoningContent: "候选唯一，提交",
				ToolCalls: []ToolCall{{
					ID: "call-submit", Name: "submit_metric_selection",
					Arguments: json.RawMessage(
						`{"metricCode":"metric_sales"}`,
					),
				}},
			},
			Model: "glm-test", FinishReason: "tool_calls",
			RequestID: "provider-2",
			Usage: Usage{
				PromptTokens: 20, CompletionTokens: 6, TotalTokens: 26,
			},
		},
	}}
	service, err := NewService(
		store, provider,
		ServiceOptions{
			Timeout: time.Second, AttemptTimeout: time.Second,
			MaxAttempts: 3, BaseRetryDelay: time.Millisecond,
			MaxRetryDelay: time.Millisecond, MaxInputBytes: 64 << 10,
		},
	)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	result, err := service.InvokeToolLoop(
		context.Background(),
		ToolInvocation{
			TenantID: "tenant", ActorID: "actor",
			Purpose:       PurposeSemanticQueryPlanning,
			PromptVersion: "test-tool-loop-v1",
			Request: ToolLoopRequest{
				Messages: []Message{
					{
						Role: MessageRoleSystem,
						Parts: []ContentPart{{
							Type: ContentTypeText, Text: "使用工具",
						}},
					},
					{
						Role: MessageRoleUser,
						Parts: []ContentPart{{
							Type: ContentTypeText, Text: "销售额",
						}},
					},
				},
				Tools: []ToolDefinition{
					{
						Name: "search_metrics", Description: "检索指标",
						Parameters: json.RawMessage(`{
							"type":"object","additionalProperties":false,
							"required":["query"],
							"properties":{"query":{"type":"string"}}
						}`),
					},
					{
						Name:        "submit_metric_selection",
						Description: "提交指标",
						Parameters: json.RawMessage(`{
							"type":"object","additionalProperties":false,
							"required":["metricCode"],
							"properties":{"metricCode":{"type":"string"}}
						}`),
					},
				},
				ToolChoice: ToolChoiceAuto, Thinking: true,
				MaxOutputTokens: 400, MaxRounds: 3, MaxToolCalls: 4,
			},
			Executor: toolLoopTestExecutor{},
		},
	)
	if err != nil {
		t.Fatalf("invoke tool loop: %v", err)
	}
	if string(result.Content) != `{"metricCode":"metric_sales"}` ||
		result.Trace.Rounds != 2 || result.Trace.ToolCalls != 2 ||
		len(result.Trace.Steps) != 2 ||
		result.Trace.Steps[0].ToolName != "search_metrics" ||
		result.Trace.Steps[0].Terminal ||
		result.Trace.Steps[0].ArgumentsHash == "" ||
		result.Trace.Steps[0].StateHash == "" ||
		result.Trace.Steps[0].NewEvidenceCount != 1 ||
		len(result.Trace.EvidenceIDs) != 1 ||
		result.Trace.Steps[1].ToolName != "submit_metric_selection" ||
		!result.Trace.Steps[1].Terminal ||
		result.Usage.PromptTokens != 30 ||
		result.Usage.CompletionTokens != 11 ||
		result.Usage.TotalTokens != 41 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if len(provider.requests) != 2 ||
		len(provider.requests[1].Messages) != 4 {
		t.Fatalf("provider requests = %#v", provider.requests)
	}
	replayed := provider.requests[1].Messages[2]
	if replayed.Role != MessageRoleAssistant ||
		replayed.ReasoningContent != "先检索候选" ||
		len(replayed.ToolCalls) != 1 {
		t.Fatalf("assistant message was not replayed: %#v", replayed)
	}
	toolResult := provider.requests[1].Messages[3]
	if toolResult.Role != MessageRoleTool ||
		toolResult.ToolCallID != "call-search" {
		t.Fatalf("tool result was not replayed: %#v", toolResult)
	}
	if store.completion.Attempts != 2 ||
		store.completion.TotalTokens != 41 ||
		store.failure.Attempts != 0 {
		t.Fatalf(
			"completion=%#v failure=%#v",
			store.completion, store.failure,
		)
	}
}

func TestInvokeToolLoopRejectsSearchWithoutNewEvidence(t *testing.T) {
	store := &toolLoopTestStore{}
	provider := &toolLoopTestProvider{responses: []ToolProviderResult{
		{
			Message: ToolMessage{ToolCalls: []ToolCall{{
				ID: "search-1", Name: "search_metrics",
				Arguments: json.RawMessage(`{"query":"销售额"}`),
			}}},
			Model: "glm-test", Usage: Usage{
				PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2,
			},
		},
		{
			Message: ToolMessage{ToolCalls: []ToolCall{{
				ID: "search-2", Name: "search_metrics",
				Arguments: json.RawMessage(`{"query":"销售额"}`),
			}}},
			Model: "glm-test", Usage: Usage{
				PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2,
			},
		},
	}}
	service, err := NewService(store, provider, ServiceOptions{
		Timeout: time.Second, AttemptTimeout: time.Second,
		MaxAttempts: 3, BaseRetryDelay: time.Millisecond,
		MaxRetryDelay: time.Millisecond, MaxInputBytes: 64 << 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.InvokeToolLoop(context.Background(), ToolInvocation{
		TenantID: "tenant", ActorID: "actor",
		Purpose: PurposeSemanticQueryPlanning, PromptVersion: "no-progress-v1",
		Request: ToolLoopRequest{
			Messages: []Message{{
				Role:  MessageRoleUser,
				Parts: []ContentPart{{Type: ContentTypeText, Text: "销售额"}},
			}},
			Tools: []ToolDefinition{{
				Name: "search_metrics", Description: "检索指标",
				Parameters: json.RawMessage(`{
					"type":"object","additionalProperties":false,
					"required":["query"],
					"properties":{"query":{"type":"string"}}
				}`),
			}},
			ToolChoice: ToolChoiceAuto, MaxOutputTokens: 100,
			MaxRounds: 2, MaxToolCalls: 2,
		},
		Executor: repeatedEvidenceToolLoopExecutor{},
	})
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) ||
		providerErr.Code != ErrorCodeToolNoProgress ||
		store.failure.ErrorCode != string(ErrorCodeToolNoProgress) {
		t.Fatalf("expected no-progress failure, got err=%v store=%#v", err, store)
	}
}

func TestProviderFamilyToolWireCompatibility(t *testing.T) {
	baseRequest := ToolProviderRequest{
		Messages: []ToolMessage{{
			Role: MessageRoleUser, Content: "question",
		}},
		Tools: []ToolDefinition{{
			Name: "search_metrics", Description: "search",
			Parameters: json.RawMessage(
				`{"type":"object","additionalProperties":false,"required":[],"properties":{}}`,
			),
		}},
		ToolChoice: ToolChoiceAuto, Thinking: true, MaxOutputTokens: 100,
	}
	tests := []struct {
		model        string
		wantThinking bool
		wantChoice   ToolChoice
	}{
		{"deepseek-v3.2", true, ""},
		{"glm-4.7", true, ToolChoiceAuto},
		{"MiniMax-M2.7", false, ToolChoiceAuto},
	}
	for _, test := range tests {
		t.Run(test.model, func(t *testing.T) {
			provider := NewOpenAICompatibleProvider(
				"https://gateway.example/v1", "secret", test.model, nil,
			)
			wire := provider.newWireToolRequest(baseRequest)
			if (wire.Thinking != nil) != test.wantThinking ||
				wire.ToolChoice != test.wantChoice {
				t.Fatalf(
					"thinking=%#v choice=%q",
					wire.Thinking, wire.ToolChoice,
				)
			}
		})
	}
}

func TestCompleteWithToolsParsesReasoningAndCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"id":"request-1","model":"glm-4.7",
				"choices":[{"finish_reason":"tool_calls","message":{
					"content":"","reasoning_content":"需要检索",
					"tool_calls":[{"id":"call-1","type":"function","function":{
						"name":"search_metrics","arguments":"{\"query\":\"销售额\"}"
					}}]
				}}],
				"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}
			}`))
		},
	))
	defer server.Close()
	provider := NewOpenAICompatibleProvider(
		server.URL, "secret", "glm-4.7", server.Client(),
	)
	result, err := provider.CompleteWithTools(
		context.Background(),
		ToolProviderRequest{
			Messages: []ToolMessage{{
				Role: MessageRoleUser, Content: "销售额",
			}},
			Tools: []ToolDefinition{{
				Name: "search_metrics", Description: "search",
				Parameters: json.RawMessage(
					`{"type":"object","additionalProperties":false,"required":["query"],"properties":{"query":{"type":"string"}}}`,
				),
			}},
			ToolChoice: ToolChoiceAuto, Thinking: true, MaxOutputTokens: 100,
		},
	)
	if err != nil {
		t.Fatalf("complete with tools: %v", err)
	}
	if result.Message.ReasoningContent != "需要检索" ||
		len(result.Message.ToolCalls) != 1 ||
		result.Message.ToolCalls[0].Name != "search_metrics" {
		t.Fatalf("unexpected provider result: %#v", result)
	}
}
