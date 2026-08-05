package ai

import (
	"encoding/json"
	"testing"
)

func TestMultiEndpointProviderPoolRoundRobinsAndPreservesOptions(t *testing.T) {
	provider := NewMultiEndpointProviderPool([]ProviderEndpoint{
		{
			Name: "gateway", BaseURL: "http://127.0.0.1:18080",
			APIKey: "gateway-key", Models: []string{"MiniMax-M2"},
		},
		{
			Name: "deepseek", BaseURL: "http://127.0.0.1:18081",
			APIKey: "deepseek-key", Models: []string{"deepseek-v4-flash"},
		},
		{
			Name: "glm", BaseURL: "http://127.0.0.1:18082",
			APIKey: "glm-key", Models: []string{"glm-5.2"},
			ThinkingEnabled: true, ResponseFormat: "json_object",
			MaxOutputTokens: 65_536,
		},
	}, ProviderSelectionRoundRobin, nil)
	if models := provider.Model(); models != "MiniMax-M2,deepseek-v4-flash,glm-5.2" {
		t.Fatalf("models = %q, want ordered three-model chain", models)
	}
	router, ok := provider.(ProviderSelector)
	if !ok {
		t.Fatal("multi-endpoint pool does not implement provider selection")
	}
	for index, expected := range []string{
		"MiniMax-M2", "deepseek-v4-flash", "glm-5.2", "MiniMax-M2",
	} {
		if selected := router.SelectProvider().Model(); selected != expected {
			t.Fatalf("selection %d = %q, want %q", index, selected, expected)
		}
	}
	selector, ok := provider.(ModelProviderSelector)
	if !ok {
		t.Fatal("multi-endpoint pool does not implement model selection")
	}
	glm, ok := selector.SelectProviderModel("glm-5.2").(*OpenAICompatibleProvider)
	if !ok {
		t.Fatal("GLM provider was not selectable")
	}
	if !glm.options.ThinkingEnabled || glm.options.ResponseFormat != "json_object" ||
		glm.options.MaxOutputTokens != 65_536 {
		t.Fatalf("GLM options = %#v", glm.options)
	}
}

func TestNewWireRequestPromptModeOmitsUnsupportedResponseFormat(t *testing.T) {
	wire := newWireRequest("MiniMax-M2", ProviderRequest{
		Messages: []Message{{
			Role:  MessageRoleUser,
			Parts: []ContentPart{{Type: ContentTypeText, Text: "hello"}},
		}},
		ResponseSchema: JSONSchema{
			Name:   "answer",
			Schema: json.RawMessage(`{"type":"object"}`),
		},
	}, ProviderOptions{ResponseFormat: "prompt"})
	payload, err := json.Marshal(wire)
	if err != nil {
		t.Fatalf("marshal wire request: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode wire request: %v", err)
	}
	if _, exists := decoded["response_format"]; exists {
		t.Fatal("response_format must be omitted in prompt mode")
	}
	messages, ok := decoded["messages"].([]any)
	if !ok || len(messages) != 2 {
		t.Fatalf("messages = %#v, want schema contract plus user message", decoded["messages"])
	}
}

func TestNewWireRequestIncludesConfiguredReasoningExtensions(t *testing.T) {
	request := ProviderRequest{
		Messages: []Message{{
			Role:  MessageRoleUser,
			Parts: []ContentPart{{Type: ContentTypeText, Text: "hello"}},
		}},
		ResponseSchema: JSONSchema{
			Name:   "answer",
			Schema: json.RawMessage(`{"type":"object"}`),
		},
	}

	wire := newWireRequest("deepseek-v4-flash", request, ProviderOptions{
		ThinkingEnabled: true,
		ReasoningEffort: " HIGH ",
		ResponseFormat:  "json_object",
		MaxOutputTokens: 65_536,
	})
	payload, err := json.Marshal(wire)
	if err != nil {
		t.Fatalf("marshal wire request: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode wire request: %v", err)
	}
	thinking, ok := decoded["thinking"].(map[string]any)
	if !ok || thinking["type"] != "enabled" {
		t.Fatalf("thinking = %#v, want enabled object", decoded["thinking"])
	}
	if decoded["reasoning_effort"] != "high" {
		t.Fatalf("reasoning_effort = %#v, want high", decoded["reasoning_effort"])
	}
	if stream, ok := decoded["stream"].(bool); !ok || stream {
		t.Fatalf("stream = %#v, want false", decoded["stream"])
	}
	if decoded["max_tokens"] != float64(65_536) {
		t.Fatalf("max_tokens = %#v, want 65536", decoded["max_tokens"])
	}
	responseFormat, ok := decoded["response_format"].(map[string]any)
	if !ok || responseFormat["type"] != "json_object" {
		t.Fatalf("response_format = %#v, want json_object", decoded["response_format"])
	}
	if _, exists := responseFormat["json_schema"]; exists {
		t.Fatalf("json_schema must be omitted in json_object mode")
	}
	messages, ok := decoded["messages"].([]any)
	if !ok || len(messages) != 2 {
		t.Fatalf("messages = %#v, want schema contract plus user message", decoded["messages"])
	}
	contract, ok := messages[0].(map[string]any)
	if !ok || contract["role"] != "system" {
		t.Fatalf("first message = %#v, want system schema contract", messages[0])
	}
}

func TestNewWireRequestOmitsUnconfiguredReasoningExtensions(t *testing.T) {
	wire := newWireRequest("compatible-model", ProviderRequest{
		ResponseSchema: JSONSchema{
			Name:   "answer",
			Schema: json.RawMessage(`{"type":"object"}`),
		},
	}, ProviderOptions{})
	payload, err := json.Marshal(wire)
	if err != nil {
		t.Fatalf("marshal wire request: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode wire request: %v", err)
	}
	if _, exists := decoded["thinking"]; exists {
		t.Fatalf("thinking must be omitted when disabled")
	}
	if _, exists := decoded["reasoning_effort"]; exists {
		t.Fatalf("reasoning_effort must be omitted when empty")
	}
	responseFormat, ok := decoded["response_format"].(map[string]any)
	if !ok || responseFormat["type"] != "json_schema" {
		t.Fatalf("response_format = %#v, want json_schema", decoded["response_format"])
	}
	if _, exists := responseFormat["json_schema"]; !exists {
		t.Fatalf("json_schema must be present by default")
	}
}

func TestDeepSeekAndGLMWireContractsCarryApplicationToolMessages(t *testing.T) {
	request := ProviderRequest{
		Messages: []Message{
			{Role: MessageRoleUser, Parts: []ContentPart{{Type: ContentTypeText, Text: "识别销售额"}}},
			{Role: MessageRoleAssistant, Parts: []ContentPart{{Type: ContentTypeText, Text: `{"action":"CALL_TOOL"}`}}},
			{
				Role: MessageRoleTool, ToolCallID: "call-search-1", ToolName: "search_semantic_objects",
				Parts: []ContentPart{{Type: ContentTypeText, Text: `{"candidates":[]}`}},
			},
		},
		ResponseSchema: JSONSchema{Name: "action", Schema: json.RawMessage(
			`{"type":"object","additionalProperties":false,"required":["ok"],"properties":{"ok":{"type":"boolean"}}}`,
		)},
		MaxOutputTokens: 8_192,
	}
	for _, fixture := range []struct {
		name    string
		model   string
		options ProviderOptions
	}{
		{
			name: "DeepSeek", model: "deepseek-v4-flash",
			options: ProviderOptions{ThinkingEnabled: true, ReasoningEffort: "high", ResponseFormat: "json_object"},
		},
		{
			name: "GLM", model: "glm-5.2",
			options: ProviderOptions{ThinkingEnabled: true, ResponseFormat: "json_object", MaxOutputTokens: 65_536},
		},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			if err := ValidateProviderRequest(request); err != nil {
				t.Fatalf("ValidateProviderRequest() error = %v", err)
			}
			wire := newWireRequest(fixture.model, request, fixture.options)
			if len(wire.Messages) != 4 { // schema contract + three application messages
				t.Fatalf("wire messages = %d, want 4", len(wire.Messages))
			}
			tool := wire.Messages[3]
			if tool.Role != MessageRoleTool || tool.ToolCallID != "call-search-1" || tool.Name != "search_semantic_objects" {
				t.Fatalf("tool wire message = %#v", tool)
			}
			if tool.Content != `{"candidates":[]}` {
				t.Fatalf("tool content = %#v", tool.Content)
			}
			if wire.MaxOutputTokens != request.MaxOutputTokens {
				t.Fatalf("max tokens = %d, want reserved request budget %d", wire.MaxOutputTokens, request.MaxOutputTokens)
			}
		})
	}
}
