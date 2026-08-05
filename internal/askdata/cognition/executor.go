package cognition

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/toolhost"
)

const (
	defaultMaxOutputTokens = 8_192
	defaultMaxSeenActions  = 16
	maximumSeenActions     = 64
)

// Invoker is the audited, quota-aware AI boundary used by the cognition
// executor. *ai.Service satisfies this interface.
type Invoker interface {
	Invoke(context.Context, ai.Invocation) (ai.InvocationResult, error)
}

type ExecutorOptions struct {
	MaxOutputTokens int
	MaxSeenActions  int
}

// Executor performs exactly one provider-neutral cognition round. The outer
// question orchestrator owns tool execution and the multi-round state machine.
type Executor struct {
	invoker Invoker
	schema  ai.JSONSchema
	options ExecutorOptions
}

// RoundRequest contains only the sanitized transcript and immutable audit
// references needed for one action. Hidden provider reasoning is deliberately
// absent from both the input and output contracts.
type RoundRequest struct {
	TenantID         string
	ActorID          string
	Stage            Stage
	PromptVersion    string
	ResourceType     string
	ResourceID       string
	PreferredModel   string
	Messages         []ai.Message
	SeenActionHashes []askdata.ContentHash
	SeenToolCallIDs  []askdata.ID
	MaxOutputTokens  int
}

type RoundResult struct {
	Action         Action
	ActionHash     askdata.ContentHash
	AIRequestID    string
	ProviderModel  string
	Attempts       int
	Usage          ai.Usage
	CostMicros     int64
	RedactionCount int
}

func NewExecutor(invoker Invoker, actionSchema ai.JSONSchema, options ExecutorOptions) (*Executor, error) {
	if invoker == nil {
		return nil, errors.New("cognition invoker is required")
	}
	if options.MaxOutputTokens == 0 {
		options.MaxOutputTokens = defaultMaxOutputTokens
	}
	if options.MaxSeenActions == 0 {
		options.MaxSeenActions = defaultMaxSeenActions
	}
	if options.MaxOutputTokens < 1 || options.MaxOutputTokens > 32_768 ||
		options.MaxSeenActions < 1 || options.MaxSeenActions > maximumSeenActions {
		return nil, errors.New("cognition executor options exceed safe bounds")
	}
	// Validate and normalize the provider-facing schema before accepting work.
	if err := ai.ValidateProviderRequest(ai.ProviderRequest{
		Messages: []ai.Message{{
			Role:  ai.MessageRoleUser,
			Parts: []ai.ContentPart{{Type: ai.ContentTypeText, Text: "validate cognition schema"}},
		}},
		ResponseSchema: actionSchema,
	}); err != nil {
		return nil, fmt.Errorf("cognition action schema: %w", err)
	}
	return &Executor{invoker: invoker, schema: actionSchema, options: options}, nil
}

func (executor *Executor) Execute(ctx context.Context, input RoundRequest) (RoundResult, error) {
	if executor == nil || executor.invoker == nil || !validStage(input.Stage) {
		return RoundResult{}, invalidCognitionAction("cognition stage or executor is invalid")
	}
	if strings.TrimSpace(input.TenantID) == "" || strings.TrimSpace(input.ActorID) == "" ||
		strings.TrimSpace(input.PromptVersion) == "" || len(input.SeenActionHashes) > executor.options.MaxSeenActions ||
		len(input.SeenToolCallIDs) > executor.options.MaxSeenActions {
		return RoundResult{}, invalidCognitionAction("cognition round metadata is invalid")
	}
	maxOutputTokens := input.MaxOutputTokens
	if maxOutputTokens == 0 {
		maxOutputTokens = executor.options.MaxOutputTokens
	}
	if maxOutputTokens < 1 || maxOutputTokens > executor.options.MaxOutputTokens {
		return RoundResult{}, invalidCognitionAction("cognition output budget is invalid")
	}
	stageSchema, err := SchemaForStage(executor.schema, input.Stage)
	if err != nil {
		return RoundResult{}, invalidCognitionAction("stage action schema could not be built")
	}
	request := ai.ProviderRequest{
		Messages:        append([]ai.Message(nil), input.Messages...),
		ResponseSchema:  stageSchema,
		MaxOutputTokens: maxOutputTokens,
	}
	if err := ai.ValidateProviderRequest(request); err != nil {
		return RoundResult{}, err
	}

	invocation, err := executor.invoker.Invoke(ctx, ai.Invocation{
		TenantID: input.TenantID, ActorID: input.ActorID,
		Purpose: ai.PurposeSemanticQuestion, PromptVersion: strings.TrimSpace(input.PromptVersion),
		ResourceType: strings.TrimSpace(input.ResourceType), ResourceID: strings.TrimSpace(input.ResourceID),
		PreferredModel: strings.TrimSpace(input.PreferredModel), Request: request,
	})
	if err != nil {
		return RoundResult{}, err
	}
	if len(bytes.TrimSpace(invocation.ProviderResult.Content)) == 0 {
		return RoundResult{}, invalidCognitionAction("provider returned an empty cognition action")
	}
	switch strings.ToLower(strings.TrimSpace(invocation.ProviderResult.FinishReason)) {
	case "", "stop":
		// Some compatible fixtures omit finish_reason; production providers are
		// normalized by ai.Service. Both represent a complete structured action.
	case "length":
		return RoundResult{}, invalidCognitionAction("provider cognition action was truncated")
	default:
		return RoundResult{}, invalidCognitionAction("provider cognition action did not finish normally")
	}
	action, err := Decode(invocation.ProviderResult.Content)
	if err != nil {
		return RoundResult{}, invalidCognitionAction("provider returned an invalid cognition action")
	}
	if action.Stage != input.Stage {
		return RoundResult{}, invalidCognitionAction("provider returned an action for a different stage")
	}
	canonical, err := json.Marshal(action)
	if err != nil {
		return RoundResult{}, invalidCognitionAction("cognition action could not be normalized")
	}
	actionHash := askdata.HashBytes(canonical)
	for _, seen := range input.SeenActionHashes {
		if err := seen.Validate(); err != nil {
			return RoundResult{}, invalidCognitionAction("seen cognition action hash is invalid")
		}
		if seen == actionHash {
			return RoundResult{}, noProgressError()
		}
	}
	seenToolCalls := make(map[askdata.ID]struct{}, len(input.SeenToolCallIDs))
	for _, callID := range input.SeenToolCallIDs {
		if err := callID.Validate(); err != nil {
			return RoundResult{}, invalidCognitionAction("seen tool call ID is invalid")
		}
		seenToolCalls[callID] = struct{}{}
	}
	if action.ToolCall != nil {
		if _, duplicate := seenToolCalls[action.ToolCall.CallID]; duplicate {
			return RoundResult{}, noProgressError()
		}
	}

	return RoundResult{
		Action: action, ActionHash: actionHash,
		AIRequestID: invocation.RequestID, ProviderModel: invocation.ProviderResult.Model,
		Attempts: invocation.Attempts, Usage: invocation.ProviderResult.Usage,
		CostMicros: invocation.CostMicros, RedactionCount: invocation.RedactionCount,
	}, nil
}

// AssistantMessage converts a validated action into the next transcript turn.
// It serializes only the structured decision, never provider reasoning.
func AssistantMessage(result RoundResult) (ai.Message, error) {
	if err := result.Action.Validate(); err != nil {
		return ai.Message{}, fmt.Errorf("assistant action: %w", err)
	}
	payload, err := json.Marshal(result.Action)
	if err != nil {
		return ai.Message{}, err
	}
	return ai.Message{
		Role:  ai.MessageRoleAssistant,
		Parts: []ai.ContentPart{{Type: ai.ContentTypeText, Text: string(payload)}},
	}, nil
}

// ToolMessage validates the typed, sanitized Tool Host response before it can
// be returned to a model. Arbitrary tool names and unbounded result bodies are
// rejected by toolhost.Response.Validate.
func ToolMessage(response toolhost.Response) (ai.Message, error) {
	if err := response.Validate(); err != nil {
		return ai.Message{}, fmt.Errorf("tool response: %w", err)
	}
	payload, err := json.Marshal(response)
	if err != nil {
		return ai.Message{}, err
	}
	return ai.Message{
		Role:       ai.MessageRoleTool,
		Parts:      []ai.ContentPart{{Type: ai.ContentTypeText, Text: string(payload)}},
		ToolCallID: string(response.CallID), ToolName: string(response.Tool),
	}, nil
}

func invalidCognitionAction(message string) error {
	return &ai.ProviderError{Code: ai.ErrorCodeInvalidOutput, Message: message}
}

func noProgressError() error {
	return &ai.ProviderError{
		Code:    ai.ErrorCodeToolNoProgress,
		Message: "cognition action repeated without new evidence",
	}
}
