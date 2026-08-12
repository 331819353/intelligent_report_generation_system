package cognition

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/toolhost"
)

const (
	defaultMaxOutputTokens = 8_192
	defaultMaxSeenActions  = 16
	maximumSeenActions     = 64
	maximumRepairCandidate = 64 << 10
)

// Invoker is the audited, quota-aware AI boundary used by the cognition
// executor. *ai.Service satisfies this interface.
type Invoker interface {
	Invoke(context.Context, ai.Invocation) (ai.InvocationResult, error)
}

type modelCatalogInvoker interface {
	ConfiguredModels() []string
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
	Provider       string
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

	invocationInput := ai.Invocation{
		TenantID: input.TenantID, ActorID: input.ActorID,
		Purpose: ai.PurposeSemanticQuestion, PromptVersion: strings.TrimSpace(input.PromptVersion),
		ResourceType: strings.TrimSpace(input.ResourceType), ResourceID: strings.TrimSpace(input.ResourceID),
		PreferredModel: strings.TrimSpace(input.PreferredModel), Request: request,
	}
	invocation, err := executor.invoker.Invoke(ctx, invocationInput)
	if err != nil {
		// Structured-output candidates never cross the provider boundary into
		// logs, but the validator's field-path-only diagnostic is safe and makes
		// production incompatibilities actionable instead of looking like a
		// generic model outage.
		if diagnostic := cognitionOutputDiagnostic(stageSchema, err); diagnostic != "" {
			slog.Warn("reject provider structured output", "stage", input.Stage, "diagnostic", diagnostic)
		}
		// Compatible providers differ in how reliably they honour a deeply typed
		// JSON contract. A schema-invalid candidate is safe to hand back to the
		// provider that produced it, and an invalid response envelope is safe to
		// retry as a fresh audited invocation. The model pool is round-robin in
		// production, so this single logical repair also gives the other provider
		// a chance without creating an unbounded retry loop.
		if repairRequest, ok := cognitionRepairRequest(request, stageSchema, err); ok {
			invocationInput.Request = repairRequest
			// Repair an available candidate with the model that produced it. A
			// malformed success envelope has no candidate to repair; let the provider
			// pool choose the next model instead of pinning the retry to the model that
			// just failed the transport-level response contract.
			var providerError *ai.ProviderError
			if !errors.As(err, &providerError) || providerError.Code != ai.ErrorCodeInvalidResponse {
				invocationInput.PreferredModel = strings.TrimSpace(invocation.ProviderResult.Model)
			} else {
				invocationInput.PreferredModel = executor.alternateModel(invocation.ProviderResult.Model)
			}
			invocation, err = executor.invoker.Invoke(ctx, invocationInput)
			if err != nil {
				if diagnostic := cognitionOutputDiagnostic(stageSchema, err); diagnostic != "" {
					slog.Warn("reject repaired provider structured output", "stage", input.Stage, "diagnostic", diagnostic)
				}
				// A provider that cannot honour the closed action contract after one
				// repair must not block the whole governed question. Restart this one
				// cognition round from its original facts on a different configured
				// model. This is explicit and bounded, so it remains fully audited and
				// cannot become an unbounded cross-provider retry loop.
				failedModel := invocation.ProviderResult.Model
				if failedModel == "" {
					failedModel = invocationInput.PreferredModel
				}
				alternate := executor.alternateModel(failedModel)
				if alternate == "" || strings.EqualFold(alternate, strings.TrimSpace(invocationInput.PreferredModel)) {
					return RoundResult{}, err
				}
				invocationInput.Request = request
				invocationInput.PreferredModel = alternate
				invocation, err = executor.invoker.Invoke(ctx, invocationInput)
				if err != nil {
					if diagnostic := cognitionOutputDiagnostic(stageSchema, err); diagnostic != "" {
						slog.Warn("reject alternate provider structured output", "stage", input.Stage, "diagnostic", diagnostic)
					}
					return RoundResult{}, err
				}
			}
		} else {
			return RoundResult{}, err
		}
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
		// The raw provider body is intentionally never logged. The local typed
		// validator only reports contract field names and stable validation rules,
		// which gives operators enough signal to repair the schema without leaking
		// question text or model output.
		slog.Warn("reject cognition action", "stage", input.Stage, "error", err)
		// JSON Schema and the executable Go contract deliberately form two
		// independent gates. A provider can satisfy the generic JSON shape while
		// still missing a tool-specific argument or violating a cross-field rule.
		// Give that exact, bounded candidate one audited structural-repair turn,
		// just as we already do for provider-side schema validation failures.
		repairRequest, ok := cognitionTypedRepairRequest(request, invocation.ProviderResult.Content, err)
		if !ok {
			return RoundResult{}, invalidCognitionAction("provider returned an invalid cognition action")
		}
		invocationInput.Request = repairRequest
		invocationInput.PreferredModel = strings.TrimSpace(invocation.ProviderResult.Model)
		invocation, err = executor.invoker.Invoke(ctx, invocationInput)
		if err != nil {
			if diagnostic := cognitionOutputDiagnostic(stageSchema, err); diagnostic != "" {
				slog.Warn("reject typed cognition repair", "stage", input.Stage, "diagnostic", diagnostic)
			}
			return RoundResult{}, err
		}
		action, err = Decode(invocation.ProviderResult.Content)
		if err != nil {
			slog.Warn("reject repaired cognition action", "stage", input.Stage, "error", err)
			alternate := executor.alternateModel(invocation.ProviderResult.Model)
			if alternate == "" || strings.EqualFold(alternate, strings.TrimSpace(invocationInput.PreferredModel)) {
				return RoundResult{}, invalidCognitionAction("provider returned an invalid cognition action after repair")
			}
			invocationInput.Request = request
			invocationInput.PreferredModel = alternate
			invocation, err = executor.invoker.Invoke(ctx, invocationInput)
			if err != nil {
				return RoundResult{}, err
			}
			action, err = Decode(invocation.ProviderResult.Content)
			if err != nil {
				slog.Warn("reject alternate cognition action", "stage", input.Stage, "error", err)
				return RoundResult{}, invalidCognitionAction("configured models returned invalid cognition actions")
			}
		}
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
		AIRequestID: invocation.RequestID, Provider: invocation.Provider,
		ProviderModel: invocation.ProviderResult.Model,
		Attempts:      invocation.Attempts, Usage: invocation.ProviderResult.Usage,
		CostMicros: invocation.CostMicros, RedactionCount: invocation.RedactionCount,
	}, nil
}

func (executor *Executor) alternateModel(failed string) string {
	catalog, ok := executor.invoker.(modelCatalogInvoker)
	if !ok {
		return ""
	}
	failed = strings.TrimSpace(failed)
	for _, model := range catalog.ConfiguredModels() {
		model = strings.TrimSpace(model)
		if model != "" && !strings.EqualFold(model, failed) {
			return model
		}
	}
	return ""
}

func cognitionTypedRepairRequest(request ai.ProviderRequest, candidate json.RawMessage, failure error) (ai.ProviderRequest, bool) {
	if len(candidate) == 0 || len(candidate) > maximumRepairCandidate || failure == nil {
		return ai.ProviderRequest{}, false
	}
	repair := request
	repair.Messages = append(append([]ai.Message(nil), request.Messages...),
		ai.Message{
			Role:  ai.MessageRoleAssistant,
			Parts: []ai.ContentPart{{Type: ai.ContentTypeText, Text: string(candidate)}},
		},
		ai.Message{
			Role: ai.MessageRoleUser,
			Parts: []ai.ContentPart{{
				Type: ai.ContentTypeText,
				Text: "上一份候选通过了 JSON 外形校验，但未通过工具参数或跨字段业务合同。只修正 JSON 结构与必填字段，不改变业务判断，不输出解释。严格依据响应 Schema 补齐所选工具的全部必填 arguments，并确保字段值来自已有事实。",
			}},
		},
	)
	return repair, true
}

func cognitionRepairRequest(request ai.ProviderRequest, schema ai.JSONSchema, failure error) (ai.ProviderRequest, bool) {
	candidate, diagnostic, invalidOutput := ai.InvalidOutputDetails(failure)
	if invalidOutput {
		if precise := cognitionCandidateDiagnostic(schema, candidate); precise != "" {
			diagnostic = precise
		}
		if len(candidate) == 0 || len(candidate) > maximumRepairCandidate || strings.TrimSpace(diagnostic) == "" {
			return ai.ProviderRequest{}, false
		}
		repair := request
		repair.Messages = append(append([]ai.Message(nil), request.Messages...),
			ai.Message{
				Role:  ai.MessageRoleAssistant,
				Parts: []ai.ContentPart{{Type: ai.ContentTypeText, Text: string(candidate)}},
			},
			ai.Message{
				Role: ai.MessageRoleUser,
				Parts: []ai.ContentPart{{
					Type: ai.ContentTypeText,
					Text: "上一份候选未通过响应合同。只修正 JSON 结构，不改变业务判断，也不要输出解释。校验规则：" + diagnostic,
				}},
			},
		)
		return repair, true
	}
	var providerError *ai.ProviderError
	if !errors.As(failure, &providerError) {
		return ai.ProviderRequest{}, false
	}
	switch providerError.Code {
	case ai.ErrorCodeInvalidResponse:
		// No candidate is available to repair, so reissue the original bounded
		// request once. Provider/network retryability is already handled inside
		// ai.Service; this branch is specifically for a malformed success envelope.
		return request, true
	default:
		return ai.ProviderRequest{}, false
	}
}

// cognitionCandidateDiagnostic selects the action's discriminated branch and
// re-validates against that single closed object. A root oneOf error is too
// vague for a model to repair; the branch diagnostic exposes only schema paths
// and rules, never candidate values or question text.
func cognitionCandidateDiagnostic(schema ai.JSONSchema, candidate json.RawMessage) string {
	var value map[string]any
	if json.Unmarshal(candidate, &value) != nil {
		return ""
	}
	action, ok := value["action"].(string)
	if !ok || action == "" {
		return ""
	}
	var root map[string]any
	if json.Unmarshal(schema.Schema, &root) != nil {
		return ""
	}
	branches, ok := root["oneOf"].([]any)
	if !ok {
		return ""
	}
	for _, rawBranch := range branches {
		branch, ok := rawBranch.(map[string]any)
		if !ok {
			continue
		}
		properties, _ := branch["properties"].(map[string]any)
		actionSchema, _ := properties["action"].(map[string]any)
		if actionSchema["const"] != action {
			continue
		}
		branchRoot := make(map[string]any, len(branch)+1)
		for key, child := range branch {
			branchRoot[key] = child
		}
		branchRoot["$defs"] = root["$defs"]
		encoded, err := json.Marshal(branchRoot)
		if err != nil {
			return ""
		}
		_, validationErr := ai.ValidateStructuredOutput(ai.JSONSchema{
			Name: schema.Name, Description: schema.Description, Schema: encoded,
		}, candidate)
		return ai.InvalidOutputDiagnostic(validationErr)
	}
	return ""
}

func cognitionOutputDiagnostic(schema ai.JSONSchema, failure error) string {
	candidate, _, invalid := ai.InvalidOutputDetails(failure)
	if invalid {
		if diagnostic := cognitionCandidateDiagnostic(schema, candidate); diagnostic != "" {
			return diagnostic
		}
	}
	return ai.InvalidOutputDiagnostic(failure)
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
// be returned to a model. The cognition protocol uses structured JSON actions,
// not provider-native function calling, so the result is a new governed user
// turn rather than an OpenAI `tool` role. Sending a tool role without a matching
// provider tool_calls envelope is rejected by compliant providers.
func ToolMessage(response toolhost.Response) (ai.Message, error) {
	if err := response.Validate(); err != nil {
		return ai.Message{}, fmt.Errorf("tool response: %w", err)
	}
	payload, err := json.Marshal(response)
	if err != nil {
		return ai.Message{}, err
	}
	return ai.Message{
		Role:  ai.MessageRoleUser,
		Parts: []ai.ContentPart{{Type: ai.ContentTypeText, Text: "GOVERNED_TOOL_RESULT\n" + string(payload)}},
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
