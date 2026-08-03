package ai

import (
	"context"
	"encoding/json"
)

const (
	MaxToolLoopRounds       = 3
	MaxToolCallsPerLoop     = 8
	MaxToolResultBytes      = 32 << 10
	MaxToolLoopResultBytes  = 64 << 10
	maxToolCallArgumentSize = 64 << 10
)

// ToolCompletionProvider is the provider-neutral boundary implemented by
// OpenAI-compatible DeepSeek, GLM and MiniMax endpoints.
type ToolCompletionProvider interface {
	CompleteWithTools(context.Context, ToolProviderRequest) (ToolProviderResult, error)
}

type ToolChoice string

const (
	ToolChoiceAuto     ToolChoice = "auto"
	ToolChoiceNone     ToolChoice = "none"
	ToolChoiceRequired ToolChoice = "required"
)

// ToolDefinition is a read-only function contract. Parameters must use the
// same closed JSON Schema subset as structured model output.
type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// ToolMessage is an internal, ephemeral conversation item. ReasoningContent is
// replayed only inside one bounded loop and is never persisted or returned by
// the service.
type ToolMessage struct {
	Role             MessageRole `json:"role"`
	Content          string      `json:"content,omitempty"`
	ReasoningContent string      `json:"reasoningContent,omitempty"`
	ToolCalls        []ToolCall  `json:"toolCalls,omitempty"`
	ToolCallID       string      `json:"toolCallId,omitempty"`
}

type ToolProviderRequest struct {
	Messages        []ToolMessage    `json:"messages"`
	Tools           []ToolDefinition `json:"tools"`
	ToolChoice      ToolChoice       `json:"toolChoice"`
	Thinking        bool             `json:"thinking"`
	Temperature     *float64         `json:"temperature,omitempty"`
	MaxOutputTokens int              `json:"maxOutputTokens"`
}

type ToolProviderResult struct {
	Message      ToolMessage `json:"message"`
	Model        string      `json:"model"`
	FinishReason string      `json:"finishReason,omitempty"`
	RequestID    string      `json:"requestId,omitempty"`
	Usage        Usage       `json:"usage"`
}

type ToolLoopRequest struct {
	Messages        []Message        `json:"messages"`
	Tools           []ToolDefinition `json:"tools"`
	ToolChoice      ToolChoice       `json:"toolChoice,omitempty"`
	Thinking        bool             `json:"thinking"`
	Temperature     *float64         `json:"temperature,omitempty"`
	MaxOutputTokens int              `json:"maxOutputTokens"`
	MaxRounds       int              `json:"maxRounds"`
	MaxToolCalls    int              `json:"maxToolCalls"`
}

type ToolExecution struct {
	Name      string
	Arguments json.RawMessage
}

type ToolExecutionResult struct {
	// Content must be one bounded JSON value. It is returned to the model for a
	// normal tool and becomes the final structured result for a terminal tool.
	Content json.RawMessage
	// EvidenceIDs identify governed records or immutable retrieval observations.
	// A non-terminal successful call may continue only when it contributes at
	// least one evidence ID that has not appeared earlier in this loop.
	EvidenceIDs []string
	// ErrorCode is a stable tool-registry code. Only a repairable error is
	// returned to the model; a non-repairable error closes the loop locally.
	ErrorCode  string
	Repairable bool
	Terminal   bool
}

type ToolExecutor interface {
	ExecuteTool(context.Context, ToolExecution) (ToolExecutionResult, error)
}

type ToolInvocation struct {
	TenantID       string
	ActorID        string
	Purpose        string
	PromptVersion  string
	ResourceType   string
	ResourceID     string
	PreferredModel string
	Request        ToolLoopRequest
	Executor       ToolExecutor
}

type ToolLoopTrace struct {
	Rounds      int                 `json:"rounds"`
	ToolCalls   int                 `json:"toolCalls"`
	EvidenceIDs []string            `json:"evidenceIds"`
	Steps       []ToolLoopStepTrace `json:"steps"`
}

type ToolLoopStepTrace struct {
	Round            int      `json:"round"`
	ToolName         string   `json:"toolName"`
	ArgumentsHash    string   `json:"argumentsHash"`
	StateHash        string   `json:"stateHash"`
	EvidenceIDs      []string `json:"evidenceIds"`
	NewEvidenceCount int      `json:"newEvidenceCount"`
	ErrorCode        string   `json:"errorCode,omitempty"`
	Terminal         bool     `json:"terminal"`
}

type ToolInvocationResult struct {
	RequestID      string          `json:"requestId"`
	Content        json.RawMessage `json:"content"`
	Model          string          `json:"model"`
	Usage          Usage           `json:"usage"`
	Attempts       int             `json:"attempts"`
	CostMicros     int64           `json:"costMicros"`
	RedactionCount int             `json:"redactionCount"`
	Trace          ToolLoopTrace   `json:"trace"`
}
