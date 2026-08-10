package registry

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/platform/database"
)

type releaseReviewRecorderFixture struct {
	input ReleaseReviewReportInput
	hash  string
}

func (recorder *releaseReviewRecorderFixture) RecordReleaseReviewReport(
	_ context.Context, _ AdminScope, _ string, input ReleaseReviewReportInput,
) (string, error) {
	recorder.input = input
	return recorder.hash, nil
}

func TestReleaseReviewerBindsStructuredOutputToKnownEvidence(t *testing.T) {
	request, evidence := validReleaseReviewRequest(t)
	envelope := ReleaseReviewEnvelope{
		SchemaVersion: ReleaseReviewSchemaVersion, Recommendation: "APPROVE",
		ImpactCodes: []string{"METRIC_DEFINITION_CHANGED"}, FailureClusterCodes: []string{},
		Risks:       []ReleaseReviewRisk{{Code: "DASHBOARD_SEMANTICS_CHANGED", Severity: "MEDIUM", EvidenceIDs: []askdata.ID{evidence.EvidenceID}}},
		EvidenceIDs: []askdata.ID{evidence.EvidenceID},
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	invoker := &assetReviewInvoker{result: ai.InvocationResult{
		RequestID: "release-review-ai-1", Attempts: 1, CostMicros: 13,
		ProviderResult: ai.ProviderResult{Content: raw, Model: "fixture-model"},
	}}
	reviewer, err := NewReleaseReviewer(invoker)
	if err != nil {
		t.Fatal(err)
	}
	result, err := reviewer.Review(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Report.Recommendation != "APPROVE" || result.ReportHash.Validate() != nil ||
		result.AIRequestID != "release-review-ai-1" ||
		invoker.invocation.ResourceType != "SEMANTIC_RELEASE_REVIEW" ||
		invoker.invocation.Purpose != ai.PurposeSemanticQuestion {
		t.Fatalf("result/invocation = %#v / %#v", result, invoker.invocation)
	}
	if strings.Contains(invoker.invocation.Request.Messages[0].Parts[0].Text, "override") ||
		!strings.Contains(invoker.invocation.Request.Messages[0].Parts[0].Text, "不得改变门禁") {
		t.Fatalf("system instruction = %q", invoker.invocation.Request.Messages[0].Parts[0].Text)
	}
}

func TestReleaseReviewerCannotApproveFailedGateOrCiteUnknownEvidence(t *testing.T) {
	request, evidence := validReleaseReviewRequest(t)
	known := map[askdata.ID]askdata.ContentHash{evidence.EvidenceID: evidence.ContentHash}
	envelope := ReleaseReviewEnvelope{
		SchemaVersion: ReleaseReviewSchemaVersion, Recommendation: "APPROVE",
		ImpactCodes: []string{}, FailureClusterCodes: []string{}, Risks: []ReleaseReviewRisk{},
		EvidenceIDs: []askdata.ID{evidence.EvidenceID},
	}
	if err := validateReleaseReviewEnvelope(envelope, false, known); err == nil {
		t.Fatal("failed gate was allowed to recommend APPROVE")
	}
	envelope.Recommendation = "REJECT"
	envelope.EvidenceIDs = []askdata.ID{"unknown-evidence"}
	if err := validateReleaseReviewEnvelope(envelope, false, known); err == nil {
		t.Fatal("unknown evidence citation was accepted")
	}
	_ = request
}

func TestReleaseReviewServicePersistsOnlyAuditedStructuredReport(t *testing.T) {
	request, evidence := validReleaseReviewRequest(t)
	envelope := ReleaseReviewEnvelope{
		SchemaVersion: ReleaseReviewSchemaVersion, Recommendation: "CONDITIONAL",
		ImpactCodes: []string{"DIMENSION_POLICY_CHANGED"}, FailureClusterCodes: []string{},
		Risks:       []ReleaseReviewRisk{{Code: "MEMBER_RECALL_RISK", Severity: "LOW", EvidenceIDs: []askdata.ID{evidence.EvidenceID}}},
		EvidenceIDs: []askdata.ID{evidence.EvidenceID},
	}
	raw, _ := json.Marshal(envelope)
	reviewer, err := NewReleaseReviewer(&assetReviewInvoker{result: ai.InvocationResult{
		RequestID: "release-review-ai-2", Attempts: 2,
		ProviderResult: ai.ProviderResult{Content: raw, Model: "fixture-model"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	recorder := &releaseReviewRecorderFixture{hash: strings.Repeat("f", 64)}
	service, err := NewReleaseReviewService(reviewer, recorder)
	if err != nil {
		t.Fatal(err)
	}
	scope := AdminScope{TenantID: request.TenantID, DomainID: request.DomainID, ActorID: request.ActorID}
	ctx := database.WithAccessContext(context.Background(), scope.ActorID, scope.DomainID)
	result, err := service.GenerateAndRecord(ctx, GenerateReleaseReviewRequest{
		Scope: scope, ReleaseID: request.ReleaseID,
		EvaluationSetID: uuid.NewString(), EvaluationBatchID: uuid.NewString(),
		Gate:          ReleaseGateResult{Passed: true, ReceiptHash: strings.Repeat("a", 64), Failures: []string{}, Facts: json.RawMessage(`{"databaseRecomputed":true}`)},
		PromptVersion: request.PromptVersion, Evidence: request.Evidence,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.PersistedReportHash != recorder.hash || recorder.input.Recommendation != "CONDITIONAL" ||
		!strings.Contains(string(recorder.input.Report), `"aiRequestId":"release-review-ai-2"`) ||
		strings.Contains(string(recorder.input.Report), "hidden reasoning") {
		t.Fatalf("service result/input = %#v / %s", result, recorder.input.Report)
	}
}

func TestReleaseReviewServiceCanClusterFailedGateButCannotApproveIt(t *testing.T) {
	request, evidence := validReleaseReviewRequest(t)
	envelope := ReleaseReviewEnvelope{
		SchemaVersion: ReleaseReviewSchemaVersion, Recommendation: "REJECT",
		ImpactCodes: []string{}, FailureClusterCodes: []string{"EVAL_SECURITY"},
		Risks:       []ReleaseReviewRisk{{Code: "SECURITY_GATE_FAILED", Severity: "CRITICAL", EvidenceIDs: []askdata.ID{evidence.EvidenceID}}},
		EvidenceIDs: []askdata.ID{evidence.EvidenceID},
	}
	raw, _ := json.Marshal(envelope)
	reviewer, err := NewReleaseReviewer(&assetReviewInvoker{result: ai.InvocationResult{
		RequestID: "release-review-ai-failed", Attempts: 1,
		ProviderResult: ai.ProviderResult{Content: raw, Model: "fixture-model"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	recorder := &releaseReviewRecorderFixture{hash: strings.Repeat("e", 64)}
	service, err := NewReleaseReviewService(reviewer, recorder)
	if err != nil {
		t.Fatal(err)
	}
	scope := AdminScope{TenantID: request.TenantID, DomainID: request.DomainID, ActorID: request.ActorID}
	ctx := database.WithAccessContext(context.Background(), scope.ActorID, scope.DomainID)
	result, err := service.GenerateAndRecord(ctx, GenerateReleaseReviewRequest{
		Scope: scope, ReleaseID: request.ReleaseID,
		EvaluationSetID: uuid.NewString(), EvaluationBatchID: uuid.NewString(),
		Gate: ReleaseGateResult{Passed: false, ReceiptHash: strings.Repeat("b", 64),
			Failures: []string{"EVAL_SECURITY"}, Facts: json.RawMessage(`{"databaseRecomputed":true}`)},
		PromptVersion: request.PromptVersion, Evidence: request.Evidence,
	})
	if err != nil || result.PersistedReportHash != recorder.hash || recorder.input.Recommendation != "REJECT" {
		t.Fatalf("failed-gate review result=%+v input=%+v err=%v", result, recorder.input, err)
	}
}

func validReleaseReviewRequest(t *testing.T) (ReleaseReviewLLMRequest, ReleaseReviewEvidence) {
	t.Helper()
	payload, err := CanonicalJSON(json.RawMessage(`{"databaseRecomputed":true,"caseCount":2000,"strictAccuracy":0.97}`))
	if err != nil {
		t.Fatal(err)
	}
	evidence := ReleaseReviewEvidence{
		EvidenceID: "release-gate-receipt", Kind: "EVALUATION_GATE",
		ContentHash: askdata.HashBytes(payload), Payload: payload,
	}
	return ReleaseReviewLLMRequest{
		TenantID: uuid.NewString(), DomainID: uuid.NewString(), ActorID: uuid.NewString(),
		ReleaseID: uuid.NewString(), PromptVersion: "semantic-release-review-v1",
		GatePassed: true, Evidence: []ReleaseReviewEvidence{evidence},
	}, evidence
}

var _ ReleaseReviewRecorder = (*releaseReviewRecorderFixture)(nil)
