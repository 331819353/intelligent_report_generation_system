package orchestrator

import (
	"encoding/json"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/cognition"
)

func confidenceEvidence(score, margin float64) askdata.ConfidenceEvidence {
	return askdata.ConfidenceEvidence{
		Score: score, Margin: margin,
		Evidence: []askdata.EvidenceRef{{
			EvidenceID: "10000000-0000-4000-8000-000000000001",
			Kind:       askdata.EvidenceKindCandidateSet,
			SourceID:   "20000000-0000-4000-8000-000000000002",
			ContentHash: askdata.ContentHash(
				"1111111111111111111111111111111111111111111111111111111111111111",
			),
		}},
		ReasonCodes: []string{"TEST"},
	}
}

// 低置信提案不得推进运行。设计原则 9 要求低置信必须继续取证、澄清或阻断；
// 在此之前 ConfidenceEvidence 被记录却从不被读取，自报 0.1 与自报 1.0 的
// 提案推进方式完全相同。
func TestLowConfidenceProposalIsRefused(t *testing.T) {
	verdict := GateProposalConfidence(cognition.Action{
		BindingProposal: &cognition.BindingProposal{
			Confidence: confidenceEvidence(0.10, 0.90),
		},
	})
	if verdict.Accepted || verdict.ConflictCode != "LOW_CONFIDENCE_PROPOSAL" {
		t.Fatalf("low-confidence binding accepted: %+v", verdict)
	}
}

// 高分但零边际同样要澄清：次优候选几乎一样好，这种歧义单看分数看不出来。
func TestConfidentButAmbiguousProposalIsRefused(t *testing.T) {
	verdict := GateProposalConfidence(cognition.Action{
		PlanProposal: &cognition.PlanProposal{
			Confidence: confidenceEvidence(0.99, 0.0),
		},
	})
	if verdict.Accepted || verdict.ConflictCode != "AMBIGUOUS_CANDIDATE_MARGIN" {
		t.Fatalf("ambiguous plan accepted: %+v", verdict)
	}
}

// 确定性绑定快路径自报 Score/Margin 均为 1，必须能通过门禁，
// 否则这道门会把系统里唯一不依赖模型的绑定路径一起挡掉。
func TestDeterministicBindingPassesTheGate(t *testing.T) {
	verdict := GateProposalConfidence(cognition.Action{
		BindingProposal: &cognition.BindingProposal{
			Confidence: confidenceEvidence(1, 1),
		},
	})
	if !verdict.Accepted {
		t.Fatalf("deterministic binding refused: %+v", verdict)
	}
}

// 不带提案的动作不归这道门管：CALL_TOOL / CLARIFY / BLOCK 另有治理，
// 让澄清本身去过置信门禁会形成循环。
func TestActionsWithoutAProposalArePassedThrough(t *testing.T) {
	for name, action := range map[string]cognition.Action{
		"call tool": {Action: cognition.ActionCallTool},
		"clarify":   {Action: cognition.ActionClarify},
		"block":     {Action: cognition.ActionBlock},
	} {
		if verdict := GateProposalConfidence(action); !verdict.Accepted {
			t.Fatalf("%s was gated: %+v", name, verdict)
		}
	}
}

// 能澄清的状态优先澄清，用户通常可以解决这种歧义；不能澄清的状态必须阻断，
// 否则被拒绝的提案照样推进，这道门就成了摆设。
func TestRefusedProposalClarifiesWhereItCanAndBlocksWhereItCannot(t *testing.T) {
	if target := confidenceRedirect(StateBinding); target != StateClarificationRequired {
		t.Fatalf("BINDING redirect = %s, want CLARIFICATION_REQUIRED", target)
	}
	if target := confidenceRedirect(StateExecuting); target != StateBlocked {
		t.Fatalf("EXECUTING redirect = %s, want BLOCKED", target)
	}
}

// 拒绝必须落成可读、审计安全的终态工件，而不是一次沉默的停摆。
func TestConfidenceRefusalProducesAnAuditSafeTerminalArtifact(t *testing.T) {
	verdict := ConfidenceVerdict{
		ConflictCode: "LOW_CONFIDENCE_PROPOSAL", Confidence: confidenceEvidence(0.1, 0.9),
	}
	for _, target := range []State{StateClarificationRequired, StateBlocked} {
		completion, err := confidenceCompletion(target, verdict)
		if err != nil {
			t.Fatalf("%s: confidenceCompletion() error = %v", target, err)
		}
		if completion.Type != completionArtifactType(target) {
			t.Fatalf("%s: artifact type = %s", target, completion.Type)
		}
		if !completionCodePattern.MatchString(completion.Code) {
			t.Fatalf("%s: completion code %q is not a stable code", target, completion.Code)
		}
		var payload any
		if json.Unmarshal(completion.Payload, &payload) != nil || !auditJSONSafe(payload) {
			t.Fatalf("%s: payload is not audit-safe: %s", target, completion.Payload)
		}
	}
}

// 被拒绝的澄清必须能被读取接口解析出来，否则用户只看到一次无解释的中断。
func TestRefusedClarificationIsReadableByTheQuestionAPI(t *testing.T) {
	completion, err := confidenceCompletion(StateClarificationRequired, ConfidenceVerdict{
		ConflictCode: "AMBIGUOUS_CANDIDATE_MARGIN", Confidence: confidenceEvidence(0.99, 0),
	})
	if err != nil {
		t.Fatalf("confidenceCompletion() error = %v", err)
	}
	var payload struct {
		ConflictCode          string `json:"conflictCode"`
		ClarificationQuestion string `json:"clarificationQuestion"`
		Retryable             bool   `json:"retryable"`
	}
	if json.Unmarshal(completion.Payload, &payload) != nil {
		t.Fatalf("payload did not decode: %s", completion.Payload)
	}
	// parsePublicClarification needs options or a retryable flag; without either
	// it returns nil and the API surfaces nothing at all.
	if payload.ConflictCode == "" || payload.ClarificationQuestion == "" || !payload.Retryable {
		t.Fatalf("clarification payload is not renderable: %+v", payload)
	}
}
