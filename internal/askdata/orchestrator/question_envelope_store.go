package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/cognition"
	"intelligent-report-generation-system/internal/askdata/understanding"
	"intelligent-report-generation-system/internal/platform/database"
)

var ErrQuestionContextUnavailable = errors.New("question context is unavailable")

// QuestionFactSource reconstructs the one conversation fact every cognition
// stage needs. The raw question exists only inside this short-lived value.
type QuestionFactSource interface {
	LoadQuestionFact(context.Context, askdata.PolicyScope, askdata.ID, Run) (GovernedFact, error)
}

// PostgresQuestionEnvelopeStore persists only authenticated ciphertext. It is
// shared by the API writer and the worker reader, which use different database
// roles and the same actor/domain RLS policy.
type PostgresQuestionEnvelopeStore struct {
	pool   *pgxpool.Pool
	policy *RetentionPolicy
}

func NewPostgresQuestionEnvelopeStore(
	pool *pgxpool.Pool,
	policy *RetentionPolicy,
) (*PostgresQuestionEnvelopeStore, error) {
	if pool == nil || policy == nil || policy.QuestionMode() != OriginalQuestionEncryptedShortTerm {
		return nil, ErrRetentionPolicyInvalid
	}
	return &PostgresQuestionEnvelopeStore{pool: pool, policy: policy}, nil
}

func (store *PostgresQuestionEnvelopeStore) SaveQuestion(
	ctx context.Context,
	binding QuestionRetentionBinding,
	rawQuestion string,
	now time.Time,
) error {
	if store == nil || store.pool == nil || store.policy == nil || ctx == nil {
		return ErrQuestionContextUnavailable
	}
	envelope, err := store.policy.RetainQuestion(binding, rawQuestion, now)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("%w: encode envelope", ErrQuestionContextUnavailable)
	}
	ctx = database.WithAccessContext(ctx, string(binding.Scope.ActorID), string(binding.DomainID))
	return database.WithTenantTx(ctx, store.pool, string(binding.Scope.TenantID), func(tx pgx.Tx) error {
		tag, insertErr := tx.Exec(ctx, `INSERT INTO askdata.question_envelopes(
			run_id,tenant_id,domain_id,actor_id,conversation_id,release_id,
			release_content_hash,policy_scope_hash,question_hash,envelope_json,expires_at
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT(run_id,tenant_id) DO NOTHING`,
			binding.RunID, binding.Scope.TenantID, binding.DomainID, binding.Scope.ActorID,
			binding.ConversationID, binding.Scope.Release.ReleaseID,
			binding.Scope.Release.ContentHash, binding.Scope.PolicyHash, binding.QuestionHash,
			encoded, envelope.ExpiresAt)
		if insertErr != nil {
			return insertErr
		}
		if tag.RowsAffected() == 1 {
			return nil
		}
		var persisted QuestionEnvelope
		if scanErr := tx.QueryRow(ctx, `SELECT envelope_json
			FROM askdata.question_envelopes
			WHERE run_id=$1 AND tenant_id=$2`, binding.RunID, binding.Scope.TenantID).
			Scan(&persisted); scanErr != nil {
			return scanErr
		}
		if persisted.validateShape() != nil || !persisted.matches(binding) {
			return ErrQuestionContextUnavailable
		}
		return nil
	})
}

// SaveClarificationQuestion carries the original encrypted question into a
// clarification child and appends only the governed option label selected by
// the user. The child receives a fresh envelope bound to its own run, release,
// policy hash and derived question hash; ciphertext is never copied between
// bindings because the AEAD associated data intentionally makes that invalid.
func (store *PostgresQuestionEnvelopeStore) SaveClarificationQuestion(
	ctx context.Context,
	parentBinding QuestionRetentionBinding,
	childBinding QuestionRetentionBinding,
	optionLabel string,
	now time.Time,
) error {
	if store == nil || store.pool == nil || store.policy == nil || ctx == nil ||
		parentBinding.validate() != nil || childBinding.validate() != nil ||
		parentBinding.Scope.TenantID != childBinding.Scope.TenantID ||
		parentBinding.Scope.ActorID != childBinding.Scope.ActorID ||
		parentBinding.DomainID != childBinding.DomainID ||
		parentBinding.ConversationID != childBinding.ConversationID ||
		parentBinding.RunID == childBinding.RunID || strings.TrimSpace(optionLabel) == "" ||
		len([]byte(optionLabel)) > 512 || now.IsZero() {
		return ErrQuestionContextUnavailable
	}

	var parentEnvelope QuestionEnvelope
	ctx = database.WithAccessContext(ctx, string(parentBinding.Scope.ActorID), string(parentBinding.DomainID))
	err := database.WithTenantTx(ctx, store.pool, string(parentBinding.Scope.TenantID), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT envelope_json
			FROM askdata.question_envelopes
			WHERE run_id=$1 AND tenant_id=$2 AND expires_at>clock_timestamp()`,
			parentBinding.RunID, parentBinding.Scope.TenantID).Scan(&parentEnvelope)
	})
	if err != nil {
		return fmt.Errorf("%w: load parent envelope", ErrQuestionContextUnavailable)
	}
	rawQuestion, err := store.policy.OpenQuestion(parentBinding, parentEnvelope, now)
	if err != nil {
		return fmt.Errorf("%w: open parent envelope", ErrQuestionContextUnavailable)
	}
	continuedQuestion := rawQuestion + "\n\n用户已选择的澄清条件：" + strings.TrimSpace(optionLabel)
	return store.SaveQuestion(ctx, childBinding, continuedQuestion, now)
}

func (store *PostgresQuestionEnvelopeStore) LoadQuestionFact(
	ctx context.Context,
	scope askdata.PolicyScope,
	domainID askdata.ID,
	run Run,
) (GovernedFact, error) {
	if store == nil || store.pool == nil || store.policy == nil || ctx == nil ||
		run.Validate() != nil || runMatchesScope(run, scope, domainID) == false || run.ConversationID == "" {
		return GovernedFact{}, ErrQuestionContextUnavailable
	}
	binding := QuestionRetentionBinding{
		Scope: scope, DomainID: domainID, ConversationID: run.ConversationID,
		RunID: run.ID, QuestionHash: run.QuestionHash,
	}
	var envelope QuestionEnvelope
	ctx = database.WithAccessContext(ctx, string(scope.ActorID), string(domainID))
	err := database.WithTenantTx(ctx, store.pool, string(scope.TenantID), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT envelope_json
			FROM askdata.question_envelopes
			WHERE run_id=$1 AND tenant_id=$2 AND expires_at>clock_timestamp()`,
			run.ID, scope.TenantID).Scan(&envelope)
	})
	if err != nil {
		return GovernedFact{}, fmt.Errorf("%w: %v", ErrQuestionContextUnavailable, err)
	}
	rawQuestion, err := store.policy.OpenQuestion(binding, envelope, time.Now().UTC())
	if err != nil {
		return GovernedFact{}, fmt.Errorf("%w: %v", ErrQuestionContextUnavailable, err)
	}
	payloadValue := struct {
		Question  string                         `json:"question"`
		RuleParse *understanding.RuleParseResult `json:"ruleParse,omitempty"`
	}{Question: rawQuestion}
	// Relative periods are deterministic calendar facts, not an LLM judgment.
	// Pin their reference date to run creation so retries and delayed workers
	// always see the same half-open range.
	normalized, normalizeErr := understanding.NormalizeQuestion(rawQuestion)
	if normalizeErr == nil {
		parser, parserErr := understanding.NewRuleParser(run.CreatedAt, 0)
		if parserErr == nil {
			rules, parseErr := parser.Parse(normalized)
			if parseErr == nil {
				payloadValue.RuleParse = &rules
			}
		}
	}
	payload, err := json.Marshal(payloadValue)
	if err != nil {
		return GovernedFact{}, ErrQuestionContextUnavailable
	}
	evidenceID := askdata.ID(askdata.HashBytes([]byte("question-fact-v1\x00" + string(run.ID) + "\x00" + string(run.QuestionHash))))
	fact, err := cognition.NewPromptFact(evidenceID, cognition.FactConversation, payload)
	if err != nil {
		return GovernedFact{}, fmt.Errorf("%w: %v", ErrQuestionContextUnavailable, err)
	}
	evidence := askdata.EvidenceRef{
		EvidenceID: fact.EvidenceID, Kind: askdata.EvidenceKindConversation,
		SourceID: run.ID, ContentHash: fact.ContentHash,
	}
	return GovernedFact{Fact: fact, Evidence: evidence}, nil
}

var _ QuestionFactSource = (*PostgresQuestionEnvelopeStore)(nil)
