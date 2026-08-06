package orchestrator

import (
	"encoding/json"
	"fmt"

	"intelligent-report-generation-system/internal/askdata"
)

type BudgetStopReason string

const (
	BudgetStopSteps             BudgetStopReason = "STEP_LIMIT"
	BudgetStopLLMCalls          BudgetStopReason = "LLM_CALL_LIMIT"
	BudgetStopToolCalls         BudgetStopReason = "TOOL_CALL_LIMIT"
	BudgetStopFormalQueries     BudgetStopReason = "FORMAL_QUERY_LIMIT"
	BudgetStopValidationQueries BudgetStopReason = "VALIDATION_QUERY_LIMIT"
	BudgetStopDuration          BudgetStopReason = "DURATION_LIMIT"
	BudgetStopTranscript        BudgetStopReason = "TRANSCRIPT_LIMIT"
)

type BudgetTerminationRequest struct {
	Run                 Run
	Usage               BudgetUsage
	Reason              BudgetStopReason
	EvidenceIDs         []askdata.ID
	PreferClarification bool
}

type BudgetTermination struct {
	TargetState State
	Completion  CompletionArtifactInput
}

// BuildBudgetTermination turns an exhausted in-memory loop into a durable
// terminal contract. Semantic stages may ask a bounded clarification; hard
// query/time ceilings fail closed as BLOCKED.
func BuildBudgetTermination(request BudgetTerminationRequest) (BudgetTermination, error) {
	if request.Run.Validate() != nil || request.Run.Terminal() || !request.Usage.Exhausted ||
		request.Usage.validate(request.Run.Limits) != nil ||
		!request.Usage.monotonicFrom(request.Run.Usage) ||
		!budgetReasonReached(request.Reason, request.Usage, request.Run.Limits) {
		return BudgetTermination{}, fmt.Errorf("%w: budget termination is invalid", ErrInvalidRun)
	}
	evidence, err := normalizedEvidenceIDs(request.EvidenceIDs)
	if err != nil {
		return BudgetTermination{}, err
	}
	target := StateBlocked
	if request.PreferClarification && semanticBudgetClarificationAllowed(request.Run.State) {
		target = StateClarificationRequired
	}
	if !CanTransition(request.Run.State, target) {
		return BudgetTermination{}, fmt.Errorf("%w: budget terminal transition is illegal", ErrIllegalTransition)
	}
	code := "BUDGET_" + string(request.Reason)
	payload, err := json.Marshal(map[string]any{
		"code": code, "reason": request.Reason, "limits": request.Run.Limits,
		"usage": request.Usage, "retryable": target == StateClarificationRequired,
	})
	if err != nil {
		return BudgetTermination{}, fmt.Errorf("%w: budget artifact failed", ErrInvalidRun)
	}
	artifactType := ArtifactBlock
	if target == StateClarificationRequired {
		artifactType = ArtifactClarification
	}
	return BudgetTermination{
		TargetState: target,
		Completion: CompletionArtifactInput{
			Code: code, Type: artifactType, SchemaVersion: "budget-termination-v1",
			EvidenceIDs: evidence, Payload: payload,
		},
	}, nil
}

func budgetReasonReached(reason BudgetStopReason, usage BudgetUsage, limits BudgetLimits) bool {
	switch reason {
	case BudgetStopSteps:
		return usage.StepCount >= limits.MaxSteps
	case BudgetStopLLMCalls:
		return usage.LLMCallsUsed >= limits.MaxLLMCalls
	case BudgetStopToolCalls:
		return usage.ToolCallsUsed >= limits.MaxToolCalls
	case BudgetStopFormalQueries:
		return usage.FormalQueriesUsed >= limits.MaxFormalQueries
	case BudgetStopValidationQueries:
		return usage.ValidationQueriesUsed >= limits.MaxValidationQueries
	case BudgetStopDuration:
		return usage.ElapsedMS >= limits.MaxDurationMS
	case BudgetStopTranscript:
		return true
	default:
		return false
	}
}

func semanticBudgetClarificationAllowed(state State) bool {
	switch state {
	case StateUnderstanding, StateRetrieving, StateBinding, StateGraphValidating,
		StatePlanValidating, StateResultVerifying:
		return true
	default:
		return false
	}
}
