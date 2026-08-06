package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/cognition"
	semanticregistry "intelligent-report-generation-system/internal/askdata/registry"
	"intelligent-report-generation-system/internal/askdata/toolhost"
)

const (
	loopCheckpointHashVersion = "question-loop-checkpoint-v1"
	toolReplaySchemaVersion   = "tool-execution-replay-v1"
)

// LoopFailure is a stable, sanitized failure summary. Raw provider, database
// and tool errors never cross the audit boundary.
type LoopFailure struct {
	Code   string
	Status EventStatus
}

func (failure LoopFailure) Validate() error {
	if !completionCodePattern.MatchString(failure.Code) ||
		(failure.Status != EventBlocked && failure.Status != EventFailed && failure.Status != EventCanceled) {
		return fmt.Errorf("%w: loop failure summary is invalid", ErrInvalidRun)
	}
	return nil
}

// LoopCheckpointRequest atomically persists every successful cognition round,
// every typed tool outcome, the absolute budget and one state checkpoint.
// CheckpointID is caller-stable so a lost response can be replayed safely.
type LoopCheckpointRequest struct {
	Scope           askdata.PolicyScope
	DomainID        askdata.ID
	RunID           askdata.ID
	ExpectedVersion int64
	CheckpointID    askdata.ID
	Stage           cognition.Stage
	TargetState     State
	Result          LoopResult
	Failure         *LoopFailure
	Hashes          HashUpdates
	Completion      *CompletionArtifactInput
}

type LoopCheckpointResult struct {
	Run      Run
	Event    Event
	Snapshot ReplaySnapshot
	Replayed bool
}

type preparedLoopCheckpoint struct {
	next             Run
	rounds           []preparedCognitionAudit
	tools            []preparedToolAudit
	completion       *Artifact
	budgetDetails    json.RawMessage
	failureDetails   json.RawMessage
	transitionDetail json.RawMessage
}

type preparedCognitionAudit struct {
	execution   CognitionExecution
	evidenceIDs []askdata.ID
}

type preparedToolAudit struct {
	call     ToolCall
	artifact Artifact
	charge   toolhost.BudgetCharge
}

type toolReplayEnvelope struct {
	SchemaVersion  string                  `json:"schemaVersion"`
	ToolCallID     askdata.ID              `json:"toolCallId"`
	Tool           toolhost.ToolName       `json:"tool"`
	DefinitionHash askdata.ContentHash     `json:"definitionHash"`
	Charge         toolhost.BudgetCharge   `json:"charge"`
	DurationMS     int64                   `json:"durationMs"`
	TimedOut       bool                    `json:"timedOut"`
	Status         toolhost.ResponseStatus `json:"status"`
	OutcomeHash    askdata.ContentHash     `json:"outcomeHash"`
	TypedOutput    json.RawMessage         `json:"typedOutput"`
	EvidenceRefs   []askdata.EvidenceRef   `json:"evidenceRefs"`
	ErrorCode      string                  `json:"errorCode"`
	Retryable      bool                    `json:"retryable"`
	MadeProgress   bool                    `json:"madeProgress"`
}

// CheckpointLoop writes one loop outcome as a single actor-scoped transaction.
// An exact retry of a committed checkpoint returns its verified replay snapshot
// without appending facts or charging the budget again.
func (store *PostgresStore) CheckpointLoop(
	ctx context.Context,
	request LoopCheckpointRequest,
) (LoopCheckpointResult, error) {
	tenantID, err := validateActorScope(ctx, request.Scope, request.DomainID)
	if err != nil {
		return LoopCheckpointResult{}, err
	}
	if !canonicalUUID(request.RunID) || request.ExpectedVersion < 1 ||
		request.CheckpointID.Validate() != nil || !stagePattern.MatchString(string(request.Stage)) {
		return LoopCheckpointResult{}, fmt.Errorf("%w: loop checkpoint identity is invalid", ErrInvalidRun)
	}
	checkpointHash, err := computeLoopCheckpointHash(request)
	if err != nil {
		return LoopCheckpointResult{}, err
	}

	var result LoopCheckpointResult
	err = store.withActorTx(ctx, pgx.TxOptions{}, tenantID, func(tx pgx.Tx) error {
		current, err := loadRunByIDTx(ctx, tx, request.Scope, request.DomainID, request.RunID, true)
		if err != nil {
			return err
		}
		if !runMatchesScope(current, request.Scope, request.DomainID) {
			return ErrPinnedScopeMismatch
		}
		snapshot, err := loadReplaySnapshotTx(ctx, tx, current)
		if err != nil {
			return err
		}
		if current.RecordVersion != request.ExpectedVersion {
			replayed, conflict := exactCheckpointReplay(snapshot, request, checkpointHash)
			if replayed {
				result = LoopCheckpointResult{
					Run: current, Event: snapshot.Events[len(snapshot.Events)-1],
					Snapshot: snapshot, Replayed: true,
				}
				return nil
			}
			if conflict {
				return ErrIdempotencyConflict
			}
			return ErrVersionConflict
		}
		if checkpointIDExists(snapshot.Events, request.CheckpointID) {
			return ErrIdempotencyConflict
		}
		if !checkpointMakesReplayProgress(snapshot, request.Result) {
			return ErrNoProgress
		}

		prepared, err := prepareLoopCheckpoint(current, request, checkpointHash)
		if err != nil {
			return err
		}
		last := snapshot.Events[len(snapshot.Events)-1]
		eventIndex, previousHash := last.Index, last.Hash
		appendEvent := func(event Event) error {
			if err := insertEventTx(ctx, tx, event); err != nil {
				return err
			}
			eventIndex, previousHash = event.Index, event.Hash
			return nil
		}

		toolIndex := 0
		for _, round := range prepared.rounds {
			event, err := newLoopAuditEvent(current, eventIndex+1, previousHash, loopAuditEventInput{
				Type: EventLLMDecision, Stage: string(request.Stage), Status: EventSucceeded,
				Code:        string(round.execution.Round.Action.Action),
				AIRequestID: askdata.ID(round.execution.Round.AIRequestID),
				ActionHash:  round.execution.Round.ActionHash, EvidenceIDs: round.evidenceIDs,
				Details: cognitionAuditDetails(round.execution), DurationMS: &round.execution.DurationMS,
			})
			if err != nil {
				return err
			}
			if err := appendEvent(event); err != nil {
				return err
			}
			if round.execution.Round.Action.Action != cognition.ActionCallTool {
				continue
			}
			tool := prepared.tools[toolIndex]
			toolIndex++
			index, err := nextArtifactIndexTx(ctx, tx, current)
			if err != nil {
				return err
			}
			tool.artifact.Index, tool.artifact.ID = index, askdata.ID(uuid.NewString())
			if err := tool.artifact.Validate(); err != nil {
				return err
			}
			if err := insertArtifactTx(ctx, tx, tool.artifact); err != nil {
				return err
			}
			artifactEvent, err := newLoopAuditEvent(current, eventIndex+1, previousHash, loopAuditEventInput{
				Type: EventArtifactRecorded, Stage: string(request.Stage), Status: EventSucceeded,
				Code: "TOOL_REPLAY_RECORDED", ArtifactHash: tool.artifact.Hash,
				EvidenceIDs: tool.artifact.EvidenceIDs,
				Details:     mustCanonicalAudit(map[string]any{"toolCallId": tool.call.CallID, "tool": tool.call.Tool}),
			})
			if err != nil {
				return err
			}
			if err := appendEvent(artifactEvent); err != nil {
				return err
			}
			tool.call.ID = askdata.ID(uuid.NewString())
			if err := tool.call.validate(); err != nil {
				return err
			}
			if err := insertToolCallTx(ctx, tx, tool.call); err != nil {
				return err
			}
			toolEvent, err := newLoopAuditEvent(current, eventIndex+1, previousHash, loopAuditEventInput{
				Type: EventToolResult, Stage: string(request.Stage), Status: EventStatus(tool.call.Status),
				Code: toolEventCode(tool.call), ToolCallID: tool.call.CallID,
				EvidenceIDs: tool.call.EvidenceIDs, Details: toolAuditDetails(tool.call),
				DurationMS: &tool.call.DurationMS,
			})
			if err != nil {
				return err
			}
			if err := appendEvent(toolEvent); err != nil {
				return err
			}
		}

		budgetEvent, err := newLoopAuditEvent(current, eventIndex+1, previousHash, loopAuditEventInput{
			Type: EventBudgetUpdated, Stage: string(request.Stage), Status: EventSucceeded,
			Code: "BUDGET_UPDATED", Details: prepared.budgetDetails,
		})
		if err != nil {
			return err
		}
		if err := appendEvent(budgetEvent); err != nil {
			return err
		}
		if request.Failure != nil {
			failureEvent, err := newLoopAuditEvent(current, eventIndex+1, previousHash, loopAuditEventInput{
				Type: EventError, Stage: string(request.Stage), Status: request.Failure.Status,
				Code: request.Failure.Code, Details: prepared.failureDetails,
			})
			if err != nil {
				return err
			}
			if err := appendEvent(failureEvent); err != nil {
				return err
			}
		}

		if prepared.completion != nil {
			index, err := nextArtifactIndexTx(ctx, tx, current)
			if err != nil {
				return err
			}
			prepared.completion.Index, prepared.completion.ID = index, askdata.ID(uuid.NewString())
			if err := insertArtifactTx(ctx, tx, *prepared.completion); err != nil {
				return err
			}
		}
		persisted, err := updateRunTx(ctx, tx, current, prepared.next)
		if err != nil {
			return err
		}
		transitionEvent, err := buildTransitionEvent(persisted, current.State, eventIndex+1, previousHash,
			TransitionEventInput{Details: prepared.transitionDetail})
		if err != nil {
			return err
		}
		if prepared.completion != nil {
			transitionEvent.ArtifactHash = prepared.completion.Hash
			transitionEvent.Hash, err = computeEventHash(transitionEvent)
			if err != nil {
				return err
			}
		}
		if err := insertEventTx(ctx, tx, transitionEvent); err != nil {
			return err
		}
		persistedSnapshot, err := loadReplaySnapshotTx(ctx, tx, persisted)
		if err != nil {
			return err
		}
		result = LoopCheckpointResult{
			Run: persisted, Event: transitionEvent, Snapshot: persistedSnapshot,
		}
		return nil
	})
	if err != nil {
		return LoopCheckpointResult{}, mapPersistenceError(err)
	}
	return result, nil
}

func prepareLoopCheckpoint(
	current Run,
	request LoopCheckpointRequest,
	checkpointHash askdata.ContentHash,
) (preparedLoopCheckpoint, error) {
	if current.Validate() != nil || current.Terminal() || request.Stage == "" ||
		!stageAllowedForRunState(request.Stage, current.State) || request.TargetState == "" {
		return preparedLoopCheckpoint{}, fmt.Errorf("%w: loop checkpoint stage is invalid", ErrInvalidRun)
	}
	if request.Failure != nil {
		if err := request.Failure.Validate(); err != nil {
			return preparedLoopCheckpoint{}, err
		}
	}
	if request.Result.Usage.validate(current.Limits) != nil ||
		!request.Result.Usage.monotonicFrom(current.Usage) {
		return preparedLoopCheckpoint{}, fmt.Errorf("%w: checkpoint budget is invalid", ErrInvalidRun)
	}
	rounds, tools, err := prepareLoopAuditFacts(current, request)
	if err != nil {
		return preparedLoopCheckpoint{}, err
	}
	if err := validateDecisionTarget(request.Result.Decision, request.Failure, request.TargetState); err != nil {
		return preparedLoopCheckpoint{}, err
	}

	transition := Transition{
		ExpectedVersion: current.RecordVersion, TargetState: request.TargetState,
		Usage: request.Result.Usage, Hashes: request.Hashes,
	}
	var completion *Artifact
	if request.Completion != nil {
		artifact, err := prepareCompletionArtifact(TransitionRequest{
			RunID: current.ID, TargetState: request.TargetState,
		}, *request.Completion)
		if err != nil {
			return preparedLoopCheckpoint{}, err
		}
		artifact.TenantID, artifact.DomainID, artifact.ActorID = current.TenantID, current.DomainID, current.ActorID
		artifact.Release, artifact.PolicyScopeHash = current.Release, current.PolicyScopeHash
		artifact.RunVersion = current.RecordVersion
		completion = &artifact
		transition.Completion = &CompletionRef{
			Code: request.Completion.Code, ArtifactType: artifact.Type, ArtifactHash: artifact.Hash,
		}
	}
	next, err := Apply(current, transition)
	if err != nil {
		return preparedLoopCheckpoint{}, err
	}
	budget := mustCanonicalAudit(map[string]any{
		"before": current.Usage, "after": request.Result.Usage, "limits": current.Limits,
	})
	failure := json.RawMessage(`{}`)
	if request.Failure != nil {
		failure = mustCanonicalAudit(map[string]any{
			"code": request.Failure.Code, "budgetExhausted": request.Result.Usage.Exhausted,
		})
	}
	transitionDetail := mustCanonicalAudit(map[string]any{
		"checkpointId": request.CheckpointID, "checkpointHash": checkpointHash,
	})
	return preparedLoopCheckpoint{
		next: next, rounds: rounds, tools: tools,
		completion: completion, budgetDetails: budget, failureDetails: failure,
		transitionDetail: transitionDetail,
	}, nil
}

func prepareLoopAuditFacts(
	current Run,
	request LoopCheckpointRequest,
) ([]preparedCognitionAudit, []preparedToolAudit, error) {
	llmDelta := request.Result.Usage.LLMCallsUsed - current.Usage.LLMCallsUsed
	stepDelta := request.Result.Usage.StepCount - current.Usage.StepCount
	toolDelta := request.Result.Usage.ToolCallsUsed - current.Usage.ToolCallsUsed
	formalDelta := request.Result.Usage.FormalQueriesUsed - current.Usage.FormalQueriesUsed
	validationDelta := request.Result.Usage.ValidationQueriesUsed - current.Usage.ValidationQueriesUsed
	if llmDelta < len(request.Result.CognitionRounds) || llmDelta > len(request.Result.CognitionRounds)+1 ||
		(llmDelta != len(request.Result.CognitionRounds) && request.Failure == nil) ||
		stepDelta != llmDelta+len(request.Result.ToolExecutions) {
		return nil, nil, fmt.Errorf("%w: loop usage does not match audited rounds", ErrInvalidRun)
	}

	rounds := make([]preparedCognitionAudit, 0, len(request.Result.CognitionRounds))
	tools := make([]preparedToolAudit, 0, len(request.Result.ToolExecutions))
	seenActions := []askdata.ContentHash{}
	toolIndex, chargedTools, chargedFormal, chargedValidation := 0, 0, 0, 0
	for _, execution := range request.Result.CognitionRounds {
		if execution.DurationMS < 0 || execution.DurationMS > 600_000 ||
			validateCognitionRound(execution.Round, request.Stage, seenActions) != nil {
			return nil, nil, fmt.Errorf("%w: cognition audit round is invalid", ErrInvalidRun)
		}
		seenActions = append(seenActions, execution.Round.ActionHash)
		if !containsContentHash(request.Result.SeenActionHashes, execution.Round.ActionHash) {
			return nil, nil, fmt.Errorf("%w: audited action is missing from replay guards", ErrInvalidRun)
		}
		evidence, err := actionEvidenceIDs(execution.Round.Action)
		if err != nil {
			return nil, nil, err
		}
		rounds = append(rounds, preparedCognitionAudit{execution: execution, evidenceIDs: evidence})
		if execution.Round.Action.Action != cognition.ActionCallTool {
			continue
		}
		if toolIndex >= len(request.Result.ToolExecutions) {
			return nil, nil, fmt.Errorf("%w: tool action is missing its execution", ErrInvalidRun)
		}
		prepared, err := prepareToolAudit(current, execution.Round.Action, request.Result.ToolExecutions[toolIndex])
		if err != nil {
			return nil, nil, err
		}
		if !containsToolCallID(request.Result.SeenToolCallIDs, execution.Round.Action.ToolCall.CallID) {
			return nil, nil, fmt.Errorf("%w: audited tool call is missing from replay guards", ErrInvalidRun)
		}
		chargedTools += prepared.charge.ToolCalls
		chargedFormal += prepared.charge.FormalQueries
		chargedValidation += prepared.charge.ValidationQueries
		tools = append(tools, prepared)
		toolIndex++
	}
	if toolIndex != len(request.Result.ToolExecutions) || chargedTools != toolDelta ||
		chargedFormal != formalDelta || chargedValidation != validationDelta {
		return nil, nil, fmt.Errorf("%w: tool charges do not match checkpoint usage", ErrInvalidRun)
	}
	if request.Result.Decision.ActionHash != "" {
		if len(request.Result.CognitionRounds) == 0 {
			return nil, nil, fmt.Errorf("%w: final decision is missing its cognition round", ErrInvalidRun)
		}
		last := request.Result.CognitionRounds[len(request.Result.CognitionRounds)-1].Round
		if validateCognitionRound(request.Result.Decision, request.Stage, nil) != nil ||
			last.Action.Action == cognition.ActionCallTool || last.ActionHash != request.Result.Decision.ActionHash ||
			!sameRoundAuditSummary(last, request.Result.Decision) {
			return nil, nil, fmt.Errorf("%w: final decision does not match the last round", ErrInvalidRun)
		}
	} else if request.Failure == nil {
		return nil, nil, fmt.Errorf("%w: successful checkpoint requires a final decision", ErrInvalidRun)
	}
	return rounds, tools, nil
}

func prepareToolAudit(current Run, action cognition.Action, execution toolhost.Execution) (preparedToolAudit, error) {
	if action.ToolCall == nil || execution.Validate() != nil ||
		execution.Response.CallID != action.ToolCall.CallID || execution.Response.Tool != action.ToolCall.Tool {
		return preparedToolAudit{}, fmt.Errorf("%w: tool audit binding is invalid", ErrInvalidRun)
	}
	requestHash, _, err := semanticregistry.CanonicalContentHash(struct {
		Tool      toolhost.ToolName      `json:"tool"`
		Arguments toolhost.ToolArguments `json:"arguments"`
	}{Tool: action.ToolCall.Tool, Arguments: action.ToolCall.Arguments})
	if err != nil {
		return preparedToolAudit{}, fmt.Errorf("%w: tool request hash failed", ErrInvalidRun)
	}
	resultHash, _, err := semanticregistry.CanonicalContentHash(execution.Response)
	if err != nil {
		return preparedToolAudit{}, fmt.Errorf("%w: tool result hash failed", ErrInvalidRun)
	}
	evidence, err := evidenceRefIDs(execution.Response.EvidenceRefs)
	if err != nil {
		return preparedToolAudit{}, err
	}
	status, errorCode := persistedToolStatus(execution.Response)
	budget := mustCanonicalAudit(map[string]any{
		"definitionHash": execution.DefinitionHash, "charge": execution.Charge,
		"timedOut": execution.TimedOut,
	})
	call := ToolCall{
		TenantID: current.TenantID, DomainID: current.DomainID, ActorID: current.ActorID,
		RunID: current.ID, Release: current.Release, PolicyScopeHash: current.PolicyScopeHash,
		RunVersion: current.RecordVersion, CallID: action.ToolCall.CallID, Tool: action.ToolCall.Tool,
		State: current.State, Status: string(status), RequestHash: requestHash, ResultHash: resultHash,
		EvidenceIDs: evidence, Budget: budget, DurationMS: execution.DurationMS, ErrorCode: errorCode,
	}
	callHash, err := computeToolCallHash(call)
	if err != nil {
		return preparedToolAudit{}, err
	}
	call.CallHash = callHash
	payload := map[string]any{
		"schemaVersion": toolReplaySchemaVersion, "toolCallId": call.CallID, "tool": call.Tool,
		"definitionHash": execution.DefinitionHash, "charge": execution.Charge,
		"durationMs": execution.DurationMS, "timedOut": execution.TimedOut,
		"status": execution.Response.Status, "outcomeHash": resultHash,
		"typedOutput": json.RawMessage(`{}`), "evidenceRefs": execution.Response.EvidenceRefs,
		"errorCode": errorCode, "retryable": false, "madeProgress": execution.Response.MadeProgress,
	}
	if execution.Response.Status == toolhost.ResponseSuccess {
		payload["typedOutput"] = execution.Response.Result
	} else if execution.Response.Error != nil {
		payload["retryable"] = execution.Response.Error.Retryable
	}
	artifactPayload := mustCanonicalAudit(payload)
	artifact := Artifact{
		TenantID: current.TenantID, DomainID: current.DomainID, ActorID: current.ActorID,
		RunID: current.ID, Release: current.Release, PolicyScopeHash: current.PolicyScopeHash,
		RunVersion: current.RecordVersion, Type: ArtifactEvidence, SchemaVersion: toolReplaySchemaVersion,
		EvidenceIDs: evidence, Payload: artifactPayload,
	}
	artifactHash, err := computeArtifactHash(artifact)
	if err != nil {
		return preparedToolAudit{}, err
	}
	artifact.Hash = artifactHash
	return preparedToolAudit{call: call, artifact: artifact, charge: execution.Charge}, nil
}

type loopAuditEventInput struct {
	Type         EventType
	Stage        string
	Status       EventStatus
	Code         string
	ToolCallID   askdata.ID
	AIRequestID  askdata.ID
	ActionHash   askdata.ContentHash
	ArtifactHash askdata.ContentHash
	EvidenceIDs  []askdata.ID
	Details      json.RawMessage
	DurationMS   *int64
}

func newLoopAuditEvent(
	run Run,
	index int,
	previousHash askdata.ContentHash,
	input loopAuditEventInput,
) (Event, error) {
	details, err := canonicalAuditObject(input.Details, maxEventDetailsBytes)
	if err != nil {
		return Event{}, err
	}
	evidence, err := normalizedEvidenceIDs(input.EvidenceIDs)
	if err != nil {
		return Event{}, err
	}
	event := Event{
		ID: askdata.ID(uuid.NewString()), TenantID: run.TenantID, DomainID: run.DomainID,
		ActorID: run.ActorID, RunID: run.ID, Release: run.Release,
		PolicyScopeHash: run.PolicyScopeHash, Index: index, RunVersion: run.RecordVersion,
		State: run.State, Type: input.Type, Stage: input.Stage, Status: input.Status,
		Code: input.Code, ToolCallID: input.ToolCallID, AIRequestID: input.AIRequestID,
		ActionHash: input.ActionHash, ArtifactHash: input.ArtifactHash,
		EvidenceIDs: evidence, Details: details, PreviousEventHash: previousHash,
		DurationMS: input.DurationMS,
	}
	hash, err := computeEventHash(event)
	if err != nil {
		return Event{}, err
	}
	event.Hash = hash
	return event, event.Validate()
}

func insertToolCallTx(ctx context.Context, tx pgx.Tx, call ToolCall) error {
	_, err := tx.Exec(ctx, `INSERT INTO askdata.tool_calls(
		id,tenant_id,domain_id,actor_id,question_run_id,release_id,
		release_content_hash,policy_scope_hash,run_version,tool_call_id,tool_name,
		state,status,request_hash,result_hash,call_hash,evidence_ids,budget_json,
		duration_ms,error_code
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)`,
		call.ID, call.TenantID, call.DomainID, call.ActorID, call.RunID, call.Release.ReleaseID,
		call.Release.ContentHash, call.PolicyScopeHash, call.RunVersion, call.CallID, call.Tool,
		call.State, call.Status, call.RequestHash, call.ResultHash, call.CallHash,
		idsToStrings(call.EvidenceIDs), []byte(call.Budget), call.DurationMS, call.ErrorCode)
	return err
}

func loadReplaySnapshotTx(ctx context.Context, tx pgx.Tx, run Run) (ReplaySnapshot, error) {
	events, err := loadEventsTx(ctx, tx, run)
	if err != nil {
		return ReplaySnapshot{}, err
	}
	artifacts, err := loadArtifactsTx(ctx, tx, run)
	if err != nil {
		return ReplaySnapshot{}, err
	}
	tools, err := loadToolCallsTx(ctx, tx, run)
	if err != nil {
		return ReplaySnapshot{}, err
	}
	snapshot := ReplaySnapshot{Run: run, Events: events, Artifacts: artifacts, ToolCalls: tools}
	return snapshot, snapshot.Validate()
}

func exactCheckpointReplay(
	snapshot ReplaySnapshot,
	request LoopCheckpointRequest,
	hash askdata.ContentHash,
) (bool, bool) {
	conflict := false
	for _, event := range snapshot.Events {
		id, storedHash, ok := checkpointIdentity(event.Details)
		if !ok || id != request.CheckpointID {
			continue
		}
		if storedHash != hash {
			return false, true
		}
		return snapshot.Run.RecordVersion == request.ExpectedVersion+1 &&
			event.Type == EventStateTransition && event.RunVersion == snapshot.Run.RecordVersion, false
	}
	return false, conflict
}

func checkpointIDExists(events []Event, checkpointID askdata.ID) bool {
	for _, event := range events {
		id, _, ok := checkpointIdentity(event.Details)
		if ok && id == checkpointID {
			return true
		}
	}
	return false
}

func checkpointIdentity(details json.RawMessage) (askdata.ID, askdata.ContentHash, bool) {
	var value struct {
		CheckpointID   askdata.ID          `json:"checkpointId"`
		CheckpointHash askdata.ContentHash `json:"checkpointHash"`
	}
	if askdata.DecodeStrictJSON(details, &value) != nil || value.CheckpointID.Validate() != nil ||
		value.CheckpointHash.Validate() != nil {
		return "", "", false
	}
	return value.CheckpointID, value.CheckpointHash, true
}

func computeLoopCheckpointHash(request LoopCheckpointRequest) (askdata.ContentHash, error) {
	actions := make([]map[string]any, 0, len(request.Result.CognitionRounds))
	for _, round := range request.Result.CognitionRounds {
		actions = append(actions, map[string]any{
			"actionHash": round.Round.ActionHash, "aiRequestId": round.Round.AIRequestID,
			"providerModel": round.Round.ProviderModel, "attempts": round.Round.Attempts,
			"usage": round.Round.Usage, "costMicros": round.Round.CostMicros,
			"redactionCount": round.Round.RedactionCount, "durationMs": round.DurationMS,
		})
	}
	tools := make([]map[string]any, 0, len(request.Result.ToolExecutions))
	for _, execution := range request.Result.ToolExecutions {
		responseHash, _, err := semanticregistry.CanonicalContentHash(execution.Response)
		if err != nil {
			return "", fmt.Errorf("%w: checkpoint tool response hash failed", ErrInvalidRun)
		}
		tools = append(tools, map[string]any{
			"definitionHash": execution.DefinitionHash, "responseHash": responseHash,
			"charge": execution.Charge, "durationMs": execution.DurationMS, "timedOut": execution.TimedOut,
		})
	}
	failureCode, failureStatus := "", EventStatus("")
	if request.Failure != nil {
		failureCode, failureStatus = request.Failure.Code, request.Failure.Status
	}
	completionHash := askdata.ContentHash("")
	if request.Completion != nil {
		artifact, err := prepareCompletionArtifact(TransitionRequest{
			RunID: request.RunID, TargetState: request.TargetState,
		}, *request.Completion)
		if err != nil {
			return "", err
		}
		completionHash = artifact.Hash
	}
	document := map[string]any{
		"schemaVersion": loopCheckpointHashVersion, "checkpointId": request.CheckpointID,
		"runId": request.RunID, "expectedVersion": request.ExpectedVersion, "stage": request.Stage,
		"targetState": request.TargetState, "usage": request.Result.Usage,
		"actions": actions, "tools": tools, "failureCode": failureCode,
		"failureStatus": failureStatus, "completionHash": completionHash,
		"hashes": hashUpdateDocument(request.Hashes),
	}
	hash, _, err := semanticregistry.CanonicalContentHash(document)
	if err != nil {
		return "", fmt.Errorf("%w: checkpoint hash failed", ErrInvalidRun)
	}
	return hash, nil
}

// BindReplayGuards constructs a resumed loop request from one fully validated
// snapshot. Completed action hashes and tool call IDs are carried into the
// cognition and outer tool gates, so a resumed process cannot execute them a
// second time.
func BindReplayGuards(snapshot ReplaySnapshot, request LoopRequest) (LoopRequest, error) {
	if snapshot.Validate() != nil || request.Run.ID != snapshot.Run.ID ||
		request.Run.RecordVersion != snapshot.Run.RecordVersion ||
		request.Run.State != snapshot.Run.State || request.Run.Release != snapshot.Run.Release ||
		request.Run.PolicyScopeHash != snapshot.Run.PolicyScopeHash ||
		request.Authorization.Scope.PolicyHash != snapshot.Run.PolicyScopeHash ||
		request.Authorization.Scope.Release != snapshot.Run.Release {
		return LoopRequest{}, fmt.Errorf("%w: replay guards do not match the persisted run", ErrReplayCorrupt)
	}
	request.SeenActionHashes = snapshot.SeenActionHashes()
	request.SeenToolCallIDs = snapshot.SeenToolCallIDs()
	if len(request.SeenActionHashes) > 16 || len(request.SeenToolCallIDs) > 16 {
		return LoopRequest{}, fmt.Errorf("%w: replay guards exceed loop bounds", ErrReplayCorrupt)
	}
	return request, nil
}

func validateToolReplayBindings(
	artifacts []Artifact,
	calls map[askdata.ID]ToolCall,
) error {
	bound := map[askdata.ID]bool{}
	for _, artifact := range artifacts {
		if artifact.SchemaVersion != toolReplaySchemaVersion {
			continue
		}
		envelope, err := decodeToolReplayArtifact(artifact)
		if err != nil {
			return err
		}
		call, exists := calls[envelope.ToolCallID]
		if !exists || bound[envelope.ToolCallID] || call.Tool != envelope.Tool ||
			call.ResultHash != envelope.OutcomeHash || call.DurationMS != envelope.DurationMS ||
			call.ErrorCode != envelope.ErrorCode || !equalIDs(call.EvidenceIDs, artifact.EvidenceIDs) {
			return fmt.Errorf("%w: tool replay artifact does not bind its outcome", ErrReplayCorrupt)
		}
		var budget struct {
			DefinitionHash askdata.ContentHash   `json:"definitionHash"`
			Charge         toolhost.BudgetCharge `json:"charge"`
			TimedOut       bool                  `json:"timedOut"`
		}
		if askdata.DecodeStrictJSON(call.Budget, &budget) != nil ||
			budget.DefinitionHash != envelope.DefinitionHash || budget.Charge != envelope.Charge ||
			budget.TimedOut != envelope.TimedOut {
			return fmt.Errorf("%w: tool replay budget binding is invalid", ErrReplayCorrupt)
		}
		status, _ := replayToolEventStatus(envelope)
		if call.Status != string(status) {
			return fmt.Errorf("%w: tool replay status is invalid", ErrReplayCorrupt)
		}
		bound[envelope.ToolCallID] = true
	}
	if len(bound) != 0 && len(bound) != len(calls) {
		return fmt.Errorf("%w: tool replay artifacts are incomplete", ErrReplayCorrupt)
	}
	return nil
}

func decodeToolReplayArtifact(artifact Artifact) (toolReplayEnvelope, error) {
	var envelope toolReplayEnvelope
	if artifact.Type != ArtifactEvidence || artifact.SchemaVersion != toolReplaySchemaVersion ||
		askdata.DecodeStrictJSON(artifact.Payload, &envelope) != nil ||
		envelope.SchemaVersion != toolReplaySchemaVersion || envelope.ToolCallID.Validate() != nil ||
		!toolhost.IsKnownTool(envelope.Tool) || envelope.DefinitionHash.Validate() != nil ||
		envelope.OutcomeHash.Validate() != nil || envelope.DurationMS < 0 || envelope.DurationMS > 600_000 {
		return toolReplayEnvelope{}, fmt.Errorf("%w: tool replay artifact is invalid", ErrReplayCorrupt)
	}
	evidenceIDs, err := evidenceRefIDs(envelope.EvidenceRefs)
	if err != nil || !equalIDs(evidenceIDs, artifact.EvidenceIDs) {
		return toolReplayEnvelope{}, fmt.Errorf("%w: tool replay evidence is invalid", ErrReplayCorrupt)
	}
	zeroCharge := envelope.Charge == (toolhost.BudgetCharge{})
	switch envelope.Status {
	case toolhost.ResponseSuccess:
		if zeroCharge || envelope.Charge.Validate() != nil || envelope.ErrorCode != "" || envelope.Retryable ||
			len(envelope.EvidenceRefs) == 0 || len(envelope.TypedOutput) == 0 {
			return toolReplayEnvelope{}, fmt.Errorf("%w: successful tool replay is invalid", ErrReplayCorrupt)
		}
		response := toolhost.Response{
			SchemaVersion: toolhost.SchemaVersion, CallID: envelope.ToolCallID, Tool: envelope.Tool,
			Status: envelope.Status, Result: envelope.TypedOutput, EvidenceRefs: envelope.EvidenceRefs,
			ResultHash: askdata.HashBytes(envelope.TypedOutput), MadeProgress: envelope.MadeProgress,
		}
		outcomeHash, _, err := semanticregistry.CanonicalContentHash(response)
		if err != nil || response.Validate() != nil || outcomeHash != envelope.OutcomeHash {
			return toolReplayEnvelope{}, fmt.Errorf("%w: successful tool replay hash is invalid", ErrReplayCorrupt)
		}
	case toolhost.ResponseRejected, toolhost.ResponseFailed:
		if !completionCodePattern.MatchString(envelope.ErrorCode) || envelope.MadeProgress ||
			len(envelope.EvidenceRefs) != 0 || string(envelope.TypedOutput) != "{}" ||
			(envelope.Status == toolhost.ResponseRejected && !zeroCharge) ||
			(envelope.Status == toolhost.ResponseFailed && (zeroCharge || envelope.Charge.Validate() != nil)) ||
			envelope.TimedOut != (envelope.ErrorCode == "TOOL_TIMEOUT") {
			return toolReplayEnvelope{}, fmt.Errorf("%w: failed tool replay is invalid", ErrReplayCorrupt)
		}
	default:
		return toolReplayEnvelope{}, fmt.Errorf("%w: tool replay status is invalid", ErrReplayCorrupt)
	}
	return envelope, nil
}

// ReplayToolExecution reconstructs the typed sanitized outcome persisted for a
// completed call. It is intended for resume diagnostics/evidence recovery; the
// replay guards still prevent dispatching that call again through the Loop.
func ReplayToolExecution(snapshot ReplaySnapshot, callID askdata.ID) (toolhost.Execution, bool, error) {
	if snapshot.Validate() != nil || callID.Validate() != nil {
		return toolhost.Execution{}, false, ErrReplayCorrupt
	}
	for _, artifact := range snapshot.Artifacts {
		if artifact.SchemaVersion != toolReplaySchemaVersion {
			continue
		}
		envelope, err := decodeToolReplayArtifact(artifact)
		if err != nil {
			return toolhost.Execution{}, false, err
		}
		if envelope.ToolCallID != callID {
			continue
		}
		response := toolhost.Response{
			SchemaVersion: toolhost.SchemaVersion, CallID: envelope.ToolCallID, Tool: envelope.Tool,
			Status: envelope.Status, MadeProgress: envelope.MadeProgress,
		}
		if envelope.Status == toolhost.ResponseSuccess {
			response.Result = append(json.RawMessage(nil), envelope.TypedOutput...)
			response.EvidenceRefs = append([]askdata.EvidenceRef(nil), envelope.EvidenceRefs...)
			response.ResultHash = askdata.HashBytes(response.Result)
		} else {
			response.Error = &toolhost.ToolError{
				Code: envelope.ErrorCode, Message: "工具执行已结束，详见稳定错误码。", Retryable: envelope.Retryable,
			}
		}
		execution := toolhost.Execution{
			DefinitionHash: envelope.DefinitionHash, Charge: envelope.Charge,
			DurationMS: envelope.DurationMS, TimedOut: envelope.TimedOut, Response: response,
		}
		if execution.Validate() != nil {
			return toolhost.Execution{}, false, ErrReplayCorrupt
		}
		return execution, true, nil
	}
	return toolhost.Execution{}, false, nil
}

func replayToolEventStatus(envelope toolReplayEnvelope) (EventStatus, string) {
	switch envelope.Status {
	case toolhost.ResponseSuccess:
		return EventSucceeded, ""
	case toolhost.ResponseRejected:
		return EventBlocked, envelope.ErrorCode
	default:
		if envelope.ErrorCode == "TOOL_CANCELED" {
			return EventCanceled, envelope.ErrorCode
		}
		return EventFailed, envelope.ErrorCode
	}
}

func hashUpdateDocument(updates HashUpdates) map[string]string {
	result := map[string]string{}
	for name, value := range map[string]*askdata.ContentHash{
		"understanding": updates.Understanding, "bindingBundle": updates.BindingBundle,
		"graphPlan": updates.GraphPlan, "semanticIr": updates.SemanticIR,
		"queryPlan": updates.QueryPlan, "result": updates.Result,
	} {
		if value != nil {
			result[name] = string(*value)
		}
	}
	return result
}

func actionEvidenceIDs(action cognition.Action) ([]askdata.ID, error) {
	values := append([]askdata.EvidenceRef(nil), action.EvidenceRefs...)
	if action.BindingProposal != nil {
		values = append(values, action.BindingProposal.Confidence.Evidence...)
	}
	if action.PlanProposal != nil {
		values = append(values, action.PlanProposal.Confidence.Evidence...)
	}
	if action.AnomalyAnalysis != nil {
		values = append(values, action.AnomalyAnalysis.EvidenceRefs...)
	}
	if action.Verification != nil {
		for _, check := range action.Verification.Checks {
			values = append(values, check.EvidenceRefs...)
		}
	}
	if action.FinalDecision != nil {
		values = append(values, action.FinalDecision.EvidenceRefs...)
	}
	if action.Clarification != nil {
		for _, option := range action.Clarification.Options {
			values = append(values, option.EvidenceRefs...)
		}
	}
	if action.Block != nil {
		values = append(values, action.Block.EvidenceRefs...)
	}
	if action.ToolCall != nil {
		for _, option := range action.ToolCall.Arguments.ClarificationOptions {
			values = append(values, option.EvidenceRefs...)
		}
	}
	return evidenceRefIDs(values)
}

func validateDecisionTarget(
	decision cognition.RoundResult,
	failure *LoopFailure,
	target State,
) error {
	if decision.ActionHash == "" {
		if failure == nil {
			return fmt.Errorf("%w: checkpoint has neither decision nor failure", ErrInvalidRun)
		}
		return nil
	}
	if failure != nil {
		return fmt.Errorf("%w: checkpoint cannot contain both a decision and a failure", ErrInvalidRun)
	}
	switch decision.Action.Action {
	case cognition.ActionClarify:
		if target != StateClarificationRequired {
			return fmt.Errorf("%w: clarification decision requires clarification state", ErrInvalidRun)
		}
	case cognition.ActionBlock:
		if target != StateBlocked {
			return fmt.Errorf("%w: block decision requires blocked state", ErrInvalidRun)
		}
	case cognition.ActionFinalize:
		if target != StateAnswered {
			return fmt.Errorf("%w: final decision requires answered state", ErrInvalidRun)
		}
	default:
		if isTerminalState(target) {
			return fmt.Errorf("%w: nonterminal decision cannot complete a run", ErrInvalidRun)
		}
	}
	return nil
}

func sameRoundAuditSummary(left, right cognition.RoundResult) bool {
	return left.ActionHash == right.ActionHash && left.AIRequestID == right.AIRequestID &&
		left.ProviderModel == right.ProviderModel && left.Attempts == right.Attempts &&
		left.Usage == right.Usage && left.CostMicros == right.CostMicros &&
		left.RedactionCount == right.RedactionCount
}

func containsContentHash(values []askdata.ContentHash, target askdata.ContentHash) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func checkpointMakesReplayProgress(snapshot ReplaySnapshot, result LoopResult) bool {
	seenActions := snapshot.SeenActionHashes()
	for _, round := range result.CognitionRounds {
		if containsContentHash(seenActions, round.Round.ActionHash) {
			return false
		}
	}
	seenCalls := snapshot.SeenToolCallIDs()
	for _, execution := range result.ToolExecutions {
		if containsToolCallID(seenCalls, execution.Response.CallID) {
			return false
		}
	}
	return true
}

func evidenceRefIDs(values []askdata.EvidenceRef) ([]askdata.ID, error) {
	ids := make([]askdata.ID, 0, len(values))
	seen := map[askdata.ID]bool{}
	for _, evidence := range values {
		if evidence.Validate() != nil {
			return nil, fmt.Errorf("%w: audited evidence is invalid", ErrInvalidRun)
		}
		if !seen[evidence.EvidenceID] {
			seen[evidence.EvidenceID] = true
			ids = append(ids, evidence.EvidenceID)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids, nil
}

func cognitionAuditDetails(execution CognitionExecution) json.RawMessage {
	return mustCanonicalAudit(map[string]any{
		"providerModel": execution.Round.ProviderModel, "attempts": execution.Round.Attempts,
		"usage": execution.Round.Usage, "costMicros": execution.Round.CostMicros,
		"redactionCount": execution.Round.RedactionCount,
	})
}

func toolAuditDetails(call ToolCall) json.RawMessage {
	return mustCanonicalAudit(map[string]any{
		"tool": call.Tool, "requestHash": call.RequestHash, "resultHash": call.ResultHash,
		"callHash": call.CallHash, "budget": call.Budget, "errorCode": call.ErrorCode,
	})
}

func mustCanonicalAudit(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	canonical, err := canonicalAuditObject(raw, maxArtifactPayloadBytes)
	if err != nil {
		panic(err)
	}
	return canonical
}

func persistedToolStatus(response toolhost.Response) (EventStatus, string) {
	switch response.Status {
	case toolhost.ResponseSuccess:
		return EventSucceeded, ""
	case toolhost.ResponseRejected:
		return EventBlocked, response.Error.Code
	default:
		if response.Error != nil && response.Error.Code == "TOOL_CANCELED" {
			return EventCanceled, response.Error.Code
		}
		return EventFailed, response.Error.Code
	}
}

func toolEventCode(call ToolCall) string {
	if call.ErrorCode != "" {
		return call.ErrorCode
	}
	return "TOOL_SUCCEEDED"
}

// ClassifyLoopFailure maps execution failures to stable audit vocabulary.
func ClassifyLoopFailure(err error) *LoopFailure {
	if err == nil {
		return nil
	}
	result := &LoopFailure{Code: "LOOP_FAILED", Status: EventFailed}
	switch {
	case errors.Is(err, context.Canceled):
		result.Code, result.Status = "LOOP_CANCELED", EventCanceled
	case errors.Is(err, ErrLoopBudgetExhausted):
		result.Code, result.Status = "BUDGET_EXHAUSTED", EventBlocked
	case errors.Is(err, ErrLoopTimeout):
		result.Code, result.Status = "LOOP_TIMEOUT", EventBlocked
	case errors.Is(err, ErrLoopNoProgress):
		result.Code = "LOOP_NO_PROGRESS"
	case errors.Is(err, ErrLoopEvidenceRejected):
		result.Code = "EVIDENCE_REJECTED"
	case errors.Is(err, ErrLoopToolUnavailable):
		result.Code = "TOOL_UNAVAILABLE"
	case errors.Is(err, ErrLoopToolBlocked):
		result.Code, result.Status = "TOOL_BLOCKED", EventBlocked
	case errors.Is(err, ErrLoopToolFailed):
		result.Code = "TOOL_FAILED"
	case errors.Is(err, ErrLoopCognitionFailed):
		result.Code = "COGNITION_FAILED"
	case errors.Is(err, ErrInvalidLoop):
		result.Code = "LOOP_CONTRACT_REJECTED"
	}
	return result
}
