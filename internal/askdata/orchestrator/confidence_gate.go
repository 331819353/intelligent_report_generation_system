package orchestrator

import (
	"encoding/json"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/cognition"
)

// Confidence floors for proposals the platform has not independently verified.
//
// BindingProposal and PlanProposal both carry ConfidenceEvidence, and until this
// gate existed nothing read it: a proposal reporting 0.1 advanced a run exactly
// like one reporting 1.0. That left design principle 9 — a low-confidence
// question must keep gathering evidence, clarify, or block — with no
// implementation, and made clarification purely a matter of model goodwill.
//
// These are floors, not calibrated probabilities. A model's self-reported score
// is not evidence of correctness, so the floor is set where a self-report is
// already an admission of doubt: a model that says it is unsure is taken at its
// word, while a model that says it is sure still has to satisfy every
// downstream deterministic check. Raising the floor further would not buy
// accuracy, because the number is not trustworthy in the direction of
// confidence — only in the direction of doubt.
//
// binding.Calibrator supersedes this once a fitted CalibrationModel exists: a
// calibrated probability is a real estimate, whereas this is a floor. The model
// cannot be fitted without a golden set (EVAL-008/009/010), so the floor holds
// the line in the meantime rather than leaving the gate unimplemented.
const (
	MinProposalConfidenceScore  = 0.50
	MinProposalConfidenceMargin = 0.10
)

// ConfidenceVerdict is the gate's decision about one proposal.
type ConfidenceVerdict struct {
	// Accepted reports whether the run may advance on this proposal.
	Accepted bool
	// ConflictCode is the stable reason a rejected proposal was refused.
	ConflictCode string
	// Confidence is the evidence the proposal reported, carried through so the
	// refusal is auditable against what the model actually claimed.
	Confidence askdata.ConfidenceEvidence
}

// GateProposalConfidence applies the floor to whichever proposal an action
// carries. Actions without a proposal are not this gate's business and pass
// through untouched: CALL_TOOL, CLARIFY and BLOCK are already governed
// elsewhere, and a clarification that failed a confidence check would be
// circular.
func GateProposalConfidence(action cognition.Action) ConfidenceVerdict {
	var confidence askdata.ConfidenceEvidence
	switch {
	case action.BindingProposal != nil:
		confidence = action.BindingProposal.Confidence
	case action.PlanProposal != nil:
		confidence = action.PlanProposal.Confidence
	default:
		return ConfidenceVerdict{Accepted: true}
	}
	verdict := ConfidenceVerdict{Accepted: true, Confidence: confidence}
	switch {
	case confidence.Score < MinProposalConfidenceScore:
		verdict.Accepted = false
		verdict.ConflictCode = "LOW_CONFIDENCE_PROPOSAL"
	case confidence.Margin < MinProposalConfidenceMargin:
		// A high score with no margin means the runner-up was nearly as good.
		// That is the ambiguity clarification exists for, and it is invisible to
		// the score alone.
		verdict.Accepted = false
		verdict.ConflictCode = "AMBIGUOUS_CANDIDATE_MARGIN"
	}
	return verdict
}

// confidenceRedirect returns the state a refused proposal drives the run to.
//
// Clarifying is preferred wherever the state graph allows it, because the user
// can usually resolve the ambiguity the gate detected. Where it does not, the
// run blocks: advancing on a proposal the platform just refused would make the
// gate decorative.
func confidenceRedirect(current State) State {
	if clarifiableStates[current] {
		return StateClarificationRequired
	}
	return StateBlocked
}

// confidenceCompletion builds the terminal artifact for a refused proposal, so
// the refusal is durable and legible to the user rather than an opaque stall.
//
// The gate has no candidate list to offer — it rejected the proposal precisely
// because the platform could not tell which reading was meant — so the
// clarification is marked retryable. parsePublicClarification turns that into a
// retry affordance, which is the honest action here: the user rephrases, and
// the run is attempted again with a better question.
func confidenceCompletion(
	target State, verdict ConfidenceVerdict,
) (*CompletionArtifactInput, error) {
	message := confidenceClarificationQuestion(verdict.ConflictCode)
	code := upperCompletionCode(verdict.ConflictCode, "QUESTION_LOW_CONFIDENCE")
	if target != StateClarificationRequired {
		payload, err := json.Marshal(map[string]any{
			"code": verdict.ConflictCode, "publicMessage": message,
			"score": verdict.Confidence.Score, "margin": verdict.Confidence.Margin,
		})
		if err != nil {
			return nil, err
		}
		return &CompletionArtifactInput{
			Code: code, Type: ArtifactBlock, SchemaVersion: RunBlockSchemaVersion,
			EvidenceIDs: evidenceIDsOf(verdict.Confidence.Evidence), Payload: payload,
		}, nil
	}
	payload, err := json.Marshal(map[string]any{
		"conflictCode": verdict.ConflictCode, "clarificationQuestion": message,
		"retryable": true,
		"score":     verdict.Confidence.Score, "margin": verdict.Confidence.Margin,
	})
	if err != nil {
		return nil, err
	}
	return &CompletionArtifactInput{
		Code: code, Type: ArtifactClarification,
		SchemaVersion: RunClarificationSchemaVersion,
		EvidenceIDs:   evidenceIDsOf(verdict.Confidence.Evidence), Payload: payload,
	}, nil
}

func confidenceClarificationQuestion(conflictCode string) string {
	if conflictCode == "AMBIGUOUS_CANDIDATE_MARGIN" {
		return "有多个口径同样接近这个问题，请确认要用哪一个。"
	}
	return "这个问题的口径还不够明确，请补充一些信息。"
}
