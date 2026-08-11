package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/toolhost"
)

// RunWorker drives claimed question runs through the AI-005 stage protocol.
//
// It owns no policy of its own: which stage runs in which state, and what a
// cognition action does to the run, both come from protocol.go, which in turn is
// the intersection of the state graph, the stage/tool matrix and the
// stage/action matrix. The worker's job is claiming, sequencing, heartbeating
// and failing closed.
type RunWorker struct {
	leases   *LeaseStore
	runs     RunTransitioner
	assembly LoopAssembler
	options  RunWorkerOptions
}

// RunTransitioner is the durable half of a run. The worker never writes run
// state directly: every advance goes through Transition so the lifecycle
// trigger, optimistic locking and event stream all stay authoritative.
type RunTransitioner interface {
	Transition(context.Context, TransitionRequest) (TransitionResult, error)
	Resume(context.Context, ResumeRequest) (ReplaySnapshot, error)
}

// LoopAssembler builds the per-run execution context. It is an interface so the
// worker does not import the tool adapters, which would invert the dependency
// between the orchestrator and the layer that serves it.
type LoopAssembler interface {
	// Assemble returns the Loop and the authorization context for one run, or an
	// error when the deployment cannot execute questions at all.
	Assemble(context.Context, RunAssembly) (*Loop, toolhost.AuthorizationContext, error)
}

// RunAssembly is everything the assembler needs to build a run's execution
// context.
type RunAssembly struct {
	Scope    askdata.PolicyScope
	DomainID askdata.ID
	RunID    askdata.ID
}

type RunWorkerOptions struct {
	WorkerID      string
	Lease         time.Duration
	MaxStages     int
	BudgetClass   RunBudgetClass
	PromptVersion string
}

func DefaultRunWorkerOptions() RunWorkerOptions {
	return RunWorkerOptions{
		Lease: 2 * time.Minute,
		// One run may cross at most this many state transitions. It bounds the
		// worker independently of the Loop's per-stage budget so a protocol bug
		// cannot spin a run forever.
		MaxStages:   32,
		BudgetClass: BudgetClassSingleQueryComplex,
	}
}

func (options RunWorkerOptions) validate() error {
	if options.WorkerID == "" || len(options.WorkerID) > 128 {
		return fmt.Errorf("%w: worker identity is required", ErrInvalidLease)
	}
	if options.Lease < 30*time.Second || options.Lease > 10*time.Minute {
		return fmt.Errorf("%w: lease must be 30s-600s", ErrInvalidLease)
	}
	if options.MaxStages < 1 || options.MaxStages > 128 {
		return fmt.Errorf("%w: stage bound is out of range", ErrInvalidLease)
	}
	return nil
}

func NewRunWorker(
	leases *LeaseStore,
	runs RunTransitioner,
	assembly LoopAssembler,
	options RunWorkerOptions,
) (*RunWorker, error) {
	if leases == nil || runs == nil || assembly == nil {
		return nil, fmt.Errorf("%w: run worker dependencies are incomplete", ErrInvalidLease)
	}
	if options.Lease == 0 {
		defaults := DefaultRunWorkerOptions()
		options.Lease, options.MaxStages = defaults.Lease, defaults.MaxStages
		if options.BudgetClass == "" {
			options.BudgetClass = defaults.BudgetClass
		}
	}
	if err := options.validate(); err != nil {
		return nil, err
	}
	return &RunWorker{leases: leases, runs: runs, assembly: assembly, options: options}, nil
}

// ProcessNext claims and drives at most one run. It reports whether it did any
// work so the caller can back off when the queue is empty.
func (worker *RunWorker) ProcessNext(ctx context.Context, tenantID string) (bool, error) {
	if worker == nil {
		return false, ErrInvalidLease
	}
	claimed, ok, err := worker.leases.Claim(ctx, tenantID, worker.options.WorkerID, worker.options.Lease)
	if err != nil || !ok {
		return false, err
	}
	defer func() {
		// Releasing is best effort: an expired lease is reclaimable anyway, and a
		// release failure must not mask the outcome of the run itself.
		_ = worker.leases.Release(context.WithoutCancel(ctx), claimed.RunID, claimed.LeaseToken)
	}()

	if claimed.ResumeMode == ResumeAbandoned {
		// The previous worker died mid-flight. Budget was already charged and
		// tool calls may already have reached the warehouse, so the run is
		// finalised rather than re-executed.
		return true, worker.failClosed(ctx, claimed, "QUESTION_RUN_ABANDONED",
			"a previous execution attempt did not complete")
	}
	return true, worker.drive(ctx, claimed)
}

func (worker *RunWorker) drive(ctx context.Context, claimed LeasedRun) error {
	scope, err := worker.scopeFor(ctx, claimed)
	if err != nil {
		return worker.failClosed(ctx, claimed, "QUESTION_SCOPE_UNAVAILABLE", "run scope could not be rebuilt")
	}
	loop, authorization, err := worker.assembly.Assemble(ctx, RunAssembly{
		Scope: scope, DomainID: claimed.DomainID, RunID: claimed.RunID,
	})
	if err != nil {
		// A deployment with no model provider cannot answer questions at all.
		// Failing the run closed with an explicit code is honest; leaving it at
		// RECEIVED forever is not.
		return worker.failClosed(ctx, claimed, "QUESTION_ENGINE_UNAVAILABLE", "question engine is not configured")
	}

	state, version := claimed.CurrentState, claimed.RecordVersion
	corrections := 0
	for stage := 0; stage < worker.options.MaxStages; stage++ {
		if terminalState(state) {
			return nil
		}
		alive, heartbeatErr := worker.leases.Heartbeat(ctx, claimed.RunID, claimed.LeaseToken, worker.options.Lease)
		if heartbeatErr != nil {
			return heartbeatErr
		}
		if !alive {
			// Someone else owns this run now. Stop writing immediately rather
			// than racing the new owner.
			return nil
		}

		if next, deterministic := DeterministicNextState(state); deterministic {
			state, version, err = worker.advance(ctx, scope, claimed, version, state, next,
				"PROTOCOL_DETERMINISTIC", nil)
			if err != nil {
				return err
			}
			continue
		}

		cognitionStage, ok := StageForState(state)
		if !ok {
			return worker.failClosedFrom(ctx, scope, claimed, version, state,
				"QUESTION_PROTOCOL_GAP", "no stage is defined for this run state")
		}
		snapshot, resumeErr := worker.runs.Resume(ctx, ResumeRequest{
			Scope: scope, DomainID: claimed.DomainID, RunID: claimed.RunID,
		})
		if resumeErr != nil {
			return resumeErr
		}
		outcome, loopErr := loop.Run(ctx, LoopRequest{
			Run: snapshot.Run, Stage: cognitionStage, BudgetClass: worker.options.BudgetClass,
			Authorization: authorization,
		})
		if loopErr != nil {
			code := "QUESTION_LOOP_FAILED"
			if errors.Is(loopErr, ErrLoopBudgetExhausted) || errors.Is(loopErr, ErrLoopTimeout) {
				code = "QUESTION_BUDGET_EXHAUSTED"
			}
			return worker.failClosedFrom(ctx, scope, claimed, version, state, code, loopErr.Error())
		}

		action := outcome.Decision.Action.Action
		next, mapped := NextState(state, action)
		if !mapped {
			// The model returned an action this state cannot act on. The stage
			// schema should have prevented it; treat it as a protocol violation
			// rather than guessing an interpretation.
			return worker.failClosedFrom(ctx, scope, claimed, version, state,
				"QUESTION_PROTOCOL_VIOLATION", string(action))
		}
		if next == state {
			// CALL_TOOL consumed budget without advancing. The Loop already
			// enforces per-stage budget; another pass would be a no-op.
			return worker.failClosedFrom(ctx, scope, claimed, version, state,
				"QUESTION_NO_PROGRESS", "stage ended without advancing the run")
		}
		if IsBoundedCorrection(state, next) {
			corrections++
			if corrections > MaxBoundedCorrections {
				return worker.failClosedFrom(ctx, scope, claimed, version, state,
					"QUESTION_CORRECTION_LIMIT", "bounded correction limit exceeded")
			}
		}
		state, version, err = worker.advance(ctx, scope, claimed, version, state, next,
			string(action), &outcome.Usage)
		if err != nil {
			return err
		}
	}
	return worker.failClosedFrom(ctx, scope, claimed, version, state,
		"QUESTION_STAGE_LIMIT", "run exceeded the stage bound")
}

func (worker *RunWorker) advance(
	ctx context.Context,
	scope askdata.PolicyScope,
	claimed LeasedRun,
	version int64,
	from, to State,
	code string,
	usage *BudgetUsage,
) (State, int64, error) {
	request := TransitionRequest{
		Scope: scope, DomainID: claimed.DomainID, RunID: claimed.RunID,
		ExpectedVersion: version, TargetState: to,
		Event: TransitionEventInput{
			Stage: string(from), Status: EventSucceeded, Code: code,
		},
	}
	if usage != nil {
		request.Usage = *usage
	}
	result, err := worker.runs.Transition(ctx, request)
	if err != nil {
		return from, version, err
	}
	return result.Run.State, result.Run.RecordVersion, nil
}

// failClosed terminalises a claimed run that never started executing.
func (worker *RunWorker) failClosed(ctx context.Context, claimed LeasedRun, code, message string) error {
	scope, err := worker.scopeFor(ctx, claimed)
	if err != nil {
		return err
	}
	return worker.failClosedFrom(ctx, scope, claimed, claimed.RecordVersion, claimed.CurrentState, code, message)
}

// failClosedFrom moves a run to BLOCKED with an explicit, auditable reason.
// Every abnormal exit goes through here so a run never silently stalls.
func (worker *RunWorker) failClosedFrom(
	ctx context.Context,
	scope askdata.PolicyScope,
	claimed LeasedRun,
	version int64,
	from State,
	code, message string,
) error {
	if terminalState(from) {
		return nil
	}
	details, _ := json.Marshal(map[string]string{"reason": boundedReason(message)})
	_, err := worker.runs.Transition(ctx, TransitionRequest{
		Scope: scope, DomainID: claimed.DomainID, RunID: claimed.RunID,
		ExpectedVersion: version, TargetState: StateBlocked,
		Event: TransitionEventInput{
			Stage: string(from), Status: EventFailed, Code: code, Details: details,
		},
	})
	return err
}

// scopeFor rebuilds the policy scope pinned to the run. The release comes from
// the run itself, never from the domain's current ACTIVE release: a run must
// keep answering under the release it was created against even if the domain
// has since activated a new one.
func (worker *RunWorker) scopeFor(ctx context.Context, claimed LeasedRun) (askdata.PolicyScope, error) {
	roleIDs, err := worker.leases.ActorRoleIDs(ctx, claimed.TenantID, claimed.ActorID)
	if err != nil {
		return askdata.PolicyScope{}, err
	}
	return askdata.NewPolicyScope(
		claimed.TenantID, claimed.ActorID,
		[]askdata.ID{claimed.DomainID}, roleIDs,
		askdata.ReleaseRef{ReleaseID: claimed.ReleaseID, ContentHash: claimed.ReleaseContentHash},
	)
}

func terminalState(state State) bool {
	switch state {
	case StateAnswered, StateBlocked, StateOutOfScope,
		StateClarificationRequired, StateClarificationExpired:
		return true
	default:
		return false
	}
}

func boundedReason(message string) string {
	const limit = 512
	if len(message) > limit {
		return message[:limit]
	}
	return message
}
