package ai

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

var toolNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,63}$`)
var evidenceIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:._@/-]{0,255}$`)

// InvokeToolLoop runs a bounded, audited think/tool/think loop. Tool execution
// remains inside the caller supplied authorization boundary; provider-generated
// names and arguments are revalidated locally before every execution.
func (s *Service) InvokeToolLoop(
	ctx context.Context,
	input ToolInvocation,
) (ToolInvocationResult, error) {
	if !s.Configured() || input.Executor == nil {
		return ToolInvocationResult{}, newProviderError(
			ErrorCodeProviderUnavailable,
			"AI tool loop is not configured", 0, false, 0, nil,
		)
	}
	provider, err := s.selectInvocationProvider(input.PreferredModel)
	if err != nil {
		return ToolInvocationResult{}, err
	}
	toolProvider, ok := provider.(ToolCompletionProvider)
	if !ok {
		return ToolInvocationResult{}, newProviderError(
			ErrorCodeProviderUnavailable,
			"AI provider does not support tool calls", 0, false, 0, nil,
		)
	}
	input.Purpose = strings.ToUpper(strings.TrimSpace(input.Purpose))
	input.PromptVersion = strings.TrimSpace(input.PromptVersion)
	if strings.TrimSpace(input.TenantID) == "" ||
		strings.TrimSpace(input.ActorID) == "" ||
		!allowedPurpose(input.Purpose) || input.PromptVersion == "" ||
		len(input.PromptVersion) > 128 {
		return ToolInvocationResult{}, ErrInvalidInvocation
	}
	request, redactions, inputBytes, err := sanitizeToolLoopRequest(
		input.Request, s.options.MaxInputBytes,
	)
	if err != nil {
		return ToolInvocationResult{}, err
	}
	requestHash, err := hashToolLoopRequest(request)
	if err != nil {
		return ToolInvocationResult{}, err
	}
	maxAttempts := min(5, request.MaxRounds+1)
	worstInputTokens := saturatingAddInt(
		inputBytes, MaxToolLoopResultBytes,
	)
	perAttemptTokens := saturatingAddInt(
		worstInputTokens, request.MaxOutputTokens,
	)
	reservedTokens := saturatingMultiplyInt(perAttemptTokens, maxAttempts)
	perAttemptCost := calculateCostMicros(
		worstInputTokens, request.MaxOutputTokens, s.options,
	)
	reservedCost := saturatingMultiplyInt64(
		perAttemptCost, int64(maxAttempts),
	)
	record, err := s.store.Start(ctx, StartRequest{
		TenantID: input.TenantID, ActorID: input.ActorID,
		Purpose: input.Purpose, PromptVersion: input.PromptVersion,
		Provider: provider.Name(), Model: provider.Model(),
		InputHash:    requestHash,
		ResourceType: strings.TrimSpace(input.ResourceType),
		ResourceID:   strings.TrimSpace(input.ResourceID),
		InputBytes:   inputBytes, RedactionCount: redactions,
		ReservedTokens: reservedTokens, ReservedCostMicros: reservedCost,
		MaxAttempts: maxAttempts,
	})
	if err != nil {
		return ToolInvocationResult{}, err
	}

	started := s.now()
	callCtx, cancel := context.WithTimeout(ctx, s.options.Timeout)
	defer cancel()
	result, attempts, callErr := s.runToolLoop(
		callCtx, toolProvider, provider.Model(), request, input.Executor,
		maxAttempts,
	)
	latency := nonNegativeMilliseconds(s.now().Sub(started))
	if callErr != nil {
		classified := NormalizeProviderError(callErr)
		if persistErr := s.persistFailure(
			ctx, input.TenantID, record.ID,
			FailureRecord{
				Attempts:  max(1, attempts),
				ErrorCode: string(classified.Code), LatencyMS: latency,
			},
		); persistErr != nil {
			return ToolInvocationResult{
				RequestID: record.ID, Attempts: attempts,
				RedactionCount: redactions,
			}, errors.Join(callErr, persistErr)
		}
		return ToolInvocationResult{
			RequestID: record.ID, Attempts: attempts,
			RedactionCount: redactions,
		}, callErr
	}
	cost := calculateCostMicros(
		result.Usage.PromptTokens, result.Usage.CompletionTokens, s.options,
	)
	if err := s.persistCompletion(
		ctx, input.TenantID, record.ID,
		CompletionRecord{
			ProviderModel:     result.Model,
			ProviderRequestID: result.providerRequestID,
			FinishReason:      "tool_calls", Attempts: attempts,
			PromptTokens:     result.Usage.PromptTokens,
			CompletionTokens: result.Usage.CompletionTokens,
			TotalTokens:      result.Usage.TotalTokens,
			CostMicros:       cost, LatencyMS: latency,
		},
	); err != nil {
		return ToolInvocationResult{
			RequestID: record.ID, Content: result.Content,
			Model: result.Model, Usage: result.Usage, Attempts: attempts,
			CostMicros: cost, RedactionCount: redactions,
			Trace: result.Trace,
		}, err
	}
	return ToolInvocationResult{
		RequestID: record.ID, Content: result.Content,
		Model: result.Model, Usage: result.Usage, Attempts: attempts,
		CostMicros: cost, RedactionCount: redactions,
		Trace: result.Trace,
	}, nil
}

type internalToolLoopResult struct {
	Content           json.RawMessage
	Model             string
	Usage             Usage
	Trace             ToolLoopTrace
	providerRequestID string
}

func (s *Service) runToolLoop(
	ctx context.Context,
	provider ToolCompletionProvider,
	configuredModel string,
	request ToolLoopRequest,
	executor ToolExecutor,
	maxAttempts int,
) (internalToolLoopResult, int, error) {
	messages := make([]ToolMessage, 0, len(request.Messages)+request.MaxRounds*2)
	for _, message := range request.Messages {
		content := ""
		for _, part := range message.Parts {
			if part.Type != ContentTypeText {
				return internalToolLoopResult{}, 0, invalidProviderRequest(
					"工具循环只接受文本消息",
				)
			}
			if content != "" {
				content += "\n"
			}
			content += part.Text
		}
		messages = append(messages, ToolMessage{
			Role: message.Role, Content: content,
		})
	}
	toolByName := make(map[string]ToolDefinition, len(request.Tools))
	for _, tool := range request.Tools {
		toolByName[tool.Name] = tool
	}
	usage := Usage{}
	attempts, toolCalls, totalToolResultBytes := 0, 0, 0
	requestIDs := []string{}
	traceSteps := []ToolLoopStepTrace{}
	evidenceByID := map[string]struct{}{}
	actionSignatures := map[string]struct{}{}
	for round := 1; round <= request.MaxRounds; round++ {
		providerRequest := ToolProviderRequest{
			Messages: messages, Tools: request.Tools,
			ToolChoice: request.ToolChoice, Thinking: request.Thinking,
			Temperature:     request.Temperature,
			MaxOutputTokens: request.MaxOutputTokens,
		}
		var step ToolProviderResult
		var callErr error
		for attempts < maxAttempts {
			attempts++
			attemptCtx, attemptCancel := context.WithTimeout(
				ctx, s.options.AttemptTimeout,
			)
			step, callErr = provider.CompleteWithTools(
				attemptCtx, providerRequest,
			)
			attemptErr := attemptCtx.Err()
			attemptCancel()
			if callErr == nil && attemptErr != nil {
				callErr = attemptErr
			}
			if callErr == nil {
				break
			}
			classified := NormalizeProviderError(callErr)
			callErr = classified
			if !classified.Retryable || attempts >= maxAttempts ||
				ctx.Err() != nil {
				break
			}
			if err := s.wait(
				ctx, retryDelay(attempts, classified.RetryAfter, s.options),
			); err != nil {
				callErr = err
				break
			}
		}
		if callErr != nil {
			return internalToolLoopResult{}, attempts, callErr
		}
		if strings.TrimSpace(step.Model) != "" &&
			!strings.EqualFold(strings.TrimSpace(step.Model), configuredModel) {
			return internalToolLoopResult{}, attempts, newProviderError(
				ErrorCodeInvalidResponse,
				"AI provider returned an unexpected model", 0, false, 0, nil,
			)
		}
		if err := validateProviderUsage(step.Usage); err != nil {
			return internalToolLoopResult{}, attempts, err
		}
		usage = addUsage(usage, step.Usage)
		requestIDs = append(requestIDs, step.RequestID)
		assistant := step.Message
		assistant.Role = MessageRoleAssistant
		messages = append(messages, assistant)
		if len(assistant.ToolCalls) == 0 {
			return internalToolLoopResult{}, attempts, newProviderError(
				ErrorCodeInvalidOutput,
				"AI tool loop ended without a terminal tool", 0, false, 0, nil,
			)
		}
		terminalCalls := 0
		var terminalResult json.RawMessage
		for _, call := range assistant.ToolCalls {
			toolCalls++
			if toolCalls > request.MaxToolCalls {
				return internalToolLoopResult{}, attempts, newProviderError(
					ErrorCodeInvalidOutput,
					"AI tool loop exceeded its tool-call budget",
					0, false, 0, nil,
				)
			}
			definition, exists := toolByName[call.Name]
			if !exists {
				return internalToolLoopResult{}, attempts, newProviderError(
					ErrorCodeInvalidOutput,
					"AI requested an unknown tool", 0, false, 0, nil,
				)
			}
			arguments, err := ValidateStructuredOutput(
				JSONSchema{Name: definition.Name, Schema: definition.Parameters},
				call.Arguments,
			)
			if err != nil {
				return internalToolLoopResult{}, attempts, err
			}
			argumentsHash := hashBytes(arguments)
			stateHash := hashEvidenceState(evidenceByID)
			executionResult, err := executor.ExecuteTool(
				ctx, ToolExecution{Name: call.Name, Arguments: arguments},
			)
			if err != nil {
				return internalToolLoopResult{}, attempts, err
			}
			executionResult.ErrorCode = strings.TrimSpace(executionResult.ErrorCode)
			signature := stateHash + "\x00" + call.Name + "\x00" +
				argumentsHash + "\x00" + executionResult.ErrorCode
			if _, repeated := actionSignatures[signature]; repeated {
				return internalToolLoopResult{}, attempts, newProviderError(
					ErrorCodeToolNoProgress,
					"AI tool loop repeated an action without new evidence",
					0, false, 0, nil,
				)
			}
			actionSignatures[signature] = struct{}{}
			evidenceIDs, err := normalizeEvidenceIDs(executionResult.EvidenceIDs)
			if err != nil {
				return internalToolLoopResult{}, attempts, err
			}
			newEvidenceCount := 0
			for _, evidenceID := range evidenceIDs {
				if _, exists := evidenceByID[evidenceID]; exists {
					continue
				}
				evidenceByID[evidenceID] = struct{}{}
				newEvidenceCount++
			}
			if executionResult.ErrorCode != "" && !executionResult.Repairable {
				return internalToolLoopResult{}, attempts, newProviderError(
					ErrorCodeToolExecutionBlocked,
					"tool registry rejected a non-repairable action",
					0, false, 0, nil,
				)
			}
			if !executionResult.Terminal && executionResult.ErrorCode == "" &&
				newEvidenceCount == 0 {
				return internalToolLoopResult{}, attempts, newProviderError(
					ErrorCodeToolNoProgress,
					"tool call produced no new evidence", 0, false, 0, nil,
				)
			}
			content, err := canonicalToolResult(executionResult.Content)
			if err != nil {
				return internalToolLoopResult{}, attempts, err
			}
			if len(content) > MaxToolResultBytes {
				return internalToolLoopResult{}, attempts, newProviderError(
					ErrorCodeInvalidOutput,
					"tool result exceeded its byte budget", 0, false, 0, nil,
				)
			}
			totalToolResultBytes += len(content)
			if totalToolResultBytes > MaxToolLoopResultBytes {
				return internalToolLoopResult{}, attempts, newProviderError(
					ErrorCodeInvalidOutput,
					"tool loop results exceeded their byte budget",
					0, false, 0, nil,
				)
			}
			if executionResult.Terminal {
				if len(evidenceByID) == 0 || executionResult.ErrorCode != "" {
					return internalToolLoopResult{}, attempts, newProviderError(
						ErrorCodeToolNoProgress,
						"terminal tool requires prior governed evidence",
						0, false, 0, nil,
					)
				}
				terminalCalls++
				terminalResult = content
				traceSteps = append(traceSteps, ToolLoopStepTrace{
					Round: round, ToolName: call.Name,
					ArgumentsHash: argumentsHash, StateHash: stateHash,
					EvidenceIDs: evidenceIDs, NewEvidenceCount: newEvidenceCount,
					ErrorCode: executionResult.ErrorCode, Terminal: true,
				})
				continue
			}
			traceSteps = append(traceSteps, ToolLoopStepTrace{
				Round: round, ToolName: call.Name,
				ArgumentsHash: argumentsHash, StateHash: stateHash,
				EvidenceIDs: evidenceIDs, NewEvidenceCount: newEvidenceCount,
				ErrorCode: executionResult.ErrorCode, Terminal: false,
			})
			messages = append(messages, ToolMessage{
				Role: MessageRoleTool, ToolCallID: call.ID,
				Content: string(content),
			})
		}
		if terminalCalls > 0 {
			if terminalCalls != 1 || len(assistant.ToolCalls) != 1 {
				return internalToolLoopResult{}, attempts, newProviderError(
					ErrorCodeInvalidOutput,
					"terminal tool must be the only call in its step",
					0, false, 0, nil,
				)
			}
			if err := validateProviderUsage(usage); err != nil {
				return internalToolLoopResult{}, attempts, err
			}
			return internalToolLoopResult{
				Content: terminalResult, Model: configuredModel, Usage: usage,
				Trace: ToolLoopTrace{
					Rounds: round, ToolCalls: toolCalls,
					EvidenceIDs: sortedEvidenceIDs(evidenceByID),
					Steps:       append([]ToolLoopStepTrace(nil), traceSteps...),
				},
				providerRequestID: hashProviderRequestIDs(requestIDs),
			}, attempts, nil
		}
		if attempts >= maxAttempts {
			break
		}
	}
	return internalToolLoopResult{}, attempts, newProviderError(
		ErrorCodeInvalidOutput,
		"AI tool loop did not reach a terminal tool", 0, false, 0, nil,
	)
}

func (s *Service) selectInvocationProvider(
	preferredModel string,
) (Provider, error) {
	preferredModel = strings.TrimSpace(preferredModel)
	provider := s.provider
	if preferredModel != "" {
		if selector, ok := provider.(ModelProviderSelector); ok {
			provider = selector.SelectProviderModel(preferredModel)
		} else if !strings.EqualFold(provider.Model(), preferredModel) {
			provider = nil
		}
	} else if selector, ok := provider.(ProviderSelector); ok {
		provider = selector.SelectProvider()
	}
	if provider == nil || !provider.Configured() {
		return nil, newProviderError(
			ErrorCodeProviderUnavailable,
			"requested AI model is not configured", 0, false, 0, nil,
		)
	}
	return provider, nil
}

func sanitizeToolLoopRequest(
	input ToolLoopRequest,
	maxInputBytes int,
) (ToolLoopRequest, int, int, error) {
	if input.ToolChoice == "" {
		input.ToolChoice = ToolChoiceAuto
	}
	if input.MaxRounds == 0 {
		input.MaxRounds = MaxToolLoopRounds
	}
	if input.MaxToolCalls == 0 {
		input.MaxToolCalls = MaxToolCallsPerLoop
	}
	if input.MaxRounds < 1 || input.MaxRounds > MaxToolLoopRounds ||
		input.MaxToolCalls < 1 || input.MaxToolCalls > MaxToolCallsPerLoop ||
		!oneOfString(
			string(input.ToolChoice), string(ToolChoiceAuto),
			string(ToolChoiceNone), string(ToolChoiceRequired),
		) || input.MaxOutputTokens < 1 || input.MaxOutputTokens > 32_768 ||
		len(input.Tools) < 1 || len(input.Tools) > 32 {
		return ToolLoopRequest{}, 0, 0, invalidProviderRequest(
			"工具循环边界无效",
		)
	}
	dummy := ProviderRequest{
		Messages: input.Messages,
		ResponseSchema: JSONSchema{
			Name: "tool_loop_input",
			Schema: json.RawMessage(
				`{"type":"object","additionalProperties":false,"required":[],"properties":{}}`,
			),
		},
		Temperature:     input.Temperature,
		MaxOutputTokens: input.MaxOutputTokens,
	}
	sanitized, redactions, _, err := sanitizeProviderRequest(
		dummy, maxInputBytes,
	)
	if err != nil {
		return ToolLoopRequest{}, 0, 0, err
	}
	result := input
	result.Messages = sanitized.Messages
	result.Temperature = sanitized.Temperature
	result.Tools = make([]ToolDefinition, 0, len(input.Tools))
	seen := map[string]bool{}
	for _, tool := range input.Tools {
		tool.Name = strings.TrimSpace(tool.Name)
		if !toolNamePattern.MatchString(tool.Name) || seen[tool.Name] ||
			!utf8.ValidString(tool.Description) ||
			len([]rune(tool.Description)) < 1 ||
			len([]rune(tool.Description)) > 1024 {
			return ToolLoopRequest{}, 0, 0, invalidProviderRequest(
				"工具定义无效",
			)
		}
		seen[tool.Name] = true
		description, count := redactSensitiveText(
			strings.TrimSpace(tool.Description),
		)
		redactions += count
		if _, count := redactSensitiveText(string(tool.Parameters)); count > 0 {
			return ToolLoopRequest{}, 0, 0, invalidProviderRequest(
				"工具参数 Schema 包含疑似凭证",
			)
		}
		schema, _, err := normalizeJSONSchema(JSONSchema{
			Name: tool.Name, Schema: tool.Parameters,
		})
		if err != nil {
			return ToolLoopRequest{}, 0, 0, err
		}
		result.Tools = append(result.Tools, ToolDefinition{
			Name: tool.Name, Description: description,
			Parameters: schema.Schema,
		})
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return ToolLoopRequest{}, 0, 0, err
	}
	if len(payload) > maxInputBytes {
		return ToolLoopRequest{}, 0, 0, invalidProviderRequest(
			"脱敏后的工具循环输入超过 %d 字节", maxInputBytes,
		)
	}
	return result, redactions, len(payload), nil
}

func canonicalToolResult(raw json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, newProviderError(
			ErrorCodeInvalidOutput, "tool returned an empty result",
			0, false, 0, nil,
		)
	}
	value, err := decodeSingleJSONValue(raw)
	if err != nil {
		return nil, newProviderError(
			ErrorCodeInvalidOutput, "tool returned invalid JSON",
			0, false, 0, err,
		)
	}
	return json.Marshal(value)
}

func hashToolLoopRequest(request ToolLoopRequest) (string, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

func hashProviderRequestIDs(ids []string) string {
	filtered := make([]string, 0, len(ids))
	for _, id := range ids {
		if value := strings.TrimSpace(id); value != "" {
			filtered = append(filtered, value)
		}
	}
	if len(filtered) == 0 {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.Join(filtered, "\x00")))
	return hex.EncodeToString(sum[:])
}

func hashBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func hashEvidenceState(evidenceByID map[string]struct{}) string {
	return hashBytes([]byte(strings.Join(sortedEvidenceIDs(evidenceByID), "\x00")))
}

func sortedEvidenceIDs(evidenceByID map[string]struct{}) []string {
	result := make([]string, 0, len(evidenceByID))
	for evidenceID := range evidenceByID {
		result = append(result, evidenceID)
	}
	sort.Strings(result)
	return result
}

func normalizeEvidenceIDs(values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	if len(values) > 128 {
		return nil, newProviderError(
			ErrorCodeInvalidOutput, "tool returned too many evidence IDs",
			0, false, 0, nil,
		)
	}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if !evidenceIDPattern.MatchString(value) {
			return nil, newProviderError(
				ErrorCodeInvalidOutput, "tool returned an invalid evidence ID",
				0, false, 0, nil,
			)
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

func addUsage(left, right Usage) Usage {
	return Usage{
		PromptTokens: saturatingAddInt(left.PromptTokens, right.PromptTokens),
		CompletionTokens: saturatingAddInt(
			left.CompletionTokens, right.CompletionTokens,
		),
		TotalTokens: saturatingAddInt(left.TotalTokens, right.TotalTokens),
	}
}

func saturatingAddInt(left, right int) int {
	if left <= 0 {
		return max(0, right)
	}
	if right <= 0 {
		return left
	}
	if left > math.MaxInt-right {
		return math.MaxInt
	}
	return left + right
}

func oneOfString(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}
