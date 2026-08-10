package orchestrator

import (
	"errors"
	"fmt"
	"time"
)

const DefaultClarificationTimeout = 30 * time.Minute

var (
	ErrClarificationExpired         = errors.New("clarification has expired")
	ErrClarificationAlreadyAnswered = errors.New("clarification was already answered")
)

type FrozenBudget struct {
	FrozenAt time.Time   `json:"frozenAt"`
	Deadline time.Time   `json:"deadline"`
	Consumed BudgetUsage `json:"consumed"`
}

func FreezeBudget(usage BudgetUsage, limits BudgetLimits, now time.Time, timeout time.Duration) (FrozenBudget, error) {
	if now.IsZero() || timeout <= 0 || timeout > 24*time.Hour || limits.Validate() != nil || usage.validate(limits) != nil {
		return FrozenBudget{}, fmt.Errorf("%w: clarification freeze is invalid", ErrInvalidRun)
	}
	frozenAt := now.UTC()
	return FrozenBudget{FrozenAt: frozenAt, Deadline: frozenAt.Add(timeout), Consumed: usage}, nil
}

// ResumeBudget returns the exact consumed snapshot. Wall-clock time spent
// waiting for the user is deliberately not added to elapsedMs.
func ResumeBudget(run Run, now time.Time) (BudgetUsage, error) {
	if run.Validate() != nil || run.State != StateClarificationRequired || now.IsZero() ||
		run.ClarificationDeadline == nil || run.BudgetConsumed == nil {
		return BudgetUsage{}, fmt.Errorf("%w: clarification cannot resume", ErrInvalidRun)
	}
	if now.UTC().After(*run.ClarificationDeadline) {
		return BudgetUsage{}, ErrClarificationExpired
	}
	usage := *run.BudgetConsumed
	usage.Exhausted = false
	return usage, nil
}

func ClarificationExpired(run Run, now time.Time) bool {
	return run.State == StateClarificationRequired && run.ClarificationDeadline != nil &&
		!now.UTC().Before(*run.ClarificationDeadline)
}
