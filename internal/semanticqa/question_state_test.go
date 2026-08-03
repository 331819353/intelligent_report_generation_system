package semanticqa

import (
	"context"
	"testing"
	"time"
)

func TestQuestionStateMachineAcceptsGovernedExecutionPath(t *testing.T) {
	machine := newQuestionStateMachine(
		"10000000-0000-4000-8000-000000000001",
	)
	machine.now = func() time.Time {
		return time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	}
	path := []QuestionState{
		QuestionStateReceived, QuestionStateAuthorized,
		QuestionStateContextReady, QuestionStatePlanReady,
		QuestionStateValidating, QuestionStateCostApproved,
		QuestionStateExecuting, QuestionStateResultVerified,
		QuestionStateAnswered,
	}
	for _, state := range path {
		if err := machine.advance(state); err != nil {
			t.Fatalf("advance to %s: %v", state, err)
		}
	}
	if machine.state != QuestionStateAnswered ||
		len(machine.lifecycle()) != len(path) {
		t.Fatalf("unexpected machine: %#v", machine)
	}
	last := machine.lifecycle()[len(path)-1]
	if last.Stage != string(QuestionStateAnswered) ||
		last.Status != QueryProgressStatusSucceeded {
		t.Fatalf("state event must expose a safe stage summary: %#v", last)
	}
}

func TestQuestionStateMachineRejectsSkippedEvidenceGates(t *testing.T) {
	machine := newQuestionStateMachine("")
	if err := machine.advance(QuestionStateReceived); err != nil {
		t.Fatal(err)
	}
	if err := machine.advance(QuestionStateExecuting); err != ErrInvalidState {
		t.Fatalf("expected invalid transition, got %v", err)
	}
}

func TestQuestionStatePersistenceSurvivesRequestCancellation(t *testing.T) {
	requestCtx, cancel := context.WithCancel(context.Background())
	cancel()
	persistCtx, persistCancel := questionStatePersistenceContext(requestCtx)
	defer persistCancel()
	if persistCtx.Err() != nil {
		t.Fatalf("cleanup persistence inherited cancellation: %v", persistCtx.Err())
	}
	if _, hasDeadline := persistCtx.Deadline(); !hasDeadline {
		t.Fatal("cleanup persistence must remain time bounded")
	}
}
