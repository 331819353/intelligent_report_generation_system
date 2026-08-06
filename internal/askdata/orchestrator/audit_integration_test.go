package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/cognition"
	"intelligent-report-generation-system/internal/askdata/toolhost"
	"intelligent-report-generation-system/internal/platform/database"
)

func TestPostgresCheckpointLoopAtomicallyTerminalizesBudgetAndReplays(t *testing.T) {
	adminURL := os.Getenv("ASKDATA_INTEGRATION_ADMIN_DATABASE_URL")
	appURL := os.Getenv("ASKDATA_INTEGRATION_DATABASE_URL")
	if adminURL == "" || appURL == "" {
		t.Skip("set ASKDATA_INTEGRATION_ADMIN_DATABASE_URL and ASKDATA_INTEGRATION_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	adminPool, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer adminPool.Close()
	appConfig, err := pgxpool.ParseConfig(appURL)
	if err != nil || appConfig.ConnConfig.User == "" {
		t.Fatalf("parse app database role: %v", err)
	}
	root, err := adminPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Rollback(ctx) }()
	fixture := createRuntimeFixture(t, ctx, root)
	appRole := pgx.Identifier{appConfig.ConnConfig.User}.Sanitize()
	if _, err := root.Exec(ctx, "SET LOCAL ROLE "+appRole); err != nil {
		t.Fatal(err)
	}
	runner := func(ctx context.Context, _ pgx.TxOptions, tenantID string, fn func(pgx.Tx) error) error {
		nested, err := root.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = nested.Rollback(ctx) }()
		access, ok := database.AccessContextFromContext(ctx)
		if !ok {
			return ErrInvalidAccessContext
		}
		if _, err := nested.Exec(ctx, `SELECT
			set_config('app.tenant_id',$1,true),
			set_config('app.access_mode','USER',true),
			set_config('app.user_id',$2,true),
			set_config('app.domain_id',$3,true)`, tenantID, access.UserID, access.DomainID); err != nil {
			return err
		}
		if err := fn(nested); err != nil {
			return err
		}
		return nested.Commit(ctx)
	}
	store := newPostgresStoreWithRunner(runner)
	actorContext := database.WithAccessContext(ctx, fixture.actorID, fixture.domainID)
	scope := fixture.scope(t, fixture.actorID)
	created, err := store.CreateRun(actorContext, CreateRunRequest{
		Scope: scope, DomainID: askdata.ID(fixture.domainID),
		ConversationID:     askdata.ID(uuid.NewString()),
		IdempotencyKeyHash: testHash("d"), QuestionHash: testHash("e"),
	})
	if err != nil {
		t.Fatal(err)
	}
	run := advanceIntegrationTo(t, actorContext, store, scope, created.Run, StateUnderstanding)
	usage := run.Usage
	usage.StepCount, usage.LLMCallsUsed = run.Limits.MaxLLMCalls, run.Limits.MaxLLMCalls
	checkpoint, err := store.Transition(actorContext, TransitionRequest{
		Scope: scope, DomainID: run.DomainID, RunID: run.ID,
		ExpectedVersion: run.RecordVersion, TargetState: run.State, Usage: usage,
	})
	if err != nil {
		t.Fatal(err)
	}
	run = checkpoint.Run
	exhausted := run.Usage
	exhausted.Exhausted = true
	termination, err := BuildBudgetTermination(BudgetTerminationRequest{
		Run: run, Usage: exhausted, Reason: BudgetStopLLMCalls,
		EvidenceIDs: []askdata.ID{"budget-evidence"}, PreferClarification: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := LoopCheckpointRequest{
		Scope: scope, DomainID: run.DomainID, RunID: run.ID,
		ExpectedVersion: run.RecordVersion, CheckpointID: "budget-checkpoint-integration",
		Stage: cognition.StageUnderstanding, TargetState: termination.TargetState,
		Result: LoopResult{Usage: exhausted}, Failure: ClassifyLoopFailure(ErrLoopBudgetExhausted),
		Completion: &termination.Completion,
	}
	result, err := store.CheckpointLoop(actorContext, request)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Run.Terminal() || result.Run.State != StateClarificationRequired || !result.Run.Usage.Exhausted ||
		len(result.Snapshot.Artifacts) != 1 || len(result.Snapshot.ToolCalls) != 0 ||
		result.Snapshot.Events[len(result.Snapshot.Events)-2].Type != EventError ||
		result.Snapshot.Events[len(result.Snapshot.Events)-1].Type != EventStateTransition {
		t.Fatalf("persisted checkpoint = %#v / %#v", result.Run, result.Snapshot)
	}
	replayed, err := store.CheckpointLoop(actorContext, request)
	if err != nil || !replayed.Replayed || replayed.Run.RecordVersion != result.Run.RecordVersion ||
		len(replayed.Snapshot.Events) != len(result.Snapshot.Events) {
		t.Fatalf("exact checkpoint replay = %#v, %v", replayed, err)
	}
	collision := request
	collision.Failure = &LoopFailure{Code: "DIFFERENT_FAILURE", Status: EventFailed}
	if _, err := store.CheckpointLoop(actorContext, collision); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("checkpoint collision error = %v", err)
	}
}

func TestPostgresCheckpointLoopPersistsCognitionToolAndReplayGuards(t *testing.T) {
	adminURL := os.Getenv("ASKDATA_INTEGRATION_ADMIN_DATABASE_URL")
	appURL := os.Getenv("ASKDATA_INTEGRATION_DATABASE_URL")
	if adminURL == "" || appURL == "" {
		t.Skip("set ASKDATA_INTEGRATION_ADMIN_DATABASE_URL and ASKDATA_INTEGRATION_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	adminPool, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer adminPool.Close()
	appConfig, err := pgxpool.ParseConfig(appURL)
	if err != nil || appConfig.ConnConfig.User == "" {
		t.Fatalf("parse app database role: %v", err)
	}
	root, err := adminPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Rollback(ctx) }()
	fixture := createRuntimeFixture(t, ctx, root)
	aiRequestIDs := []askdata.ID{
		insertQuestionAIRequest(t, ctx, root, fixture, "cognition-a"),
		insertQuestionAIRequest(t, ctx, root, fixture, "cognition-b"),
	}
	appRole := pgx.Identifier{appConfig.ConnConfig.User}.Sanitize()
	if _, err := root.Exec(ctx, "SET LOCAL ROLE "+appRole); err != nil {
		t.Fatal(err)
	}
	runner := func(ctx context.Context, _ pgx.TxOptions, tenantID string, fn func(pgx.Tx) error) error {
		nested, err := root.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = nested.Rollback(ctx) }()
		access, ok := database.AccessContextFromContext(ctx)
		if !ok {
			return ErrInvalidAccessContext
		}
		if _, err := nested.Exec(ctx, `SELECT
			set_config('app.tenant_id',$1,true),
			set_config('app.access_mode','USER',true),
			set_config('app.user_id',$2,true),
			set_config('app.domain_id',$3,true)`, tenantID, access.UserID, access.DomainID); err != nil {
			return err
		}
		if err := fn(nested); err != nil {
			return err
		}
		return nested.Commit(ctx)
	}
	store := newPostgresStoreWithRunner(runner)
	actorContext := database.WithAccessContext(ctx, fixture.actorID, fixture.domainID)
	scope := fixture.scope(t, fixture.actorID)
	created, err := store.CreateRun(actorContext, CreateRunRequest{
		Scope: scope, DomainID: askdata.ID(fixture.domainID),
		ConversationID:     askdata.ID(uuid.NewString()),
		IdempotencyKeyHash: testHash("7"), QuestionHash: testHash("8"),
	})
	if err != nil {
		t.Fatal(err)
	}
	run := advanceIntegrationTo(t, actorContext, store, scope, created.Run, StateRetrieving)
	fact, err := cognition.NewPromptFact(
		"conversation-audit-integration", cognition.FactConversation,
		json.RawMessage(`{"questionSummary":"销售额"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	conversation := askdata.EvidenceRef{
		EvidenceID: fact.EvidenceID, Kind: askdata.EvidenceKindConversation,
		SourceID: run.ID, ContentHash: fact.ContentHash,
	}
	loopRequest := LoopRequest{
		Run: run, Stage: cognition.StageCandidateJudgment,
		Facts: []GovernedFact{{Fact: fact, Evidence: conversation}},
		Authorization: toolhost.AuthorizationContext{
			Scope: scope, DomainID: run.DomainID,
			Permissions: []toolhost.Permission{
				toolhost.PermissionSemanticRead, toolhost.PermissionClarificationRequest,
			},
		},
	}
	toolEvidence := loopEvidence("search-tool-result", askdata.EvidenceKindCandidateSet)
	loopRunner := &scriptedLoopCognition{actions: []cognition.Action{
		searchToolAction(loopRequest, conversation),
		bindingAction(cognition.StageCandidateJudgment, toolEvidence),
	}}
	loopTools := &fakeLoopTools{
		available: []toolhost.ToolName{toolhost.ToolSearchSemanticObjects},
		evidence:  toolEvidence, progress: true,
	}
	loop, _ := NewLoop(loopRunner, loopTools)
	loopResult, err := loop.Run(actorContext, loopRequest)
	if err != nil {
		t.Fatal(err)
	}
	for index := range loopResult.CognitionRounds {
		loopResult.CognitionRounds[index].Round.AIRequestID = string(aiRequestIDs[index])
	}
	loopResult.Decision.AIRequestID = string(aiRequestIDs[len(aiRequestIDs)-1])
	checkpointRequest := LoopCheckpointRequest{
		Scope: scope, DomainID: run.DomainID, RunID: run.ID,
		ExpectedVersion: run.RecordVersion, CheckpointID: "tool-checkpoint-integration",
		Stage: cognition.StageCandidateJudgment, TargetState: StateBinding, Result: loopResult,
	}
	checkpoint, err := store.CheckpointLoop(actorContext, checkpointRequest)
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.Run.State != StateBinding || len(checkpoint.Snapshot.ToolCalls) != 1 ||
		len(checkpoint.Snapshot.Artifacts) != 1 ||
		checkpoint.Snapshot.ToolCalls[0].CallID != "call-search-loop" ||
		len(checkpoint.Snapshot.SeenActionHashes()) != 2 ||
		len(checkpoint.Snapshot.SeenToolCallIDs()) != 1 {
		t.Fatalf("audited loop replay = %#v", checkpoint.Snapshot)
	}
	if checkpointSummaryContainsUnsafeText(checkpoint.Snapshot.Artifacts[0].Payload) {
		t.Fatalf("unsafe replay artifact: %s", checkpoint.Snapshot.Artifacts[0].Payload)
	}
	replayedExecution, found, err := ReplayToolExecution(checkpoint.Snapshot, "call-search-loop")
	if err != nil || !found || replayedExecution.Response.ResultHash != loopResult.ToolExecutions[0].Response.ResultHash ||
		replayedExecution.DefinitionHash != loopResult.ToolExecutions[0].DefinitionHash ||
		replayedExecution.Charge != loopResult.ToolExecutions[0].Charge {
		t.Fatalf("replayed tool execution = %#v, %v/%v", replayedExecution, found, err)
	}
	replayed, err := store.CheckpointLoop(actorContext, checkpointRequest)
	if err != nil || !replayed.Replayed || len(replayed.Snapshot.ToolCalls) != 1 {
		t.Fatalf("tool checkpoint replay = %#v, %v", replayed, err)
	}

	replayRunner := &scriptedLoopCognition{actions: []cognition.Action{
		searchToolAction(LoopRequest{Run: checkpoint.Run, Stage: cognition.StageCandidateJudgment}, conversation),
	}}
	replayTools := &fakeLoopTools{available: []toolhost.ToolName{toolhost.ToolSearchSemanticObjects}}
	replayLoop, _ := NewLoop(replayRunner, replayTools)
	loopRequest.Run = checkpoint.Run
	loopRequest, err = BindReplayGuards(checkpoint.Snapshot, loopRequest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := replayLoop.Run(actorContext, loopRequest); !errors.Is(err, ErrLoopNoProgress) || len(replayTools.calls) != 0 {
		t.Fatalf("replayed completed tool error/calls = %v/%d", err, len(replayTools.calls))
	}
}

func insertQuestionAIRequest(
	t *testing.T,
	ctx context.Context,
	tx pgx.Tx,
	fixture runtimeFixture,
	resourceID string,
) askdata.ID {
	t.Helper()
	var id string
	err := tx.QueryRow(ctx, `INSERT INTO platform.ai_requests(
		tenant_id,actor_user_id,purpose,resource_type,resource_id,provider,
		model_name,provider_model,prompt_version,input_hash,input_bytes,
		redaction_count,reserved_tokens,reserved_cost_micros,max_attempts,attempts,
		status,finish_reason,prompt_tokens,completion_tokens,total_tokens,cost_micros,
		latency_ms,completed_at
	) VALUES(
		$1,$2,'SEMANTIC_QUESTION','ASKDATA_QUESTION_RUN',$3,'synthetic-provider',
		'synthetic-model','synthetic-model','integration-v1',$4,10,0,15,0,1,1,
		'SUCCEEDED','stop',10,5,15,0,1,now()
	) RETURNING id::text`, fixture.tenantID, fixture.actorID, resourceID, testHash("9")).Scan(&id)
	if err != nil {
		t.Fatalf("insert question AI request: %v", err)
	}
	return askdata.ID(id)
}
