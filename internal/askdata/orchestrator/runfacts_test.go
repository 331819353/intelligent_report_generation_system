package orchestrator

import (
	"bytes"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/cognition"
)

func TestDecisionFactPayloadDoesNotLeakPriorStageEvidenceReferences(t *testing.T) {
	prior := loopEvidence("candidate-evidence", askdata.EvidenceKindCandidateSet)
	action := bindingAction(cognition.StageDisambiguation, prior)

	payload, kind, _, ok, err := decisionFactPayload(action)
	if err != nil || !ok || kind != cognition.FactBindingEvidence {
		t.Fatalf("decisionFactPayload() = (%s, %s, %t, %v)", payload, kind, ok, err)
	}
	if bytes.Contains(payload, []byte(prior.EvidenceID)) || bytes.Contains(payload, []byte("evidenceRefs")) {
		t.Fatalf("carried fact leaked prior stage evidence: %s", payload)
	}
	if !bytes.Contains(payload, []byte("metric-sales-v1")) || !bytes.Contains(payload, []byte("CERTIFIED_MATCH")) {
		t.Fatalf("carried fact lost the validated binding decision: %s", payload)
	}
}
