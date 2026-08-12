package askdatahttp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/registry"
	"intelligent-report-generation-system/internal/platform/database"
)

type fakeReleaseLifecycleBackend struct {
	*fakeAdminBackend
	activation  registry.ReleaseActivationResult
	gate        registry.ReleaseGateResult
	reviewInput registry.ReleaseReviewReportInput
	err         error
}

func (backend *fakeReleaseLifecycleBackend) ValidateAndStartProjection(context.Context, registry.AdminScope, string) (registry.ReleaseProjectionStartResult, error) {
	return registry.ReleaseProjectionStartResult{}, backend.err
}
func (backend *fakeReleaseLifecycleBackend) RetryFailedProjections(context.Context, registry.AdminScope, string) (registry.ReleaseProjectionRetryResult, error) {
	return registry.ReleaseProjectionRetryResult{Status: "PROJECTING", RetriedCount: 1}, backend.err
}
func (backend *fakeReleaseLifecycleBackend) PlanEvaluationBatch(context.Context, registry.AdminScope, string, registry.EvaluationBatchPlanInput) (registry.EvaluationBatchPlanResult, error) {
	return registry.EvaluationBatchPlanResult{}, backend.err
}
func (backend *fakeReleaseLifecycleBackend) RecordErrorBudget(context.Context, registry.AdminScope, string, registry.ErrorBudgetAttachmentInput) (string, error) {
	return strings.Repeat("a", 64), backend.err
}
func (backend *fakeReleaseLifecycleBackend) RecomputeReleaseGate(context.Context, registry.AdminScope, string, registry.ReleaseGateInput) (registry.ReleaseGateResult, error) {
	return backend.gate, backend.err
}
func (backend *fakeReleaseLifecycleBackend) RecordReleaseReviewReport(_ context.Context, _ registry.AdminScope, _ string, input registry.ReleaseReviewReportInput) (string, error) {
	backend.reviewInput = input
	return strings.Repeat("b", 64), backend.err
}
func (backend *fakeReleaseLifecycleBackend) SubmitReleaseApproval(context.Context, registry.AdminScope, string, registry.ReleaseApprovalInput) (registry.ReleaseApprovalResult, error) {
	return registry.ReleaseApprovalResult{}, backend.err
}
func (backend *fakeReleaseLifecycleBackend) ActivateRelease(context.Context, registry.AdminScope, string, registry.ReleaseActivationInput) (registry.ReleaseActivationResult, error) {
	return backend.activation, backend.err
}
func (backend *fakeReleaseLifecycleBackend) GetReleaseLifecycle(context.Context, registry.AdminScope, string) (registry.ReleaseLifecycleSnapshot, error) {
	return registry.ReleaseLifecycleSnapshot{}, backend.err
}

func TestReleaseLifecycleActivationRouteAndIdempotencyKey(t *testing.T) {
	scope := testAdminScope()
	releaseID, setID, batchID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	backend := &fakeReleaseLifecycleBackend{
		fakeAdminBackend: &fakeAdminBackend{},
		activation: registry.ReleaseActivationResult{
			Activated: true, ActiveReleaseID: releaseID, ReleaseStateVersion: 2,
			GateReceiptHash: strings.Repeat("a", 64), Failures: []string{},
		},
	}
	handler := newProtectedAdminHandler(backend, func(context.Context) (registry.AdminScope, error) {
		return scope, nil
	})
	body := `{"evaluationSetId":"` + setID + `","evaluationBatchId":"` + batchID + `","expectedStateVersion":1}`
	request := httptest.NewRequest(http.MethodPost,
		"/api/v1/askdata/semantic/releases/"+releaseID+"/activate", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "release-activation-1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"activated":true`) ||
		!strings.Contains(response.Body.String(), `"releaseStateVersion":2`) {
		t.Fatalf("activation response = %d %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost,
		"/api/v1/askdata/semantic/releases/"+releaseID+"/activate", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("missing key response = %d %s", response.Code, response.Body.String())
	}
}

func TestReleaseLifecycleGateFailureUsesStableHTTPCode(t *testing.T) {
	scope := testAdminScope()
	releaseID := uuid.NewString()
	backend := &fakeReleaseLifecycleBackend{fakeAdminBackend: &fakeAdminBackend{}, err: registry.ErrReleaseGateFailed}
	handler := newProtectedAdminHandler(backend, func(context.Context) (registry.AdminScope, error) {
		return scope, nil
	})
	request := httptest.NewRequest(http.MethodPost,
		"/api/v1/askdata/semantic/releases/"+releaseID+"/gate",
		strings.NewReader(`{"evaluationSetId":"`+uuid.NewString()+`","evaluationBatchId":"`+uuid.NewString()+`"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "release-gate-1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), `"code":"RELEASE_GATE_FAILED"`) {
		t.Fatalf("gate response = %d %s", response.Code, response.Body.String())
	}
}

type releaseReviewHTTPInvoker struct {
	result     ai.InvocationResult
	invocation ai.Invocation
}

func (invoker *releaseReviewHTTPInvoker) Invoke(_ context.Context, invocation ai.Invocation) (ai.InvocationResult, error) {
	invoker.invocation = invocation
	return invoker.result, nil
}

func TestReleaseReviewGenerateRouteRecomputesDatabaseGateAndPersistsModelAudit(t *testing.T) {
	scope := testAdminScope()
	releaseID, setID, batchID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	gateFacts := json.RawMessage(`{"databaseRecomputed":true,"caseCount":2000,"failureCodes":[]}`)
	gateHash := strings.Repeat("a", 64)
	evidenceID := askdata.ID("release-gate-" + gateHash[:16])
	envelope := registry.ReleaseReviewEnvelope{
		SchemaVersion: registry.ReleaseReviewSchemaVersion, Recommendation: "APPROVE",
		ImpactCodes: []string{"RELEASE_GATE_PASSED"}, FailureClusterCodes: []string{},
		Risks:       []registry.ReleaseReviewRisk{{Code: "CANARY_REVIEW_REQUIRED", Severity: "LOW", EvidenceIDs: []askdata.ID{evidenceID}}},
		EvidenceIDs: []askdata.ID{evidenceID},
	}
	modelOutput, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	invoker := &releaseReviewHTTPInvoker{result: ai.InvocationResult{
		RequestID: "release-review-http-1", Attempts: 1, CostMicros: 7,
		ProviderResult: ai.ProviderResult{Content: modelOutput, Model: "review-model-v1"},
	}}
	reviewer, err := registry.NewReleaseReviewer(invoker)
	if err != nil {
		t.Fatal(err)
	}
	backend := &fakeReleaseLifecycleBackend{
		fakeAdminBackend: &fakeAdminBackend{},
		gate:             registry.ReleaseGateResult{Passed: true, ReceiptHash: gateHash, Facts: gateFacts, Failures: []string{}},
	}
	reviewService, err := registry.NewReleaseReviewService(reviewer, backend)
	if err != nil {
		t.Fatal(err)
	}
	handler := newProtectedAdminHandlerWithImports(
		backend, func(context.Context) (registry.AdminScope, error) { return scope, nil },
		nil, nil, nil, ImportMutationServices{ReleaseReview: reviewService},
	)
	body := `{"evaluationSetId":"` + setID + `","evaluationBatchId":"` + batchID + `","promptVersion":"release-review-v1","preferredModel":"review-model-v1"}`
	request := httptest.NewRequest(http.MethodPost,
		"/api/v1/askdata/semantic/releases/"+releaseID+"/review-report/generate", strings.NewReader(body))
	request = request.WithContext(database.WithAccessContext(request.Context(), scope.ActorID, scope.DomainID))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "generate-review-1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), `"aiRequestId":"release-review-http-1"`) ||
		backend.reviewInput.GateReceiptHash != gateHash || backend.reviewInput.Recommendation != "APPROVE" ||
		!strings.Contains(string(backend.reviewInput.Report), `"providerModel":"review-model-v1"`) {
		t.Fatalf("review response=%d %s input=%+v", response.Code, response.Body.String(), backend.reviewInput)
	}
	if invoker.invocation.ResourceID != releaseID || strings.Contains(string(backend.reviewInput.Report), "hidden reasoning") {
		t.Fatalf("review invocation/input=%+v / %s", invoker.invocation, backend.reviewInput.Report)
	}
}

var _ registry.ReleaseLifecycleBackend = (*fakeReleaseLifecycleBackend)(nil)
