package orchestrator

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"intelligent-report-generation-system/internal/askdata"
)

func TestCanTransitionMatchesGovernedLifecycle(t *testing.T) {
	states := []State{
		StateReceived, StateAuthorized, StateContextReady, StateUnderstanding,
		StateRetrieving, StateBinding, StateGraphValidating, StateIRReady,
		StatePlanValidating, StateExecuting, StateResultVerifying, StateAnswerVerifying,
		StateClarificationRequired, StateClarificationExpired, StateOutOfScope, StateAnswered, StateBlocked,
	}
	allowed := map[State]map[State]bool{
		StateReceived:              {StateReceived: true, StateAuthorized: true, StateBlocked: true},
		StateAuthorized:            {StateAuthorized: true, StateContextReady: true, StateBlocked: true},
		StateContextReady:          {StateContextReady: true, StateUnderstanding: true, StateBlocked: true},
		StateUnderstanding:         {StateUnderstanding: true, StateRetrieving: true, StateClarificationRequired: true, StateOutOfScope: true, StateBlocked: true},
		StateRetrieving:            {StateRetrieving: true, StateBinding: true, StateClarificationRequired: true, StateBlocked: true},
		StateBinding:               {StateBinding: true, StateGraphValidating: true, StateClarificationRequired: true, StateOutOfScope: true, StateBlocked: true},
		StateGraphValidating:       {StateGraphValidating: true, StateIRReady: true, StateClarificationRequired: true, StateBlocked: true},
		StateIRReady:               {StateIRReady: true, StatePlanValidating: true, StateClarificationRequired: true, StateBlocked: true},
		StatePlanValidating:        {StatePlanValidating: true, StateExecuting: true, StateBinding: true, StateClarificationRequired: true, StateBlocked: true},
		StateExecuting:             {StateExecuting: true, StateResultVerifying: true, StateBlocked: true},
		StateResultVerifying:       {StateResultVerifying: true, StateAnswerVerifying: true, StateBinding: true, StateClarificationRequired: true, StateBlocked: true},
		StateAnswerVerifying:       {StateAnswerVerifying: true, StateAnswered: true, StateClarificationRequired: true, StateBlocked: true},
		StateClarificationRequired: {StateClarificationExpired: true},
		StateClarificationExpired:  {}, StateOutOfScope: {}, StateAnswered: {}, StateBlocked: {},
	}
	for _, from := range states {
		for _, to := range states {
			if got, want := CanTransition(from, to), allowed[from][to]; got != want {
				t.Errorf("CanTransition(%s,%s) = %v, want %v", from, to, got, want)
			}
		}
	}
	if CanTransition("UNKNOWN", StateReceived) || CanTransition(StateReceived, "UNKNOWN") {
		t.Fatal("unknown state was accepted")
	}
}

func TestApplyCorrectionClearsOnlyDownstreamHashes(t *testing.T) {
	for _, state := range []State{StatePlanValidating, StateResultVerifying} {
		t.Run(string(state), func(t *testing.T) {
			run := validRun(state)
			run.Hashes = completeHashes()
			next, err := Apply(run, Transition{
				ExpectedVersion: run.RecordVersion,
				TargetState:     StateBinding,
				Usage:           run.Usage,
			})
			if err != nil {
				t.Fatalf("Apply() error = %v", err)
			}
			if next.RecordVersion != run.RecordVersion+1 || next.State != StateBinding {
				t.Fatalf("next state/version = %s/%d", next.State, next.RecordVersion)
			}
			if next.Hashes.Understanding != run.Hashes.Understanding {
				t.Fatal("correction cleared understanding hash")
			}
			if next.Hashes.BindingBundle != "" || next.Hashes.GraphPlan != "" ||
				next.Hashes.SemanticIR != "" || next.Hashes.QueryPlan != "" || next.Hashes.Result != "" {
				t.Fatalf("correction retained downstream hashes: %#v", next.Hashes)
			}
		})
	}
}

func TestApplyRejectsCorrectionThatReusesDownstreamHash(t *testing.T) {
	run := validRun(StatePlanValidating)
	run.Hashes = completeHashes()
	hash := testHash("7")
	_, err := Apply(run, Transition{
		ExpectedVersion: run.RecordVersion, TargetState: StateBinding,
		Usage: run.Usage, Hashes: HashUpdates{BindingBundle: &hash},
	})
	if !errors.Is(err, ErrInvalidRun) {
		t.Fatalf("Apply() error = %v, want ErrInvalidRun", err)
	}
}

func TestApplyTerminalCompletionShapes(t *testing.T) {
	tests := []struct {
		name        string
		from, to    State
		artifact    ArtifactType
		disposition Disposition
	}{
		{"answered", StateAnswerVerifying, StateAnswered, ArtifactAnswer, DispositionDirect},
		{"clarification", StateBinding, StateClarificationRequired, ArtifactClarification, DispositionClarify},
		{"blocked", StateUnderstanding, StateBlocked, ArtifactBlock, DispositionRefuse},
		{"out of scope", StateUnderstanding, StateOutOfScope, ArtifactBlock, DispositionRefuse},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			run := validRun(test.from)
			if test.to == StateAnswered {
				run.Hashes = completeHashes()
			}
			next, err := Apply(run, Transition{
				ExpectedVersion: run.RecordVersion, TargetState: test.to, Usage: run.Usage,
				Completion: &CompletionRef{
					Code: "TEST_COMPLETION", ArtifactType: test.artifact, ArtifactHash: testHash("f"),
				},
			})
			if err != nil {
				t.Fatalf("Apply() error = %v", err)
			}
			if !next.Terminal() || next.Disposition != test.disposition || next.CompletedAt == nil {
				t.Fatalf("terminal shape = %#v", next)
			}
			if _, err := Apply(next, Transition{ExpectedVersion: next.RecordVersion, TargetState: next.State, Usage: next.Usage}); !errors.Is(err, ErrTerminalRun) {
				t.Fatalf("terminal Apply() error = %v", err)
			}
		})
	}
}

func TestApplyFailsClosedForIncompleteAnswerAndWrongArtifact(t *testing.T) {
	run := validRun(StateAnswerVerifying)
	run.Hashes = completeHashes()
	run.Hashes.Result = ""
	_, err := Apply(run, Transition{
		ExpectedVersion: run.RecordVersion, TargetState: StateAnswered, Usage: run.Usage,
		Completion: &CompletionRef{Code: "ANSWER_READY", ArtifactType: ArtifactAnswer, ArtifactHash: testHash("a")},
	})
	if !errors.Is(err, ErrInvalidRun) {
		t.Fatalf("incomplete answer error = %v", err)
	}
	run.Hashes = completeHashes()
	_, err = Apply(run, Transition{
		ExpectedVersion: run.RecordVersion, TargetState: StateAnswered, Usage: run.Usage,
		Completion: &CompletionRef{Code: "ANSWER_READY", ArtifactType: ArtifactBlock, ArtifactHash: testHash("a")},
	})
	if !errors.Is(err, ErrInvalidRun) {
		t.Fatalf("wrong artifact error = %v", err)
	}
}

func TestApplyUsesAbsoluteMonotonicBudgetAndRejectsEmptyCheckpoint(t *testing.T) {
	run := validRun(StateRetrieving)
	if _, err := Apply(run, Transition{
		ExpectedVersion: run.RecordVersion, TargetState: run.State, Usage: run.Usage,
	}); !errors.Is(err, ErrNoProgress) {
		t.Fatalf("empty checkpoint error = %v", err)
	}

	usage := run.Usage
	usage.LLMCallsUsed = 1
	usage.ElapsedMS = 10
	next, err := Apply(run, Transition{
		ExpectedVersion: run.RecordVersion, TargetState: run.State, Usage: usage,
	})
	if err != nil || next.Usage != usage || next.RecordVersion != run.RecordVersion+1 {
		t.Fatalf("budget checkpoint = %#v, %v", next, err)
	}
	decreased := usage
	decreased.ElapsedMS--
	if _, err := Apply(next, Transition{
		ExpectedVersion: next.RecordVersion, TargetState: next.State, Usage: decreased,
	}); !errors.Is(err, ErrInvalidRun) {
		t.Fatalf("decreasing budget error = %v", err)
	}
	exceeded := usage
	exceeded.LLMCallsUsed = run.Limits.MaxLLMCalls + 1
	if _, err := Apply(run, Transition{
		ExpectedVersion: run.RecordVersion, TargetState: StateBinding, Usage: exceeded,
	}); !errors.Is(err, ErrInvalidRun) {
		t.Fatalf("exceeded budget error = %v", err)
	}
}

func TestApplyRejectsStaleVersionAndHashOverwrite(t *testing.T) {
	run := validRun(StateUnderstanding)
	if _, err := Apply(run, Transition{
		ExpectedVersion: run.RecordVersion - 1, TargetState: StateRetrieving, Usage: run.Usage,
	}); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale version error = %v", err)
	}
	run.Hashes.Understanding = testHash("1")
	replacement := testHash("2")
	if _, err := Apply(run, Transition{
		ExpectedVersion: run.RecordVersion, TargetState: StateRetrieving,
		Usage: run.Usage, Hashes: HashUpdates{Understanding: &replacement},
	}); !errors.Is(err, ErrInvalidRun) {
		t.Fatalf("hash overwrite error = %v", err)
	}
}

func TestApplyEnforcesGovernedHashStagesAndDependencies(t *testing.T) {
	early := validRun(StateReceived)
	resultHash := testHash("6")
	usage := early.Usage
	usage.StepCount = 1
	if _, err := Apply(early, Transition{
		ExpectedVersion: early.RecordVersion, TargetState: StateReceived, Usage: usage,
		Hashes: HashUpdates{Result: &resultHash},
	}); !errors.Is(err, ErrInvalidRun) {
		t.Fatalf("early result hash error = %v", err)
	}

	missingUpstream := validRun(StateRetrieving)
	bindingHash := testHash("2")
	if _, err := Apply(missingUpstream, Transition{
		ExpectedVersion: missingUpstream.RecordVersion, TargetState: StateBinding,
		Usage: missingUpstream.Usage, Hashes: HashUpdates{BindingBundle: &bindingHash},
	}); !errors.Is(err, ErrInvalidRun) {
		t.Fatalf("binding without understanding error = %v", err)
	}

	late := validRun(StateAnswerVerifying)
	understandingHash := testHash("1")
	graphHash, semanticIRHash, queryPlanHash := testHash("3"), testHash("4"), testHash("5")
	if _, err := Apply(late, Transition{
		ExpectedVersion: late.RecordVersion, TargetState: StateAnswered, Usage: late.Usage,
		Hashes: HashUpdates{
			Understanding: &understandingHash, BindingBundle: &bindingHash, GraphPlan: &graphHash,
			SemanticIR: &semanticIRHash, QueryPlan: &queryPlanHash, Result: &resultHash,
		},
		Completion: &CompletionRef{
			Code: "ANSWER_READY", ArtifactType: ArtifactAnswer, ArtifactHash: testHash("f"),
		},
	}); !errors.Is(err, ErrInvalidRun) {
		t.Fatalf("late completion-chain injection error = %v", err)
	}

	invalidSnapshot := validRun(StateGraphValidating)
	invalidSnapshot.Hashes.GraphPlan = graphHash
	if err := invalidSnapshot.Validate(); !errors.Is(err, ErrInvalidRun) {
		t.Fatalf("noncontiguous persisted hashes error = %v", err)
	}
}

func TestApplyAcceptsEachHashOnlyAtItsGovernedStage(t *testing.T) {
	run := validRun(StateContextReady)
	understandingHash, bindingHash := testHash("1"), testHash("2")
	graphHash, semanticIRHash := testHash("3"), testHash("4")
	queryPlanHash, resultHash := testHash("5"), testHash("6")
	steps := []struct {
		target State
		hashes HashUpdates
	}{
		{StateUnderstanding, HashUpdates{Understanding: &understandingHash}},
		{StateRetrieving, HashUpdates{}},
		{StateBinding, HashUpdates{BindingBundle: &bindingHash}},
		{StateGraphValidating, HashUpdates{GraphPlan: &graphHash}},
		{StateIRReady, HashUpdates{SemanticIR: &semanticIRHash}},
		{StatePlanValidating, HashUpdates{QueryPlan: &queryPlanHash}},
		{StateExecuting, HashUpdates{}},
		{StateResultVerifying, HashUpdates{Result: &resultHash}},
	}
	for _, step := range steps {
		next, err := Apply(run, Transition{
			ExpectedVersion: run.RecordVersion, TargetState: step.target,
			Usage: run.Usage, Hashes: step.hashes,
		})
		if err != nil {
			t.Fatalf("Apply(%s -> %s) error = %v", run.State, step.target, err)
		}
		run = next
	}
	if run.Hashes != completeHashes() {
		t.Fatalf("governed hash chain = %#v", run.Hashes)
	}
}

func TestApplyRejectsIllegalJumpWithoutChangingInput(t *testing.T) {
	run := validRun(StateReceived)
	original := run
	if _, err := Apply(run, Transition{
		ExpectedVersion: run.RecordVersion, TargetState: StateExecuting, Usage: run.Usage,
	}); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("illegal jump error = %v", err)
	}
	if run != original {
		t.Fatal("Apply() mutated the input run on an illegal transition")
	}
}

func TestApplyRejectsExecutingToAnsweredEvenWithCompletion(t *testing.T) {
	run := validRun(StateExecuting)
	run.Hashes = completeHashes()
	_, err := Apply(run, Transition{
		ExpectedVersion: run.RecordVersion, TargetState: StateAnswered, Usage: run.Usage,
		Completion: &CompletionRef{
			Code: "ANSWER_VERIFIED", ArtifactType: ArtifactAnswer, ArtifactHash: testHash("f"),
		},
	})
	if !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("EXECUTING -> ANSWERED error = %v, want ErrIllegalTransition", err)
	}
}

func validRun(state State) Run {
	return Run{
		ID: askdata.ID(uuid.NewString()), TenantID: askdata.ID(uuid.NewString()),
		DomainID: askdata.ID(uuid.NewString()), ActorID: askdata.ID(uuid.NewString()),
		TraceID: askdata.ID(uuid.NewString()), IdempotencyKeyHash: testHash("a"),
		QuestionHash: testHash("b"), PolicyScopeHash: testHash("c"),
		Release: askdata.ReleaseRef{ReleaseID: askdata.ID(uuid.NewString()), ContentHash: testHash("d")},
		State:   state, Disposition: DispositionPending, Limits: DefaultBudgetLimits(), RecordVersion: 1,
	}
}

func completeHashes() RunHashes {
	return RunHashes{
		Understanding: testHash("1"), BindingBundle: testHash("2"), GraphPlan: testHash("3"),
		SemanticIR: testHash("4"), QueryPlan: testHash("5"), Result: testHash("6"),
	}
}

func testHash(character string) askdata.ContentHash {
	return askdata.ContentHash(strings.Repeat(character, 64))
}
