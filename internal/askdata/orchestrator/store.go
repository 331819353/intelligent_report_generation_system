package orchestrator

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/registry"
	"intelligent-report-generation-system/internal/askdata/toolhost"
	"intelligent-report-generation-system/internal/platform/database"
)

const (
	eventHashSchemaVersion    = "question-run-event-v1"
	artifactHashSchemaVersion = "question-artifact-v1"
	toolCallHashSchemaVersion = "question-tool-call-v1"
	maxEventDetailsBytes      = 60 << 10
	maxArtifactPayloadBytes   = 240 << 10
)

var (
	stagePattern         = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
	schemaVersionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@/-]{0,127}$`)
	auditKeyNormalizer   = regexp.MustCompile(`[^a-z0-9]`)
)

type CreateRunRequest struct {
	Scope              askdata.PolicyScope
	DomainID           askdata.ID
	ConversationID     askdata.ID
	ParentRunID        askdata.ID
	IdempotencyKeyHash askdata.ContentHash
	QuestionHash       askdata.ContentHash
	Limits             BudgetLimits
}

type CreateResult struct {
	Run      Run
	Replayed bool
}

type CompletionArtifactInput struct {
	Code          string
	Type          ArtifactType
	SchemaVersion string
	EvidenceIDs   []askdata.ID
	Payload       json.RawMessage
	ExpectedHash  askdata.ContentHash
}

type TransitionEventInput struct {
	Stage       string
	Status      EventStatus
	Code        string
	AIRequestID askdata.ID
	ActionHash  askdata.ContentHash
	EvidenceIDs []askdata.ID
	Details     json.RawMessage
	DurationMS  *int64
}

type TransitionRequest struct {
	Scope           askdata.PolicyScope
	DomainID        askdata.ID
	RunID           askdata.ID
	ExpectedVersion int64
	TargetState     State
	Usage           BudgetUsage
	Hashes          HashUpdates
	Completion      *CompletionArtifactInput
	Event           TransitionEventInput
}

type TransitionResult struct {
	Run   Run
	Event Event
}

type ResumeRequest struct {
	Scope    askdata.PolicyScope
	DomainID askdata.ID
	RunID    askdata.ID
}

type Event struct {
	ID                askdata.ID
	TenantID          askdata.ID
	DomainID          askdata.ID
	ActorID           askdata.ID
	RunID             askdata.ID
	Release           askdata.ReleaseRef
	PolicyScopeHash   askdata.ContentHash
	Index             int
	RunVersion        int64
	State             State
	Type              EventType
	Stage             string
	Status            EventStatus
	Code              string
	ToolCallID        askdata.ID
	AIRequestID       askdata.ID
	ActionHash        askdata.ContentHash
	ArtifactHash      askdata.ContentHash
	EvidenceIDs       []askdata.ID
	Details           json.RawMessage
	PreviousEventHash askdata.ContentHash
	Hash              askdata.ContentHash
	DurationMS        *int64
	CreatedAt         time.Time
}

type Artifact struct {
	ID              askdata.ID
	TenantID        askdata.ID
	DomainID        askdata.ID
	ActorID         askdata.ID
	RunID           askdata.ID
	Release         askdata.ReleaseRef
	PolicyScopeHash askdata.ContentHash
	Index           int
	RunVersion      int64
	Type            ArtifactType
	SchemaVersion   string
	Hash            askdata.ContentHash
	EvidenceIDs     []askdata.ID
	Payload         json.RawMessage
	CreatedAt       time.Time
}

type ToolCall struct {
	ID              askdata.ID
	TenantID        askdata.ID
	DomainID        askdata.ID
	ActorID         askdata.ID
	RunID           askdata.ID
	Release         askdata.ReleaseRef
	PolicyScopeHash askdata.ContentHash
	RunVersion      int64
	CallID          askdata.ID
	Tool            toolhost.ToolName
	State           State
	Status          string
	RequestHash     askdata.ContentHash
	ResultHash      askdata.ContentHash
	CallHash        askdata.ContentHash
	EvidenceIDs     []askdata.ID
	Budget          json.RawMessage
	DurationMS      int64
	ErrorCode       string
	CreatedAt       time.Time
}

type ReplaySnapshot struct {
	Run       Run
	Events    []Event
	Artifacts []Artifact
	ToolCalls []ToolCall
}

type transactionRunner func(
	context.Context, pgx.TxOptions, string, func(pgx.Tx) error,
) error

type PostgresStore struct {
	pool   *pgxpool.Pool
	runner transactionRunner
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

func newPostgresStoreWithRunner(runner transactionRunner) *PostgresStore {
	return &PostgresStore{runner: runner}
}

func (store *PostgresStore) CreateRun(ctx context.Context, request CreateRunRequest) (CreateResult, error) {
	tenantID, prepared, err := prepareCreateRequest(ctx, request)
	if err != nil {
		return CreateResult{}, err
	}
	var result CreateResult
	err = store.withActorTx(ctx, pgx.TxOptions{}, tenantID, func(tx pgx.Tx) error {
		created, replayed, err := createRunTx(ctx, tx, prepared)
		if err != nil {
			return err
		}
		result = CreateResult{Run: created, Replayed: replayed}
		return nil
	})
	if err == nil {
		return result, nil
	}

	// A concurrent exact creator may win just as the release is superseded.
	// Because the ACTIVE trigger fires before ON CONFLICT, retry the lookup in a
	// fresh actor-scoped transaction before classifying that database rejection.
	if isConstraintRace(err) {
		lookupErr := store.withActorTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly}, tenantID, func(tx pgx.Tx) error {
			existing, found, findErr := findRunByIdempotencyTx(ctx, tx, prepared)
			if findErr != nil {
				return findErr
			}
			if !found {
				return err
			}
			if !runMatchesCreate(existing, prepared) {
				return ErrIdempotencyConflict
			}
			if err := validateRunReplayTx(ctx, tx, existing); err != nil {
				return err
			}
			result = CreateResult{Run: existing, Replayed: true}
			return nil
		})
		if lookupErr == nil {
			return result, nil
		}
		err = lookupErr
	}
	return CreateResult{}, mapPersistenceError(err)
}

func (store *PostgresStore) Transition(ctx context.Context, request TransitionRequest) (TransitionResult, error) {
	tenantID, err := validateActorScope(ctx, request.Scope, request.DomainID)
	if err != nil {
		return TransitionResult{}, err
	}
	if !canonicalUUID(request.RunID) || request.ExpectedVersion < 1 {
		return TransitionResult{}, fmt.Errorf("%w: run ID or expected version is invalid", ErrInvalidRun)
	}
	details, err := canonicalAuditObject(request.Event.Details, maxEventDetailsBytes)
	if err != nil {
		return TransitionResult{}, err
	}
	request.Event.Details = details

	var completion *Artifact
	if request.Completion != nil {
		prepared, err := prepareCompletionArtifact(request, *request.Completion)
		if err != nil {
			return TransitionResult{}, err
		}
		completion = &prepared
	}

	var result TransitionResult
	err = store.withActorTx(ctx, pgx.TxOptions{}, tenantID, func(tx pgx.Tx) error {
		current, err := loadRunByIDTx(ctx, tx, request.Scope, request.DomainID, request.RunID, true)
		if err != nil {
			return err
		}
		if !runMatchesScope(current, request.Scope, request.DomainID) {
			return ErrPinnedScopeMismatch
		}
		existingEvents, err := loadEventsTx(ctx, tx, current)
		if err != nil {
			return err
		}
		existingArtifacts, err := loadArtifactsTx(ctx, tx, current)
		if err != nil {
			return err
		}
		existingTools, err := loadToolCallsTx(ctx, tx, current)
		if err != nil {
			return err
		}
		if err := (ReplaySnapshot{
			Run: current, Events: existingEvents, Artifacts: existingArtifacts, ToolCalls: existingTools,
		}).Validate(); err != nil {
			return err
		}
		var completionRef *CompletionRef
		if completion != nil {
			completion.TenantID = current.TenantID
			completion.DomainID = current.DomainID
			completion.ActorID = current.ActorID
			completion.Release = current.Release
			completion.PolicyScopeHash = current.PolicyScopeHash
			completion.RunVersion = current.RecordVersion
			completionRef = &CompletionRef{
				Code: request.Completion.Code, ArtifactType: completion.Type, ArtifactHash: completion.Hash,
			}
		}
		next, err := Apply(current, Transition{
			ExpectedVersion: request.ExpectedVersion, TargetState: request.TargetState,
			Usage: request.Usage, Hashes: request.Hashes, Completion: completionRef,
		})
		if err != nil {
			return err
		}

		lastEvent := existingEvents[len(existingEvents)-1]
		lastIndex, previousHash := lastEvent.Index, lastEvent.Hash
		if completion != nil {
			index, err := nextArtifactIndexTx(ctx, tx, current)
			if err != nil {
				return err
			}
			completion.Index = index
			completion.ID = askdata.ID(uuid.NewString())
			if err := insertArtifactTx(ctx, tx, *completion); err != nil {
				return err
			}
		}

		persisted, err := updateRunTx(ctx, tx, current, next)
		if err != nil {
			return err
		}
		event, err := buildTransitionEvent(persisted, current.State, lastIndex+1, previousHash, request.Event)
		if err != nil {
			return err
		}
		if completion != nil {
			event.ArtifactHash = completion.Hash
			event.Hash, err = computeEventHash(event)
			if err != nil {
				return err
			}
		}
		if err := insertEventTx(ctx, tx, event); err != nil {
			return err
		}
		result = TransitionResult{Run: persisted, Event: event}
		return nil
	})
	if err != nil {
		return TransitionResult{}, mapPersistenceError(err)
	}
	return result, nil
}

func (store *PostgresStore) Resume(ctx context.Context, request ResumeRequest) (ReplaySnapshot, error) {
	tenantID, err := validateActorScope(ctx, request.Scope, request.DomainID)
	if err != nil {
		return ReplaySnapshot{}, err
	}
	if !canonicalUUID(request.RunID) {
		return ReplaySnapshot{}, fmt.Errorf("%w: run ID must be a UUID", ErrInvalidRun)
	}
	var snapshot ReplaySnapshot
	err = store.withActorTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly,
	}, tenantID, func(tx pgx.Tx) error {
		run, err := loadRunByIDTx(ctx, tx, request.Scope, request.DomainID, request.RunID, false)
		if err != nil {
			return err
		}
		if !runMatchesScope(run, request.Scope, request.DomainID) {
			return ErrPinnedScopeMismatch
		}
		events, err := loadEventsTx(ctx, tx, run)
		if err != nil {
			return err
		}
		artifacts, err := loadArtifactsTx(ctx, tx, run)
		if err != nil {
			return err
		}
		tools, err := loadToolCallsTx(ctx, tx, run)
		if err != nil {
			return err
		}
		snapshot = ReplaySnapshot{Run: run, Events: events, Artifacts: artifacts, ToolCalls: tools}
		return snapshot.Validate()
	})
	if err != nil {
		return ReplaySnapshot{}, mapPersistenceError(err)
	}
	return snapshot, nil
}

func (store *PostgresStore) withActorTx(
	ctx context.Context, options pgx.TxOptions, tenantID string, fn func(pgx.Tx) error,
) error {
	if store == nil || fn == nil {
		return ErrPersistence
	}
	if store.runner != nil {
		return store.runner(ctx, options, tenantID, fn)
	}
	if store.pool == nil {
		return ErrPersistence
	}
	access, ok := database.AccessContextFromContext(ctx)
	if !ok || access.UserID == "" || access.DomainID == "" {
		return ErrInvalidAccessContext
	}
	tx, err := store.pool.BeginTx(ctx, options)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT
		set_config('app.tenant_id',$1,true),
		set_config('app.access_mode','USER',true),
		set_config('app.user_id',$2,true),
		set_config('app.domain_id',$3,true)`, tenantID, access.UserID, access.DomainID); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type preparedCreate struct {
	request CreateRunRequest
	run     Run
}

func prepareCreateRequest(ctx context.Context, request CreateRunRequest) (string, preparedCreate, error) {
	tenantID, err := validateActorScope(ctx, request.Scope, request.DomainID)
	if err != nil {
		return "", preparedCreate{}, err
	}
	if err := request.IdempotencyKeyHash.Validate(); err != nil {
		return "", preparedCreate{}, fmt.Errorf("%w: idempotency key hash is invalid", ErrInvalidRun)
	}
	if err := request.QuestionHash.Validate(); err != nil {
		return "", preparedCreate{}, fmt.Errorf("%w: question hash is invalid", ErrInvalidRun)
	}
	for name, id := range map[string]askdata.ID{
		"conversation ID": request.ConversationID, "parent run ID": request.ParentRunID,
	} {
		if id != "" && !canonicalUUID(id) {
			return "", preparedCreate{}, fmt.Errorf("%w: %s must be a UUID", ErrInvalidRun, name)
		}
	}
	if request.ParentRunID != "" && request.ConversationID == "" {
		return "", preparedCreate{}, fmt.Errorf("%w: parent run requires a conversation ID", ErrInvalidRun)
	}
	limits := request.Limits
	if limits.IsZero() {
		limits = DefaultBudgetLimits()
	}
	if err := limits.Validate(); err != nil {
		return "", preparedCreate{}, err
	}
	request.Limits = limits
	run := Run{
		ID: askdata.ID(uuid.NewString()), TenantID: request.Scope.TenantID,
		DomainID: request.DomainID, ActorID: request.Scope.ActorID,
		ConversationID: request.ConversationID, ParentRunID: request.ParentRunID,
		TraceID: askdata.ID(uuid.NewString()), IdempotencyKeyHash: request.IdempotencyKeyHash,
		QuestionHash: request.QuestionHash, PolicyScopeHash: request.Scope.PolicyHash,
		Release: request.Scope.Release, State: StateReceived, Disposition: DispositionPending,
		Limits: limits, RecordVersion: 1,
	}
	if err := run.Validate(); err != nil {
		return "", preparedCreate{}, err
	}
	return tenantID, preparedCreate{request: request, run: run}, nil
}

func validateActorScope(
	ctx context.Context, scope askdata.PolicyScope, domainID askdata.ID,
) (string, error) {
	if err := scope.Validate(); err != nil {
		return "", fmt.Errorf("%w: policy scope is invalid", ErrPinnedScopeMismatch)
	}
	access, ok := database.AccessContextFromContext(ctx)
	if !ok || access.UserID == "" || access.DomainID == "" ||
		access.UserID != string(scope.ActorID) || access.DomainID != string(domainID) {
		return "", ErrInvalidAccessContext
	}
	if !canonicalUUID(scope.TenantID) || !canonicalUUID(scope.ActorID) ||
		!canonicalUUID(domainID) || !canonicalUUID(scope.Release.ReleaseID) {
		return "", fmt.Errorf("%w: scope identifiers must be canonical UUIDs", ErrPinnedScopeMismatch)
	}
	selected := false
	for _, candidate := range scope.DomainIDs {
		if !canonicalUUID(candidate) {
			return "", fmt.Errorf("%w: domain identifiers must be canonical UUIDs", ErrPinnedScopeMismatch)
		}
		selected = selected || candidate == domainID
	}
	if !selected {
		return "", fmt.Errorf("%w: selected domain is outside the policy scope", ErrPinnedScopeMismatch)
	}
	return string(scope.TenantID), nil
}

func canonicalUUID(id askdata.ID) bool {
	parsed, err := uuid.Parse(string(id))
	return err == nil && parsed.String() == string(id)
}

func createRunTx(ctx context.Context, tx pgx.Tx, prepared preparedCreate) (Run, bool, error) {
	if existing, found, err := findRunByIdempotencyTx(ctx, tx, prepared); err != nil {
		return Run{}, false, err
	} else if found {
		if !runMatchesCreate(existing, prepared) {
			return Run{}, false, ErrIdempotencyConflict
		}
		if err := validateRunReplayTx(ctx, tx, existing); err != nil {
			return Run{}, false, err
		}
		return existing, true, nil
	}
	if prepared.run.ParentRunID != "" {
		parent, err := loadRunByIDTx(
			ctx, tx, prepared.request.Scope, prepared.request.DomainID, prepared.run.ParentRunID, false,
		)
		if err != nil {
			return Run{}, false, err
		}
		if parent.ConversationID != prepared.run.ConversationID ||
			!runMatchesScope(parent, prepared.request.Scope, prepared.request.DomainID) {
			return Run{}, false, ErrPinnedScopeMismatch
		}
	}
	run, inserted, err := insertRunTx(ctx, tx, prepared.run)
	if err != nil {
		return Run{}, false, err
	}
	if !inserted {
		existing, found, err := findRunByIdempotencyTx(ctx, tx, prepared)
		if err != nil {
			return Run{}, false, err
		}
		if !found || !runMatchesCreate(existing, prepared) {
			return Run{}, false, ErrIdempotencyConflict
		}
		if err := validateRunReplayTx(ctx, tx, existing); err != nil {
			return Run{}, false, err
		}
		return existing, true, nil
	}
	event, err := newEvent(run, 1, "", TransitionEventInput{
		Stage: string(StateReceived), Status: EventSucceeded, Code: "RUN_RECEIVED",
		Details: json.RawMessage(`{}`),
	})
	if err != nil {
		return Run{}, false, err
	}
	if err := insertEventTx(ctx, tx, event); err != nil {
		return Run{}, false, err
	}
	return run, false, nil
}

func findRunByIdempotencyTx(
	ctx context.Context, tx pgx.Tx, prepared preparedCreate,
) (Run, bool, error) {
	run, err := scanRun(tx.QueryRow(ctx, runSelect+`
		WHERE tenant_id=$1 AND actor_id=$2 AND idempotency_key_hash=$3`,
		prepared.run.TenantID, prepared.run.ActorID, prepared.run.IdempotencyKeyHash))
	if errors.Is(err, pgx.ErrNoRows) {
		return Run{}, false, nil
	}
	return run, err == nil, err
}

func runMatchesCreate(run Run, prepared preparedCreate) bool {
	want := prepared.run
	return run.TenantID == want.TenantID && run.DomainID == want.DomainID &&
		run.ActorID == want.ActorID && run.ConversationID == want.ConversationID &&
		run.ParentRunID == want.ParentRunID && run.IdempotencyKeyHash == want.IdempotencyKeyHash &&
		run.QuestionHash == want.QuestionHash && run.PolicyScopeHash == want.PolicyScopeHash &&
		run.Release == want.Release && run.Limits == want.Limits
}

func runMatchesScope(run Run, scope askdata.PolicyScope, domainID askdata.ID) bool {
	return run.TenantID == scope.TenantID && run.ActorID == scope.ActorID &&
		run.DomainID == domainID && run.PolicyScopeHash == scope.PolicyHash &&
		run.Release == scope.Release
}

func insertRunTx(ctx context.Context, tx pgx.Tx, run Run) (Run, bool, error) {
	row := tx.QueryRow(ctx, `INSERT INTO askdata.question_runs(
		id,tenant_id,domain_id,actor_id,conversation_id,parent_run_id,trace_id,
		idempotency_key_hash,question_hash,policy_scope_hash,release_id,
		release_content_hash,max_steps,max_llm_calls,max_tool_calls,
		max_formal_queries,max_validation_queries,max_duration_ms
	) VALUES(
		$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18
	) ON CONFLICT ON CONSTRAINT askdata_question_runs_idempotency_key DO NOTHING
	RETURNING `+runColumns,
		run.ID, run.TenantID, run.DomainID, run.ActorID, nullableID(run.ConversationID),
		nullableID(run.ParentRunID), run.TraceID, run.IdempotencyKeyHash, run.QuestionHash,
		run.PolicyScopeHash, run.Release.ReleaseID, run.Release.ContentHash,
		run.Limits.MaxSteps, run.Limits.MaxLLMCalls, run.Limits.MaxToolCalls,
		run.Limits.MaxFormalQueries, run.Limits.MaxValidationQueries, run.Limits.MaxDurationMS)
	created, err := scanRun(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Run{}, false, nil
	}
	return created, err == nil, err
}

func loadRunByIDTx(
	ctx context.Context, tx pgx.Tx, scope askdata.PolicyScope, domainID, runID askdata.ID, lock bool,
) (Run, error) {
	query := runSelect + ` WHERE id=$1 AND tenant_id=$2 AND domain_id=$3 AND actor_id=$4`
	if lock {
		query += ` FOR UPDATE`
	}
	run, err := scanRun(tx.QueryRow(ctx, query, runID, scope.TenantID, domainID, scope.ActorID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Run{}, ErrRunNotFound
	}
	return run, err
}

func updateRunTx(ctx context.Context, tx pgx.Tx, current, next Run) (Run, error) {
	row := tx.QueryRow(ctx, `UPDATE askdata.question_runs SET
		current_state=$1,disposition=$2,completion_code=$3,
		completion_artifact_hash=$4,understanding_hash=$5,binding_bundle_hash=$6,
		graph_plan_hash=$7,semantic_ir_hash=$8,query_plan_hash=$9,result_hash=$10,
		step_count=$11,llm_calls_used=$12,tool_calls_used=$13,
		formal_queries_used=$14,validation_queries_used=$15,elapsed_ms=$16,
		budget_exhausted=$17,record_version=record_version+1
	WHERE id=$18 AND tenant_id=$19 AND domain_id=$20 AND actor_id=$21
	  AND record_version=$22 AND current_state=$23
	RETURNING `+runColumns,
		next.State, next.Disposition, next.CompletionCode, nullableHash(next.CompletionArtifact),
		nullableHash(next.Hashes.Understanding), nullableHash(next.Hashes.BindingBundle),
		nullableHash(next.Hashes.GraphPlan), nullableHash(next.Hashes.SemanticIR),
		nullableHash(next.Hashes.QueryPlan), nullableHash(next.Hashes.Result),
		next.Usage.StepCount, next.Usage.LLMCallsUsed, next.Usage.ToolCallsUsed,
		next.Usage.FormalQueriesUsed, next.Usage.ValidationQueriesUsed,
		next.Usage.ElapsedMS, next.Usage.Exhausted,
		current.ID, current.TenantID, current.DomainID, current.ActorID,
		current.RecordVersion, current.State)
	updated, err := scanRun(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Run{}, ErrVersionConflict
	}
	return updated, err
}

const runColumns = `
	id::text,tenant_id::text,domain_id::text,actor_id::text,
	COALESCE(conversation_id::text,''),COALESCE(parent_run_id::text,''),trace_id::text,
	idempotency_key_hash,question_hash,policy_scope_hash,release_id::text,
	release_content_hash,current_state,disposition,completion_code,
	COALESCE(completion_artifact_hash,''),COALESCE(understanding_hash,''),
	COALESCE(binding_bundle_hash,''),COALESCE(graph_plan_hash,''),
	COALESCE(semantic_ir_hash,''),COALESCE(query_plan_hash,''),COALESCE(result_hash,''),
	max_steps,max_llm_calls,max_tool_calls,max_formal_queries,max_validation_queries,
	max_duration_ms,step_count,llm_calls_used,tool_calls_used,formal_queries_used,
	validation_queries_used,elapsed_ms,budget_exhausted,record_version,
	created_at,updated_at,completed_at`

const runSelect = `SELECT ` + runColumns + ` FROM askdata.question_runs`

type rowScanner interface{ Scan(...any) error }

func scanRun(row rowScanner) (Run, error) {
	var run Run
	var id, tenantID, domainID, actorID, conversationID, parentID, traceID string
	var releaseID, state, disposition string
	var idempotencyHash, questionHash, policyHash, releaseHash string
	var completionHash, understandingHash, bindingHash, graphHash, irHash, planHash, resultHash string
	var completed sql.NullTime
	err := row.Scan(
		&id, &tenantID, &domainID, &actorID, &conversationID, &parentID, &traceID,
		&idempotencyHash, &questionHash, &policyHash, &releaseID, &releaseHash,
		&state, &disposition, &run.CompletionCode, &completionHash,
		&understandingHash, &bindingHash, &graphHash, &irHash, &planHash, &resultHash,
		&run.Limits.MaxSteps, &run.Limits.MaxLLMCalls, &run.Limits.MaxToolCalls,
		&run.Limits.MaxFormalQueries, &run.Limits.MaxValidationQueries, &run.Limits.MaxDurationMS,
		&run.Usage.StepCount, &run.Usage.LLMCallsUsed, &run.Usage.ToolCallsUsed,
		&run.Usage.FormalQueriesUsed, &run.Usage.ValidationQueriesUsed, &run.Usage.ElapsedMS,
		&run.Usage.Exhausted, &run.RecordVersion, &run.CreatedAt, &run.UpdatedAt, &completed,
	)
	if err != nil {
		return Run{}, err
	}
	run.ID, run.TenantID, run.DomainID, run.ActorID = askdata.ID(id), askdata.ID(tenantID), askdata.ID(domainID), askdata.ID(actorID)
	run.ConversationID, run.ParentRunID, run.TraceID = askdata.ID(conversationID), askdata.ID(parentID), askdata.ID(traceID)
	run.IdempotencyKeyHash, run.QuestionHash = askdata.ContentHash(idempotencyHash), askdata.ContentHash(questionHash)
	run.PolicyScopeHash = askdata.ContentHash(policyHash)
	run.Release = askdata.ReleaseRef{ReleaseID: askdata.ID(releaseID), ContentHash: askdata.ContentHash(releaseHash)}
	run.State, run.Disposition = State(state), Disposition(disposition)
	run.CompletionArtifact = askdata.ContentHash(completionHash)
	run.Hashes = RunHashes{
		Understanding: askdata.ContentHash(understandingHash), BindingBundle: askdata.ContentHash(bindingHash),
		GraphPlan: askdata.ContentHash(graphHash), SemanticIR: askdata.ContentHash(irHash),
		QueryPlan: askdata.ContentHash(planHash), Result: askdata.ContentHash(resultHash),
	}
	if completed.Valid {
		value := completed.Time
		run.CompletedAt = &value
	}
	if err := run.Validate(); err != nil {
		return Run{}, err
	}
	return run, nil
}

func nullableID(id askdata.ID) any {
	if id == "" {
		return nil
	}
	return id
}

func nullableHash(hash askdata.ContentHash) any {
	if hash == "" {
		return nil
	}
	return hash
}

func prepareCompletionArtifact(request TransitionRequest, input CompletionArtifactInput) (Artifact, error) {
	if !completionCodePattern.MatchString(input.Code) || input.Type != completionArtifactType(request.TargetState) {
		return Artifact{}, fmt.Errorf("%w: completion code or artifact type is invalid", ErrInvalidRun)
	}
	if !schemaVersionPattern.MatchString(input.SchemaVersion) {
		return Artifact{}, fmt.Errorf("%w: artifact schema version is invalid", ErrInvalidRun)
	}
	evidence, err := normalizedEvidenceIDs(input.EvidenceIDs)
	if err != nil {
		return Artifact{}, err
	}
	payload, err := canonicalAuditObject(input.Payload, maxArtifactPayloadBytes)
	if err != nil {
		return Artifact{}, err
	}
	artifact := Artifact{
		RunID: request.RunID, Type: input.Type, SchemaVersion: input.SchemaVersion,
		EvidenceIDs: evidence, Payload: payload,
	}
	hash, err := computeArtifactHash(artifact)
	if err != nil {
		return Artifact{}, err
	}
	if input.ExpectedHash != "" && input.ExpectedHash != hash {
		return Artifact{}, fmt.Errorf("%w: declared artifact hash does not match", ErrInvalidRun)
	}
	artifact.Hash = hash
	return artifact, nil
}

func nextArtifactIndexTx(ctx context.Context, tx pgx.Tx, run Run) (int, error) {
	var next int
	if err := tx.QueryRow(ctx, `SELECT COALESCE(max(artifact_index),0)+1
		FROM askdata.question_artifacts WHERE tenant_id=$1 AND question_run_id=$2`,
		run.TenantID, run.ID).Scan(&next); err != nil {
		return 0, err
	}
	if next < 1 || next > 1_000_000 {
		return 0, fmt.Errorf("%w: artifact index is exhausted", ErrReplayCorrupt)
	}
	return next, nil
}

func insertArtifactTx(ctx context.Context, tx pgx.Tx, artifact Artifact) error {
	_, err := tx.Exec(ctx, `INSERT INTO askdata.question_artifacts(
		id,tenant_id,domain_id,actor_id,question_run_id,release_id,
		release_content_hash,policy_scope_hash,artifact_index,run_version,
		artifact_type,schema_version,artifact_hash,evidence_ids,payload_json
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		artifact.ID, artifact.TenantID, artifact.DomainID, artifact.ActorID, artifact.RunID,
		artifact.Release.ReleaseID, artifact.Release.ContentHash, artifact.PolicyScopeHash,
		artifact.Index, artifact.RunVersion, artifact.Type, artifact.SchemaVersion,
		artifact.Hash, idsToStrings(artifact.EvidenceIDs), []byte(artifact.Payload))
	return err
}

func lastEventPositionTx(ctx context.Context, tx pgx.Tx, run Run) (int, askdata.ContentHash, error) {
	var index int
	var hash string
	err := tx.QueryRow(ctx, `SELECT event_index,event_hash
		FROM askdata.question_run_events
		WHERE tenant_id=$1 AND question_run_id=$2
		ORDER BY event_index DESC LIMIT 1`, run.TenantID, run.ID).Scan(&index, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", nil
	}
	if err != nil {
		return 0, "", err
	}
	return index, askdata.ContentHash(hash), nil
}

func buildTransitionEvent(
	run Run, previousState State, index int, previousHash askdata.ContentHash, input TransitionEventInput,
) (Event, error) {
	expectedStage := string(run.State)
	expectedStatus := EventSucceeded
	if run.State == StateBlocked || run.State == StateClarificationRequired {
		expectedStatus = EventBlocked
	}
	expectedCode := "STATE_" + string(run.State)
	if run.State == StateBinding && (previousState == StatePlanValidating || previousState == StateResultVerifying) {
		expectedCode = "SEMANTIC_CORRECTION"
	}
	if run.Terminal() {
		expectedCode = run.CompletionCode
	}
	if (input.Stage != "" && input.Stage != expectedStage) ||
		(input.Status != "" && input.Status != expectedStatus) ||
		(input.Code != "" && input.Code != expectedCode) {
		return Event{}, fmt.Errorf("%w: transition event contradicts the persisted state", ErrInvalidRun)
	}
	input.Stage, input.Status, input.Code = expectedStage, expectedStatus, expectedCode
	return newEvent(run, index, previousHash, input)
}

func newEvent(
	run Run, index int, previousHash askdata.ContentHash, input TransitionEventInput,
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
		State: run.State, Type: EventStateTransition, Stage: input.Stage,
		Status: input.Status, Code: input.Code, AIRequestID: input.AIRequestID,
		ActionHash: input.ActionHash, EvidenceIDs: evidence, Details: details,
		PreviousEventHash: previousHash, DurationMS: input.DurationMS,
	}
	hash, err := computeEventHash(event)
	if err != nil {
		return Event{}, err
	}
	event.Hash = hash
	return event, event.Validate()
}

func insertEventTx(ctx context.Context, tx pgx.Tx, event Event) error {
	summary, err := storedEventSummary(event)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO askdata.question_run_events(
		id,tenant_id,domain_id,actor_id,question_run_id,release_id,
		release_content_hash,policy_scope_hash,event_index,run_version,state,
		event_type,stage,status,code,tool_call_id,ai_request_id,action_hash,
		artifact_hash,evidence_ids,summary_json,event_hash,duration_ms
	) VALUES(
		$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,
		$18,$19,$20,$21,$22,$23
	)`, event.ID, event.TenantID, event.DomainID, event.ActorID, event.RunID,
		event.Release.ReleaseID, event.Release.ContentHash, event.PolicyScopeHash,
		event.Index, event.RunVersion, event.State, event.Type, event.Stage,
		event.Status, event.Code, string(event.ToolCallID), nullableID(event.AIRequestID),
		nullableHash(event.ActionHash), nullableHash(event.ArtifactHash),
		idsToStrings(event.EvidenceIDs), []byte(summary), event.Hash, event.DurationMS)
	return err
}

type eventSummaryEnvelope struct {
	PreviousEventHash string          `json:"previousEventHash"`
	Details           json.RawMessage `json:"details"`
}

func storedEventSummary(event Event) (json.RawMessage, error) {
	raw, err := registry.CanonicalValue(eventSummaryEnvelope{
		PreviousEventHash: string(event.PreviousEventHash), Details: event.Details,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: event summary cannot be canonicalized", ErrInvalidRun)
	}
	if len(raw) > 65_000 {
		return nil, fmt.Errorf("%w: event summary is too large", ErrInvalidRun)
	}
	return json.RawMessage(raw), nil
}

func decodeEventSummary(raw []byte) (askdata.ContentHash, json.RawMessage, error) {
	var envelope eventSummaryEnvelope
	if err := askdata.DecodeStrictJSON(raw, &envelope); err != nil {
		return "", nil, fmt.Errorf("%w: event summary envelope is invalid", ErrReplayCorrupt)
	}
	details, err := canonicalAuditObject(envelope.Details, maxEventDetailsBytes)
	if err != nil {
		return "", nil, fmt.Errorf("%w: event details are invalid", ErrReplayCorrupt)
	}
	previous := askdata.ContentHash(envelope.PreviousEventHash)
	if previous != "" && previous.Validate() != nil {
		return "", nil, fmt.Errorf("%w: previous event hash is invalid", ErrReplayCorrupt)
	}
	return previous, details, nil
}

func computeEventHash(event Event) (askdata.ContentHash, error) {
	document := struct {
		SchemaVersion     string              `json:"schemaVersion"`
		TenantID          askdata.ID          `json:"tenantId"`
		DomainID          askdata.ID          `json:"domainId"`
		ActorID           askdata.ID          `json:"actorId"`
		RunID             askdata.ID          `json:"runId"`
		Release           askdata.ReleaseRef  `json:"release"`
		PolicyScopeHash   askdata.ContentHash `json:"policyScopeHash"`
		Index             int                 `json:"eventIndex"`
		RunVersion        int64               `json:"runVersion"`
		State             State               `json:"state"`
		Type              EventType           `json:"eventType"`
		Stage             string              `json:"stage"`
		Status            EventStatus         `json:"status"`
		Code              string              `json:"code"`
		ToolCallID        askdata.ID          `json:"toolCallId"`
		AIRequestID       askdata.ID          `json:"aiRequestId"`
		ActionHash        askdata.ContentHash `json:"actionHash"`
		ArtifactHash      askdata.ContentHash `json:"artifactHash"`
		EvidenceIDs       []askdata.ID        `json:"evidenceIds"`
		PreviousEventHash askdata.ContentHash `json:"previousEventHash"`
		Details           json.RawMessage     `json:"details"`
		DurationMS        *int64              `json:"durationMs"`
	}{
		SchemaVersion: eventHashSchemaVersion, TenantID: event.TenantID,
		DomainID: event.DomainID, ActorID: event.ActorID, RunID: event.RunID,
		Release: event.Release, PolicyScopeHash: event.PolicyScopeHash,
		Index: event.Index, RunVersion: event.RunVersion, State: event.State,
		Type: event.Type, Stage: event.Stage, Status: event.Status, Code: event.Code,
		ToolCallID: event.ToolCallID, AIRequestID: event.AIRequestID,
		ActionHash: event.ActionHash, ArtifactHash: event.ArtifactHash,
		EvidenceIDs: event.EvidenceIDs, PreviousEventHash: event.PreviousEventHash,
		Details: event.Details, DurationMS: event.DurationMS,
	}
	hash, _, err := registry.CanonicalContentHash(document)
	if err != nil {
		return "", fmt.Errorf("%w: event hash cannot be computed", ErrInvalidRun)
	}
	return hash, nil
}

func computeArtifactHash(artifact Artifact) (askdata.ContentHash, error) {
	document := struct {
		SchemaVersion   string          `json:"schemaVersion"`
		RunID           askdata.ID      `json:"runId"`
		ArtifactType    ArtifactType    `json:"artifactType"`
		ContractVersion string          `json:"contractVersion"`
		EvidenceIDs     []askdata.ID    `json:"evidenceIds"`
		Payload         json.RawMessage `json:"payload"`
	}{
		SchemaVersion: artifactHashSchemaVersion, RunID: artifact.RunID,
		ArtifactType: artifact.Type, ContractVersion: artifact.SchemaVersion,
		EvidenceIDs: artifact.EvidenceIDs, Payload: artifact.Payload,
	}
	hash, _, err := registry.CanonicalContentHash(document)
	if err != nil {
		return "", fmt.Errorf("%w: artifact hash cannot be computed", ErrInvalidRun)
	}
	return hash, nil
}

func computeToolCallHash(call ToolCall) (askdata.ContentHash, error) {
	document := struct {
		SchemaVersion   string              `json:"schemaVersion"`
		TenantID        askdata.ID          `json:"tenantId"`
		DomainID        askdata.ID          `json:"domainId"`
		ActorID         askdata.ID          `json:"actorId"`
		RunID           askdata.ID          `json:"runId"`
		Release         askdata.ReleaseRef  `json:"release"`
		PolicyScopeHash askdata.ContentHash `json:"policyScopeHash"`
		RunVersion      int64               `json:"runVersion"`
		CallID          askdata.ID          `json:"toolCallId"`
		Tool            toolhost.ToolName   `json:"toolName"`
		State           State               `json:"state"`
		Status          string              `json:"status"`
		RequestHash     askdata.ContentHash `json:"requestHash"`
		ResultHash      askdata.ContentHash `json:"resultHash"`
		EvidenceIDs     []askdata.ID        `json:"evidenceIds"`
		Budget          json.RawMessage     `json:"budget"`
		DurationMS      int64               `json:"durationMs"`
		ErrorCode       string              `json:"errorCode"`
	}{
		SchemaVersion: toolCallHashSchemaVersion, TenantID: call.TenantID,
		DomainID: call.DomainID, ActorID: call.ActorID, RunID: call.RunID,
		Release: call.Release, PolicyScopeHash: call.PolicyScopeHash,
		RunVersion: call.RunVersion, CallID: call.CallID, Tool: call.Tool,
		State: call.State, Status: call.Status, RequestHash: call.RequestHash,
		ResultHash: call.ResultHash, EvidenceIDs: call.EvidenceIDs,
		Budget: call.Budget, DurationMS: call.DurationMS, ErrorCode: call.ErrorCode,
	}
	hash, _, err := registry.CanonicalContentHash(document)
	if err != nil {
		return "", fmt.Errorf("%w: tool call hash cannot be computed", ErrInvalidRun)
	}
	return hash, nil
}

func (event Event) Validate() error {
	for _, id := range []askdata.ID{event.ID, event.TenantID, event.DomainID, event.ActorID, event.RunID} {
		if !canonicalUUID(id) {
			return fmt.Errorf("%w: event identity is invalid", ErrReplayCorrupt)
		}
	}
	if err := event.Release.Validate(); err != nil || !canonicalUUID(event.Release.ReleaseID) ||
		event.PolicyScopeHash.Validate() != nil || event.Index < 1 || event.Index > 1_000_000 ||
		event.RunVersion < 1 {
		return fmt.Errorf("%w: event pin or position is invalid", ErrReplayCorrupt)
	}
	if _, ok := validStates[event.State]; !ok {
		return fmt.Errorf("%w: event state is invalid", ErrReplayCorrupt)
	}
	if !validEventType(event.Type) || !validEventStatus(event.Status) ||
		(event.Stage != "" && !stagePattern.MatchString(event.Stage)) ||
		(event.Code != "" && !completionCodePattern.MatchString(event.Code)) {
		return fmt.Errorf("%w: event vocabulary is invalid", ErrReplayCorrupt)
	}
	if event.ToolCallID != "" && event.ToolCallID.Validate() != nil {
		return fmt.Errorf("%w: event tool call ID is invalid", ErrReplayCorrupt)
	}
	if event.AIRequestID != "" && !canonicalUUID(event.AIRequestID) {
		return fmt.Errorf("%w: event AI request ID is invalid", ErrReplayCorrupt)
	}
	for _, hash := range []askdata.ContentHash{event.ActionHash, event.ArtifactHash} {
		if hash != "" && hash.Validate() != nil {
			return fmt.Errorf("%w: event optional hash is invalid", ErrReplayCorrupt)
		}
	}
	if event.PreviousEventHash != "" && event.PreviousEventHash.Validate() != nil {
		return fmt.Errorf("%w: previous event hash is invalid", ErrReplayCorrupt)
	}
	if err := event.validateTypeShape(); err != nil {
		return err
	}
	evidence, err := normalizedEvidenceIDs(event.EvidenceIDs)
	if err != nil || !equalIDs(evidence, event.EvidenceIDs) {
		return fmt.Errorf("%w: event evidence IDs are not canonical", ErrReplayCorrupt)
	}
	canonical, err := canonicalAuditObject(event.Details, maxEventDetailsBytes)
	if err != nil || string(canonical) != string(event.Details) {
		return fmt.Errorf("%w: event details are not canonical", ErrReplayCorrupt)
	}
	if event.DurationMS != nil && (*event.DurationMS < 0 || *event.DurationMS > 600_000) {
		return fmt.Errorf("%w: event duration is invalid", ErrReplayCorrupt)
	}
	expected, err := computeEventHash(event)
	if err != nil || expected != event.Hash {
		return fmt.Errorf("%w: event hash does not match", ErrReplayCorrupt)
	}
	return nil
}

func (event Event) validateTypeShape() error {
	pairedDecision := (event.AIRequestID == "") == (event.ActionHash == "")
	valid := false
	switch event.Type {
	case EventStateTransition:
		valid = event.ToolCallID == "" && pairedDecision &&
			(event.ArtifactHash == "" || isTerminalState(event.State))
	case EventLLMDecision:
		valid = event.ToolCallID == "" && event.AIRequestID != "" &&
			event.ActionHash != "" && event.ArtifactHash == ""
	case EventToolResult:
		valid = event.ToolCallID != "" && event.AIRequestID == "" &&
			event.ActionHash == "" && event.ArtifactHash == ""
	case EventArtifactRecorded:
		valid = event.ToolCallID == "" && event.AIRequestID == "" &&
			event.ActionHash == "" && event.ArtifactHash != ""
	case EventBudgetUpdated, EventCorrection, EventError, EventProgress:
		valid = event.ToolCallID == "" && event.ArtifactHash == ""
	}
	if !valid {
		return fmt.Errorf("%w: event type references are invalid", ErrReplayCorrupt)
	}
	return nil
}

func (artifact Artifact) Validate() error {
	for _, id := range []askdata.ID{artifact.ID, artifact.TenantID, artifact.DomainID, artifact.ActorID, artifact.RunID} {
		if !canonicalUUID(id) {
			return fmt.Errorf("%w: artifact identity is invalid", ErrReplayCorrupt)
		}
	}
	if err := artifact.Release.Validate(); err != nil || !canonicalUUID(artifact.Release.ReleaseID) ||
		artifact.PolicyScopeHash.Validate() != nil || artifact.Index < 1 || artifact.Index > 1_000_000 ||
		artifact.RunVersion < 1 || !schemaVersionPattern.MatchString(artifact.SchemaVersion) {
		return fmt.Errorf("%w: artifact pin, position or schema is invalid", ErrReplayCorrupt)
	}
	if _, ok := validArtifactTypes[artifact.Type]; !ok {
		return fmt.Errorf("%w: artifact type is invalid", ErrReplayCorrupt)
	}
	evidence, err := normalizedEvidenceIDs(artifact.EvidenceIDs)
	if err != nil || !equalIDs(evidence, artifact.EvidenceIDs) {
		return fmt.Errorf("%w: artifact evidence IDs are not canonical", ErrReplayCorrupt)
	}
	canonical, err := canonicalAuditObject(artifact.Payload, maxArtifactPayloadBytes)
	if err != nil || string(canonical) != string(artifact.Payload) {
		return fmt.Errorf("%w: artifact payload is not canonical", ErrReplayCorrupt)
	}
	expected, err := computeArtifactHash(artifact)
	if err != nil || expected != artifact.Hash {
		return fmt.Errorf("%w: artifact hash does not match", ErrReplayCorrupt)
	}
	return nil
}

func validEventType(value EventType) bool {
	switch value {
	case EventStateTransition, EventLLMDecision, EventToolResult, EventArtifactRecorded,
		EventBudgetUpdated, EventCorrection, EventError, EventProgress:
		return true
	default:
		return false
	}
}

func validEventStatus(value EventStatus) bool {
	switch value {
	case EventStarted, EventSucceeded, EventBlocked, EventFailed, EventCanceled:
		return true
	default:
		return false
	}
}

func (snapshot ReplaySnapshot) Validate() error {
	if err := snapshot.Run.Validate(); err != nil {
		return fmt.Errorf("%w: persisted run is invalid", ErrReplayCorrupt)
	}
	statesByVersion, err := replayEventStates(snapshot.Run, snapshot.Events)
	if err != nil {
		return err
	}
	if snapshot.Run.Terminal() {
		terminalEvent := snapshot.Events[len(snapshot.Events)-1]
		if terminalEvent.ArtifactHash != snapshot.Run.CompletionArtifact ||
			terminalEvent.Code != snapshot.Run.CompletionCode {
			return fmt.Errorf("%w: terminal event does not bind the run completion", ErrReplayCorrupt)
		}
	}

	completionFound := false
	artifactsByHash := make(map[askdata.ContentHash]Artifact, len(snapshot.Artifacts))
	for index, artifact := range snapshot.Artifacts {
		if err := artifact.Validate(); err != nil {
			return err
		}
		if artifact.Index != index+1 || !artifactMatchesRun(artifact, snapshot.Run) ||
			artifact.RunVersion > snapshot.Run.RecordVersion || statesByVersion[artifact.RunVersion] == "" ||
			(snapshot.Run.Terminal() && artifact.RunVersion >= snapshot.Run.RecordVersion) {
			return fmt.Errorf("%w: artifact chain identity or version is invalid", ErrReplayCorrupt)
		}
		if artifact.Hash == snapshot.Run.CompletionArtifact {
			completionFound = artifact.Type == completionArtifactType(snapshot.Run.State) &&
				artifact.RunVersion == snapshot.Run.RecordVersion-1
		}
		if _, duplicate := artifactsByHash[artifact.Hash]; duplicate {
			return fmt.Errorf("%w: duplicate artifact hash", ErrReplayCorrupt)
		}
		artifactsByHash[artifact.Hash] = artifact
	}
	if snapshot.Run.Terminal() && !completionFound {
		return fmt.Errorf("%w: terminal completion artifact is missing", ErrReplayCorrupt)
	}

	callsByID := make(map[askdata.ID]ToolCall, len(snapshot.ToolCalls))
	for _, call := range snapshot.ToolCalls {
		if err := call.validate(); err != nil {
			return err
		}
		if !toolCallMatchesRun(call, snapshot.Run) || call.RunVersion > snapshot.Run.RecordVersion ||
			statesByVersion[call.RunVersion] != call.State ||
			(snapshot.Run.Terminal() && call.RunVersion >= snapshot.Run.RecordVersion) {
			return fmt.Errorf("%w: tool outcome pin, version or state is invalid", ErrReplayCorrupt)
		}
		if _, duplicate := callsByID[call.CallID]; duplicate {
			return fmt.Errorf("%w: duplicate tool call ID", ErrReplayCorrupt)
		}
		callsByID[call.CallID] = call
	}
	return validateEventFactReferences(snapshot.Events, artifactsByHash, callsByID)
}

func validateEventFactReferences(
	events []Event,
	artifacts map[askdata.ContentHash]Artifact,
	calls map[askdata.ID]ToolCall,
) error {
	referencedArtifacts := make(map[askdata.ContentHash]struct{}, len(artifacts))
	referencedCalls := make(map[askdata.ID]struct{}, len(calls))
	for _, event := range events {
		switch event.Type {
		case EventToolResult:
			call, exists := calls[event.ToolCallID]
			if !exists || call.RunVersion != event.RunVersion || call.State != event.State ||
				EventStatus(call.Status) != event.Status {
				return fmt.Errorf("%w: tool result event does not bind an exact tool outcome", ErrReplayCorrupt)
			}
			if _, duplicate := referencedCalls[event.ToolCallID]; duplicate {
				return fmt.Errorf("%w: tool outcome has multiple result events", ErrReplayCorrupt)
			}
			referencedCalls[event.ToolCallID] = struct{}{}
		case EventArtifactRecorded:
			artifact, exists := artifacts[event.ArtifactHash]
			if !exists || artifact.RunVersion != event.RunVersion {
				return fmt.Errorf("%w: artifact event does not bind an exact artifact", ErrReplayCorrupt)
			}
			if _, duplicate := referencedArtifacts[event.ArtifactHash]; duplicate {
				return fmt.Errorf("%w: artifact has multiple recorded events", ErrReplayCorrupt)
			}
			referencedArtifacts[event.ArtifactHash] = struct{}{}
		case EventStateTransition:
			if event.ArtifactHash == "" {
				continue
			}
			artifact, exists := artifacts[event.ArtifactHash]
			if !exists || !isTerminalState(event.State) || artifact.RunVersion != event.RunVersion-1 {
				return fmt.Errorf("%w: terminal transition does not bind its completion artifact", ErrReplayCorrupt)
			}
			if _, duplicate := referencedArtifacts[event.ArtifactHash]; duplicate {
				return fmt.Errorf("%w: completion artifact has multiple events", ErrReplayCorrupt)
			}
			referencedArtifacts[event.ArtifactHash] = struct{}{}
		}
	}
	if len(referencedCalls) != len(calls) || len(referencedArtifacts) != len(artifacts) {
		return fmt.Errorf("%w: persisted fact is missing its audit event", ErrReplayCorrupt)
	}
	return nil
}

func replayEventStates(run Run, events []Event) (map[int64]State, error) {
	if len(events) == 0 {
		return nil, fmt.Errorf("%w: initial event is missing", ErrReplayCorrupt)
	}
	statesByVersion := make(map[int64]State, run.RecordVersion)
	currentVersion := int64(0)
	var currentState State
	var previousHash askdata.ContentHash
	for index, event := range events {
		if err := event.Validate(); err != nil {
			return nil, err
		}
		if event.Index != index+1 || !eventMatchesRun(event, run) ||
			event.PreviousEventHash != previousHash {
			return nil, fmt.Errorf("%w: event chain identity or index is invalid", ErrReplayCorrupt)
		}
		if index == 0 {
			if event.Type != EventStateTransition || event.RunVersion != 1 || event.State != StateReceived ||
				event.PreviousEventHash != "" || event.Stage != string(StateReceived) ||
				event.Status != EventSucceeded || event.Code != "RUN_RECEIVED" {
				return nil, fmt.Errorf("%w: initial event shape is invalid", ErrReplayCorrupt)
			}
			currentVersion, currentState = 1, StateReceived
			statesByVersion[1] = StateReceived
		} else {
			if isTerminalState(currentState) {
				return nil, fmt.Errorf("%w: event was appended after terminal completion", ErrReplayCorrupt)
			}
			switch event.RunVersion {
			case currentVersion:
				if event.State != currentState || event.Type == EventStateTransition {
					return nil, fmt.Errorf("%w: same-version event changes state", ErrReplayCorrupt)
				}
			case currentVersion + 1:
				if event.Type != EventStateTransition || !CanTransition(currentState, event.State) {
					return nil, fmt.Errorf("%w: event contains an illegal state transition", ErrReplayCorrupt)
				}
				expectedStatus := EventSucceeded
				if event.State == StateBlocked || event.State == StateClarificationRequired {
					expectedStatus = EventBlocked
				}
				expectedCode := "STATE_" + string(event.State)
				if event.State == StateBinding &&
					(currentState == StatePlanValidating || currentState == StateResultVerifying) {
					expectedCode = "SEMANTIC_CORRECTION"
				}
				if isTerminalState(event.State) {
					expectedCode = run.CompletionCode
				}
				if event.Stage != string(event.State) || event.Status != expectedStatus || event.Code != expectedCode {
					return nil, fmt.Errorf("%w: transition event semantics contradict its state", ErrReplayCorrupt)
				}
				currentVersion, currentState = event.RunVersion, event.State
				statesByVersion[currentVersion] = currentState
			default:
				return nil, fmt.Errorf("%w: event run version is not contiguous", ErrReplayCorrupt)
			}
		}
		previousHash = event.Hash
	}
	if currentVersion != run.RecordVersion || currentState != run.State {
		return nil, fmt.Errorf("%w: replay does not match the current run", ErrReplayCorrupt)
	}
	return statesByVersion, nil
}

func validateRunReplayTx(ctx context.Context, tx pgx.Tx, run Run) error {
	events, err := loadEventsTx(ctx, tx, run)
	if err != nil {
		return err
	}
	artifacts, err := loadArtifactsTx(ctx, tx, run)
	if err != nil {
		return err
	}
	tools, err := loadToolCallsTx(ctx, tx, run)
	if err != nil {
		return err
	}
	return (ReplaySnapshot{Run: run, Events: events, Artifacts: artifacts, ToolCalls: tools}).Validate()
}

func (snapshot ReplaySnapshot) SeenActionHashes() []askdata.ContentHash {
	seen := map[askdata.ContentHash]struct{}{}
	result := make([]askdata.ContentHash, 0)
	for _, event := range snapshot.Events {
		if event.ActionHash == "" {
			continue
		}
		if _, exists := seen[event.ActionHash]; exists {
			continue
		}
		seen[event.ActionHash] = struct{}{}
		result = append(result, event.ActionHash)
	}
	return result
}

func (snapshot ReplaySnapshot) SeenToolCallIDs() []askdata.ID {
	seen := map[askdata.ID]struct{}{}
	result := make([]askdata.ID, 0, len(snapshot.ToolCalls))
	for _, call := range snapshot.ToolCalls {
		if _, exists := seen[call.CallID]; exists {
			continue
		}
		seen[call.CallID] = struct{}{}
		result = append(result, call.CallID)
	}
	return result
}

func eventMatchesRun(event Event, run Run) bool {
	return event.TenantID == run.TenantID && event.DomainID == run.DomainID &&
		event.ActorID == run.ActorID && event.RunID == run.ID && event.Release == run.Release &&
		event.PolicyScopeHash == run.PolicyScopeHash
}

func artifactMatchesRun(artifact Artifact, run Run) bool {
	return artifact.TenantID == run.TenantID && artifact.DomainID == run.DomainID &&
		artifact.ActorID == run.ActorID && artifact.RunID == run.ID && artifact.Release == run.Release &&
		artifact.PolicyScopeHash == run.PolicyScopeHash
}

func toolCallMatchesRun(call ToolCall, run Run) bool {
	return call.TenantID == run.TenantID && call.DomainID == run.DomainID &&
		call.ActorID == run.ActorID && call.RunID == run.ID && call.Release == run.Release &&
		call.PolicyScopeHash == run.PolicyScopeHash
}

func (call ToolCall) validate() error {
	for _, id := range []askdata.ID{call.ID, call.TenantID, call.DomainID, call.ActorID, call.RunID} {
		if !canonicalUUID(id) {
			return fmt.Errorf("%w: tool outcome identity is invalid", ErrReplayCorrupt)
		}
	}
	if call.CallID.Validate() != nil || !toolhost.IsKnownTool(call.Tool) || call.RunVersion < 1 ||
		call.Release.Validate() != nil || !canonicalUUID(call.Release.ReleaseID) ||
		call.PolicyScopeHash.Validate() != nil || call.RequestHash.Validate() != nil ||
		call.ResultHash.Validate() != nil || call.CallHash.Validate() != nil ||
		call.DurationMS < 0 || call.DurationMS > 600_000 {
		return fmt.Errorf("%w: tool outcome contract is invalid", ErrReplayCorrupt)
	}
	if _, ok := validStates[call.State]; !ok {
		return fmt.Errorf("%w: tool outcome state is invalid", ErrReplayCorrupt)
	}
	if call.Status != "SUCCEEDED" && call.Status != "BLOCKED" && call.Status != "FAILED" && call.Status != "CANCELED" {
		return fmt.Errorf("%w: tool outcome status is invalid", ErrReplayCorrupt)
	}
	if (call.Status == "SUCCEEDED" && call.ErrorCode != "") ||
		(call.Status != "SUCCEEDED" && !completionCodePattern.MatchString(call.ErrorCode)) {
		return fmt.Errorf("%w: tool outcome error shape is invalid", ErrReplayCorrupt)
	}
	evidence, err := normalizedEvidenceIDs(call.EvidenceIDs)
	if err != nil || !equalIDs(evidence, call.EvidenceIDs) {
		return fmt.Errorf("%w: tool evidence IDs are not canonical", ErrReplayCorrupt)
	}
	canonical, err := canonicalAuditObject(call.Budget, 16<<10)
	if err != nil || string(canonical) != string(call.Budget) {
		return fmt.Errorf("%w: tool budget summary is not canonical", ErrReplayCorrupt)
	}
	expected, err := computeToolCallHash(call)
	if err != nil || expected != call.CallHash {
		return fmt.Errorf("%w: tool call hash does not match", ErrReplayCorrupt)
	}
	return nil
}

func loadEventsTx(ctx context.Context, tx pgx.Tx, run Run) ([]Event, error) {
	rows, err := tx.Query(ctx, `SELECT
		id::text,tenant_id::text,domain_id::text,actor_id::text,question_run_id::text,
		release_id::text,release_content_hash,policy_scope_hash,event_index,run_version,
		state,event_type,stage,status,code,tool_call_id,COALESCE(ai_request_id::text,''),
		COALESCE(action_hash,''),COALESCE(artifact_hash,''),evidence_ids,summary_json,
		event_hash,duration_ms,created_at
	FROM askdata.question_run_events
	WHERE tenant_id=$1 AND domain_id=$2 AND actor_id=$3 AND question_run_id=$4
	ORDER BY event_index`, run.TenantID, run.DomainID, run.ActorID, run.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Event{}
	for rows.Next() {
		event, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, event)
	}
	return result, rows.Err()
}

func scanEvent(row rowScanner) (Event, error) {
	var event Event
	var id, tenant, domain, actor, runID, releaseID, releaseHash, policyHash string
	var state, eventType, status, toolCallID, aiRequest, actionHash, artifactHash, eventHash string
	var evidence []string
	var summary []byte
	var duration sql.NullInt64
	if err := row.Scan(
		&id, &tenant, &domain, &actor, &runID, &releaseID, &releaseHash, &policyHash,
		&event.Index, &event.RunVersion, &state, &eventType, &event.Stage, &status,
		&event.Code, &toolCallID, &aiRequest, &actionHash, &artifactHash,
		&evidence, &summary, &eventHash, &duration, &event.CreatedAt,
	); err != nil {
		return Event{}, err
	}
	previous, details, err := decodeEventSummary(summary)
	if err != nil {
		return Event{}, err
	}
	event.ID, event.TenantID, event.DomainID, event.ActorID, event.RunID =
		askdata.ID(id), askdata.ID(tenant), askdata.ID(domain), askdata.ID(actor), askdata.ID(runID)
	event.Release = askdata.ReleaseRef{ReleaseID: askdata.ID(releaseID), ContentHash: askdata.ContentHash(releaseHash)}
	event.PolicyScopeHash, event.State, event.Type = askdata.ContentHash(policyHash), State(state), EventType(eventType)
	event.Status, event.AIRequestID = EventStatus(status), askdata.ID(aiRequest)
	event.ToolCallID = askdata.ID(toolCallID)
	event.ActionHash, event.ArtifactHash = askdata.ContentHash(actionHash), askdata.ContentHash(artifactHash)
	event.EvidenceIDs, event.Details, event.PreviousEventHash = stringsToIDs(evidence), details, previous
	event.Hash = askdata.ContentHash(eventHash)
	if duration.Valid {
		value := duration.Int64
		event.DurationMS = &value
	}
	return event, nil
}

func loadArtifactsTx(ctx context.Context, tx pgx.Tx, run Run) ([]Artifact, error) {
	rows, err := tx.Query(ctx, `SELECT
		id::text,tenant_id::text,domain_id::text,actor_id::text,question_run_id::text,
		release_id::text,release_content_hash,policy_scope_hash,artifact_index,run_version,
		artifact_type,schema_version,artifact_hash,evidence_ids,payload_json,created_at
	FROM askdata.question_artifacts
	WHERE tenant_id=$1 AND domain_id=$2 AND actor_id=$3 AND question_run_id=$4
	ORDER BY artifact_index`, run.TenantID, run.DomainID, run.ActorID, run.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Artifact{}
	for rows.Next() {
		artifact, err := scanArtifact(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, artifact)
	}
	return result, rows.Err()
}

func scanArtifact(row rowScanner) (Artifact, error) {
	var artifact Artifact
	var id, tenant, domain, actor, runID, releaseID, releaseHash, policyHash string
	var artifactType, artifactHash string
	var evidence []string
	var payload []byte
	if err := row.Scan(
		&id, &tenant, &domain, &actor, &runID, &releaseID, &releaseHash, &policyHash,
		&artifact.Index, &artifact.RunVersion, &artifactType, &artifact.SchemaVersion,
		&artifactHash, &evidence, &payload, &artifact.CreatedAt,
	); err != nil {
		return Artifact{}, err
	}
	canonical, err := canonicalAuditObject(payload, maxArtifactPayloadBytes)
	if err != nil {
		return Artifact{}, fmt.Errorf("%w: persisted artifact payload is invalid", ErrReplayCorrupt)
	}
	artifact.ID, artifact.TenantID, artifact.DomainID, artifact.ActorID, artifact.RunID =
		askdata.ID(id), askdata.ID(tenant), askdata.ID(domain), askdata.ID(actor), askdata.ID(runID)
	artifact.Release = askdata.ReleaseRef{ReleaseID: askdata.ID(releaseID), ContentHash: askdata.ContentHash(releaseHash)}
	artifact.PolicyScopeHash = askdata.ContentHash(policyHash)
	artifact.Type, artifact.Hash = ArtifactType(artifactType), askdata.ContentHash(artifactHash)
	artifact.EvidenceIDs, artifact.Payload = stringsToIDs(evidence), canonical
	return artifact, nil
}

func loadToolCallsTx(ctx context.Context, tx pgx.Tx, run Run) ([]ToolCall, error) {
	rows, err := tx.Query(ctx, `SELECT
		id::text,tenant_id::text,domain_id::text,actor_id::text,question_run_id::text,
		release_id::text,release_content_hash,policy_scope_hash,run_version,tool_call_id,
		tool_name,state,status,request_hash,result_hash,call_hash,evidence_ids,
		budget_json,duration_ms,error_code,created_at
	FROM askdata.tool_calls
	WHERE tenant_id=$1 AND domain_id=$2 AND actor_id=$3 AND question_run_id=$4
	ORDER BY created_at,id`, run.TenantID, run.DomainID, run.ActorID, run.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ToolCall{}
	for rows.Next() {
		call, err := scanToolCall(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, call)
	}
	return result, rows.Err()
}

func scanToolCall(row rowScanner) (ToolCall, error) {
	var call ToolCall
	var id, tenant, domain, actor, runID, releaseID, releaseHash, policyHash string
	var callID, tool, state, requestHash, resultHash, callHash string
	var evidence []string
	var budget []byte
	if err := row.Scan(
		&id, &tenant, &domain, &actor, &runID, &releaseID, &releaseHash, &policyHash,
		&call.RunVersion, &callID, &tool, &state, &call.Status,
		&requestHash, &resultHash, &callHash, &evidence, &budget,
		&call.DurationMS, &call.ErrorCode, &call.CreatedAt,
	); err != nil {
		return ToolCall{}, err
	}
	canonical, err := canonicalAuditObject(budget, 16<<10)
	if err != nil {
		return ToolCall{}, fmt.Errorf("%w: persisted tool budget is invalid", ErrReplayCorrupt)
	}
	call.ID, call.TenantID, call.DomainID, call.ActorID, call.RunID =
		askdata.ID(id), askdata.ID(tenant), askdata.ID(domain), askdata.ID(actor), askdata.ID(runID)
	call.Release = askdata.ReleaseRef{ReleaseID: askdata.ID(releaseID), ContentHash: askdata.ContentHash(releaseHash)}
	call.PolicyScopeHash, call.Tool, call.State = askdata.ContentHash(policyHash), toolhost.ToolName(tool), State(state)
	call.CallID = askdata.ID(callID)
	call.RequestHash, call.ResultHash, call.CallHash =
		askdata.ContentHash(requestHash), askdata.ContentHash(resultHash), askdata.ContentHash(callHash)
	call.EvidenceIDs, call.Budget = stringsToIDs(evidence), canonical
	return call, nil
}

func canonicalAuditObject(raw []byte, maximum int) (json.RawMessage, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		raw = []byte(`{}`)
	}
	canonical, err := registry.CanonicalJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: audit JSON is invalid", ErrInvalidRun)
	}
	if len(canonical) > maximum {
		return nil, fmt.Errorf("%w: audit JSON exceeds its size limit", ErrInvalidRun)
	}
	var object map[string]any
	if err := askdata.DecodeStrictJSON(canonical, &object); err != nil || object == nil {
		return nil, fmt.Errorf("%w: audit JSON must be an object", ErrInvalidRun)
	}
	if !auditJSONSafe(object) {
		return nil, fmt.Errorf("%w: audit JSON contains a forbidden field", ErrInvalidRun)
	}
	return json.RawMessage(canonical), nil
}

var forbiddenAuditKeys = map[string]struct{}{
	"sql": {}, "rawsql": {}, "query": {}, "statement": {}, "password": {}, "secret": {},
	"credentials": {}, "rows": {}, "samplerows": {}, "rawdata": {},
	"question": {}, "questiontext": {}, "questionsummary": {}, "rawquestion": {},
	"prompt": {}, "prompts": {}, "systemprompt": {}, "developerprompt": {}, "userprompt": {},
	"messages": {}, "messagehistory": {}, "reasoning": {}, "reasoningcontent": {},
	"chainofthought": {}, "thought": {}, "thoughts": {}, "cot": {}, "analysis": {}, "modelanalysis": {},
	"parameters": {}, "params": {}, "parametervalues": {}, "bindparameters": {}, "bindvalues": {},
	"arguments": {}, "toolarguments": {}, "queryparameters": {}, "queryparams": {},
	"sqlparameters": {}, "sqlparams": {}, "sqltext": {}, "sqlquery": {}, "querytext": {},
	"rawquery": {}, "statementtext": {}, "resultrows": {}, "resultset": {}, "resultdata": {},
	"datarows": {}, "recordset": {}, "rawresult": {}, "rawresultdata": {}, "rawresponse": {},
	"response": {}, "responsebody": {}, "completion": {}, "completionbody": {},
	"modeloutput": {}, "modelresponse": {}, "requestbody": {}, "requestpayload": {},
	"resultpayload": {}, "answer": {}, "answertext": {}, "naturalanswer": {},
}

func auditJSONSafe(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			normalized := auditKeyNormalizer.ReplaceAllString(strings.ToLower(key), "")
			if _, forbidden := forbiddenAuditKeys[normalized]; forbidden || !auditJSONSafe(child) {
				return false
			}
		}
	case []any:
		for _, child := range typed {
			if !auditJSONSafe(child) {
				return false
			}
		}
	}
	return true
}

func normalizedEvidenceIDs(values []askdata.ID) ([]askdata.ID, error) {
	if len(values) > 64 {
		return nil, fmt.Errorf("%w: too many evidence IDs", ErrInvalidRun)
	}
	// Persisted PostgreSQL arrays round-trip an empty set as [] rather than
	// null. Canonicalize to an allocated empty slice before hashing so restart
	// replay produces the identical preimage.
	result := make([]askdata.ID, len(values))
	copy(result, values)
	for _, id := range result {
		if err := id.Validate(); err != nil {
			return nil, fmt.Errorf("%w: evidence ID is invalid", ErrInvalidRun)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	for index := 1; index < len(result); index++ {
		if result[index] == result[index-1] {
			return nil, fmt.Errorf("%w: evidence IDs must be unique", ErrInvalidRun)
		}
	}
	return result, nil
}

func equalIDs(left, right []askdata.ID) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func idsToStrings(values []askdata.ID) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = string(value)
	}
	return result
}

func stringsToIDs(values []string) []askdata.ID {
	result := make([]askdata.ID, len(values))
	for index, value := range values {
		result[index] = askdata.ID(value)
	}
	return result
}

func isConstraintRace(err error) bool {
	var pgError *pgconn.PgError
	return errors.As(err, &pgError) && (pgError.Code == "23505" || pgError.Code == "23514")
}

func mapPersistenceError(err error) error {
	if err == nil {
		return nil
	}
	for _, sentinel := range []error{
		ErrInvalidRun, ErrIllegalTransition, ErrTerminalRun, ErrVersionConflict,
		ErrRunNotFound, ErrIdempotencyConflict, ErrReplayCorrupt,
		ErrPinnedScopeMismatch, ErrInvalidAccessContext, ErrNoProgress,
	} {
		if errors.Is(err, sentinel) {
			return err
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var pgError *pgconn.PgError
	if errors.As(err, &pgError) {
		switch pgError.Code {
		case "40001":
			return ErrVersionConflict
		case "23514", "22023":
			return ErrInvalidRun
		case "55000":
			return ErrTerminalRun
		case "23503":
			return ErrPinnedScopeMismatch
		case "23505":
			if pgError.ConstraintName == "askdata_question_runs_idempotency_key" {
				return ErrIdempotencyConflict
			}
			return ErrReplayCorrupt
		case "42501":
			return ErrInvalidAccessContext
		}
	}
	return ErrPersistence
}
