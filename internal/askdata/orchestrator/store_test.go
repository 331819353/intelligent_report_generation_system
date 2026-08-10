package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/toolhost"
	"intelligent-report-generation-system/internal/platform/database"
)

func TestPersistenceMapsRetainedReleaseToGovernedError(t *testing.T) {
	err := mapPersistenceError(&pgconn.PgError{
		Code: "23514", Message: "RELEASE_NOT_RUNNABLE: semantic release cannot create a new question run",
	})
	if !errors.Is(err, ErrReleaseNotRunnable) {
		t.Fatalf("mapped error = %v", err)
	}
}

func TestValidateActorScopeFailsClosedWithoutExactAccessContext(t *testing.T) {
	scope, domainID := testPolicyScope(t)
	if _, err := validateActorScope(context.Background(), scope, domainID); !errors.Is(err, ErrInvalidAccessContext) {
		t.Fatalf("missing context error = %v", err)
	}
	wrongActor := database.WithAccessContext(context.Background(), uuid.NewString(), string(domainID))
	if _, err := validateActorScope(wrongActor, scope, domainID); !errors.Is(err, ErrInvalidAccessContext) {
		t.Fatalf("wrong actor error = %v", err)
	}
	wrongDomain := database.WithAccessContext(context.Background(), string(scope.ActorID), uuid.NewString())
	if _, err := validateActorScope(wrongDomain, scope, domainID); !errors.Is(err, ErrInvalidAccessContext) {
		t.Fatalf("wrong domain error = %v", err)
	}
	ctx := database.WithAccessContext(context.Background(), string(scope.ActorID), string(domainID))
	if tenantID, err := validateActorScope(ctx, scope, domainID); err != nil || tenantID != string(scope.TenantID) {
		t.Fatalf("valid scope = %q, %v", tenantID, err)
	}
}

func TestPrepareCreateAcceptsOnlyOneGovernedSeedSource(t *testing.T) {
	scope, domainID := testPolicyScope(t)
	ctx := database.WithAccessContext(context.Background(), string(scope.ActorID), string(domainID))
	semanticIR := json.RawMessage(`{"irVersion":"semantic-ir-v1"}`)
	base := CreateRunRequest{
		Scope: scope, DomainID: domainID, ConversationID: askdata.ID(uuid.NewString()),
		IdempotencyKeyHash: testHash("1"), QuestionHash: testHash("2"),
	}
	base.SeedContext = &SeedContext{
		Source: SeedSourceSavedQuestion, SavedQuestionID: askdata.ID(uuid.NewString()),
		SemanticIR: semanticIR, SemanticIRHash: askdata.HashBytes(semanticIR),
		PinnedReleaseID: scope.Release.ReleaseID,
	}
	if _, _, err := prepareCreateRequest(ctx, base); err != nil {
		t.Fatalf("saved question seed error = %v", err)
	}

	forged := base
	copy := *base.SeedContext
	copy.ReportVersionID = askdata.ID(uuid.NewString())
	forged.SeedContext = &copy
	if _, _, err := prepareCreateRequest(ctx, forged); !errors.Is(err, ErrInvalidRun) {
		t.Fatalf("mixed seed source error = %v", err)
	}

	unknown := base
	copy = *base.SeedContext
	copy.Source = "UNTRUSTED"
	unknown.SeedContext = &copy
	if _, _, err := prepareCreateRequest(ctx, unknown); !errors.Is(err, ErrInvalidRun) {
		t.Fatalf("unknown seed source error = %v", err)
	}
}

func TestCanonicalAuditObjectRejectsSensitiveKeysAtAnyDepth(t *testing.T) {
	for _, raw := range []string{
		`{"prompt":"hidden"}`,
		`{"nested":{"result_rows":[1]}}`,
		`{"items":[{"toolArguments":{"sqlText":"select"}}]}`,
		`{"password":"secret"}`,
	} {
		if _, err := canonicalAuditObject([]byte(raw), maxEventDetailsBytes); !errors.Is(err, ErrInvalidRun) {
			t.Errorf("canonicalAuditObject(%s) error = %v", raw, err)
		}
	}
	canonical, err := canonicalAuditObject([]byte(`{"z":1.0,"a":{"code":"SAFE"}}`), maxEventDetailsBytes)
	if err != nil || string(canonical) != `{"a":{"code":"SAFE"},"z":1}` {
		t.Fatalf("safe canonical JSON = %s, %v", canonical, err)
	}
}

func TestEventRejectsNonCanonicalAIRequestUUIDBeforePersistence(t *testing.T) {
	run := validRun(StateReceived)
	_, err := newEvent(run, 1, "", TransitionEventInput{
		Stage: string(StateReceived), Status: EventSucceeded, Code: "RUN_RECEIVED",
		AIRequestID: askdata.ID(strings.ToUpper(uuid.NewString())), Details: json.RawMessage(`{}`),
	})
	if !errors.Is(err, ErrReplayCorrupt) {
		t.Fatalf("noncanonical AI request UUID error = %v", err)
	}
}

func TestArtifactHashIsCanonicalAndDeclaredHashIsChecked(t *testing.T) {
	scope, domainID := testPolicyScope(t)
	runID := askdata.ID(uuid.NewString())
	request := TransitionRequest{Scope: scope, DomainID: domainID, RunID: runID, TargetState: StateBlocked}
	left, err := prepareCompletionArtifact(request, CompletionArtifactInput{
		Code: "POLICY_BLOCK", Type: ArtifactBlock, SchemaVersion: "block-v1",
		EvidenceIDs: []askdata.ID{"evidence-b", "evidence-a"}, Payload: json.RawMessage(`{"b":1.0,"a":true}`),
	})
	if err != nil {
		t.Fatalf("prepareCompletionArtifact(left) error = %v", err)
	}
	right, err := prepareCompletionArtifact(request, CompletionArtifactInput{
		Code: "POLICY_BLOCK", Type: ArtifactBlock, SchemaVersion: "block-v1",
		EvidenceIDs: []askdata.ID{"evidence-a", "evidence-b"}, Payload: json.RawMessage(`{"a":true,"b":1}`),
		ExpectedHash: left.Hash,
	})
	if err != nil || right.Hash != left.Hash {
		t.Fatalf("canonical artifact hashes = %s/%s, %v", left.Hash, right.Hash, err)
	}
	_, err = prepareCompletionArtifact(request, CompletionArtifactInput{
		Code: "POLICY_BLOCK", Type: ArtifactBlock, SchemaVersion: "block-v1",
		Payload: json.RawMessage(`{"code":"BLOCK"}`), ExpectedHash: testHash("0"),
	})
	if !errors.Is(err, ErrInvalidRun) {
		t.Fatalf("wrong declared hash error = %v", err)
	}
}

func TestReplaySnapshotValidatesEventChainWithoutEquatingEventsAndVersions(t *testing.T) {
	run := validRun(StateReceived)
	event1, err := newEvent(run, 1, "", TransitionEventInput{
		Stage: string(StateReceived), Status: EventSucceeded, Code: "RUN_RECEIVED", Details: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("newEvent(1) error = %v", err)
	}
	actionHash := testHash("8")
	progress := event1
	progress.ID = askdata.ID(uuid.NewString())
	progress.Index = 2
	progress.Type = EventProgress
	progress.Code = "EVIDENCE_READY"
	progress.ActionHash = actionHash
	progress.PreviousEventHash = event1.Hash
	progress.Hash, err = computeEventHash(progress)
	if err != nil {
		t.Fatalf("compute progress hash: %v", err)
	}

	next, err := Apply(run, Transition{
		ExpectedVersion: 1, TargetState: StateAuthorized, Usage: run.Usage,
	})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	event3, err := newEvent(next, 3, progress.Hash, TransitionEventInput{
		Stage: string(StateAuthorized), Status: EventSucceeded, Code: "STATE_AUTHORIZED", Details: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("newEvent(3) error = %v", err)
	}
	call := ToolCall{
		ID: askdata.ID(uuid.NewString()), TenantID: run.TenantID, DomainID: run.DomainID,
		ActorID: run.ActorID, RunID: run.ID, Release: run.Release,
		PolicyScopeHash: run.PolicyScopeHash, RunVersion: 2, CallID: "call-1",
		Tool: toolhost.ToolSearchSemanticObjects, State: StateAuthorized, Status: "SUCCEEDED",
		RequestHash: testHash("1"), ResultHash: testHash("2"),
		EvidenceIDs: []askdata.ID{}, Budget: json.RawMessage(`{}`), DurationMS: 1,
	}
	call.CallHash, err = computeToolCallHash(call)
	if err != nil {
		t.Fatalf("compute tool call hash: %v", err)
	}
	toolEvent := event3
	toolEvent.ID = askdata.ID(uuid.NewString())
	toolEvent.Index = 4
	toolEvent.Type = EventToolResult
	toolEvent.Code = "TOOL_SUCCEEDED"
	toolEvent.ToolCallID = call.CallID
	toolEvent.PreviousEventHash = event3.Hash
	toolEvent.Hash, err = computeEventHash(toolEvent)
	if err != nil {
		t.Fatalf("compute tool event hash: %v", err)
	}
	snapshot := ReplaySnapshot{
		Run: next, Events: []Event{event1, progress, event3, toolEvent}, ToolCalls: []ToolCall{call},
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if got := snapshot.SeenActionHashes(); len(got) != 1 || got[0] != actionHash {
		t.Fatalf("SeenActionHashes() = %#v", got)
	}
	if got := snapshot.SeenToolCallIDs(); len(got) != 1 || got[0] != "call-1" {
		t.Fatalf("SeenToolCallIDs() = %#v", got)
	}

	tampered := snapshot
	tampered.Events = append([]Event(nil), snapshot.Events...)
	tampered.Events[1].Index = 3
	tampered.Events[1].Hash, _ = computeEventHash(tampered.Events[1])
	tampered.Events[2].PreviousEventHash = tampered.Events[1].Hash
	tampered.Events[2].Hash, _ = computeEventHash(tampered.Events[2])
	if err := tampered.Validate(); !errors.Is(err, ErrReplayCorrupt) {
		t.Fatalf("index tamper error = %v", err)
	}
	tampered = snapshot
	tampered.Events = append([]Event(nil), snapshot.Events...)
	tampered.Events[1].Hash = testHash("9")
	if err := tampered.Validate(); !errors.Is(err, ErrReplayCorrupt) {
		t.Fatalf("hash tamper error = %v", err)
	}
	tampered = snapshot
	tampered.ToolCalls = append([]ToolCall(nil), snapshot.ToolCalls...)
	tampered.ToolCalls[0].ResultHash = testHash("4")
	if err := tampered.Validate(); !errors.Is(err, ErrReplayCorrupt) {
		t.Fatalf("tool outcome tamper error = %v", err)
	}
	unreferenced := snapshot
	unreferenced.Events = append([]Event(nil), snapshot.Events[:3]...)
	if err := unreferenced.Validate(); !errors.Is(err, ErrReplayCorrupt) {
		t.Fatalf("unreferenced tool outcome error = %v", err)
	}
	dangling := snapshot
	dangling.Events = append([]Event(nil), snapshot.Events...)
	dangling.Events[3].ToolCallID = "missing-call"
	dangling.Events[3].Hash, _ = computeEventHash(dangling.Events[3])
	if err := dangling.Validate(); !errors.Is(err, ErrReplayCorrupt) {
		t.Fatalf("dangling tool event error = %v", err)
	}
	missingArtifact := snapshot
	missingArtifact.Events = append([]Event(nil), snapshot.Events...)
	artifactEvent := toolEvent
	artifactEvent.ID = askdata.ID(uuid.NewString())
	artifactEvent.Index = 5
	artifactEvent.Type = EventArtifactRecorded
	artifactEvent.ToolCallID = ""
	artifactEvent.ArtifactHash = testHash("7")
	artifactEvent.PreviousEventHash = toolEvent.Hash
	artifactEvent.Hash, _ = computeEventHash(artifactEvent)
	missingArtifact.Events = append(missingArtifact.Events, artifactEvent)
	if err := missingArtifact.Validate(); !errors.Is(err, ErrReplayCorrupt) {
		t.Fatalf("dangling artifact event error = %v", err)
	}
}

func TestLLMDecisionEventRequiresAIRequestAndActionHash(t *testing.T) {
	run := validRun(StateReceived)
	event, err := newEvent(run, 1, "", TransitionEventInput{
		Stage: string(StateReceived), Status: EventSucceeded, Code: "RUN_RECEIVED",
		Details: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	event.Type = EventLLMDecision
	event.ActionHash = testHash("1")
	event.Hash, _ = computeEventHash(event)
	if err := event.Validate(); !errors.Is(err, ErrReplayCorrupt) {
		t.Fatalf("LLM decision without AI request error = %v", err)
	}
}

func TestReplayTerminalRequiresMatchingCompletionArtifact(t *testing.T) {
	base := validRun(StateReceived)
	initial, _ := newEvent(base, 1, "", TransitionEventInput{
		Stage: string(StateReceived), Status: EventSucceeded, Code: "RUN_RECEIVED", Details: json.RawMessage(`{}`),
	})
	blockedArtifact, err := prepareCompletionArtifact(TransitionRequest{
		RunID: base.ID, TargetState: StateBlocked,
	}, CompletionArtifactInput{
		Code: "SAFE_BLOCK", Type: ArtifactBlock, SchemaVersion: "block-v1", Payload: json.RawMessage(`{"code":"SAFE_BLOCK"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	blockedArtifact.ID = askdata.ID(uuid.NewString())
	blockedArtifact.TenantID, blockedArtifact.DomainID, blockedArtifact.ActorID = base.TenantID, base.DomainID, base.ActorID
	blockedArtifact.Release, blockedArtifact.PolicyScopeHash = base.Release, base.PolicyScopeHash
	blockedArtifact.Index, blockedArtifact.RunVersion = 1, 1
	blocked, err := Apply(base, Transition{
		ExpectedVersion: 1, TargetState: StateBlocked, Usage: base.Usage,
		Completion: &CompletionRef{Code: "SAFE_BLOCK", ArtifactType: ArtifactBlock, ArtifactHash: blockedArtifact.Hash},
	})
	if err != nil {
		t.Fatal(err)
	}
	final, _ := newEvent(blocked, 2, initial.Hash, TransitionEventInput{
		Stage: string(StateBlocked), Status: EventBlocked, Code: "SAFE_BLOCK", Details: json.RawMessage(`{}`),
	})
	final.ArtifactHash = blockedArtifact.Hash
	final.Hash, _ = computeEventHash(final)
	snapshot := ReplaySnapshot{Run: blocked, Events: []Event{initial, final}}
	if err := snapshot.Validate(); !errors.Is(err, ErrReplayCorrupt) {
		t.Fatalf("missing completion artifact error = %v", err)
	}
	snapshot.Artifacts = []Artifact{blockedArtifact}
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("matching completion artifact error = %v", err)
	}
	tampered := snapshot
	tampered.Events = append([]Event(nil), snapshot.Events...)
	tampered.Events[1].ArtifactHash = testHash("9")
	tampered.Events[1].Hash, _ = computeEventHash(tampered.Events[1])
	if err := tampered.Validate(); !errors.Is(err, ErrReplayCorrupt) {
		t.Fatalf("terminal event completion mismatch error = %v", err)
	}
}

func TestBuildTransitionEventRejectsContradictoryStageStatusAndCode(t *testing.T) {
	run := validRun(StateAuthorized)
	run.RecordVersion = 2
	for _, input := range []TransitionEventInput{
		{Stage: string(StateExecuting)},
		{Status: EventFailed},
		{Code: "WRONG_CODE"},
	} {
		input.Details = json.RawMessage(`{}`)
		if _, err := buildTransitionEvent(run, StateReceived, 2, testHash("a"), input); !errors.Is(err, ErrInvalidRun) {
			t.Errorf("buildTransitionEvent(%#v) error = %v", input, err)
		}
	}
}

func testPolicyScope(t *testing.T) (askdata.PolicyScope, askdata.ID) {
	t.Helper()
	tenantID, actorID, domainID := askdata.ID(uuid.NewString()), askdata.ID(uuid.NewString()), askdata.ID(uuid.NewString())
	release := askdata.ReleaseRef{ReleaseID: askdata.ID(uuid.NewString()), ContentHash: testHash("a")}
	scope, err := askdata.NewPolicyScope(tenantID, actorID, []askdata.ID{domainID}, []askdata.ID{askdata.ID(uuid.NewString())}, release)
	if err != nil {
		t.Fatalf("NewPolicyScope() error = %v", err)
	}
	return scope, domainID
}
