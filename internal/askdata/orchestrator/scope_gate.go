package orchestrator

import (
	"encoding/json"

	"intelligent-report-generation-system/internal/askdata/cognition"
	"intelligent-report-generation-system/internal/askdata/understanding"
)

// scopeVerdictFor classifies the question before the UNDERSTANDING stage spends
// a model call on it.
//
// The read API has surfaced a scopeVerdict field since question.go:812, keyed on
// a BLOCK artifact carrying ScopeVerdictSchemaVersion, but nothing ever produced
// one: understanding.Classify had no production caller, so OUT_OF_SCOPE was a
// terminal state no run could reach and the refusal contract — "this is a detail
// export, go file a data request" — was unreachable UI. Runs that should have
// been refused in one deterministic step instead entered the loop and burned
// budget failing to bind an unanswerable question.
//
// Classification is deliberately deterministic and rule-first. Whether a
// question is in scope at all is a governance decision, not a judgment call, and
// it must not depend on a model being available or agreeable.
//
// ok is false when the question is answerable, when no question fact is
// present, or when the verdict is malformed — the run proceeds normally, since
// failing open here only costs a normal run, while failing closed on a bad
// classification would refuse answerable questions.
func scopeVerdictFor(facts []GovernedFact) (understanding.ScopeVerdict, bool) {
	question, ok := questionTextFromFacts(facts)
	if !ok {
		return understanding.ScopeVerdict{}, false
	}
	_, verdict := understanding.Classify(understanding.QuestionUnderstanding{
		SchemaVersion: understanding.SchemaVersion, Question: question,
	})
	if verdict.Outcome != understanding.ScopeOutcomeOutOfScope || verdict.Validate() != nil {
		return understanding.ScopeVerdict{}, false
	}
	return verdict, true
}

// questionTextFromFacts reads the raw question out of the governed conversation
// fact. It is the same envelope the loop already receives, so no new decryption
// boundary is introduced here.
func questionTextFromFacts(facts []GovernedFact) (string, bool) {
	for _, governed := range facts {
		if governed.Fact.Kind != cognition.FactConversation {
			continue
		}
		var payload struct {
			Question string `json:"question"`
		}
		if json.Unmarshal(governed.Fact.Payload, &payload) == nil && payload.Question != "" {
			return payload.Question, true
		}
	}
	return "", false
}

// scopeCompletion turns an out-of-scope verdict into the terminal artifact the
// question API already knows how to read.
//
// The verdict is stored verbatim because it is the contract: its nextActions
// carry the redirect (file a data request, open the definition card) that makes
// a refusal useful rather than a dead end. ScopeVerdict is built to be
// audit-safe — it carries no raw question text, only the classification and its
// lexicon provenance.
func scopeCompletion(verdict understanding.ScopeVerdict) (*CompletionArtifactInput, error) {
	payload, err := json.Marshal(verdict)
	if err != nil {
		return nil, err
	}
	return &CompletionArtifactInput{
		Code:          upperCompletionCode(string(verdict.Reason), "QUESTION_OUT_OF_SCOPE"),
		Type:          ArtifactBlock,
		SchemaVersion: understanding.ScopeVerdictSchemaVersion,
		Payload:       payload,
	}, nil
}
