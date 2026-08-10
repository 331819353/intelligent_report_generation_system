package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"intelligent-report-generation-system/internal/askdata"
)

const (
	BudgetConsumptionSchemaVersion = "run-budget-consumption-v1"
	MetricBudgetTargetExceeded     = "budget_target_exceeded"
)

// RunBudgetUsage contains every governed consumption dimension. Elapsed is
// active execution time only; clarification wait time is excluded by the
// ActiveBudgetClock and by the persisted FreezeBudget/ResumeBudget path.
type RunBudgetUsage struct {
	LLMCallsUsed          int   `json:"llmCallsUsed"`
	ToolCallsUsed         int   `json:"toolCallsUsed"`
	PrimaryQueriesUsed    int   `json:"primaryQueriesUsed"`
	ValidationQueriesUsed int   `json:"validationQueriesUsed"`
	CandidateComparesUsed int   `json:"candidateComparesUsed"`
	MaxJoinHopsUsed       int   `json:"maxJoinHopsUsed"`
	ElapsedMS             int64 `json:"elapsedMs"`
}

func RunBudgetUsageFromLegacy(usage BudgetUsage) RunBudgetUsage {
	return RunBudgetUsage{
		LLMCallsUsed: usage.LLMCallsUsed, ToolCallsUsed: usage.ToolCallsUsed,
		PrimaryQueriesUsed:    usage.FormalQueriesUsed,
		ValidationQueriesUsed: usage.ValidationQueriesUsed,
		ElapsedMS:             usage.ElapsedMS,
	}
}

func (usage RunBudgetUsage) validate(budget RunBudget) error {
	if budget.Validate() != nil || usage.LLMCallsUsed < 0 || usage.LLMCallsUsed > budget.MaxLLMCalls ||
		usage.ToolCallsUsed < 0 || usage.ToolCallsUsed > budget.MaxToolCalls ||
		usage.PrimaryQueriesUsed < 0 || usage.PrimaryQueriesUsed > budget.MaxPrimaryQueries ||
		usage.ValidationQueriesUsed < 0 || usage.ValidationQueriesUsed > budget.MaxValidationQueries ||
		usage.CandidateComparesUsed < 0 || usage.CandidateComparesUsed > budget.MaxCandidateCompares ||
		usage.MaxJoinHopsUsed < 0 || usage.MaxJoinHopsUsed > budget.MaxJoinHops ||
		usage.ElapsedMS < 0 || usage.ElapsedMS > int64((10*time.Minute)/time.Millisecond) {
		return fmt.Errorf("%w: run budget consumption is invalid", ErrInvalidRun)
	}
	return nil
}

type BudgetLimitSnapshot struct {
	MaxLLMCalls          int   `json:"maxLlmCalls"`
	MaxToolCalls         int   `json:"maxToolCalls"`
	MaxPrimaryQueries    int   `json:"maxPrimaryQueries"`
	MaxValidationQueries int   `json:"maxValidationQueries"`
	MaxCandidateCompares int   `json:"maxCandidateCompares"`
	MaxJoinHops          int   `json:"maxJoinHops"`
	HardTimeoutMS        int64 `json:"hardTimeoutMs"`
	P95TargetMS          int64 `json:"p95TargetMs"`
	MaxConcurrentPlans   int   `json:"maxConcurrentPlans"`
}

type BudgetConsumption struct {
	SchemaVersion     string              `json:"schemaVersion"`
	RunType           RunType             `json:"runType"`
	BudgetClass       RunBudgetClass      `json:"budgetClass"`
	Limits            BudgetLimitSnapshot `json:"limits"`
	Usage             RunBudgetUsage      `json:"usage"`
	P95TargetExceeded bool                `json:"p95TargetExceeded"`
}

// SnapshotBudgetConsumption produces the complete bounded JSON contract used
// for durable cost attribution and error-budget analysis.
func SnapshotBudgetConsumption(budget RunBudget, usage RunBudgetUsage) (BudgetConsumption, error) {
	if err := usage.validate(budget); err != nil {
		return BudgetConsumption{}, err
	}
	return BudgetConsumption{
		SchemaVersion: BudgetConsumptionSchemaVersion,
		RunType:       budget.RunType, BudgetClass: budget.BudgetClass,
		Limits: BudgetLimitSnapshot{
			MaxLLMCalls: budget.MaxLLMCalls, MaxToolCalls: budget.MaxToolCalls,
			MaxPrimaryQueries:    budget.MaxPrimaryQueries,
			MaxValidationQueries: budget.MaxValidationQueries,
			MaxCandidateCompares: budget.MaxCandidateCompares,
			MaxJoinHops:          budget.MaxJoinHops,
			HardTimeoutMS:        int64(budget.HardTimeout / time.Millisecond),
			P95TargetMS:          int64(budget.P95Target / time.Millisecond),
			MaxConcurrentPlans:   budget.MaxConcurrentPlans,
		},
		Usage:             usage,
		P95TargetExceeded: time.Duration(usage.ElapsedMS)*time.Millisecond > budget.P95Target,
	}, nil
}

func (consumption BudgetConsumption) JSON() (json.RawMessage, error) {
	if consumption.SchemaVersion != BudgetConsumptionSchemaVersion {
		return nil, fmt.Errorf("%w: budget consumption schema is invalid", ErrInvalidRun)
	}
	encoded, err := json.Marshal(consumption)
	if err != nil {
		return nil, fmt.Errorf("%w: budget consumption encoding failed", ErrInvalidRun)
	}
	return encoded, nil
}

type BudgetTargetExceededMetric struct {
	Name            string         `json:"name"`
	DomainID        askdata.ID     `json:"domainId"`
	RunType         RunType        `json:"runType"`
	BudgetClass     RunBudgetClass `json:"budgetClass"`
	ActiveElapsedMS int64          `json:"activeElapsedMs"`
	TargetMS        int64          `json:"targetMs"`
}

type BudgetMetricRecorder interface {
	RecordBudgetMetric(context.Context, BudgetTargetExceededMetric)
}

type BudgetObservation struct {
	TargetExceeded     bool
	HardTimeoutReached bool
	Interrupt          bool
}

// BudgetMonitor emits the P95 metric once. Crossing P95 never interrupts;
// only the independent hard timeout requests control-flow interruption.
type BudgetMonitor struct {
	domainID askdata.ID
	budget   RunBudget
	recorder BudgetMetricRecorder
	emitted  bool
}

func NewBudgetMonitor(domainID askdata.ID, budget RunBudget, recorder BudgetMetricRecorder) (*BudgetMonitor, error) {
	if domainID.Validate() != nil || budget.Validate() != nil {
		return nil, fmt.Errorf("%w: budget monitor is invalid", ErrInvalidRun)
	}
	return &BudgetMonitor{domainID: domainID, budget: budget, recorder: recorder}, nil
}

func (monitor *BudgetMonitor) Observe(ctx context.Context, usage RunBudgetUsage) (BudgetObservation, error) {
	if monitor == nil || ctx == nil || usage.validate(monitor.budget) != nil {
		return BudgetObservation{}, fmt.Errorf("%w: budget observation is invalid", ErrInvalidRun)
	}
	elapsed := time.Duration(usage.ElapsedMS) * time.Millisecond
	targetExceeded := elapsed > monitor.budget.P95Target
	hardTimeoutReached := elapsed >= monitor.budget.HardTimeout
	if targetExceeded && !monitor.emitted {
		monitor.emitted = true
		if monitor.recorder != nil {
			monitor.recorder.RecordBudgetMetric(ctx, BudgetTargetExceededMetric{
				Name: MetricBudgetTargetExceeded, DomainID: monitor.domainID,
				RunType: monitor.budget.RunType, BudgetClass: monitor.budget.BudgetClass,
				ActiveElapsedMS: usage.ElapsedMS,
				TargetMS:        int64(monitor.budget.P95Target / time.Millisecond),
			})
		}
	}
	return BudgetObservation{
		TargetExceeded: targetExceeded, HardTimeoutReached: hardTimeoutReached,
		Interrupt: hardTimeoutReached,
	}, nil
}

type HardTimeoutOutcome string

const (
	HardTimeoutPartial       HardTimeoutOutcome = "PARTIAL"
	HardTimeoutClarification HardTimeoutOutcome = "CLARIFICATION"
	HardTimeoutTimeout       HardTimeoutOutcome = "TIMEOUT"
)

type HardTimeoutEvidence struct {
	HasUsableResult        bool
	HasGovernedEvidence    bool
	ClarificationAvailable bool
}

// ResolveHardTimeout enforces the ordered degradation contract: preserve a
// usable result first, ask a bounded evidence-backed clarification second, and
// expose TIMEOUT only when neither safer outcome exists.
func ResolveHardTimeout(evidence HardTimeoutEvidence) HardTimeoutOutcome {
	if evidence.HasUsableResult {
		return HardTimeoutPartial
	}
	if evidence.HasGovernedEvidence && evidence.ClarificationAvailable {
		return HardTimeoutClarification
	}
	return HardTimeoutTimeout
}

// ActiveBudgetClock measures execution time while permitting clarification
// waits to freeze the clock without changing the remaining hard-timeout budget.
type ActiveBudgetClock struct {
	startedAt   time.Time
	accumulated time.Duration
	frozen      bool
}

func NewActiveBudgetClock(startedAt time.Time, alreadyConsumed time.Duration) (*ActiveBudgetClock, error) {
	if startedAt.IsZero() || alreadyConsumed < 0 || alreadyConsumed > 10*time.Minute {
		return nil, fmt.Errorf("%w: active budget clock is invalid", ErrInvalidRun)
	}
	return &ActiveBudgetClock{startedAt: startedAt.UTC(), accumulated: alreadyConsumed}, nil
}

func (clock *ActiveBudgetClock) Freeze(now time.Time) error {
	if clock == nil || clock.frozen || now.IsZero() || now.UTC().Before(clock.startedAt) {
		return fmt.Errorf("%w: active budget clock cannot freeze", ErrInvalidRun)
	}
	clock.accumulated += now.UTC().Sub(clock.startedAt)
	clock.frozen = true
	return nil
}

func (clock *ActiveBudgetClock) Resume(now time.Time) error {
	if clock == nil || !clock.frozen || now.IsZero() {
		return fmt.Errorf("%w: active budget clock cannot resume", ErrInvalidRun)
	}
	clock.startedAt = now.UTC()
	clock.frozen = false
	return nil
}

func (clock *ActiveBudgetClock) Elapsed(now time.Time) (time.Duration, error) {
	if clock == nil || now.IsZero() {
		return 0, fmt.Errorf("%w: active budget clock is invalid", ErrInvalidRun)
	}
	if clock.frozen {
		return clock.accumulated, nil
	}
	if now.UTC().Before(clock.startedAt) {
		return 0, fmt.Errorf("%w: active budget clock moved backwards", ErrInvalidRun)
	}
	return clock.accumulated + now.UTC().Sub(clock.startedAt), nil
}
