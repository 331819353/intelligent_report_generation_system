package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

type wireToolRequest struct {
	Model           string               `json:"model"`
	Messages        []wireToolMessage    `json:"messages"`
	Tools           []wireToolDefinition `json:"tools"`
	ToolChoice      ToolChoice           `json:"tool_choice,omitempty"`
	Thinking        *wireThinking        `json:"thinking,omitempty"`
	Temperature     *float64             `json:"temperature,omitempty"`
	MaxOutputTokens int                  `json:"max_tokens,omitempty"`
}

type wireThinking struct {
	Type          string `json:"type"`
	ClearThinking *bool  `json:"clear_thinking,omitempty"`
}

type wireToolMessage struct {
	Role             MessageRole    `json:"role"`
	Content          string         `json:"content"`
	ReasoningContent string         `json:"reasoning_content,omitempty"`
	ToolCalls        []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string         `json:"tool_call_id,omitempty"`
}

type wireToolDefinition struct {
	Type     string           `json:"type"`
	Function wireToolFunction `json:"function"`
}

type wireToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type wireToolCall struct {
	ID       string               `json:"id"`
	Type     string               `json:"type"`
	Function wireToolCallFunction `json:"function"`
}

type wireToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type toolProviderEnvelope struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Content          *string         `json:"content"`
			ReasoningContent string          `json:"reasoning_content"`
			ToolCalls        []wireToolCall  `json:"tool_calls"`
			Refusal          json.RawMessage `json:"refusal"`
		} `json:"message"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// CompleteWithTools executes one provider step. It deliberately does not run
// tools; the service owns authorization, argument validation and loop bounds.
func (p *OpenAICompatibleProvider) CompleteWithTools(
	ctx context.Context,
	request ToolProviderRequest,
) (ToolProviderResult, error) {
	if !p.Configured() {
		return ToolProviderResult{}, newProviderError(
			ErrorCodeProviderUnavailable, "AI provider is not configured",
			0, false, 0, nil,
		)
	}
	payload, err := json.Marshal(p.newWireToolRequest(request))
	if err != nil {
		return ToolProviderResult{}, invalidRequest(err)
	}
	httpRequest, err := http.NewRequestWithContext(
		ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(payload),
	)
	if err != nil {
		return ToolProviderResult{}, invalidRequest(err)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")
	response, err := p.http.Do(httpRequest)
	if err != nil {
		return ToolProviderResult{}, classifyTransportError(ctx, err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK ||
		response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(
			io.Discard, io.LimitReader(response.Body, MaxProviderResponseBytes+1),
		)
		return ToolProviderResult{}, classifyHTTPError(
			response.StatusCode, response.Header.Get("Retry-After"), timeNow(),
		)
	}
	body, err := readProviderResponse(response)
	if err != nil {
		return ToolProviderResult{}, err
	}
	return decodeToolProviderResponse(
		body, p.model,
		firstNonBlank(
			response.Header.Get("x-request-id"),
			response.Header.Get("request-id"),
		),
	)
}

// timeNow is a seam for the shared Retry-After parser without exposing
// provider response bodies or provider-specific error payloads.
var timeNow = func() time.Time { return time.Now() }

func (p *OpenAICompatibleProvider) newWireToolRequest(
	request ToolProviderRequest,
) wireToolRequest {
	messages := make([]wireToolMessage, 0, len(request.Messages))
	for _, message := range request.Messages {
		wireMessage := wireToolMessage{
			Role: message.Role, Content: message.Content,
			ReasoningContent: message.ReasoningContent,
			ToolCallID:       message.ToolCallID,
		}
		for _, call := range message.ToolCalls {
			wireMessage.ToolCalls = append(wireMessage.ToolCalls, wireToolCall{
				ID: call.ID, Type: "function",
				Function: wireToolCallFunction{
					Name: call.Name, Arguments: string(call.Arguments),
				},
			})
		}
		messages = append(messages, wireMessage)
	}
	tools := make([]wireToolDefinition, 0, len(request.Tools))
	for _, tool := range request.Tools {
		tools = append(tools, wireToolDefinition{
			Type: "function",
			Function: wireToolFunction{
				Name: tool.Name, Description: tool.Description,
				Parameters: tool.Parameters,
			},
		})
	}
	wire := wireToolRequest{
		Model: p.model, Messages: messages, Tools: tools,
		ToolChoice: request.ToolChoice, Temperature: request.Temperature,
		MaxOutputTokens: request.MaxOutputTokens,
	}
	family := compatibleProviderFamily(p.baseURL, p.model)
	if request.Thinking && (family == "deepseek" || family == "glm") {
		wire.Thinking = &wireThinking{Type: "enabled"}
		if family == "glm" {
			clearThinking := false
			wire.Thinking.ClearThinking = &clearThinking
		}
	}
	// DeepSeek thinking endpoints have had versions that reject an explicit
	// auto tool_choice. Omitting it preserves the protocol's default auto mode.
	if family == "deepseek" && wire.ToolChoice == ToolChoiceAuto {
		wire.ToolChoice = ""
	}
	return wire
}

func compatibleProviderFamily(baseURL, model string) string {
	value := strings.ToLower(strings.TrimSpace(baseURL + " " + model))
	switch {
	case strings.Contains(value, "deepseek"):
		return "deepseek"
	case strings.Contains(value, "bigmodel") ||
		strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "glm"):
		return "glm"
	case strings.Contains(value, "minimaxi") ||
		strings.Contains(strings.ToLower(model), "minimax"):
		return "minimax"
	default:
		return "openai-compatible"
	}
}

func decodeToolProviderResponse(
	body []byte,
	configuredModel, headerRequestID string,
) (ToolProviderResult, error) {
	value, err := decodeSingleJSONValue(body)
	if err != nil {
		return ToolProviderResult{}, newProviderError(
			ErrorCodeInvalidResponse, "AI provider returned an invalid response",
			0, false, 0, err,
		)
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return ToolProviderResult{}, newProviderError(
			ErrorCodeInvalidResponse, "AI provider returned an invalid response",
			0, false, 0, err,
		)
	}
	var envelope toolProviderEnvelope
	if json.Unmarshal(canonical, &envelope) != nil || len(envelope.Choices) != 1 ||
		envelope.Usage == nil {
		return ToolProviderResult{}, newProviderError(
			ErrorCodeInvalidResponse,
			"AI provider did not return one tool completion", 0, false, 0, nil,
		)
	}
	choice := envelope.Choices[0]
	if hasRefusal(choice.Message.Refusal) {
		return ToolProviderResult{}, newProviderError(
			ErrorCodeRefusal, "AI provider refused the request",
			0, false, 0, nil,
		)
	}
	content := ""
	if choice.Message.Content != nil {
		content = *choice.Message.Content
	}
	if !utf8.ValidString(content) ||
		!utf8.ValidString(choice.Message.ReasoningContent) {
		return ToolProviderResult{}, newProviderError(
			ErrorCodeInvalidResponse,
			"AI provider returned invalid message text", 0, false, 0, nil,
		)
	}
	calls := make([]ToolCall, 0, len(choice.Message.ToolCalls))
	seenIDs := map[string]bool{}
	for _, item := range choice.Message.ToolCalls {
		id := strings.TrimSpace(item.ID)
		name := strings.TrimSpace(item.Function.Name)
		if id == "" || len(id) > 256 || seenIDs[id] ||
			!toolNamePattern.MatchString(name) ||
			len(item.Function.Arguments) > maxToolCallArgumentSize {
			return ToolProviderResult{}, newProviderError(
				ErrorCodeInvalidResponse,
				"AI provider returned an invalid tool call", 0, false, 0, nil,
			)
		}
		seenIDs[id] = true
		arguments, argumentErr := canonicalJSONObject(
			[]byte(item.Function.Arguments),
		)
		if argumentErr != nil {
			return ToolProviderResult{}, newProviderError(
				ErrorCodeInvalidResponse,
				"AI provider returned invalid tool arguments",
				0, false, 0, argumentErr,
			)
		}
		calls = append(calls, ToolCall{
			ID: id, Name: name, Arguments: arguments,
		})
	}
	if len(calls) == 0 && strings.TrimSpace(content) == "" {
		return ToolProviderResult{}, newProviderError(
			ErrorCodeInvalidResponse,
			"AI provider returned neither content nor tool calls",
			0, false, 0, nil,
		)
	}
	usage := Usage{
		PromptTokens:     envelope.Usage.PromptTokens,
		CompletionTokens: envelope.Usage.CompletionTokens,
		TotalTokens:      envelope.Usage.TotalTokens,
	}
	if err := validateProviderUsage(usage); err != nil {
		return ToolProviderResult{}, err
	}
	return ToolProviderResult{
		Message: ToolMessage{
			Role: MessageRoleAssistant, Content: content,
			ReasoningContent: choice.Message.ReasoningContent,
			ToolCalls:        calls,
		},
		Model:        configuredModel,
		FinishReason: normalizeFinishReason(choice.FinishReason),
		RequestID:    firstNonBlank(headerRequestID, envelope.ID),
		Usage:        usage,
	}, nil
}

func canonicalJSONObject(raw []byte) (json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = []byte(`{}`)
	}
	value, err := decodeSingleJSONValue(raw)
	if err != nil {
		return nil, err
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, errors.New("tool arguments must be a JSON object")
	}
	return json.Marshal(value)
}

var _ ToolCompletionProvider = (*OpenAICompatibleProvider)(nil)
