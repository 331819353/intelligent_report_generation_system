package orchestrator

import (
	"encoding/json"
	"fmt"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/observability"
	"intelligent-report-generation-system/internal/config"
)

type RunType string

const (
	RunTypeSingleQuery RunType = "SINGLE_QUERY"
	RunTypeBundle      RunType = "BUNDLE"
	RunTypeDefinition  RunType = "DEFINITION"
)

const RunCostExceededCompletionCode = "RUN_COST_EXCEEDED"

// RunBudgetClass distinguishes the two SINGLE_QUERY execution paths while
// preserving the externally governed RunType contract.
type RunBudgetClass string

const (
	BudgetClassSingleQueryFast    RunBudgetClass = "SINGLE_QUERY_FAST"
	BudgetClassSingleQueryComplex RunBudgetClass = "SINGLE_QUERY_COMPLEX"
	BudgetClassBundle             RunBudgetClass = "BUNDLE"
	BudgetClassDefinition         RunBudgetClass = "DEFINITION"
)

type RunBudget struct {
	RunType              RunType        `json:"runType"`
	BudgetClass          RunBudgetClass `json:"budgetClass"`
	MaxLLMCalls          int            `json:"maxLlmCalls"`
	MaxToolCalls         int            `json:"maxToolCalls"`
	MaxPrimaryQueries    int            `json:"maxPrimaryQueries"`
	MaxValidationQueries int            `json:"maxValidationQueries"`
	MaxCandidateCompares int            `json:"maxCandidateCompares"`
	MaxJoinHops          int            `json:"maxJoinHops"`
	HardTimeout          time.Duration  `json:"hardTimeout"`
	P95Target            time.Duration  `json:"p95Target"`
	MaxConcurrentPlans   int            `json:"maxConcurrentPlans,omitempty"`
}

func DefaultRunBudget(class RunBudgetClass) (RunBudget, error) {
	var budget RunBudget
	switch class {
	case BudgetClassSingleQueryFast:
		budget = RunBudget{
			RunType: RunTypeSingleQuery, BudgetClass: class,
			MaxLLMCalls: 1, MaxToolCalls: 4, MaxPrimaryQueries: 1,
			MaxValidationQueries: 1, MaxCandidateCompares: 2, MaxJoinHops: 4,
			HardTimeout: 25 * time.Second, P95Target: 8 * time.Second,
		}
	case BudgetClassSingleQueryComplex:
		budget = RunBudget{
			RunType: RunTypeSingleQuery, BudgetClass: class,
			MaxLLMCalls: 4, MaxToolCalls: 8, MaxPrimaryQueries: 2,
			MaxValidationQueries: 3, MaxCandidateCompares: 2, MaxJoinHops: 4,
			HardTimeout: 25 * time.Second, P95Target: 18 * time.Second,
		}
	case BudgetClassBundle:
		budget = RunBudget{
			RunType: RunTypeBundle, BudgetClass: class,
			MaxLLMCalls: 2, MaxToolCalls: 10, MaxPrimaryQueries: 6,
			MaxValidationQueries: 2, MaxCandidateCompares: 2, MaxJoinHops: 4,
			HardTimeout: 30 * time.Second, P95Target: 25 * time.Second,
			MaxConcurrentPlans: 4,
		}
	case BudgetClassDefinition:
		budget = RunBudget{
			RunType: RunTypeDefinition, BudgetClass: class,
			MaxLLMCalls: 1, MaxToolCalls: 2,
			HardTimeout: 10 * time.Second, P95Target: 3 * time.Second,
		}
	default:
		return RunBudget{}, fmt.Errorf("%w: unknown run budget class", ErrInvalidRun)
	}
	return budget, nil
}

func (budget RunBudget) Validate() error {
	expectedType, validClass := runTypeForBudgetClass(budget.BudgetClass)
	if !validClass || budget.RunType != expectedType || budget.MaxLLMCalls < 1 || budget.MaxLLMCalls > 4 ||
		budget.MaxToolCalls < 0 || budget.MaxToolCalls > 10 ||
		budget.MaxPrimaryQueries < 0 || budget.MaxPrimaryQueries > 6 ||
		budget.MaxValidationQueries < 0 || budget.MaxValidationQueries > 3 ||
		budget.MaxCandidateCompares < 0 || budget.MaxCandidateCompares > 2 ||
		budget.MaxJoinHops < 0 || budget.MaxJoinHops > 4 ||
		budget.HardTimeout < 100*time.Millisecond || budget.HardTimeout > 30*time.Second ||
		budget.P95Target <= 0 || budget.P95Target > budget.HardTimeout {
		return fmt.Errorf("%w: run budget exceeds the governed bounds", ErrInvalidRun)
	}
	if budget.RunType == RunTypeBundle {
		if budget.MaxConcurrentPlans < 1 || budget.MaxConcurrentPlans > 4 {
			return fmt.Errorf("%w: bundle concurrency exceeds the governed bounds", ErrInvalidRun)
		}
	} else if budget.MaxConcurrentPlans != 0 {
		return fmt.Errorf("%w: concurrency is only valid for bundle runs", ErrInvalidRun)
	}
	return nil
}

// Limits returns the scalar limits persisted by the current question-run
// schema. Candidate comparisons, join depth, P95 and bundle concurrency remain
// explicit RunBudget controls because they are enforced by their owning
// planners rather than by the generic cognition loop.
func (budget RunBudget) Limits() (BudgetLimits, error) {
	if err := budget.Validate(); err != nil {
		return BudgetLimits{}, err
	}
	maxSteps := 16
	switch budget.BudgetClass {
	case BudgetClassSingleQueryFast:
		maxSteps = 8
	case BudgetClassDefinition:
		maxSteps = 4
	}
	limits := BudgetLimits{
		MaxSteps: maxSteps, MaxLLMCalls: budget.MaxLLMCalls,
		MaxToolCalls: budget.MaxToolCalls, MaxFormalQueries: budget.MaxPrimaryQueries,
		MaxValidationQueries: budget.MaxValidationQueries,
		MaxDurationMS:        int64(budget.HardTimeout / time.Millisecond),
	}
	if err := limits.Validate(); err != nil {
		return BudgetLimits{}, err
	}
	return limits, nil
}

func runTypeForBudgetClass(class RunBudgetClass) (RunType, bool) {
	switch class {
	case BudgetClassSingleQueryFast, BudgetClassSingleQueryComplex:
		return RunTypeSingleQuery, true
	case BudgetClassBundle:
		return RunTypeBundle, true
	case BudgetClassDefinition:
		return RunTypeDefinition, true
	default:
		return "", false
	}
}

// BudgetCatalog is immutable after construction and deterministically resolves
// a domain override before falling back to the governed default.
type BudgetCatalog struct {
	defaults  map[RunBudgetClass]RunBudget
	overrides map[askdata.ID]map[RunBudgetClass]RunBudget
}

func NewBudgetCatalog(configured []config.AskDataBudgetOverride) (*BudgetCatalog, error) {
	catalog := &BudgetCatalog{
		defaults:  make(map[RunBudgetClass]RunBudget, 4),
		overrides: make(map[askdata.ID]map[RunBudgetClass]RunBudget),
	}
	for _, class := range []RunBudgetClass{
		BudgetClassSingleQueryFast, BudgetClassSingleQueryComplex,
		BudgetClassBundle, BudgetClassDefinition,
	} {
		budget, err := DefaultRunBudget(class)
		if err != nil {
			return nil, err
		}
		catalog.defaults[class] = budget
	}
	for _, item := range configured {
		class := RunBudgetClass(item.BudgetClass)
		runType, valid := runTypeForBudgetClass(class)
		if !valid {
			return nil, fmt.Errorf("%w: domain override has an unknown budget class", ErrInvalidRun)
		}
		domainID := askdata.ID(item.DomainID)
		if domainID.Validate() != nil {
			return nil, fmt.Errorf("%w: domain override has an invalid domain", ErrInvalidRun)
		}
		budget := RunBudget{
			RunType: runType, BudgetClass: class,
			MaxLLMCalls: item.MaxLLMCalls, MaxToolCalls: item.MaxToolCalls,
			MaxPrimaryQueries:    item.MaxPrimaryQueries,
			MaxValidationQueries: item.MaxValidationQueries,
			MaxCandidateCompares: item.MaxCandidateCompares,
			MaxJoinHops:          item.MaxJoinHops, HardTimeout: item.HardTimeout,
			P95Target: item.P95Target, MaxConcurrentPlans: item.MaxConcurrentPlans,
		}
		if budget.Validate() != nil {
			return nil, fmt.Errorf("%w: domain override is outside governed bounds", ErrInvalidRun)
		}
		if catalog.overrides[domainID] == nil {
			catalog.overrides[domainID] = make(map[RunBudgetClass]RunBudget)
		}
		if _, duplicate := catalog.overrides[domainID][class]; duplicate {
			return nil, fmt.Errorf("%w: duplicate domain budget override", ErrInvalidRun)
		}
		catalog.overrides[domainID][class] = budget
	}
	return catalog, nil
}

func (catalog *BudgetCatalog) Resolve(domainID askdata.ID, class RunBudgetClass) (RunBudget, error) {
	if catalog == nil || domainID.Validate() != nil {
		return RunBudget{}, fmt.Errorf("%w: budget resolution scope is invalid", ErrInvalidRun)
	}
	budget, exists := catalog.defaults[class]
	if !exists {
		return RunBudget{}, fmt.Errorf("%w: budget class is invalid", ErrInvalidRun)
	}
	if domain, overridden := catalog.overrides[domainID]; overridden {
		if value, found := domain[class]; found {
			budget = value
		}
	}
	return budget, nil
}

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

// BuildRunCostExceededTermination converts the quota governor's fail-closed
// signal into a durable clarification artifact. Its payload is deliberately
// limited to quota limiters and their restore time.
func BuildRunCostExceededTermination(
	run Run,
	usage BudgetUsage,
	decision observability.QuotaDecision,
	evidenceIDs []askdata.ID,
) (BudgetTermination, error) {
	if run.Validate() != nil || run.Terminal() || usage.validate(run.Limits) != nil ||
		!usage.monotonicFrom(run.Usage) || !semanticBudgetClarificationAllowed(run.State) ||
		!CanTransition(run.State, StateClarificationRequired) ||
		decision.Status != observability.QuotaRunCostExceeded || decision.Allowed ||
		!decision.RequireClarification || len(decision.Limiters) == 0 {
		return BudgetTermination{}, fmt.Errorf("%w: run cost termination is invalid", ErrInvalidRun)
	}
	limiters := append([]observability.QuotaLimiter(nil), decision.Limiters...)
	var restoreAt time.Time
	foundRunCostLimiter := false
	for _, limiter := range limiters {
		if limiter.Scope == observability.QuotaScopeRun && limiter.ScopeID == run.ID &&
			limiter.Period == observability.QuotaPeriodRun &&
			limiter.Dimension == observability.QuotaDimensionCostCents && limiter.Exceeded {
			foundRunCostLimiter = true
		}
		if limiter.ResetAt.IsZero() || limiter.Used < 0 || limiter.Reserved < 0 ||
			limiter.Limit <= 0 || limiter.Remaining < 0 {
			return BudgetTermination{}, fmt.Errorf("%w: run cost limiter is invalid", ErrInvalidRun)
		}
		if limiter.Exceeded && limiter.ResetAt.After(restoreAt) {
			restoreAt = limiter.ResetAt.UTC()
		}
	}
	if !foundRunCostLimiter || restoreAt.IsZero() {
		return BudgetTermination{}, fmt.Errorf("%w: run cost limiter is missing", ErrInvalidRun)
	}
	evidence, err := normalizedEvidenceIDs(evidenceIDs)
	if err != nil {
		return BudgetTermination{}, err
	}
	payload, err := json.Marshal(struct {
		Limiters  []observability.QuotaLimiter `json:"limiters"`
		RestoreAt time.Time                    `json:"restoreAt"`
	}{Limiters: limiters, RestoreAt: restoreAt})
	if err != nil {
		return BudgetTermination{}, fmt.Errorf("%w: run cost artifact failed", ErrInvalidRun)
	}
	return BudgetTermination{
		TargetState: StateClarificationRequired,
		Completion: CompletionArtifactInput{
			Code: RunCostExceededCompletionCode, Type: ArtifactClarification,
			SchemaVersion: "run-cost-exceeded-v1", EvidenceIDs: evidence, Payload: payload,
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
