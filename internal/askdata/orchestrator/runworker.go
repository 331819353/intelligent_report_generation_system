package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/cognition"
	"intelligent-report-generation-system/internal/askdata/toolhost"
	"intelligent-report-generation-system/internal/platform/database"
)

// RunWorker drives claimed question runs through the AI-005 stage protocol.
//
// It owns no policy of its own: which stage runs in which state, and what a
// cognition action does to the run, both come from protocol.go, which in turn is
// the intersection of the state graph, the stage/tool matrix and the
// stage/action matrix. The worker's job is claiming, sequencing, heartbeating
// and failing closed.
type RunWorker struct {
	leases        *LeaseStore
	runs          RunTransitioner
	assembly      LoopAssembler
	questionFacts QuestionFactSource
	options       RunWorkerOptions
}

func (worker *RunWorker) SetQuestionFactSource(source QuestionFactSource) {
	if worker != nil {
		worker.questionFacts = source
	}
}

// RunTransitioner is the durable half of a run. The worker never writes run
// state directly: every advance goes through Transition so the lifecycle
// trigger, optimistic locking and event stream all stay authoritative.
type RunTransitioner interface {
	Transition(context.Context, TransitionRequest) (TransitionResult, error)
	Resume(context.Context, ResumeRequest) (ReplaySnapshot, error)
}

// RunLoopCheckpointer is implemented by the production store. Keeping it as a
// separate capability lets small transition fakes stay focused, while real
// workers atomically retain every model round, tool outcome, budget update and
// state transition instead of losing the only useful diagnostic on failure.
type RunLoopCheckpointer interface {
	CheckpointLoop(context.Context, LoopCheckpointRequest) (LoopCheckpointResult, error)
}

// LoopAssembler builds the per-run execution context. It is an interface so the
// worker does not import the tool adapters, which would invert the dependency
// between the orchestrator and the layer that serves it.
type LoopAssembler interface {
	// Assemble returns the Loop and the authorization context for one run, or an
	// error when the deployment cannot execute questions at all.
	Assemble(context.Context, RunAssembly) (*Loop, toolhost.AuthorizationContext, error)
}

// RunAnswerFinalizer is the trusted boundary that turns an executed, verified
// query result into the terminal Answer artifact. The worker deliberately does
// not ask the cognition model to reproduce result rows at this point: exact
// cells, citations and release bindings come from the run-scoped tool binding.
type RunAnswerFinalizer interface {
	FinalizeAnswer(
		context.Context,
		AnswerTransitionStore,
		askdata.PolicyScope,
		askdata.ID,
		Run,
	) (Run, error)
	ReleaseRun(askdata.ID)
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
		// A single compatible-provider call can legitimately approach two
		// minutes. Keep a five-minute ownership window and heartbeat it in the
		// background so a healthy worker is never reclaimed between checkpoints.
		Lease: 5 * time.Minute,
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
	// Bind the actor/domain before starting either the drive or its heartbeat.
	// drive() also verifies and rebinds the reconstructed scope, but deriving the
	// execution context here prevents helper goroutines and failure paths from
	// observing the unscoped system poll context.
	executionCtx := database.WithAccessContext(ctx, string(claimed.ActorID), string(claimed.DomainID))
	defer func() {
		// Releasing is best effort: an expired lease is reclaimable anyway, and a
		// release failure must not mask the outcome of the run itself.
		_ = worker.leases.Release(context.WithoutCancel(executionCtx), claimed.RunID, claimed.LeaseToken)
	}()

	if claimed.ResumeMode == ResumeAbandoned {
		// The previous worker died mid-flight. Budget was already charged and
		// tool calls may already have reached the warehouse, so the run is
		// finalised rather than re-executed.
		return true, worker.failClosed(executionCtx, claimed, "QUESTION_RUN_ABANDONED",
			"a previous execution attempt did not complete")
	}
	driveErr := worker.driveWithLeaseHeartbeat(executionCtx, claimed)
	if driveErr == nil || ctx.Err() != nil || errors.Is(driveErr, ErrInvalidLease) || errors.Is(driveErr, ErrPinnedScopeMismatch) {
		return true, driveErr
	}
	// The lease is still held here. Close an unexpected infrastructure or
	// persistence failure in the same attempt instead of releasing a live,
	// nonterminal run and waiting for the next claim to classify it as an
	// abandoned execution. The latest version and usage are reloaded by
	// failClosed, so partial protocol progress remains accurately accounted.
	if closeErr := worker.failClosed(executionCtx, claimed, "QUESTION_WORKER_FAILED", driveErr.Error()); closeErr != nil {
		return true, errors.Join(driveErr, closeErr)
	}
	return true, nil
}

// driveWithLeaseHeartbeat keeps ownership alive while a provider request or a
// warehouse query is in flight. Heartbeating only between protocol stages is
// insufficient because either external call may legitimately take longer than
// the two-minute default lease; in that case the same healthy worker used to
// see its own run as abandoned immediately after the call returned.
func (worker *RunWorker) driveWithLeaseHeartbeat(ctx context.Context, claimed LeasedRun) error {
	driveCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	interval := worker.options.Lease / 3
	if interval < 10*time.Second {
		interval = 10 * time.Second
	}
	heartbeatFailure := make(chan error, 1)
	heartbeatStopped := make(chan struct{})
	go func() {
		defer close(heartbeatStopped)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-driveCtx.Done():
				return
			case <-ticker.C:
				alive, err := worker.leases.Heartbeat(
					driveCtx, claimed.RunID, claimed.LeaseToken, worker.options.Lease,
				)
				if err != nil {
					select {
					case heartbeatFailure <- err:
					default:
					}
					cancel()
					return
				}
				if !alive {
					select {
					case heartbeatFailure <- fmt.Errorf("%w: question run lease was lost", ErrInvalidLease):
					default:
					}
					cancel()
					return
				}
			}
		}
	}()

	driveErr := worker.drive(driveCtx, claimed)
	cancel()
	<-heartbeatStopped
	select {
	case heartbeatErr := <-heartbeatFailure:
		return heartbeatErr
	default:
		return driveErr
	}
}

func (worker *RunWorker) drive(ctx context.Context, claimed LeasedRun) error {
	scope, err := worker.scopeFor(ctx, claimed)
	if err != nil {
		return worker.failClosed(ctx, claimed, "QUESTION_SCOPE_UNAVAILABLE", "run scope could not be rebuilt")
	}
	// Durable run operations deliberately enforce the same actor/domain context
	// as API requests. The worker first rebuilds and verifies the pinned scope,
	// then binds that exact identity to every state transition and envelope read.
	ctx = database.WithAccessContext(ctx, string(scope.ActorID), string(claimed.DomainID))
	initial, err := worker.runs.Resume(ctx, ResumeRequest{
		Scope: scope, DomainID: claimed.DomainID, RunID: claimed.RunID,
	})
	if err != nil {
		return err
	}
	if worker.questionFacts == nil {
		return worker.failClosedFrom(ctx, scope, claimed, initial.Run.RecordVersion, initial.Run.State,
			initial.Run.Usage, "QUESTION_CONTEXT_UNAVAILABLE", "question context source is not configured")
	}
	questionFact, err := worker.questionFacts.LoadQuestionFact(ctx, scope, claimed.DomainID, initial.Run)
	if err != nil {
		return worker.failClosedFrom(ctx, scope, claimed, initial.Run.RecordVersion, initial.Run.State,
			initial.Run.Usage,
			"QUESTION_CONTEXT_UNAVAILABLE", "encrypted question context could not be opened")
	}
	policyFact, err := questionPolicyFact(scope, claimed.DomainID, claimed.RunID)
	if err != nil {
		return worker.failClosedFrom(ctx, scope, claimed, initial.Run.RecordVersion, initial.Run.State,
			initial.Run.Usage,
			"QUESTION_CONTEXT_UNAVAILABLE", "question policy context could not be built")
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
	if finalizer, ok := worker.assembly.(RunAnswerFinalizer); ok {
		defer finalizer.ReleaseRun(claimed.RunID)
	}

	state, version, usage := initial.Run.State, initial.Run.RecordVersion, initial.Run.Usage
	governedFacts := []GovernedFact{questionFact, policyFact}
	corrections := 0
	var chain governedChain
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

		if state == StateResultVerifying {
			finalizer, ok := worker.assembly.(RunAnswerFinalizer)
			if !ok {
				return worker.failClosedFrom(ctx, scope, claimed, version, state, usage,
					"ANSWER_FINALIZER_UNAVAILABLE", "trusted answer finalizer is not configured")
			}
			snapshot, resumeErr := worker.runs.Resume(ctx, ResumeRequest{
				Scope: scope, DomainID: claimed.DomainID, RunID: claimed.RunID,
			})
			if resumeErr != nil {
				return resumeErr
			}
			completed, finalizeErr := finalizer.FinalizeAnswer(
				ctx, worker.runs, scope, claimed.DomainID, snapshot.Run,
			)
			if finalizeErr != nil {
				latest, latestErr := worker.runs.Resume(ctx, ResumeRequest{
					Scope: scope, DomainID: claimed.DomainID, RunID: claimed.RunID,
				})
				if latestErr != nil {
					return finalizeErr
				}
				return worker.failClosedFrom(ctx, scope, claimed,
					latest.Run.RecordVersion, latest.Run.State, latest.Run.Usage,
					"ANSWER_FINALIZATION_FAILED", finalizeErr.Error())
			}
			if completed.State != StateAnswered {
				return worker.failClosedFrom(ctx, scope, claimed,
					completed.RecordVersion, completed.State, completed.Usage,
					"ANSWER_FINALIZATION_INCOMPLETE", "trusted finalizer did not complete the run")
			}
			return nil
		}

		// A candidate-stage binding is already complete once the trusted tool
		// host has resolved its graph and validated the semantic bundle. Advance
		// that happy path without asking a second model to restate the same
		// binding. Correction paths reset the chain and therefore still enter the
		// dedicated DISAMBIGUATION stage.
		if state == StateBinding && chain.binding != "" && chain.graphPlan != "" {
			state, version, err = worker.advance(ctx, scope, claimed, version, state, StateGraphValidating,
				"BINDING_GRAPH_VALIDATED", &usage, chain, nil)
			if err != nil {
				return err
			}
			continue
		}

		if next, deterministic := DeterministicNextState(state); deterministic {
			// A deterministic hop still carries the chain: GRAPH_VALIDATING,
			// PLAN_VALIDATING and RESULT_VERIFYING are the states that own the
			// graph plan, query plan and result links, and no model round runs
			// while passing through them.
			state, version, err = worker.advance(ctx, scope, claimed, version, state, next,
				"PROTOCOL_DETERMINISTIC", &usage, chain, nil)
			if err != nil {
				return err
			}
			continue
		}

		// Scope is decided before the model is consulted: an unanswerable
		// question is refused in one deterministic step with the redirect the
		// user actually needs, instead of entering the loop to fail at binding.
		if state == StateUnderstanding {
			if verdict, outOfScope := scopeVerdictFor(governedFacts); outOfScope {
				completion, completionErr := scopeCompletion(verdict)
				if completionErr != nil {
					return worker.failClosedFrom(ctx, scope, claimed, version, state, usage,
						"QUESTION_COMPLETION_INVALID", completionErr.Error())
				}
				_, _, err = worker.advance(ctx, scope, claimed, version, state, StateOutOfScope,
					"QUESTION_OUT_OF_SCOPE", &usage, chain, completion)
				return err
			}
		}

		cognitionStage, ok := StageForState(state)
		if !ok {
			return worker.failClosedFrom(ctx, scope, claimed, version, state, usage,
				"QUESTION_PROTOCOL_GAP", "no stage is defined for this run state")
		}
		snapshot, resumeErr := worker.runs.Resume(ctx, ResumeRequest{
			Scope: scope, DomainID: claimed.DomainID, RunID: claimed.RunID,
		})
		if resumeErr != nil {
			return resumeErr
		}
		loopRequest, replayErr := BindReplayGuards(snapshot, LoopRequest{
			Run: snapshot.Run, Stage: cognitionStage, BudgetClass: worker.options.BudgetClass,
			Facts: factsForStage(cognitionStage, governedFacts), Authorization: authorization,
		})
		if replayErr != nil {
			return worker.failClosedFrom(ctx, scope, claimed, version, state, usage,
				"QUESTION_REPLAY_INVALID", "persisted question audit could not be verified")
		}
		outcome, loopErr := loop.Run(ctx, loopRequest)
		if loopErr != nil {
			code := "QUESTION_LOOP_FAILED"
			if errors.Is(loopErr, ErrLoopBudgetExhausted) || errors.Is(loopErr, ErrLoopTimeout) {
				code = "QUESTION_BUDGET_EXHAUSTED"
			}
			failedUsage := usage
			if outcome.Usage.monotonicFrom(usage) && outcome.Usage.validate(snapshot.Run.Limits) == nil {
				failedUsage = outcome.Usage
			}
			outcome.Usage = failedUsage
			if checkpointer, ok := worker.runs.(RunLoopCheckpointer); ok {
				details, _ := json.Marshal(map[string]string{
					"code": code, "reason": boundedReason(loopErr.Error()),
				})
				checkpoint, checkpointErr := checkpointer.CheckpointLoop(ctx, LoopCheckpointRequest{
					Scope: scope, DomainID: claimed.DomainID, RunID: claimed.RunID,
					ExpectedVersion: version,
					CheckpointID:    loopCheckpointID(claimed.RunID, version, cognitionStage),
					Stage:           cognitionStage,
					TargetState:     StateBlocked,
					Result:          outcome,
					Failure:         &LoopFailure{Code: code, Status: EventBlocked},
					Hashes:          chain.updatesFor(state, StateBlocked),
					Completion: &CompletionArtifactInput{
						Code: code, Type: ArtifactBlock,
						SchemaVersion: RunBlockSchemaVersion, Payload: details,
					},
				})
				if checkpointErr != nil {
					return checkpointErr
				}
				state, version, usage = checkpoint.Run.State, checkpoint.Run.RecordVersion, checkpoint.Run.Usage
				return nil
			}
			return worker.failClosedFrom(ctx, scope, claimed, version, state, failedUsage, code, loopErr.Error())
		}
		newFacts, factsErr := outcomeFacts(snapshot.Run, outcome)
		if factsErr != nil {
			return worker.failClosedFrom(ctx, scope, claimed, version, state, outcome.Usage,
				"QUESTION_CONTEXT_INVALID", "validated stage evidence could not be carried forward")
		}
		governedFacts = append(governedFacts, newFacts...)

		action := outcome.Decision.Action.Action
		next, mapped := NextState(state, action)
		if !mapped {
			// The model returned an action this state cannot act on. The stage
			// schema should have prevented it; treat it as a protocol violation
			// rather than guessing an interpretation.
			return worker.failClosedFrom(ctx, scope, claimed, version, state, outcome.Usage,
				"QUESTION_PROTOCOL_VIOLATION", string(action))
		}
		if next == state {
			// CALL_TOOL consumed budget without advancing. The Loop already
			// enforces per-stage budget; another pass would be a no-op.
			return worker.failClosedFrom(ctx, scope, claimed, version, state, outcome.Usage,
				"QUESTION_NO_PROGRESS", "stage ended without advancing the run")
		}
		// A proposal the platform will not stand behind must not advance the run.
		// Until this gate existed, ConfidenceEvidence was recorded and never
		// read, so a self-reported 0.1 advanced exactly like a 1.0.
		confidence := GateProposalConfidence(outcome.Decision.Action)
		if !confidence.Accepted {
			next = confidenceRedirect(state)
		}
		if IsBoundedCorrection(state, next) {
			corrections++
			if corrections > MaxBoundedCorrections {
				return worker.failClosedFrom(ctx, scope, claimed, version, state, outcome.Usage,
					"QUESTION_CORRECTION_LIMIT", "bounded correction limit exceeded")
			}
			// Retreating to BINDING invalidates everything downstream of it.
			chain.reset()
		} else if confidence.Accepted {
			// A refused proposal contributes no link: the chain records work the
			// platform accepted, not work it just turned down.
			chain.observe(outcome.Decision, outcome.ToolExecutions)
		}
		var (
			completion    *CompletionArtifactInput
			completionErr error
		)
		if confidence.Accepted {
			completion, completionErr = completionForDecision(next, outcome.Decision)
		} else {
			completion, completionErr = confidenceCompletion(next, confidence)
		}
		if completionErr != nil {
			return worker.failClosedFrom(ctx, scope, claimed, version, state, outcome.Usage,
				"QUESTION_COMPLETION_INVALID", completionErr.Error())
		}
		if checkpointer, ok := worker.runs.(RunLoopCheckpointer); ok {
			checkpoint, checkpointErr := checkpointer.CheckpointLoop(ctx, LoopCheckpointRequest{
				Scope: scope, DomainID: claimed.DomainID, RunID: claimed.RunID,
				ExpectedVersion: version,
				CheckpointID:    loopCheckpointID(claimed.RunID, version, cognitionStage),
				Stage:           cognitionStage,
				TargetState:     next,
				Result:          outcome,
				Hashes:          chain.updatesFor(state, next),
				Completion:      completion,
			})
			if checkpointErr != nil {
				return checkpointErr
			}
			state, version = checkpoint.Run.State, checkpoint.Run.RecordVersion
		} else {
			state, version, err = worker.advance(ctx, scope, claimed, version, state, next,
				string(action), &outcome.Usage, chain, completion)
		}
		if err != nil {
			return err
		}
		usage = outcome.Usage
	}
	return worker.failClosedFrom(ctx, scope, claimed, version, state, usage,
		"QUESTION_STAGE_LIMIT", "run exceeded the stage bound")
}

func loopCheckpointID(runID askdata.ID, version int64, stage cognition.Stage) askdata.ID {
	return askdata.ID(fmt.Sprintf("loop-%s-%d-%s", runID, version, stage))
}

func questionPolicyFact(
	scope askdata.PolicyScope,
	domainID, runID askdata.ID,
) (GovernedFact, error) {
	payload, err := json.Marshal(map[string]string{
		"domainId": string(domainID), "releaseId": string(scope.Release.ReleaseID),
		"releaseContentHash": string(scope.Release.ContentHash),
		"policyScopeHash":    string(scope.PolicyHash),
	})
	if err != nil {
		return GovernedFact{}, err
	}
	evidenceID := askdata.ID(askdata.HashBytes([]byte(
		"question-policy-fact-v1\x00" + string(runID) + "\x00" + string(scope.PolicyHash),
	)))
	fact, err := cognition.NewPromptFact(evidenceID, cognition.FactPolicyEvidence, payload)
	if err != nil {
		return GovernedFact{}, err
	}
	return GovernedFact{
		Fact: fact,
		Evidence: askdata.EvidenceRef{
			EvidenceID: evidenceID, Kind: askdata.EvidenceKindPolicy,
			SourceID: domainID, ContentHash: fact.ContentHash,
		},
	}, nil
}

func (worker *RunWorker) advance(
	ctx context.Context,
	scope askdata.PolicyScope,
	claimed LeasedRun,
	version int64,
	from, to State,
	code string,
	usage *BudgetUsage,
	chain governedChain,
	completion *CompletionArtifactInput,
) (State, int64, error) {
	details, _ := json.Marshal(map[string]string{"action": code})
	request := TransitionRequest{
		Scope: scope, DomainID: claimed.DomainID, RunID: claimed.RunID,
		ExpectedVersion: version, TargetState: to,
		Hashes:     chain.updatesFor(from, to),
		Completion: completion,
		Event: TransitionEventInput{
			// Stage/status/code are derived from the persisted target state by the
			// store. Keeping the cognition action in bounded details avoids letting
			// an untrusted action contradict the durable transition protocol.
			Details: details,
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
	ctx = database.WithAccessContext(ctx, string(scope.ActorID), string(claimed.DomainID))
	snapshot, err := worker.runs.Resume(ctx, ResumeRequest{
		Scope: scope, DomainID: claimed.DomainID, RunID: claimed.RunID,
	})
	if err != nil {
		return err
	}
	return worker.failClosedFrom(ctx, scope, claimed, snapshot.Run.RecordVersion,
		snapshot.Run.State, snapshot.Run.Usage, code, message)
}

// failClosedFrom moves a run to BLOCKED with an explicit, auditable reason.
// Every abnormal exit goes through here so a run never silently stalls.
func (worker *RunWorker) failClosedFrom(
	ctx context.Context,
	scope askdata.PolicyScope,
	claimed LeasedRun,
	version int64,
	from State,
	usage BudgetUsage,
	code, message string,
) error {
	if terminalState(from) {
		return nil
	}
	details, _ := json.Marshal(map[string]string{"code": code, "reason": boundedReason(message)})
	_, err := worker.runs.Transition(ctx, TransitionRequest{
		Scope: scope, DomainID: claimed.DomainID, RunID: claimed.RunID,
		ExpectedVersion: version, TargetState: StateBlocked,
		Usage: usage,
		// BLOCKED is terminal, and Apply refuses a terminal transition without a
		// completion artifact. Without this the fail-closed path itself failed,
		// leaving the run to stall in place — the one outcome this function
		// exists to prevent.
		Completion: &CompletionArtifactInput{
			Code: code, Type: ArtifactBlock,
			SchemaVersion: RunBlockSchemaVersion, Payload: details,
		},
		Event: TransitionEventInput{
			Stage: string(StateBlocked), Status: EventBlocked, Code: code, Details: details,
		},
	})
	return err
}

// scopeFor rebuilds the policy scope pinned to the run. The release comes from
// the run itself, never from the domain's current ACTIVE release: a run must
// keep answering under the release it was created against even if the domain
// has since activated a new one.
func (worker *RunWorker) scopeFor(ctx context.Context, claimed LeasedRun) (askdata.PolicyScope, error) {
	roleIDs, err := worker.leases.ActorRoleIDs(ctx, claimed.TenantID, claimed.ActorID, claimed.RunID)
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
