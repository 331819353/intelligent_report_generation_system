package orchestrator

import (
	"errors"
	"testing"
	"time"
)

func TestFreezeAndResumeBudgetStartsASeparateChildEnvelope(t *testing.T) {
	now := time.Date(2026, 8, 8, 10, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	limits := DefaultBudgetLimits()
	usage := BudgetUsage{StepCount: 7, LLMCallsUsed: 2, ToolCallsUsed: 4, ElapsedMS: 1_860}
	frozen, err := FreezeBudget(usage, limits, now, DefaultClarificationTimeout)
	if err != nil {
		t.Fatal(err)
	}
	run := validRun(StateUnderstanding)
	run.Limits, run.Usage = limits, usage
	run.State, run.Disposition = StateClarificationRequired, DispositionClarify
	run.CompletionCode, run.CompletionArtifact = "METRIC_AMBIGUOUS", testHash("7")
	completed := frozen.FrozenAt
	run.CompletedAt = &completed
	run.ClarificationDeadline, run.BudgetFrozenAt, run.BudgetConsumed = &frozen.Deadline, &frozen.FrozenAt, &frozen.Consumed
	resumed, err := ResumeBudget(run, now.Add(20*time.Minute))
	if err != nil || resumed != (BudgetUsage{}) {
		t.Fatalf("ResumeBudget() = %#v, %v", resumed, err)
	}
	if _, err := ResumeBudget(run, now.Add(31*time.Minute)); !errors.Is(err, ErrClarificationExpired) {
		t.Fatalf("expired resume error = %v", err)
	}
}

func TestClarificationExpiredAtDeadline(t *testing.T) {
	now := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	frozen, _ := FreezeBudget(BudgetUsage{}, DefaultBudgetLimits(), now, DefaultClarificationTimeout)
	run := validRun(StateUnderstanding)
	run.State, run.Disposition = StateClarificationRequired, DispositionClarify
	run.CompletionCode, run.CompletionArtifact = "METRIC_AMBIGUOUS", testHash("7")
	completed := frozen.FrozenAt
	run.CompletedAt = &completed
	run.ClarificationDeadline = &frozen.Deadline
	run.BudgetFrozenAt = &frozen.FrozenAt
	run.BudgetConsumed = &frozen.Consumed
	if ClarificationExpired(run, frozen.Deadline.Add(-time.Nanosecond)) || !ClarificationExpired(run, frozen.Deadline) {
		t.Fatal("deadline boundary was not enforced")
	}
}
