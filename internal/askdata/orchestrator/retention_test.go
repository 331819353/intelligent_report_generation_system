package orchestrator

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"intelligent-report-generation-system/internal/askdata"
)

func TestHashOnlyRetentionNeverCreatesRecoverableQuestionMaterial(t *testing.T) {
	policy, err := NewRetentionPolicy(RetentionConfig{
		QuestionMode: OriginalQuestionHashOnly, QuestionTTL: 24 * time.Hour,
		RunArtifactTTL: 30 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("NewRetentionPolicy() error = %v", err)
	}
	binding := retentionBinding(t)
	envelope, err := policy.RetainQuestion(binding, "华东区本月销售额", time.Now())
	if err != nil {
		t.Fatalf("RetainQuestion() error = %v", err)
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	if envelope.Ciphertext != "" || envelope.ExpiresAt != nil ||
		strings.Contains(string(raw), "华东") {
		t.Fatalf("hash-only envelope retained question material: %s", raw)
	}
	if _, err := policy.OpenQuestion(binding, envelope, time.Now()); !errors.Is(err, ErrQuestionEnvelopeInvalid) {
		t.Fatalf("OpenQuestion(hash-only) error = %v", err)
	}
}

func TestEncryptedQuestionIsBoundToScopeAndExpires(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	policy, err := NewRetentionPolicy(RetentionConfig{
		QuestionMode: OriginalQuestionEncryptedShortTerm,
		QuestionTTL:  2 * time.Hour, RunArtifactTTL: 24 * time.Hour,
		QuestionEncryptionKey: key,
	})
	if err != nil {
		t.Fatalf("NewRetentionPolicy() error = %v", err)
	}
	binding := retentionBinding(t)
	now := time.Date(2026, 8, 6, 10, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	left, err := policy.RetainQuestion(binding, "本月净销售额", now)
	if err != nil {
		t.Fatalf("RetainQuestion(left) error = %v", err)
	}
	right, err := policy.RetainQuestion(binding, "本月净销售额", now)
	if err != nil {
		t.Fatalf("RetainQuestion(right) error = %v", err)
	}
	if left.Ciphertext == right.Ciphertext || strings.Contains(left.Ciphertext, "销售额") {
		t.Fatal("question encryption did not use a random nonce or exposed plaintext")
	}
	opened, err := policy.OpenQuestion(binding, left, now.Add(time.Hour))
	if err != nil || opened != "本月净销售额" {
		t.Fatalf("OpenQuestion() = %q, %v", opened, err)
	}

	tampered := left
	tampered.QuestionHash = testHash("e")
	if _, err := policy.OpenQuestion(binding, tampered, now.Add(time.Hour)); !errors.Is(err, ErrQuestionEnvelopeInvalid) {
		t.Fatalf("tampered metadata error = %v", err)
	}
	if _, err := policy.OpenQuestion(binding, left, now.Add(2*time.Hour)); !errors.Is(err, ErrQuestionRetentionExpired) {
		t.Fatalf("expired question error = %v", err)
	}
}

func TestRetentionPolicyRejectsUnsafeModeKeyAndTTLCombinations(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	for _, config := range []RetentionConfig{
		{QuestionMode: "PLAINTEXT", QuestionTTL: time.Hour, RunArtifactTTL: 24 * time.Hour},
		{QuestionMode: OriginalQuestionHashOnly, QuestionTTL: 8 * 24 * time.Hour, RunArtifactTTL: 30 * 24 * time.Hour},
		{QuestionMode: OriginalQuestionHashOnly, QuestionTTL: 24 * time.Hour, RunArtifactTTL: time.Hour},
		{QuestionMode: OriginalQuestionHashOnly, QuestionTTL: time.Hour, RunArtifactTTL: time.Hour, QuestionEncryptionKey: key},
		{QuestionMode: OriginalQuestionEncryptedShortTerm, QuestionTTL: time.Hour, RunArtifactTTL: time.Hour},
	} {
		if _, err := NewRetentionPolicy(config); !errors.Is(err, ErrRetentionPolicyInvalid) {
			t.Errorf("NewRetentionPolicy(%#v) error = %v", config, err)
		}
	}
}

func TestConversationInheritanceRequiresExactIdentityConversationAndRelease(t *testing.T) {
	scope, domainID := testPolicyScope(t)
	now := time.Now().UTC()
	previous := validRun(StateAnswered)
	previous.TenantID, previous.ActorID, previous.DomainID = scope.TenantID, scope.ActorID, domainID
	previous.Release, previous.PolicyScopeHash = scope.Release, scope.PolicyHash
	previous.ConversationID = askdata.ID(uuid.NewString())
	previous.Hashes = completeHashes()
	previous.Disposition, previous.CompletionCode = DispositionDirect, "ANSWER_READY"
	previous.CompletionArtifact = testHash("f")
	previous.CreatedAt, previous.UpdatedAt, previous.CompletedAt = now.Add(-time.Minute), now, &now
	if err := previous.Validate(); err != nil {
		t.Fatalf("previous run is invalid: %v", err)
	}
	decision, err := ResolveConversationInheritance(previous, scope, domainID, previous.ConversationID)
	if err != nil || decision != ConversationInherit {
		t.Fatalf("exact inheritance = %q, %v", decision, err)
	}

	changedPolicy, err := askdata.NewPolicyScope(
		scope.TenantID, scope.ActorID, []askdata.ID{domainID},
		[]askdata.ID{askdata.ID(uuid.NewString())}, scope.Release,
	)
	if err != nil {
		t.Fatalf("changed policy scope: %v", err)
	}
	decision, err = ResolveConversationInheritance(previous, changedPolicy, domainID, previous.ConversationID)
	if err != nil || decision != ConversationResetScope {
		t.Fatalf("policy change inheritance = %q, %v", decision, err)
	}

	drifted, err := askdata.NewPolicyScope(
		scope.TenantID, scope.ActorID, []askdata.ID{domainID}, scope.RoleIDs,
		askdata.ReleaseRef{ReleaseID: askdata.ID(uuid.NewString()), ContentHash: testHash("9")},
	)
	if err != nil {
		t.Fatalf("drifted scope: %v", err)
	}
	if _, err := ResolveConversationInheritance(previous, drifted, domainID, previous.ConversationID); !errors.Is(err, ErrConversationInheritance) {
		t.Fatalf("release drift inheritance error = %v", err)
	}
	if _, err := ResolveConversationInheritance(previous, scope, domainID, askdata.ID(uuid.NewString())); !errors.Is(err, ErrConversationInheritance) {
		t.Fatalf("cross-conversation inheritance error = %v", err)
	}
}

func TestArtifactPurgeKeepsImmutableStatisticsAndDigests(t *testing.T) {
	snapshot := retentionSnapshot(t)
	policy, err := NewRetentionPolicy(RetentionConfig{
		QuestionMode: OriginalQuestionHashOnly, QuestionTTL: 24 * time.Hour,
		RunArtifactTTL: 7 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("NewRetentionPolicy() error = %v", err)
	}
	before, err := policy.PlanArtifactPurge(snapshot, snapshot.Run.CreatedAt.Add(6*24*time.Hour))
	if err != nil {
		t.Fatalf("PlanArtifactPurge(before) error = %v", err)
	}
	after, err := policy.PlanArtifactPurge(snapshot, snapshot.Run.CreatedAt.Add(7*24*time.Hour))
	if err != nil {
		t.Fatalf("PlanArtifactPurge(after) error = %v", err)
	}
	if before.Expired || len(before.PayloadArtifactIDs) != 0 || !after.Expired ||
		len(after.PayloadArtifactIDs) != 1 {
		t.Fatalf("unexpected purge boundary: before=%#v after=%#v", before, after)
	}
	if before.Statistics.StatisticsHash != after.Statistics.StatisticsHash ||
		before.Statistics.ArtifactCount != 1 || len(after.Digests) != 1 ||
		after.Digests[0].Hash != snapshot.Artifacts[0].Hash {
		t.Fatalf("immutable statistics/digest changed across TTL: %#v / %#v", before, after)
	}
	raw, err := json.Marshal(after)
	if err != nil {
		t.Fatalf("marshal purge plan: %v", err)
	}
	if strings.Contains(string(raw), "PAYLOAD_ONLY") {
		t.Fatalf("purge plan retained artifact payload: %s", raw)
	}
}

func TestConversationPinMatchRejectsSilentReleaseSwitch(t *testing.T) {
	anchor := validRun(StateReceived)
	anchor.ConversationID = askdata.ID(uuid.NewString())
	candidate := anchor
	candidate.ID = askdata.ID(uuid.NewString())
	candidate.TraceID = askdata.ID(uuid.NewString())
	candidate.IdempotencyKeyHash = testHash("8")
	if !conversationPinMatches(anchor, candidate) {
		t.Fatal("exact conversation pin did not match")
	}
	candidate.Release = askdata.ReleaseRef{
		ReleaseID: askdata.ID(uuid.NewString()), ContentHash: testHash("9"),
	}
	if conversationPinMatches(anchor, candidate) {
		t.Fatal("conversation accepted a silent release switch")
	}
}

func retentionBinding(t *testing.T) QuestionRetentionBinding {
	t.Helper()
	scope, domainID := testPolicyScope(t)
	return QuestionRetentionBinding{
		Scope: scope, DomainID: domainID, ConversationID: askdata.ID(uuid.NewString()),
		RunID: askdata.ID(uuid.NewString()), QuestionHash: testHash("a"),
	}
}

func retentionSnapshot(t *testing.T) ReplaySnapshot {
	t.Helper()
	scope, domainID := testPolicyScope(t)
	createdAt := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	run := validRun(StateReceived)
	run.TenantID, run.ActorID, run.DomainID = scope.TenantID, scope.ActorID, domainID
	run.Release, run.PolicyScopeHash = scope.Release, scope.PolicyHash
	run.ConversationID = askdata.ID(uuid.NewString())
	run.CreatedAt, run.UpdatedAt = createdAt, createdAt
	initial, err := newEvent(run, 1, "", TransitionEventInput{
		Stage: string(StateReceived), Status: EventSucceeded,
		Code: "RUN_RECEIVED", Details: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("initial event: %v", err)
	}
	events := []Event{initial}
	current := run
	for _, target := range []State{StateAuthorized, StateContextReady, StateUnderstanding} {
		next, err := Apply(current, Transition{
			ExpectedVersion: current.RecordVersion, TargetState: target, Usage: current.Usage,
		})
		if err != nil {
			t.Fatalf("advance to %s: %v", target, err)
		}
		event, err := buildTransitionEvent(next, current.State, len(events)+1, events[len(events)-1].Hash,
			TransitionEventInput{Details: json.RawMessage(`{}`)})
		if err != nil {
			t.Fatalf("event for %s: %v", target, err)
		}
		events = append(events, event)
		current = next
	}
	artifact, err := prepareCompletionArtifact(TransitionRequest{
		Scope: scope, DomainID: domainID, RunID: current.ID, TargetState: StateBlocked,
	}, CompletionArtifactInput{
		Code: "BLOCK_SAFE", Type: ArtifactBlock, SchemaVersion: "block-v1",
		Payload: json.RawMessage(`{"code":"BLOCK_SAFE","detailCode":"PAYLOAD_ONLY"}`),
	})
	if err != nil {
		t.Fatalf("prepare artifact: %v", err)
	}
	artifact.ID, artifact.TenantID, artifact.DomainID = askdata.ID(uuid.NewString()), current.TenantID, current.DomainID
	artifact.ActorID, artifact.Release = current.ActorID, current.Release
	artifact.PolicyScopeHash, artifact.RunVersion = current.PolicyScopeHash, current.RecordVersion
	artifact.Index, artifact.CreatedAt = 1, createdAt.Add(time.Minute)
	blocked, err := Apply(current, Transition{
		ExpectedVersion: current.RecordVersion, TargetState: StateBlocked, Usage: current.Usage,
		Completion: &CompletionRef{Code: "BLOCK_SAFE", ArtifactType: ArtifactBlock, ArtifactHash: artifact.Hash},
	})
	if err != nil {
		t.Fatalf("block run: %v", err)
	}
	terminal, err := buildTransitionEvent(blocked, current.State, len(events)+1, events[len(events)-1].Hash,
		TransitionEventInput{Details: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("terminal event: %v", err)
	}
	terminal.ArtifactHash = artifact.Hash
	terminal.Hash, err = computeEventHash(terminal)
	if err != nil {
		t.Fatalf("terminal event hash: %v", err)
	}
	snapshot := ReplaySnapshot{Run: blocked, Events: append(events, terminal), Artifacts: []Artifact{artifact}}
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("retention snapshot: %v", err)
	}
	return snapshot
}
