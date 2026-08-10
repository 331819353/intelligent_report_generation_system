package datarequest

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	platformidempotency "intelligent-report-generation-system/internal/platform/idempotency"
)

type dataRequestIdempotencyRepository struct {
	mu     sync.Mutex
	record platformidempotency.Record
}

func (repository *dataRequestIdempotencyRepository) Begin(
	_ context.Context, _ platformidempotency.Identity, _, _ string, hash string, _ time.Time,
) (platformidempotency.Record, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.record.State == "" {
		repository.record = platformidempotency.Record{
			State: platformidempotency.StateInFlight, RequestHash: hash,
		}
		return platformidempotency.Record{
			State: platformidempotency.StateAcquired, RequestHash: hash,
		}, nil
	}
	if repository.record.RequestHash != hash {
		return platformidempotency.Record{
			State: platformidempotency.StateReused, RequestHash: repository.record.RequestHash,
		}, nil
	}
	return repository.record, nil
}

func (repository *dataRequestIdempotencyRepository) Complete(
	_ context.Context, _ platformidempotency.Identity, _, _, hash string, status int, body []byte,
) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	repository.record = platformidempotency.Record{
		State: platformidempotency.StateReplay, RequestHash: hash,
		ResponseStatus: status, ResponseBody: append([]byte(nil), body...),
	}
	return nil
}

func (repository *dataRequestIdempotencyRepository) Release(
	_ context.Context, _ platformidempotency.Identity, _, _, _ string,
) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	repository.record = platformidempotency.Record{}
	return nil
}

func TestHandlerCreatesListsAndSubmitsDataRequest(t *testing.T) {
	now := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	identity := testIdentity()
	store := &fakeStore{}
	service := NewService(store)
	service.now = func() time.Time { return now }
	handler := newProtectedHandler(service, func(context.Context) (Identity, error) {
		return identity, nil
	})
	datasetVersionID := uuid.NewString()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/data-requests", strings.NewReader(fmt.Sprintf(`{
		"requestText":"导出订单明细",
		"parsedContext":{},
		"businessPurpose":"月度经营复盘",
		"requiredFields":[{"datasetVersionId":"%s","fieldId":"order_id"}],
		"slaDueAt":"2026-08-10T09:00:00Z"
	}`, datasetVersionID)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || store.createCalls != 1 ||
		!strings.Contains(response.Body.String(), `"state":"DRAFT"`) {
		t.Fatalf("create = %d/%s, calls=%d", response.Code, response.Body.String(), store.createCalls)
	}
	requestID := store.createdCommand.ID

	listResponse := httptest.NewRecorder()
	handler.ServeHTTP(listResponse, httptest.NewRequest(http.MethodGet, "/api/v1/data-requests?limit=20", nil))
	if listResponse.Code != http.StatusOK || !strings.Contains(listResponse.Body.String(), requestID) {
		t.Fatalf("list = %d/%s", listResponse.Code, listResponse.Body.String())
	}

	submit := httptest.NewRequest(
		http.MethodPost, "/api/v1/data-requests/"+requestID+"/submit",
		strings.NewReader(`{"recordVersion":1}`),
	)
	submit.Header.Set("Content-Type", "application/json")
	submitResponse := httptest.NewRecorder()
	handler.ServeHTTP(submitResponse, submit)
	if submitResponse.Code != http.StatusOK ||
		!strings.Contains(submitResponse.Body.String(), `"state":"SUBMITTED"`) {
		t.Fatalf("submit = %d/%s", submitResponse.Code, submitResponse.Body.String())
	}
}

func TestHandlerRejectsUnknownFieldsAndUnauthenticatedRequests(t *testing.T) {
	identity := testIdentity()
	service := NewService(&fakeStore{})
	handler := newProtectedHandler(service, func(context.Context) (Identity, error) {
		return identity, nil
	})
	request := httptest.NewRequest(
		http.MethodPost, "/api/v1/data-requests",
		strings.NewReader(`{"requestText":"x","sql":"select secret"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || strings.Contains(response.Body.String(), "select secret") {
		t.Fatalf("unknown field = %d/%s", response.Code, response.Body.String())
	}

	unauthenticated := newProtectedHandler(service, func(context.Context) (Identity, error) {
		return Identity{}, ErrPermissionDenied
	})
	response = httptest.NewRecorder()
	unauthenticated.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/data-requests", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated = %d/%s", response.Code, response.Body.String())
	}
}

func TestCreateDataRequestUsesSharedIdempotencyBoundary(t *testing.T) {
	identity := testIdentity()
	store := &fakeStore{}
	service := NewService(store)
	service.now = func() time.Time { return time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC) }
	repository := &dataRequestIdempotencyRepository{}
	protected := newProtectedHandler(service, func(context.Context) (Identity, error) {
		return identity, nil
	})
	handler := withIdempotency(protected, repository, func(context.Context) (Identity, error) {
		return identity, nil
	})
	body := fmt.Sprintf(`{
		"requestText":"导出订单明细",
		"parsedContext":{},
		"businessPurpose":"月度经营复盘",
		"requiredFields":[{"datasetVersionId":"%s","fieldId":"order_id"}],
		"slaDueAt":"2026-08-10T09:00:00Z"
	}`, uuid.NewString())

	missing := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/data-requests", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(missing, request)
	if missing.Code != http.StatusBadRequest || !strings.Contains(missing.Body.String(), "IDEMPOTENCY_KEY_REQUIRED") ||
		store.createCalls != 0 {
		t.Fatalf("missing key = %d/%s calls=%d", missing.Code, missing.Body.String(), store.createCalls)
	}

	serve := func() *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/data-requests", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", "data-request-create-0001")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	first, replay := serve(), serve()
	if first.Code != http.StatusCreated || replay.Code != http.StatusCreated || store.createCalls != 1 ||
		replay.Header().Get("Idempotency-Replayed") != "true" || first.Body.String() != replay.Body.String() {
		t.Fatalf("first=%d/%s replay=%d/%s calls=%d", first.Code, first.Body.String(), replay.Code, replay.Body.String(), store.createCalls)
	}
}

func TestHandlerMapsStableWorkflowErrors(t *testing.T) {
	for _, test := range []struct {
		err    error
		status int
		code   string
	}{
		{ErrApproverUnavailable, http.StatusConflict, "DATAREQ_APPROVER_UNAVAILABLE"},
		{ErrSecurityCosignRequired, http.StatusConflict, "DATAREQ_SECURITY_COSIGN_REQUIRED"},
		{ErrInvalidTransition, http.StatusConflict, "DATAREQ_TRANSITION_INVALID"},
		{ErrPermissionDenied, http.StatusForbidden, "DATAREQ_PERMISSION_DENIED"},
		{ErrVersionConflict, http.StatusConflict, "DATAREQ_VERSION_CONFLICT"},
	} {
		response := httptest.NewRecorder()
		writeServiceError(response, test.err)
		if response.Code != test.status || !strings.Contains(response.Body.String(), `"code":"`+test.code+`"`) {
			t.Fatalf("error %v = %d/%s", test.err, response.Code, response.Body.String())
		}
	}
}

func TestHandlerEnqueuesControlledExportWithoutRowsOrStorageLocation(t *testing.T) {
	now := time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC)
	identity := testIdentity()
	requestID := uuid.NewString()
	store := &exportStoreFixture{}
	store.items = map[string]Request{requestID: {
		ID: requestID, TenantID: identity.TenantID, DomainID: identity.DomainID,
		State: StateApproved, RecordVersion: 3, ApproverUserIDs: []string{identity.ActorID},
		SensitivityLevel: SensitivityInternal,
		RequiredFields:   []FieldRef{{DatasetVersionID: uuid.NewString(), FieldID: "order_id"}},
	}}
	service := NewService(store)
	service.exportBridge.now = func() time.Time { return now }
	handler := newProtectedHandler(service, func(context.Context) (Identity, error) {
		return identity, nil
	})
	request := httptest.NewRequest(
		http.MethodPost, "/api/v1/data-requests/"+requestID+"/exports",
		strings.NewReader(`{"recordVersion":3}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || !strings.Contains(response.Body.String(), `"state":"PENDING"`) ||
		strings.Contains(strings.ToLower(response.Body.String()), "row") ||
		strings.Contains(strings.ToLower(response.Body.String()), "storage") ||
		strings.Contains(strings.ToLower(response.Body.String()), "sql") {
		t.Fatalf("controlled export response=%d/%s", response.Code, response.Body.String())
	}
}
