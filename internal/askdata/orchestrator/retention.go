package orchestrator

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"intelligent-report-generation-system/internal/askdata"
)

const (
	questionEnvelopeVersion = "askdata-question-envelope-v1"
	runStatisticsVersion    = "askdata-run-statistics-v1"
	maxRetainedQuestionSize = 16 << 10
)

var (
	ErrRetentionPolicyInvalid      = errors.New("askdata retention policy is invalid")
	ErrQuestionEnvelopeInvalid     = errors.New("retained question envelope is invalid")
	ErrQuestionRetentionExpired    = errors.New("retained question has expired")
	ErrConversationInheritance     = errors.New("conversation context cannot be inherited")
	questionEnvelopeAssociatedData = []byte("intelligent-report:askdata-question:v1")
)

// OriginalQuestionMode controls whether raw user text crosses the request
// lifetime. HASH_ONLY is the safe default and needs no encryption key.
type OriginalQuestionMode string

const (
	OriginalQuestionHashOnly           OriginalQuestionMode = "HASH_ONLY"
	OriginalQuestionEncryptedShortTerm OriginalQuestionMode = "ENCRYPTED_SHORT_TERM"
)

// RetentionConfig is deliberately independent from the process-wide config
// package so the privacy boundary can be tested without loading credentials.
type RetentionConfig struct {
	QuestionMode          OriginalQuestionMode
	QuestionTTL           time.Duration
	RunArtifactTTL        time.Duration
	QuestionEncryptionKey string
}

// RetentionPolicy owns question encryption and deterministic expiry planning.
// It never persists plaintext and never removes the immutable run ledger.
type RetentionPolicy struct {
	mode           OriginalQuestionMode
	questionTTL    time.Duration
	runArtifactTTL time.Duration
	questionCipher cipher.AEAD
}

func NewRetentionPolicy(config RetentionConfig) (*RetentionPolicy, error) {
	if config.QuestionMode != OriginalQuestionHashOnly &&
		config.QuestionMode != OriginalQuestionEncryptedShortTerm {
		return nil, fmt.Errorf("%w: unsupported original-question mode", ErrRetentionPolicyInvalid)
	}
	if config.QuestionTTL <= 0 || config.QuestionTTL > 7*24*time.Hour {
		return nil, fmt.Errorf("%w: question TTL must be within (0, 7d]", ErrRetentionPolicyInvalid)
	}
	if config.RunArtifactTTL < config.QuestionTTL || config.RunArtifactTTL > 365*24*time.Hour {
		return nil, fmt.Errorf("%w: artifact TTL must be at least the question TTL and at most 365d", ErrRetentionPolicyInvalid)
	}
	policy := &RetentionPolicy{
		mode: config.QuestionMode, questionTTL: config.QuestionTTL,
		runArtifactTTL: config.RunArtifactTTL,
	}
	keyText := strings.TrimSpace(config.QuestionEncryptionKey)
	if config.QuestionMode == OriginalQuestionHashOnly {
		if keyText != "" {
			return nil, fmt.Errorf("%w: hash-only mode must not load a question encryption key", ErrRetentionPolicyInvalid)
		}
		return policy, nil
	}
	key, err := base64.StdEncoding.DecodeString(keyText)
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("%w: question encryption key must be base64-encoded 32 bytes", ErrRetentionPolicyInvalid)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("%w: initialize question cipher", ErrRetentionPolicyInvalid)
	}
	policy.questionCipher, err = cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("%w: initialize question AEAD", ErrRetentionPolicyInvalid)
	}
	return policy, nil
}

func (policy *RetentionPolicy) QuestionMode() OriginalQuestionMode {
	if policy == nil {
		return ""
	}
	return policy.mode
}

func (policy *RetentionPolicy) RunArtifactTTL() time.Duration {
	if policy == nil {
		return 0
	}
	return policy.runArtifactTTL
}

// QuestionRetentionBinding binds ciphertext to the exact authorization and
// semantic-release context of one run. Swapping an envelope between actors,
// conversations, runs, policies, or releases makes decryption fail closed.
type QuestionRetentionBinding struct {
	Scope          askdata.PolicyScope
	DomainID       askdata.ID
	ConversationID askdata.ID
	RunID          askdata.ID
	QuestionHash   askdata.ContentHash
}

func (binding QuestionRetentionBinding) validate() error {
	if err := binding.Scope.Validate(); err != nil {
		return fmt.Errorf("%w: scope", ErrQuestionEnvelopeInvalid)
	}
	for _, id := range []askdata.ID{binding.Scope.TenantID, binding.Scope.ActorID,
		binding.DomainID, binding.ConversationID, binding.RunID, binding.Scope.Release.ReleaseID} {
		if !retentionCanonicalUUID(id) {
			return fmt.Errorf("%w: identity", ErrQuestionEnvelopeInvalid)
		}
	}
	foundDomain := false
	for _, domainID := range binding.Scope.DomainIDs {
		foundDomain = foundDomain || domainID == binding.DomainID
	}
	if !foundDomain || binding.QuestionHash.Validate() != nil {
		return fmt.Errorf("%w: domain or question hash", ErrQuestionEnvelopeInvalid)
	}
	return nil
}

// QuestionEnvelope contains either a hash-only retention receipt or an
// authenticated ciphertext. It intentionally has no plaintext field.
type QuestionEnvelope struct {
	Version         string               `json:"version"`
	Mode            OriginalQuestionMode `json:"mode"`
	TenantID        askdata.ID           `json:"tenantId"`
	DomainID        askdata.ID           `json:"domainId"`
	ActorID         askdata.ID           `json:"actorId"`
	ConversationID  askdata.ID           `json:"conversationId"`
	RunID           askdata.ID           `json:"runId"`
	Release         askdata.ReleaseRef   `json:"release"`
	PolicyScopeHash askdata.ContentHash  `json:"policyScopeHash"`
	QuestionHash    askdata.ContentHash  `json:"questionHash"`
	Ciphertext      string               `json:"ciphertext,omitempty"`
	CreatedAt       time.Time            `json:"createdAt"`
	ExpiresAt       *time.Time           `json:"expiresAt,omitempty"`
}

// RetainQuestion discards rawText immediately in HASH_ONLY mode. Encrypted
// mode uses a random nonce and authenticates all scope and expiry metadata.
func (policy *RetentionPolicy) RetainQuestion(
	binding QuestionRetentionBinding,
	rawText string,
	now time.Time,
) (QuestionEnvelope, error) {
	if policy == nil || binding.validate() != nil || now.IsZero() || !utf8.ValidString(rawText) ||
		len([]byte(rawText)) == 0 || len([]byte(rawText)) > maxRetainedQuestionSize {
		return QuestionEnvelope{}, ErrQuestionEnvelopeInvalid
	}
	createdAt := now.UTC()
	envelope := QuestionEnvelope{
		Version: questionEnvelopeVersion, Mode: policy.mode,
		TenantID: binding.Scope.TenantID, DomainID: binding.DomainID,
		ActorID: binding.Scope.ActorID, ConversationID: binding.ConversationID,
		RunID: binding.RunID, Release: binding.Scope.Release,
		PolicyScopeHash: binding.Scope.PolicyHash, QuestionHash: binding.QuestionHash,
		CreatedAt: createdAt,
	}
	if policy.mode == OriginalQuestionHashOnly {
		return envelope, envelope.validateShape()
	}
	if policy.questionCipher == nil {
		return QuestionEnvelope{}, ErrRetentionPolicyInvalid
	}
	expiresAt := createdAt.Add(policy.questionTTL)
	envelope.ExpiresAt = &expiresAt
	aad, err := envelope.associatedData()
	if err != nil {
		return QuestionEnvelope{}, err
	}
	nonce := make([]byte, policy.questionCipher.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return QuestionEnvelope{}, fmt.Errorf("seal retained question: %w", err)
	}
	sealed := policy.questionCipher.Seal(nonce, nonce, []byte(rawText), aad)
	envelope.Ciphertext = base64.RawURLEncoding.EncodeToString(sealed)
	return envelope, envelope.validateShape()
}

// OpenQuestion is only available for the encrypted short-term policy and
// rejects expired or context-swapped envelopes with non-sensitive errors.
func (policy *RetentionPolicy) OpenQuestion(
	binding QuestionRetentionBinding,
	envelope QuestionEnvelope,
	now time.Time,
) (string, error) {
	if policy == nil || policy.mode != OriginalQuestionEncryptedShortTerm ||
		policy.questionCipher == nil || binding.validate() != nil ||
		envelope.validateShape() != nil || !envelope.matches(binding) || now.IsZero() {
		return "", ErrQuestionEnvelopeInvalid
	}
	if envelope.ExpiresAt == nil || !now.UTC().Before(*envelope.ExpiresAt) {
		return "", ErrQuestionRetentionExpired
	}
	sealed, err := base64.RawURLEncoding.DecodeString(envelope.Ciphertext)
	if err != nil || len(sealed) <= policy.questionCipher.NonceSize() {
		return "", ErrQuestionEnvelopeInvalid
	}
	aad, err := envelope.associatedData()
	if err != nil {
		return "", ErrQuestionEnvelopeInvalid
	}
	nonceSize := policy.questionCipher.NonceSize()
	plain, err := policy.questionCipher.Open(nil, sealed[:nonceSize], sealed[nonceSize:], aad)
	if err != nil || !utf8.Valid(plain) || len(plain) == 0 || len(plain) > maxRetainedQuestionSize {
		return "", ErrQuestionEnvelopeInvalid
	}
	return string(plain), nil
}

func (envelope QuestionEnvelope) validateShape() error {
	for _, id := range []askdata.ID{envelope.TenantID, envelope.DomainID, envelope.ActorID,
		envelope.ConversationID, envelope.RunID, envelope.Release.ReleaseID} {
		if !retentionCanonicalUUID(id) {
			return ErrQuestionEnvelopeInvalid
		}
	}
	if envelope.Version != questionEnvelopeVersion || envelope.CreatedAt.IsZero() ||
		envelope.Release.Validate() != nil || envelope.PolicyScopeHash.Validate() != nil ||
		envelope.QuestionHash.Validate() != nil {
		return ErrQuestionEnvelopeInvalid
	}
	switch envelope.Mode {
	case OriginalQuestionHashOnly:
		if envelope.Ciphertext != "" || envelope.ExpiresAt != nil {
			return ErrQuestionEnvelopeInvalid
		}
	case OriginalQuestionEncryptedShortTerm:
		if envelope.Ciphertext == "" || envelope.ExpiresAt == nil ||
			!envelope.ExpiresAt.After(envelope.CreatedAt) {
			return ErrQuestionEnvelopeInvalid
		}
	default:
		return ErrQuestionEnvelopeInvalid
	}
	return nil
}

func (envelope QuestionEnvelope) matches(binding QuestionRetentionBinding) bool {
	return envelope.TenantID == binding.Scope.TenantID &&
		envelope.DomainID == binding.DomainID && envelope.ActorID == binding.Scope.ActorID &&
		envelope.ConversationID == binding.ConversationID && envelope.RunID == binding.RunID &&
		envelope.Release == binding.Scope.Release && envelope.PolicyScopeHash == binding.Scope.PolicyHash &&
		envelope.QuestionHash == binding.QuestionHash
}

func (envelope QuestionEnvelope) associatedData() ([]byte, error) {
	document := struct {
		DomainSeparator string               `json:"domainSeparator"`
		Version         string               `json:"version"`
		Mode            OriginalQuestionMode `json:"mode"`
		TenantID        askdata.ID           `json:"tenantId"`
		DomainID        askdata.ID           `json:"domainId"`
		ActorID         askdata.ID           `json:"actorId"`
		ConversationID  askdata.ID           `json:"conversationId"`
		RunID           askdata.ID           `json:"runId"`
		Release         askdata.ReleaseRef   `json:"release"`
		PolicyScopeHash askdata.ContentHash  `json:"policyScopeHash"`
		QuestionHash    askdata.ContentHash  `json:"questionHash"`
		CreatedAt       time.Time            `json:"createdAt"`
		ExpiresAt       *time.Time           `json:"expiresAt"`
	}{
		DomainSeparator: string(questionEnvelopeAssociatedData),
		Version:         envelope.Version, Mode: envelope.Mode, TenantID: envelope.TenantID,
		DomainID: envelope.DomainID, ActorID: envelope.ActorID,
		ConversationID: envelope.ConversationID, RunID: envelope.RunID,
		Release: envelope.Release, PolicyScopeHash: envelope.PolicyScopeHash,
		QuestionHash: envelope.QuestionHash, CreatedAt: envelope.CreatedAt,
		ExpiresAt: envelope.ExpiresAt,
	}
	raw, err := json.Marshal(document)
	if err != nil {
		return nil, ErrQuestionEnvelopeInvalid
	}
	return raw, nil
}

type ConversationInheritanceDecision string

const (
	ConversationInherit    ConversationInheritanceDecision = "INHERIT"
	ConversationResetScope ConversationInheritanceDecision = "RESET_SCOPE"
)

// ResolveConversationInheritance allows only a completed, governed answer in
// the same tenant/actor/domain/conversation/release to seed the next turn.
// A role-policy change resets inherited content rather than leaking it.
func ResolveConversationInheritance(
	previous Run,
	currentScope askdata.PolicyScope,
	domainID, conversationID askdata.ID,
) (ConversationInheritanceDecision, error) {
	if previous.Validate() != nil || currentScope.Validate() != nil ||
		!retentionCanonicalUUID(domainID) || !retentionCanonicalUUID(conversationID) {
		return "", ErrConversationInheritance
	}
	if previous.TenantID != currentScope.TenantID || previous.ActorID != currentScope.ActorID ||
		previous.DomainID != domainID || previous.ConversationID != conversationID ||
		previous.Release != currentScope.Release {
		return "", ErrConversationInheritance
	}
	if previous.State != StateAnswered || !previous.Hashes.completeAnswerChain() {
		return "", ErrConversationInheritance
	}
	if previous.PolicyScopeHash != currentScope.PolicyHash {
		return ConversationResetScope, nil
	}
	return ConversationInherit, nil
}

// ArtifactDigest is the permanent, payload-free part of a replay artifact.
type ArtifactDigest struct {
	ID            askdata.ID          `json:"id"`
	Index         int                 `json:"index"`
	RunVersion    int64               `json:"runVersion"`
	Type          ArtifactType        `json:"type"`
	SchemaVersion string              `json:"schemaVersion"`
	Hash          askdata.ContentHash `json:"hash"`
	EvidenceIDs   []askdata.ID        `json:"evidenceIds"`
	CreatedAt     time.Time           `json:"createdAt"`
}

// ImmutableRunStatistics is payload-free and remains stable before and after
// an artifact TTL sweep. Hashing the projection makes accidental mutation
// detectable without retaining user text, result rows, or artifact payloads.
type ImmutableRunStatistics struct {
	Version        string                `json:"version"`
	TenantID       askdata.ID            `json:"tenantId"`
	DomainID       askdata.ID            `json:"domainId"`
	RunID          askdata.ID            `json:"runId"`
	Release        askdata.ReleaseRef    `json:"release"`
	State          State                 `json:"state"`
	Disposition    Disposition           `json:"disposition"`
	CompletionCode string                `json:"completionCode,omitempty"`
	Usage          BudgetUsage           `json:"usage"`
	EventCount     int                   `json:"eventCount"`
	ArtifactCount  int                   `json:"artifactCount"`
	ToolCallCount  int                   `json:"toolCallCount"`
	ArtifactHashes []askdata.ContentHash `json:"artifactHashes"`
	CreatedAt      time.Time             `json:"createdAt"`
	CompletedAt    *time.Time            `json:"completedAt,omitempty"`
	StatisticsHash askdata.ContentHash   `json:"statisticsHash"`
}

// ArtifactPurgePlan separates expiring payload IDs from the immutable ledger
// projection that a storage adapter must preserve atomically.
type ArtifactPurgePlan struct {
	PayloadExpiresAt   time.Time              `json:"payloadExpiresAt"`
	Expired            bool                   `json:"expired"`
	PayloadArtifactIDs []askdata.ID           `json:"payloadArtifactIds"`
	Digests            []ArtifactDigest       `json:"digests"`
	Statistics         ImmutableRunStatistics `json:"statistics"`
}

func (policy *RetentionPolicy) PlanArtifactPurge(
	snapshot ReplaySnapshot,
	now time.Time,
) (ArtifactPurgePlan, error) {
	if policy == nil || policy.runArtifactTTL <= 0 || now.IsZero() || snapshot.Validate() != nil {
		return ArtifactPurgePlan{}, ErrRetentionPolicyInvalid
	}
	digests := make([]ArtifactDigest, 0, len(snapshot.Artifacts))
	artifactIDs := make([]askdata.ID, 0, len(snapshot.Artifacts))
	hashes := make([]askdata.ContentHash, 0, len(snapshot.Artifacts))
	for _, artifact := range snapshot.Artifacts {
		digests = append(digests, ArtifactDigest{
			ID: artifact.ID, Index: artifact.Index, RunVersion: artifact.RunVersion,
			Type: artifact.Type, SchemaVersion: artifact.SchemaVersion, Hash: artifact.Hash,
			EvidenceIDs: append([]askdata.ID(nil), artifact.EvidenceIDs...), CreatedAt: artifact.CreatedAt,
		})
		artifactIDs = append(artifactIDs, artifact.ID)
		hashes = append(hashes, artifact.Hash)
	}
	sort.Slice(hashes, func(i, j int) bool { return hashes[i] < hashes[j] })
	statistics := ImmutableRunStatistics{
		Version: runStatisticsVersion, TenantID: snapshot.Run.TenantID,
		DomainID: snapshot.Run.DomainID, RunID: snapshot.Run.ID,
		Release: snapshot.Run.Release, State: snapshot.Run.State,
		Disposition: snapshot.Run.Disposition, CompletionCode: snapshot.Run.CompletionCode,
		Usage: snapshot.Run.Usage, EventCount: len(snapshot.Events),
		ArtifactCount: len(snapshot.Artifacts), ToolCallCount: len(snapshot.ToolCalls),
		ArtifactHashes: hashes, CreatedAt: snapshot.Run.CreatedAt,
		CompletedAt: snapshot.Run.CompletedAt,
	}
	statisticsHash, err := immutableStatisticsHash(statistics)
	if err != nil {
		return ArtifactPurgePlan{}, err
	}
	statistics.StatisticsHash = statisticsHash
	expiresAt := snapshot.Run.CreatedAt.UTC().Add(policy.runArtifactTTL)
	expired := !now.UTC().Before(expiresAt)
	if !expired {
		artifactIDs = nil
	}
	return ArtifactPurgePlan{
		PayloadExpiresAt: expiresAt, Expired: expired,
		PayloadArtifactIDs: artifactIDs, Digests: digests, Statistics: statistics,
	}, nil
}

func immutableStatisticsHash(statistics ImmutableRunStatistics) (askdata.ContentHash, error) {
	statistics.StatisticsHash = ""
	raw, err := json.Marshal(statistics)
	if err != nil {
		return "", fmt.Errorf("%w: statistics", ErrRetentionPolicyInvalid)
	}
	return askdata.HashBytes(raw), nil
}

func retentionCanonicalUUID(id askdata.ID) bool {
	parsed, err := uuid.Parse(string(id))
	return err == nil && parsed.String() == string(id)
}
