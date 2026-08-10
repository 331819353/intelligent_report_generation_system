package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/answer"
	"intelligent-report-generation-system/internal/askdata/registry"
	"intelligent-report-generation-system/internal/platform/database"
)

func TestPostgresStoreResumeTransactionIsRepeatableReadOnly(t *testing.T) {
	appURL := os.Getenv("ASKDATA_INTEGRATION_DATABASE_URL")
	if appURL == "" {
		t.Skip("set ASKDATA_INTEGRATION_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatalf("open app pool: %v", err)
	}
	defer pool.Close()
	store := NewPostgresStore(pool)
	tenantID, actorID, domainID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	ctx = database.WithAccessContext(ctx, actorID, domainID)
	var isolation, readOnly string
	err = store.withActorTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly,
	}, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SHOW transaction_isolation`).Scan(&isolation); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SHOW transaction_read_only`).Scan(&readOnly)
	})
	if err != nil {
		t.Fatalf("repeatable read-only transaction: %v", err)
	}
	if isolation != "repeatable read" || readOnly != "on" {
		t.Fatalf("transaction mode = %q/%q", isolation, readOnly)
	}
}

func TestQuestionStateMatrixMatchesPostgres(t *testing.T) {
	adminURL := os.Getenv("ASKDATA_INTEGRATION_ADMIN_DATABASE_URL")
	if adminURL == "" {
		t.Skip("set ASKDATA_INTEGRATION_ADMIN_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatalf("open admin pool: %v", err)
	}
	defer pool.Close()
	states := []State{
		StateReceived, StateAuthorized, StateContextReady, StateUnderstanding,
		StateRetrieving, StateBinding, StateGraphValidating, StateIRReady,
		StatePlanValidating, StateExecuting, StateResultVerifying, StateAnswerVerifying,
		StateClarificationRequired, StateClarificationExpired, StateOutOfScope, StateAnswered, StateBlocked,
	}
	for _, from := range states {
		for _, to := range states {
			var databaseAllows bool
			if err := pool.QueryRow(ctx,
				`SELECT askdata.valid_question_run_transition($1,$2)`, from, to,
			).Scan(&databaseAllows); err != nil {
				t.Fatalf("database transition %s -> %s: %v", from, to, err)
			}
			if goAllows := CanTransition(from, to); goAllows != databaseAllows {
				t.Errorf("transition %s -> %s: Go=%v database=%v", from, to, goAllows, databaseAllows)
			}
		}
	}
}

func TestPostgresStoreQuestionLifecycleResumeAndPinnedRelease(t *testing.T) {
	adminURL := os.Getenv("ASKDATA_INTEGRATION_ADMIN_DATABASE_URL")
	appURL := os.Getenv("ASKDATA_INTEGRATION_DATABASE_URL")
	if adminURL == "" || appURL == "" {
		t.Skip("set ASKDATA_INTEGRATION_ADMIN_DATABASE_URL and ASKDATA_INTEGRATION_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	adminPool, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatalf("open admin pool: %v", err)
	}
	defer adminPool.Close()
	appConfig, err := pgxpool.ParseConfig(appURL)
	if err != nil || appConfig.ConnConfig.User == "" {
		t.Fatalf("parse app database role: %v", err)
	}

	root, err := adminPool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin rollback fixture: %v", err)
	}
	defer func() { _ = root.Rollback(ctx) }()
	fixture := createRuntimeFixture(t, ctx, root)
	appRole := pgx.Identifier{appConfig.ConnConfig.User}.Sanitize()
	if _, err := root.Exec(ctx, "SET LOCAL ROLE "+appRole); err != nil {
		t.Fatalf("set app role: %v", err)
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
	projectionGuard := &integrationProjectionGuard{}
	store := newPostgresStoreWithRunnerAndProjectionGuard(runner, projectionGuard)
	actorContext := database.WithAccessContext(ctx, fixture.actorID, fixture.domainID)
	scope := fixture.scope(t, fixture.actorID)
	request := CreateRunRequest{
		Scope: scope, DomainID: askdata.ID(fixture.domainID),
		ConversationID:     askdata.ID(uuid.NewString()),
		IdempotencyKeyHash: testHash("b"), QuestionHash: testHash("c"),
	}

	created, err := store.CreateRun(actorContext, request)
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	if created.Replayed || created.Run.State != StateReceived || created.Run.RecordVersion != 1 ||
		created.Run.Release != scope.Release || created.Run.PolicyScopeHash != scope.PolicyHash {
		t.Fatalf("created run = %#v", created)
	}
	replayed, err := store.CreateRun(actorContext, request)
	if err != nil || !replayed.Replayed || replayed.Run.ID != created.Run.ID {
		t.Fatalf("exact CreateRun replay = %#v, %v", replayed, err)
	}
	continuation := request
	continuation.IdempotencyKeyHash, continuation.QuestionHash = testHash("a"), testHash("2")
	if continued, err := store.CreateRun(actorContext, continuation); err != nil || continued.Replayed ||
		continued.Run.Release != scope.Release {
		t.Fatalf("same-release conversation continuation = %#v, %v", continued, err)
	}
	driftedScope, err := askdata.NewPolicyScope(
		scope.TenantID, scope.ActorID, scope.DomainIDs, scope.RoleIDs,
		askdata.ReleaseRef{ReleaseID: askdata.ID(uuid.NewString()), ContentHash: testHash("9")},
	)
	if err != nil {
		t.Fatalf("create drifted scope: %v", err)
	}
	drifted := request
	drifted.Scope = driftedScope
	drifted.IdempotencyKeyHash, drifted.QuestionHash = testHash("0"), testHash("1")
	if _, err := store.CreateRun(actorContext, drifted); !errors.Is(err, ErrReleaseNotRunnable) {
		t.Fatalf("non-ACTIVE pre-pin release error = %v", err)
	}
	collision := request
	collision.QuestionHash = testHash("d")
	if _, err := store.CreateRun(actorContext, collision); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("idempotency collision error = %v", err)
	}
	if _, err := store.CreateRun(context.Background(), request); !errors.Is(err, ErrInvalidAccessContext) {
		t.Fatalf("missing access context error = %v", err)
	}

	snapshot, err := store.Resume(actorContext, ResumeRequest{Scope: scope, DomainID: request.DomainID, RunID: created.Run.ID})
	if err != nil || len(snapshot.Events) != 1 || snapshot.Events[0].Index != 1 {
		t.Fatalf("initial Resume() = %#v, %v", snapshot, err)
	}
	seedRequest := request
	seedRequest.ConversationID = askdata.ID(uuid.NewString())
	seedRequest.IdempotencyKeyHash, seedRequest.QuestionHash = testHash("c"), testHash("d")
	seedRequest.SeedContext = &SeedContext{
		Source: SeedSourceSavedQuestion, SavedQuestionID: askdata.ID(fixture.savedQuestionID),
		SemanticIR: fixture.savedQuestionIR, SemanticIRHash: askdata.ContentHash(fixture.savedQuestionIRHash),
		PinnedReleaseID: askdata.ID(fixture.releaseID),
	}
	seeded, err := store.CreateRun(actorContext, seedRequest)
	if err != nil {
		t.Fatalf("create saved-question seeded run: %v", err)
	}
	seededSnapshot, err := store.Resume(actorContext, ResumeRequest{
		Scope: scope, DomainID: seedRequest.DomainID, RunID: seeded.Run.ID,
	})
	if err != nil || seededSnapshot.Seed == nil ||
		seededSnapshot.Seed.Source != SeedSourceSavedQuestion ||
		seededSnapshot.Seed.SavedQuestionID != askdata.ID(fixture.savedQuestionID) ||
		seededSnapshot.Seed.SemanticIRHash != askdata.ContentHash(fixture.savedQuestionIRHash) {
		t.Fatalf("saved-question seed replay = %#v, %v", seededSnapshot.Seed, err)
	}
	observerSeed := seedRequest
	observerSeed.Scope = fixture.scope(t, fixture.observerID)
	observerSeed.ConversationID = askdata.ID(uuid.NewString())
	observerSeed.IdempotencyKeyHash = testHash("f")
	if _, err := store.CreateRun(
		database.WithAccessContext(ctx, fixture.observerID, fixture.domainID), observerSeed,
	); err == nil {
		t.Fatal("private saved question accepted a different viewer seed")
	}
	if _, err := store.Transition(actorContext, TransitionRequest{
		Scope: scope, DomainID: request.DomainID, RunID: created.Run.ID,
		ExpectedVersion: created.Run.RecordVersion, TargetState: StateExecuting, Usage: created.Run.Usage,
	}); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("illegal Transition() error = %v", err)
	}
	unchanged, err := store.Resume(actorContext, ResumeRequest{Scope: scope, DomainID: request.DomainID, RunID: created.Run.ID})
	if err != nil || unchanged.Run.RecordVersion != 1 || len(unchanged.Events) != 1 {
		t.Fatalf("illegal transition changed persistence: %#v, %v", unchanged, err)
	}

	authorized := transitionIntegration(t, actorContext, store, scope, created.Run, StateAuthorized, HashUpdates{}, nil)
	if _, err := store.Transition(actorContext, TransitionRequest{
		Scope: scope, DomainID: request.DomainID, RunID: authorized.ID,
		ExpectedVersion: created.Run.RecordVersion, TargetState: StateContextReady, Usage: authorized.Usage,
	}); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale Transition() error = %v", err)
	}
	contextReady := transitionIntegration(t, actorContext, store, scope, authorized, StateContextReady, HashUpdates{}, nil)
	if projectionGuard.calls != 1 || projectionGuard.lastReleaseID != fixture.releaseID {
		t.Fatalf("projection guard calls = %d, release = %s", projectionGuard.calls, projectionGuard.lastReleaseID)
	}

	mismatchRequest := request
	mismatchRequest.ConversationID = askdata.ID(uuid.NewString())
	mismatchRequest.IdempotencyKeyHash, mismatchRequest.QuestionHash = testHash("d"), testHash("3")
	mismatchCreated, err := store.CreateRun(actorContext, mismatchRequest)
	if err != nil {
		t.Fatalf("create projection mismatch run: %v", err)
	}
	mismatchAuthorized := transitionIntegration(
		t, actorContext, store, scope, mismatchCreated.Run, StateAuthorized, HashUpdates{}, nil,
	)
	projectionGuard.err = &registry.ReleaseProjectionMismatchError{
		Code: registry.ReleaseProjectionMismatchCode, ReleaseID: fixture.releaseID,
		ReleaseStatus: "ACTIVE", ContentHash: fixture.releaseHash,
		Mismatches: []registry.ProjectionMismatch{{
			Projection: "GRAPH", Expected: fixture.releaseHash,
			Applied: string(testHash("9")), Status: "FAILED",
			LastUpdated: time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC),
		}},
	}
	if _, err := store.Transition(actorContext, TransitionRequest{
		Scope: scope, DomainID: request.DomainID, RunID: mismatchAuthorized.ID,
		ExpectedVersion: mismatchAuthorized.RecordVersion,
		TargetState:     StateContextReady, Usage: mismatchAuthorized.Usage,
	}); !errors.Is(err, ErrReleaseProjectionMismatch) {
		t.Fatalf("projection mismatch Transition() error = %v", err)
	}
	mismatchSnapshot, err := store.Resume(actorContext, ResumeRequest{
		Scope: scope, DomainID: request.DomainID, RunID: mismatchAuthorized.ID,
	})
	if err != nil {
		t.Fatalf("resume projection mismatch run: %v", err)
	}
	lastMismatchEvent := mismatchSnapshot.Events[len(mismatchSnapshot.Events)-1]
	if mismatchSnapshot.Run.State != StateAuthorized ||
		lastMismatchEvent.Type != EventError || lastMismatchEvent.Status != EventBlocked ||
		lastMismatchEvent.Code != registry.ReleaseProjectionMismatchCode ||
		!strings.Contains(string(lastMismatchEvent.Details), `"projection":"GRAPH"`) {
		t.Fatalf("projection mismatch snapshot = %#v / %s", mismatchSnapshot.Run, lastMismatchEvent.Details)
	}
	projectionGuard.err = nil
	understandingHash := testHash("1")
	understanding := transitionIntegration(t, actorContext, store, scope, contextReady, StateUnderstanding,
		HashUpdates{Understanding: &understandingHash}, nil)
	usage := understanding.Usage
	usage.StepCount, usage.LLMCallsUsed, usage.ElapsedMS = 1, 1, 7
	retrievingResult, err := store.Transition(actorContext, TransitionRequest{
		Scope: scope, DomainID: request.DomainID, RunID: understanding.ID,
		ExpectedVersion: understanding.RecordVersion, TargetState: StateRetrieving,
		Usage: usage, Event: TransitionEventInput{Details: json.RawMessage(`{"evidenceCount":1}`)},
	})
	if err != nil {
		t.Fatalf("transition to RETRIEVING: %v", err)
	}
	retrieving := retrievingResult.Run
	completion := &CompletionArtifactInput{
		Code: "POLICY_BLOCK", Type: ArtifactBlock, SchemaVersion: "block-v1",
		EvidenceIDs: []askdata.ID{"policy-evidence"}, Payload: json.RawMessage(`{"code":"POLICY_BLOCK"}`),
	}
	blocked := transitionIntegration(t, actorContext, store, scope, retrieving, StateBlocked, HashUpdates{}, completion)
	if !blocked.Terminal() || blocked.CompletedAt == nil || blocked.CompletionArtifact == "" {
		t.Fatalf("blocked run = %#v", blocked)
	}
	snapshot, err = store.Resume(actorContext, ResumeRequest{Scope: scope, DomainID: request.DomainID, RunID: blocked.ID})
	if err != nil || len(snapshot.Events) != 6 || len(snapshot.Artifacts) != 1 ||
		snapshot.Artifacts[0].RunVersion != blocked.RecordVersion-1 ||
		snapshot.Events[len(snapshot.Events)-1].RunVersion != blocked.RecordVersion {
		t.Fatalf("terminal Resume() = events:%d artifacts:%d run:%#v err:%v",
			len(snapshot.Events), len(snapshot.Artifacts), snapshot.Run, err)
	}
	if _, err := store.Transition(actorContext, TransitionRequest{
		Scope: scope, DomainID: request.DomainID, RunID: blocked.ID,
		ExpectedVersion: blocked.RecordVersion, TargetState: StateBlocked, Usage: blocked.Usage,
	}); !errors.Is(err, ErrTerminalRun) {
		t.Fatalf("terminal Transition() error = %v", err)
	}

	observerScope := fixture.scope(t, fixture.observerID)
	observerContext := database.WithAccessContext(ctx, fixture.observerID, fixture.domainID)
	if _, err := store.Resume(observerContext, ResumeRequest{
		Scope: observerScope, DomainID: request.DomainID, RunID: blocked.ID,
	}); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("cross-actor Resume() error = %v", err)
	}

	answerRequest := request
	answerRequest.ConversationID = askdata.ID(uuid.NewString())
	answerRequest.IdempotencyKeyHash, answerRequest.QuestionHash = testHash("e"), testHash("f")
	answerCreated, err := store.CreateRun(actorContext, answerRequest)
	if err != nil {
		t.Fatalf("create answer run: %v", err)
	}
	answerReady := advanceIntegrationTo(t, actorContext, store, scope, answerCreated.Run, StateResultVerifying)
	answerReady = transitionIntegration(t, actorContext, store, scope, answerReady, StateAnswerVerifying, HashUpdates{}, nil)
	answerPayload := answerArtifactPayload(t, answerReady.ID, askdata.ID(fixture.releaseID))
	answered := transitionIntegration(t, actorContext, store, scope, answerReady, StateAnswered, HashUpdates{},
		&CompletionArtifactInput{
			Code: "ANSWER_READY", Type: ArtifactAnswer, SchemaVersion: answer.SchemaVersion,
			EvidenceIDs: []askdata.ID{"answer-evidence"}, Payload: answerPayload,
		})
	answerSnapshot, err := store.Resume(actorContext, ResumeRequest{
		Scope: scope, DomainID: request.DomainID, RunID: answered.ID,
	})
	if err != nil || answered.State != StateAnswered || len(answerSnapshot.Events) != 13 ||
		len(answerSnapshot.Artifacts) != 1 {
		t.Fatalf("answered replay = %#v, events=%d artifacts=%d, err=%v",
			answered, len(answerSnapshot.Events), len(answerSnapshot.Artifacts), err)
	}
	decodedAnswer, err := answer.Decode(answerSnapshot.Artifacts[0].Payload)
	if err != nil || decodedAnswer.RunID != answered.ID {
		t.Fatalf("persisted Answer Artifact = %#v, %v", decodedAnswer, err)
	}
	immutabilityTx, err := root.Begin(ctx)
	if err != nil {
		t.Fatalf("begin Answer Artifact immutability check: %v", err)
	}
	if _, err := immutabilityTx.Exec(ctx, "RESET ROLE"); err != nil {
		_ = immutabilityTx.Rollback(ctx)
		t.Fatalf("reset app role before immutability check: %v", err)
	}
	if _, err := immutabilityTx.Exec(ctx, `UPDATE askdata.question_artifacts SET payload_json='{}'::jsonb
		WHERE tenant_id=$1 AND id=$2`, fixture.tenantID, answerSnapshot.Artifacts[0].ID); err == nil {
		_ = immutabilityTx.Rollback(ctx)
		t.Fatal("persisted Answer Artifact allowed an in-place update")
	}
	_ = immutabilityTx.Rollback(ctx)
	var pinnedReleaseID string
	if err := root.QueryRow(ctx, `SELECT pinned_release_id::text
		FROM askdata.conversations WHERE tenant_id=$1 AND id=$2`,
		fixture.tenantID, answerRequest.ConversationID).Scan(&pinnedReleaseID); err != nil ||
		pinnedReleaseID != fixture.releaseID {
		t.Fatalf("conversation pin after successful binding = %q, %v", pinnedReleaseID, err)
	}

	clarifyRequest := request
	clarifyRequest.ConversationID = askdata.ID(uuid.NewString())
	clarifyRequest.IdempotencyKeyHash, clarifyRequest.QuestionHash = testHash("7"), testHash("8")
	clarifyCreated, err := store.CreateRun(actorContext, clarifyRequest)
	if err != nil {
		t.Fatalf("create clarification run: %v", err)
	}
	understood := advanceIntegrationTo(t, actorContext, store, scope, clarifyCreated.Run, StateUnderstanding)
	clarified := transitionIntegration(t, actorContext, store, scope, understood, StateClarificationRequired,
		HashUpdates{}, &CompletionArtifactInput{
			Code: "NEEDS_CLARIFICATION", Type: ArtifactClarification, SchemaVersion: "clarification-v1",
			Payload: json.RawMessage(`{"code":"NEEDS_CLARIFICATION"}`),
		})
	clarificationSnapshot, err := store.Resume(actorContext, ResumeRequest{
		Scope: scope, DomainID: request.DomainID, RunID: clarified.ID,
	})
	if err != nil || clarificationSnapshot.Run.State != StateClarificationRequired ||
		len(clarificationSnapshot.Artifacts) != 1 || clarificationSnapshot.Run.ClarificationDeadline == nil ||
		clarificationSnapshot.Run.BudgetFrozenAt == nil || clarificationSnapshot.Run.BudgetConsumed == nil {
		t.Fatalf("clarification replay = %#v, %v", clarificationSnapshot, err)
	}
	expired, err := store.ExpireClarification(actorContext, ResumeRequest{
		Scope: scope, DomainID: request.DomainID, RunID: clarified.ID,
	}, clarificationSnapshot.Run.ClarificationDeadline.Add(time.Nanosecond))
	if err != nil || !expired {
		t.Fatalf("ExpireClarification() = %v, %v", expired, err)
	}
	expiredSnapshot, err := store.Resume(actorContext, ResumeRequest{
		Scope: scope, DomainID: request.DomainID, RunID: clarified.ID,
	})
	if err != nil || expiredSnapshot.Run.State != StateClarificationExpired ||
		expiredSnapshot.Run.CompletionCode != "CLARIFICATION_EXPIRED" ||
		len(expiredSnapshot.Artifacts) != 1 ||
		expiredSnapshot.Run.CompletionArtifact != clarificationSnapshot.Run.CompletionArtifact {
		t.Fatalf("expired clarification replay = %#v, %v", expiredSnapshot, err)
	}

	planCorrectionRequest := request
	planCorrectionRequest.ConversationID = askdata.ID(uuid.NewString())
	planCorrectionRequest.IdempotencyKeyHash, planCorrectionRequest.QuestionHash = testHash("9"), testHash("0")
	planCreated, err := store.CreateRun(actorContext, planCorrectionRequest)
	if err != nil {
		t.Fatalf("create plan-correction run: %v", err)
	}
	planValidating := advanceIntegrationTo(t, actorContext, store, scope, planCreated.Run, StatePlanValidating)
	planCorrected := transitionIntegration(t, actorContext, store, scope, planValidating, StateBinding, HashUpdates{}, nil)
	if snapshot, err := store.Resume(actorContext, ResumeRequest{
		Scope: scope, DomainID: request.DomainID, RunID: planCorrected.ID,
	}); err != nil || snapshot.Run.Hashes.Understanding == "" ||
		snapshot.Run.Hashes.BindingBundle != "" || snapshot.Run.Hashes.GraphPlan != "" ||
		snapshot.Run.Hashes.SemanticIR != "" || snapshot.Run.Hashes.QueryPlan != "" || snapshot.Run.Hashes.Result != "" {
		t.Fatalf("PLAN correction replay = %#v, %v", snapshot.Run, err)
	}

	resultCorrectionRequest := request
	resultCorrectionRequest.ConversationID = askdata.ID(uuid.NewString())
	resultCorrectionRequest.IdempotencyKeyHash, resultCorrectionRequest.QuestionHash = testHash("4"), testHash("5")
	resultCreated, err := store.CreateRun(actorContext, resultCorrectionRequest)
	if err != nil {
		t.Fatalf("create result-correction run: %v", err)
	}
	resultVerifying := advanceIntegrationTo(t, actorContext, store, scope, resultCreated.Run, StateResultVerifying)
	resultCorrected := transitionIntegration(t, actorContext, store, scope, resultVerifying, StateBinding, HashUpdates{}, nil)
	if snapshot, err := store.Resume(actorContext, ResumeRequest{
		Scope: scope, DomainID: request.DomainID, RunID: resultCorrected.ID,
	}); err != nil || snapshot.Run.Hashes.Understanding == "" ||
		snapshot.Run.Hashes.BindingBundle != "" || snapshot.Run.Hashes.Result != "" {
		t.Fatalf("RESULT correction replay = %#v, %v", snapshot.Run, err)
	}

	// Existing runs must remain resumable and idempotently creatable after their
	// pinned release stops being the active release. A genuinely new run may not
	// bind that historical release.
	if _, err := root.Exec(ctx, "RESET ROLE"); err != nil {
		t.Fatalf("reset app role: %v", err)
	}
	referenceID := uuid.NewString()
	if _, err := root.Exec(ctx, `INSERT INTO askdata.release_references(
		tenant_id,release_id,reference_type,reference_id,reference_name,owner_id
	) VALUES($1,$2,'REPORT_VERSION',$3,'historical report',$4)`,
		fixture.tenantID, fixture.releaseID, referenceID, fixture.actorID); err != nil {
		t.Fatalf("reference pinned release: %v", err)
	}
	if _, err := root.Exec(ctx, `UPDATE askdata.releases SET status='SUPERSEDED'
		WHERE id=$1 AND tenant_id=$2`, fixture.releaseID, fixture.tenantID); err != nil {
		t.Fatalf("supersede pinned release: %v", err)
	}
	var historicalStatus string
	if err := root.QueryRow(ctx, `SELECT status FROM askdata.releases WHERE id=$1`,
		fixture.releaseID).Scan(&historicalStatus); err != nil || historicalStatus != "RETAINED" {
		t.Fatalf("referenced superseded release status = %q, %v", historicalStatus, err)
	}
	if _, err := root.Exec(ctx, "SET LOCAL ROLE "+appRole); err != nil {
		t.Fatalf("restore app role: %v", err)
	}
	replayed, err = store.CreateRun(actorContext, request)
	if err != nil || !replayed.Replayed || replayed.Run.Release != scope.Release {
		t.Fatalf("historical exact replay = %#v, %v", replayed, err)
	}
	if _, err := store.Resume(actorContext, ResumeRequest{Scope: scope, DomainID: request.DomainID, RunID: blocked.ID}); err != nil {
		t.Fatalf("resume superseded pin: %v", err)
	}
	newRequest := request
	newRequest.IdempotencyKeyHash = testHash("6")
	newRequest.QuestionHash = testHash("a")
	if _, err := store.CreateRun(actorContext, newRequest); !errors.Is(err, ErrReleaseNotRunnable) {
		t.Fatalf("new run on retained release error = %v", err)
	}
}

func answerArtifactPayload(t *testing.T, runID, releaseID askdata.ID) json.RawMessage {
	t.Helper()
	artifact := answer.AnswerArtifact{
		SchemaVersion: answer.SchemaVersion,
		RunID:         runID,
		Layers: answer.AnswerLayers{
			Structured: answer.StructuredLayer{
				Headline: &answer.MetricValue{
					MetricVersionID: "metric:sales@v1", Value: "1280000.00", Unit: "CNY",
					Label: "销售额", ColumnKey: "sales_amount",
				},
				Cards: []answer.MetricValue{}, ChartSpec: nil, TableRef: "result:artifact:v1",
			},
			Narrative: answer.NarrativeLayer{Findings: []string{}, Citations: nil},
		},
		Verification: answer.Verification{
			VerifierVersion: "1.0.0", PolicyWordlistVersion: "1.0.0",
			Attempts: 2, Passed: false, Degraded: true,
		},
		Provenance: answer.Provenance{
			PromptVersion: "answer-v3", ModelPolicy: "narrative-standard",
			EvidenceHash:      askdata.HashBytes([]byte("answer evidence")),
			ResultHash:        askdata.HashBytes([]byte("answer result")),
			SemanticReleaseID: releaseID, ChartRuleVersion: "1.0.0",
		},
	}
	raw, err := artifact.MarshalCanonical()
	if err != nil {
		t.Fatalf("marshal Answer Artifact: %v", err)
	}
	return raw
}

func advanceIntegrationTo(
	t *testing.T, ctx context.Context, store *PostgresStore, scope askdata.PolicyScope,
	run Run, target State,
) Run {
	t.Helper()
	understandingHash, bindingHash := testHash("1"), testHash("2")
	graphHash, semanticIRHash := testHash("3"), testHash("4")
	queryPlanHash, resultHash := testHash("5"), testHash("6")
	steps := []struct {
		state  State
		hashes HashUpdates
	}{
		{StateAuthorized, HashUpdates{}},
		{StateContextReady, HashUpdates{}},
		{StateUnderstanding, HashUpdates{Understanding: &understandingHash}},
		{StateRetrieving, HashUpdates{}},
		{StateBinding, HashUpdates{BindingBundle: &bindingHash}},
		{StateGraphValidating, HashUpdates{GraphPlan: &graphHash}},
		{StateIRReady, HashUpdates{SemanticIR: &semanticIRHash}},
		{StatePlanValidating, HashUpdates{QueryPlan: &queryPlanHash}},
		{StateExecuting, HashUpdates{}},
		{StateResultVerifying, HashUpdates{Result: &resultHash}},
		{StateAnswerVerifying, HashUpdates{}},
	}
	for _, step := range steps {
		run = transitionIntegration(t, ctx, store, scope, run, step.state, step.hashes, nil)
		if run.State == target {
			return run
		}
	}
	t.Fatalf("unsupported integration target %s", target)
	return Run{}
}

func transitionIntegration(
	t *testing.T, ctx context.Context, store *PostgresStore, scope askdata.PolicyScope,
	run Run, target State, hashes HashUpdates, completion *CompletionArtifactInput,
) Run {
	t.Helper()
	result, err := store.Transition(ctx, TransitionRequest{
		Scope: scope, DomainID: run.DomainID, RunID: run.ID,
		ExpectedVersion: run.RecordVersion, TargetState: target, Usage: run.Usage,
		Hashes: hashes, Completion: completion,
	})
	if err != nil {
		t.Fatalf("Transition(%s -> %s) error = %v", run.State, target, err)
	}
	return result.Run
}

type runtimeFixture struct {
	tenantID, actorID, observerID, domainID, releaseID, releaseHash string
	savedQuestionID, savedQuestionIRHash                            string
	savedQuestionIR                                                 json.RawMessage
}

func (fixture runtimeFixture) scope(t *testing.T, actorID string) askdata.PolicyScope {
	t.Helper()
	scope, err := askdata.NewPolicyScope(
		askdata.ID(fixture.tenantID), askdata.ID(actorID),
		[]askdata.ID{askdata.ID(fixture.domainID)}, []askdata.ID{askdata.ID(uuid.NewString())},
		askdata.ReleaseRef{ReleaseID: askdata.ID(fixture.releaseID), ContentHash: askdata.ContentHash(fixture.releaseHash)},
	)
	if err != nil {
		t.Fatalf("create fixture policy scope: %v", err)
	}
	return scope
}

func createRuntimeFixture(t *testing.T, ctx context.Context, tx pgx.Tx) runtimeFixture {
	t.Helper()
	fixture := runtimeFixture{releaseHash: string(testHash("a"))}
	suffix := uuid.NewString()[:8]
	if err := tx.QueryRow(ctx, `INSERT INTO platform.tenants(code,name)
		VALUES($1,$2) RETURNING id::text`, "orch_"+suffix, "orchestrator integration "+suffix).Scan(&fixture.tenantID); err != nil {
		t.Fatalf("insert fixture tenant: %v", err)
	}
	insertUser := func(employee, email string) string {
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO platform.users(
			tenant_id,employee_no,email,display_name,password_hash,status
		) VALUES($1,$2,$3,$4,$5,'ACTIVE') RETURNING id::text`,
			fixture.tenantID, employee, email, employee,
			"integration-only-not-a-login-secret").Scan(&id); err != nil {
			t.Fatalf("insert fixture user: %v", err)
		}
		return id
	}
	fixture.actorID = insertUser("ORCHA"+suffix, "orch.a."+suffix+"@example.invalid")
	fixture.observerID = insertUser("ORCHB"+suffix, "orch.b."+suffix+"@example.invalid")
	if err := tx.QueryRow(ctx, `INSERT INTO platform.business_domains(
		tenant_id,code,name,is_default,created_by
	) VALUES($1,$2,$3,true,$4) RETURNING id::text`, fixture.tenantID,
		"orch_"+suffix, "orchestrator "+suffix, fixture.actorID).Scan(&fixture.domainID); err != nil {
		t.Fatalf("insert fixture domain: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.domain_memberships(
		tenant_id,domain_id,user_id,status,member_role,assigned_by
	) VALUES($1,$2,$3,'ACTIVE','MEMBER',$3),($1,$2,$4,'ACTIVE','MEMBER',$3)`,
		fixture.tenantID, fixture.domainID, fixture.actorID, fixture.observerID); err != nil {
		t.Fatalf("insert fixture memberships: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.domains(id,tenant_id,code,name,owner_id)
		VALUES($1,$2,$3,$4,$5)`, fixture.domainID, fixture.tenantID,
		"orch_"+suffix, "orchestrator "+suffix, fixture.actorID); err != nil {
		t.Fatalf("insert askdata fixture domain: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO askdata.releases(
		tenant_id,domain_id,semantic_version,content_hash,status,object_count,
		created_by,updated_by,activated_by,ready_at,activated_at
	) VALUES($1,$2,$3,$4,'ACTIVE',0,$5,$5,$5,now(),now()) RETURNING id::text`,
		fixture.tenantID, fixture.domainID, "orch-"+suffix, fixture.releaseHash,
		fixture.actorID).Scan(&fixture.releaseID); err != nil {
		t.Fatalf("insert ACTIVE fixture release: %v", err)
	}
	semanticIR, err := json.Marshal(map[string]string{
		"irVersion": "semantic-ir-v1", "semanticReleaseId": fixture.releaseID,
		"semanticContentHash": fixture.releaseHash, "domainId": fixture.domainID,
	})
	if err != nil {
		t.Fatalf("marshal saved question fixture IR: %v", err)
	}
	fixture.savedQuestionIR = semanticIR
	fixture.savedQuestionIRHash = string(askdata.HashBytes(semanticIR))
	if err := tx.QueryRow(ctx, `INSERT INTO askdata.saved_questions(
		tenant_id,domain_id,owner_user_id,visibility,name,question_text,
		semantic_ir_json,semantic_ir_hash,semantic_release_id,semantic_release_content_hash
	) VALUES($1,$2,$3,'PRIVATE','saved seed','saved seed question',$4,$5,$6,$7)
	RETURNING id::text`, fixture.tenantID, fixture.domainID, fixture.actorID,
		semanticIR, fixture.savedQuestionIRHash, fixture.releaseID, fixture.releaseHash,
	).Scan(&fixture.savedQuestionID); err != nil {
		t.Fatalf("insert saved question fixture: %v", err)
	}
	return fixture
}

type integrationProjectionGuard struct {
	err           error
	calls         int
	lastReleaseID string
}

func (guard *integrationProjectionGuard) AssertRunnable(_ context.Context, releaseID string) error {
	guard.calls++
	guard.lastReleaseID = releaseID
	return guard.err
}
