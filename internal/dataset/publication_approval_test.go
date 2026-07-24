package dataset

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/access"
	"intelligent-report-generation-system/internal/auth"
)

const approvalTestRequestID = "55555555-5555-4555-8555-555555555555"

type publicationApprovalStoreStub struct {
	submitResult  PublicationRequest
	submitErr     error
	submitCalls   int
	submitTenant  string
	submitActor   string
	submitDataset string
	submitPlan    SubmitPublicationPlan

	listResult  []PublicationRequest
	listTotal   int
	listErr     error
	listCalls   int
	listTenant  string
	listDataset string
	listLimit   int
	listOffset  int

	request    PublicationRequest
	getErr     error
	getCalls   int
	getTenant  string
	getDataset string
	getRequest string

	saveResult      PublicationRequest
	saveErr         error
	saveCalls       int
	saveTenant      string
	saveDataset     string
	saveRequest     PublicationRequest
	savePreparation PublicationCandidatePreparation

	approveRequest         PublicationRequest
	approvePublished       VersionRecord
	approveErr             error
	approveCalls           int
	approveTenant          string
	approveActor           string
	approveDataset         string
	approveRequestID       string
	approveExpectedVersion int64
	approveNote            string
	approvePlan            PublishPlan

	rejectResult    PublicationRequest
	rejectErr       error
	rejectCalls     int
	rejectTenant    string
	rejectActor     string
	rejectDataset   string
	rejectRequestID string
	rejectInput     RejectPublicationInput
}

func (s *publicationApprovalStoreStub) SavePublicationCandidatePreparation(
	_ context.Context,
	tenantID, datasetID string,
	request PublicationRequest,
	preparation PublicationCandidatePreparation,
) (PublicationRequest, error) {
	s.saveCalls++
	s.saveTenant, s.saveDataset = tenantID, datasetID
	s.saveRequest, s.savePreparation = request, preparation
	if s.saveResult.ID != "" || s.saveErr != nil {
		return s.saveResult, s.saveErr
	}
	request.MetricCandidateStatus = preparation.Status
	request.MetricCandidateTotal = preparation.Total
	request.MetricCandidateReady = preparation.Ready
	request.MetricCandidateReview = preparation.Review
	request.MetricCandidateBlocked = preparation.Blocked
	request.MetricCandidateWarning = preparation.Warning
	request.MetricCandidateErrorCode = preparation.ErrorCode
	return request, nil
}

func (s *publicationApprovalStoreStub) SubmitPublicationRequest(
	_ context.Context,
	tenantID, actorID, datasetID string,
	plan SubmitPublicationPlan,
) (PublicationRequest, error) {
	s.submitCalls++
	s.submitTenant, s.submitActor, s.submitDataset, s.submitPlan = tenantID, actorID, datasetID, plan
	return s.submitResult, s.submitErr
}

func (s *publicationApprovalStoreStub) ListPublicationRequests(
	_ context.Context,
	tenantID, datasetID string,
	limit, offset int,
) ([]PublicationRequest, int, error) {
	s.listCalls++
	s.listTenant, s.listDataset, s.listLimit, s.listOffset = tenantID, datasetID, limit, offset
	return s.listResult, s.listTotal, s.listErr
}

func (s *publicationApprovalStoreStub) GetPublicationRequest(
	_ context.Context,
	tenantID, datasetID, requestID string,
) (PublicationRequest, error) {
	s.getCalls++
	s.getTenant, s.getDataset, s.getRequest = tenantID, datasetID, requestID
	return s.request, s.getErr
}

func (s *publicationApprovalStoreStub) ApproveAndPublish(
	_ context.Context,
	tenantID, actorID, datasetID, requestID string,
	expectedVersion int64,
	note string,
	plan PublishPlan,
) (PublicationRequest, VersionRecord, error) {
	s.approveCalls++
	s.approveTenant, s.approveActor, s.approveDataset = tenantID, actorID, datasetID
	s.approveRequestID, s.approveExpectedVersion, s.approveNote, s.approvePlan = requestID, expectedVersion, note, plan
	return s.approveRequest, s.approvePublished, s.approveErr
}

func (s *publicationApprovalStoreStub) RejectPublicationRequest(
	_ context.Context,
	tenantID, actorID, datasetID, requestID string,
	input RejectPublicationInput,
) (PublicationRequest, error) {
	s.rejectCalls++
	s.rejectTenant, s.rejectActor, s.rejectDataset, s.rejectRequestID, s.rejectInput = tenantID, actorID, datasetID, requestID, input
	return s.rejectResult, s.rejectErr
}

func publicationApprovalFixture(t *testing.T) (*memoryStore, *memoryPublicationValidator, *publicationApprovalStoreStub, *PublicationApprovalService, Prepared) {
	t.Helper()
	prepared, err := Prepare(readExample(t))
	if err != nil {
		t.Fatal(err)
	}
	datasetStore := &memoryStore{record: Record{
		ID: httpTestDatasetID, Status: "DRAFT", Version: 3,
		DraftVersionID: httpTestDraftID, DraftRecordVersion: 2,
		DSLHash: prepared.DSLHash, PlanHash: prepared.PlanHash,
		DSL: prepared.DSLJSON, LogicalPlan: prepared.LogicalPlanJSON,
	}}
	validator := &memoryPublicationValidator{}
	approvalStore := &publicationApprovalStoreStub{}
	datasetService := NewService(datasetStore, validator)
	return datasetStore, validator, approvalStore, NewPublicationApprovalService(approvalStore, datasetService), prepared
}

func pendingPublicationRequest(prepared Prepared) PublicationRequest {
	return PublicationRequest{
		ID: approvalTestRequestID, DatasetID: httpTestDatasetID,
		Status: PublicationRequestPending, Version: 1,
		DraftVersionID: httpTestDraftID, ExpectedDatasetVersion: 3,
		ExpectedDraftRecordVersion: 2, ExpectedDSLHash: prepared.DSLHash,
		ExpectedPlanHash: prepared.PlanHash, RequesterID: "requester-1",
		ValidationParameters:       map[string]any{"start_date": "2026-01-01"},
		ReservedPublishedVersionID: httpTestVersionID,
		MetricCandidateStatus:      PublicationCandidateLegacy,
	}
}

type publicationCandidateGeneratorStub struct {
	calls       int
	preparation PublicationCandidatePreparation
	err         error
	request     PublicationRequest
	draft       Record
}

func (s *publicationCandidateGeneratorStub) GeneratePublicationCandidates(
	_ context.Context,
	_, _ string,
	request PublicationRequest,
	draft Record,
) (PublicationCandidatePreparation, error) {
	s.calls++
	s.request, s.draft = request, draft
	return s.preparation, s.err
}

func submitPublicationInput(prepared Prepared) SubmitPublicationInput {
	return SubmitPublicationInput{
		DraftVersionID: httpTestDraftID, ExpectedVersion: 3,
		ExpectedDraftRecordVersion: 2, ExpectedDSLHash: prepared.DSLHash,
		ValidationParameters: map[string]any{"start_date": "2026-01-01"},
		Note:                 "  请核对订单口径  ",
	}
}

func TestPublicationApprovalServiceSubmitsFrozenDraftAndRejectsStaleEnvelope(t *testing.T) {
	_, _, store, service, prepared := publicationApprovalFixture(t)
	store.submitResult = pendingPublicationRequest(prepared)

	result, err := service.Submit(
		context.Background(), "tenant-1", "requester-1", httpTestDatasetID,
		submitPublicationInput(prepared),
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.ID != approvalTestRequestID || store.submitCalls != 1 ||
		store.submitTenant != "tenant-1" || store.submitActor != "requester-1" || store.submitDataset != httpTestDatasetID {
		t.Fatalf("result=%#v store=%#v", result, store)
	}
	if store.submitPlan.Input.Note != "请核对订单口径" ||
		store.submitPlan.Input.DraftVersionID != httpTestDraftID ||
		store.submitPlan.ExpectedPlanHash != prepared.PlanHash {
		t.Fatalf("submit plan=%#v", store.submitPlan)
	}
	var parameters map[string]any
	if err := json.Unmarshal(store.submitPlan.ParametersJSON, &parameters); err != nil || parameters["start_date"] != "2026-01-01" {
		t.Fatalf("parameters=%#v err=%v", parameters, err)
	}

	stale := submitPublicationInput(prepared)
	stale.ExpectedVersion--
	if _, err := service.Submit(context.Background(), "tenant-1", "requester-1", httpTestDatasetID, stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale submit error=%v", err)
	}
	if store.submitCalls != 1 {
		t.Fatalf("stale submission reached store: calls=%d", store.submitCalls)
	}
}

func TestPublicationApprovalServiceReturnsPendingRequestWithoutGeneratingCandidates(t *testing.T) {
	_, _, store, service, prepared := publicationApprovalFixture(t)
	pending := pendingPublicationRequest(prepared)
	pending.MetricCandidateStatus = PublicationCandidatePending
	store.submitResult = pending

	result, err := service.Submit(
		context.Background(), "tenant-1", "requester-1", httpTestDatasetID,
		submitPublicationInput(prepared),
	)
	if err != nil {
		t.Fatal(err)
	}
	if store.saveCalls != 0 {
		t.Fatalf("submission generated candidates in request path: store=%#v", store)
	}
	if result.MetricCandidateStatus != PublicationCandidatePending ||
		result.MetricCandidateTotal != 0 {
		t.Fatalf("result=%#v", result)
	}
}

func TestPublicationApprovalServiceApprovesThroughAtomicStoreCall(t *testing.T) {
	datasetStore, validator, store, service, prepared := publicationApprovalFixture(t)
	pending := pendingPublicationRequest(prepared)
	approved := pending
	approved.Status, approved.Version = PublicationRequestApproved, 2
	approved.ReviewerID = "reviewer-1"
	approved.PublishedVersionID = httpTestVersionID
	published := VersionRecord{
		ID: httpTestVersionID, DatasetID: httpTestDatasetID, Status: "PUBLISHED", VersionNo: 1,
		DraftVersionID: httpTestDraftID, DraftRecordVersion: 2,
		DSLHash: prepared.DSLHash, PlanHash: prepared.PlanHash,
	}
	store.request, store.approveRequest, store.approvePublished = pending, approved, published

	result, err := service.Approve(
		context.Background(), "tenant-1", "reviewer-1", httpTestDatasetID, approvalTestRequestID,
		ApprovePublicationInput{ExpectedVersion: 1, Note: "  同意发布  "},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Request.Status != PublicationRequestApproved || result.PublishedVersion.ID != httpTestVersionID {
		t.Fatalf("result=%#v", result)
	}
	if store.approveCalls != 1 || store.approveTenant != "tenant-1" || store.approveActor != "reviewer-1" ||
		store.approveDataset != httpTestDatasetID || store.approveRequestID != approvalTestRequestID ||
		store.approveExpectedVersion != 1 || store.approveNote != "同意发布" {
		t.Fatalf("approve store=%#v", store)
	}
	if store.approvePlan.IdempotencyKey != approvalTestRequestID || store.approvePlan.DraftVersionID != httpTestDraftID ||
		store.approvePlan.Prepared.DSLHash != prepared.DSLHash || store.approvePlan.Prepared.PlanHash != prepared.PlanHash ||
		store.approvePlan.RequestHash == "" {
		t.Fatalf("approve plan=%#v", store.approvePlan)
	}
	if validator.candidate.Parameters["start_date"] != "2026-01-01" || validator.candidate.DraftVersionID != httpTestDraftID {
		t.Fatalf("validation candidate=%#v", validator.candidate)
	}
	if datasetStore.publishCalls != 0 {
		t.Fatalf("approval fell back to non-atomic direct Publish: calls=%d", datasetStore.publishCalls)
	}
}

func TestPublicationApprovalServiceRejectsStaleApprovalBeforeCommit(t *testing.T) {
	datasetStore, validator, store, service, prepared := publicationApprovalFixture(t)
	store.request = pendingPublicationRequest(prepared)

	_, err := service.Approve(
		context.Background(), "tenant-1", "reviewer-1", httpTestDatasetID, approvalTestRequestID,
		ApprovePublicationInput{ExpectedVersion: 2},
	)
	if !errors.Is(err, ErrPublicationRequestConflict) || store.approveCalls != 0 || validator.candidate.DatasetID != "" {
		t.Fatalf("request-version conflict err=%v approveCalls=%d candidate=%#v", err, store.approveCalls, validator.candidate)
	}

	datasetStore.record.Version++
	_, err = service.Approve(
		context.Background(), "tenant-1", "reviewer-1", httpTestDatasetID, approvalTestRequestID,
		ApprovePublicationInput{ExpectedVersion: 1},
	)
	if !errors.Is(err, ErrConflict) || store.approveCalls != 0 || validator.candidate.DatasetID != "" {
		t.Fatalf("stale draft err=%v approveCalls=%d candidate=%#v", err, store.approveCalls, validator.candidate)
	}
}

func TestPublicationApprovalServiceDoesNotApproveBeforeCandidatesAreReady(t *testing.T) {
	_, validator, store, service, prepared := publicationApprovalFixture(t)
	pending := pendingPublicationRequest(prepared)
	pending.MetricCandidateStatus = PublicationCandidatePending
	store.request = pending

	_, err := service.Approve(
		context.Background(), "tenant-1", "reviewer-1", httpTestDatasetID, approvalTestRequestID,
		ApprovePublicationInput{ExpectedVersion: 1},
	)
	if !errors.Is(err, ErrPublicationCandidatesPending) || store.approveCalls != 0 ||
		validator.candidate.DatasetID != "" {
		t.Fatalf("err=%v approveCalls=%d candidate=%#v", err, store.approveCalls, validator.candidate)
	}
}

func TestPublicationApprovalServiceRejectsWithRequiredReason(t *testing.T) {
	_, _, store, service, prepared := publicationApprovalFixture(t)
	rejected := pendingPublicationRequest(prepared)
	rejected.Status, rejected.Version, rejected.ReviewNote = PublicationRequestRejected, 2, "缺少财务确认"
	store.rejectResult = rejected

	result, err := service.Reject(
		context.Background(), "tenant-1", "reviewer-1", httpTestDatasetID, approvalTestRequestID,
		RejectPublicationInput{ExpectedVersion: 1, Reason: "  缺少财务确认  "},
	)
	if err != nil || result.Status != PublicationRequestRejected || store.rejectCalls != 1 ||
		store.rejectInput.Reason != "缺少财务确认" || store.rejectInput.ExpectedVersion != 1 {
		t.Fatalf("result=%#v err=%v store=%#v", result, err, store)
	}
	if _, err := service.Reject(
		context.Background(), "tenant-1", "reviewer-1", httpTestDatasetID, approvalTestRequestID,
		RejectPublicationInput{ExpectedVersion: 1, Reason: "  "},
	); !errors.Is(err, ErrInvalidDocument) {
		t.Fatalf("blank rejection error=%v", err)
	}
	if store.rejectCalls != 1 {
		t.Fatalf("blank rejection reached store: calls=%d", store.rejectCalls)
	}
}

func newPublicationApprovalHTTPHarness(
	t *testing.T,
	datasetStore *memoryStore,
	approvalStore *publicationApprovalStoreStub,
	allow func(access.Check) bool,
) datasetHTTPHarness {
	t.Helper()
	tokenManager := auth.NewTokenManager("dataset-approval-http-test", "01234567890123456789012345678901", time.Hour)
	token, _, err := tokenManager.Issue(httpTestUserID, httpTestTenantID, httpTestSessionID, 1)
	if err != nil {
		t.Fatal(err)
	}
	authStore := &httpAuthStore{
		user: auth.LoginUser{ID: httpTestUserID, TenantID: httpTestTenantID, Status: auth.UserStatusActive, TokenVersion: 1},
		session: auth.Session{
			ID: httpTestSessionID, TenantID: httpTestTenantID, UserID: httpTestUserID,
			TokenVersion: 1, UserStatus: auth.UserStatusActive, ExpiresAt: time.Now().UTC().Add(time.Hour),
		},
	}
	authService := auth.NewService(authStore, auth.NewPasswordManager(4), tokenManager, time.Hour)
	permissionStore := &httpPermissionStore{allow: allow}
	permissions := access.NewService(permissionStore)
	validator := &httpPublicationValidator{}
	datasetService := NewService(datasetStore, validator)
	approvalService := NewPublicationApprovalService(approvalStore, datasetService)
	approvalHandler := NewPublicationApprovalHandler(authService, permissions, approvalService)

	// Mirror cmd/api route precedence: exact approval routes own publication while the generic
	// dataset subtree continues to serve the rest of the API.
	api := http.NewServeMux()
	api.Handle("POST /api/v1/datasets/{id}/publish", approvalHandler)
	api.Handle("POST /api/v1/datasets/{id}/publish-requests", approvalHandler)
	api.Handle("GET /api/v1/datasets/{id}/publish-requests", approvalHandler)
	api.Handle("POST /api/v1/datasets/{id}/publish-requests/{requestId}/approve", approvalHandler)
	api.Handle("POST /api/v1/datasets/{id}/publish-requests/{requestId}/reject", approvalHandler)
	api.Handle("/api/v1/datasets/", NewHandler(authService, permissions, datasetService))
	return datasetHTTPHarness{handler: api, token: token, permissions: permissionStore, validator: validator}
}

func TestPublicationApprovalHTTPPublicPublishSubmitsInsteadOfPublishing(t *testing.T) {
	datasetStore, _, approvalStore, _, prepared := publicationApprovalFixture(t)
	approvalStore.submitResult = pendingPublicationRequest(prepared)
	harness := newPublicationApprovalHTTPHarness(t, datasetStore, approvalStore, func(check access.Check) bool {
		return check.ResourceType == "DATASET" && check.Action == "MANAGE" && check.ObjectID == httpTestDatasetID
	})

	response := performDatasetHTTPRequest(
		t, harness, http.MethodPost, "/api/v1/datasets/"+httpTestDatasetID+"/publish",
		mustDatasetJSON(t, submitPublicationInput(prepared)), nil,
	)
	if response.Code != http.StatusAccepted || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	var body PublicationRequest
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body.ID != approvalTestRequestID || body.Status != PublicationRequestPending {
		t.Fatalf("body=%#v err=%v", body, err)
	}
	if approvalStore.submitCalls != 1 || datasetStore.publishCalls != 0 {
		t.Fatalf("submitCalls=%d directPublishCalls=%d", approvalStore.submitCalls, datasetStore.publishCalls)
	}
	if len(harness.permissions.checks) != 1 || harness.permissions.checks[0].Action != "MANAGE" ||
		harness.permissions.checks[0].ObjectID != httpTestDatasetID {
		t.Fatalf("permission checks=%#v", harness.permissions.checks)
	}
	if json.Valid(response.Body.Bytes()) && jsonContainsKey(response.Body.Bytes(), "validationParameters") {
		t.Fatalf("response leaked validation parameters: %s", response.Body.String())
	}
}

func TestPublicationApprovalHTTPRoutesSeparateReadManageAndPublish(t *testing.T) {
	newHarness := func(t *testing.T, allowedAction string) (datasetHTTPHarness, *publicationApprovalStoreStub, Prepared) {
		datasetStore, _, store, _, prepared := publicationApprovalFixture(t)
		pending := pendingPublicationRequest(prepared)
		approved := pending
		approved.Status, approved.Version, approved.PublishedVersionID = PublicationRequestApproved, 2, httpTestVersionID
		store.submitResult = pending
		store.listResult, store.listTotal = []PublicationRequest{pending}, 1
		store.request = pending
		store.approveRequest = approved
		store.approvePublished = VersionRecord{ID: httpTestVersionID, DatasetID: httpTestDatasetID, Status: "PUBLISHED"}
		rejected := pending
		rejected.Status, rejected.Version, rejected.ReviewNote = PublicationRequestRejected, 2, "口径待确认"
		store.rejectResult = rejected
		harness := newPublicationApprovalHTTPHarness(t, datasetStore, store, func(check access.Check) bool {
			return check.ResourceType == "DATASET" && check.Action == allowedAction && check.ObjectID == httpTestDatasetID
		})
		return harness, store, prepared
	}

	t.Run("list requires read", func(t *testing.T) {
		harness, store, _ := newHarness(t, "READ")
		response := performDatasetHTTPRequest(t, harness, http.MethodGet,
			"/api/v1/datasets/"+httpTestDatasetID+"/publish-requests?limit=25&offset=0", nil, nil)
		if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" || store.listCalls != 1 || store.listLimit != 25 {
			t.Fatalf("status=%d body=%s store=%#v", response.Code, response.Body.String(), store)
		}
		if len(harness.permissions.checks) != 1 || harness.permissions.checks[0].Action != "READ" {
			t.Fatalf("permission checks=%#v", harness.permissions.checks)
		}
	})

	t.Run("approve requires publish", func(t *testing.T) {
		harness, store, _ := newHarness(t, "PUBLISH")
		response := performDatasetHTTPRequest(t, harness, http.MethodPost,
			"/api/v1/datasets/"+httpTestDatasetID+"/publish-requests/"+approvalTestRequestID+"/approve",
			mustDatasetJSON(t, ApprovePublicationInput{ExpectedVersion: 1, Note: "同意"}), nil)
		if response.Code != http.StatusCreated || response.Header().Get("Cache-Control") != "no-store" || store.approveCalls != 1 {
			t.Fatalf("status=%d body=%s store=%#v", response.Code, response.Body.String(), store)
		}
		if len(harness.permissions.checks) != 1 || harness.permissions.checks[0].Action != "PUBLISH" {
			t.Fatalf("permission checks=%#v", harness.permissions.checks)
		}
	})

	t.Run("reject requires publish", func(t *testing.T) {
		harness, store, _ := newHarness(t, "PUBLISH")
		response := performDatasetHTTPRequest(t, harness, http.MethodPost,
			"/api/v1/datasets/"+httpTestDatasetID+"/publish-requests/"+approvalTestRequestID+"/reject",
			mustDatasetJSON(t, RejectPublicationInput{ExpectedVersion: 1, Reason: "口径待确认"}), nil)
		if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" || store.rejectCalls != 1 {
			t.Fatalf("status=%d body=%s store=%#v", response.Code, response.Body.String(), store)
		}
		if len(harness.permissions.checks) != 1 || harness.permissions.checks[0].Action != "PUBLISH" {
			t.Fatalf("permission checks=%#v", harness.permissions.checks)
		}
	})

	t.Run("manage cannot approve", func(t *testing.T) {
		harness, store, _ := newHarness(t, "MANAGE")
		response := performDatasetHTTPRequest(t, harness, http.MethodPost,
			"/api/v1/datasets/"+httpTestDatasetID+"/publish-requests/"+approvalTestRequestID+"/approve",
			mustDatasetJSON(t, ApprovePublicationInput{ExpectedVersion: 1}), nil)
		if response.Code != http.StatusForbidden || readDatasetErrorCode(t, response) != "PERMISSION_DENIED" || store.approveCalls != 0 {
			t.Fatalf("status=%d body=%s approveCalls=%d", response.Code, response.Body.String(), store.approveCalls)
		}
	})
}

func TestPublicationApprovalHTTPMapsConflictWithoutMutation(t *testing.T) {
	datasetStore, _, store, _, prepared := publicationApprovalFixture(t)
	store.request = pendingPublicationRequest(prepared)
	store.approveErr = ErrPublicationRequestConflict
	harness := newPublicationApprovalHTTPHarness(t, datasetStore, store, func(check access.Check) bool {
		return check.Action == "PUBLISH"
	})

	response := performDatasetHTTPRequest(t, harness, http.MethodPost,
		"/api/v1/datasets/"+httpTestDatasetID+"/publish-requests/"+approvalTestRequestID+"/approve",
		mustDatasetJSON(t, ApprovePublicationInput{ExpectedVersion: 1}), nil)
	if response.Code != http.StatusConflict || readDatasetErrorCode(t, response) != "DATASET_PUBLICATION_REQUEST_CONFLICT" ||
		datasetStore.publishCalls != 0 {
		t.Fatalf("status=%d body=%s directPublishCalls=%d", response.Code, response.Body.String(), datasetStore.publishCalls)
	}
}

func jsonContainsKey(raw []byte, key string) bool {
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	_, exists := value[key]
	return exists
}
