package askdatahttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/orchestrator"
)

type fakeBackend struct {
	createResult        OperationResult
	createErr           error
	createInput         CreateQuestionInput
	createIdentity      RequestIdentity
	createCalls         int
	getSnapshots        []orchestrator.ReplaySnapshot
	getErr              error
	getCalls            int
	clarificationResult OperationResult
	clarificationErr    error
	clarificationInput  SubmitClarificationInput
	clarificationCalls  int
}

func (backend *fakeBackend) CreateQuestion(
	_ context.Context,
	identity RequestIdentity,
	input CreateQuestionInput,
) (OperationResult, error) {
	backend.createCalls++
	backend.createIdentity = identity
	backend.createInput = input
	return backend.createResult, backend.createErr
}

func (backend *fakeBackend) GetQuestion(
	_ context.Context,
	_ RequestIdentity,
	_ askdata.ID,
) (orchestrator.ReplaySnapshot, error) {
	backend.getCalls++
	if backend.getErr != nil {
		return orchestrator.ReplaySnapshot{}, backend.getErr
	}
	if len(backend.getSnapshots) == 0 {
		return orchestrator.ReplaySnapshot{}, orchestrator.ErrRunNotFound
	}
	index := backend.getCalls - 1
	if index >= len(backend.getSnapshots) {
		index = len(backend.getSnapshots) - 1
	}
	return backend.getSnapshots[index], nil
}

func (backend *fakeBackend) SubmitClarification(
	_ context.Context,
	_ RequestIdentity,
	input SubmitClarificationInput,
) (OperationResult, error) {
	backend.clarificationCalls++
	backend.clarificationInput = input
	return backend.clarificationResult, backend.clarificationErr
}

func TestCreateQuestionHashesRawInputAndReturnsReconnectContract(t *testing.T) {
	identity := testIdentity()
	snapshot := testSnapshot(orchestrator.StateReceived, 1)
	backend := &fakeBackend{createResult: OperationResult{Snapshot: snapshot}}
	handler := testHandler(backend, identity)
	rawQuestion := "各渠道销售额是多少？"
	request := httptest.NewRequest(http.MethodPost, "/api/v1/questions", strings.NewReader(
		`{"question":"  `+rawQuestion+`  "}`,
	))
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	request.Header.Set("Idempotency-Key", "request-0001")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if backend.createCalls != 1 || backend.createIdentity != identity {
		t.Fatalf("create call = %d, identity = %#v", backend.createCalls, backend.createIdentity)
	}
	if backend.createInput.QuestionHash != askdata.HashBytes([]byte(questionHashDomain+rawQuestion)) ||
		backend.createInput.IdempotencyKeyHash != askdata.HashBytes([]byte(questionIdempotencyDomain+"request-0001")) ||
		!canonicalUUID(backend.createInput.ConversationID) {
		t.Fatalf("create input = %#v", backend.createInput)
	}
	body := response.Body.String()
	if strings.Contains(body, rawQuestion) || strings.Contains(body, string(backend.createInput.QuestionHash)) {
		t.Fatalf("response leaks question material: %s", body)
	}
	var view OperationView
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil ||
		view.RunID != snapshot.Run.ID || view.EventsURL != "/api/v1/questions/"+string(snapshot.Run.ID)+"/events" {
		t.Fatalf("operation view = %#v, %v", view, err)
	}
}

func TestCreateQuestionRejectsUnauthenticatedMalformedAndOversizedRequests(t *testing.T) {
	identity := testIdentity()
	backend := &fakeBackend{}
	unauthenticated := newProtectedHandler(
		backend,
		func(context.Context) (RequestIdentity, error) { return RequestIdentity{}, ErrUnauthenticated },
		defaultStreamOptions(),
	)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/questions", strings.NewReader(`{"question":"safe"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "request-0002")
	response := httptest.NewRecorder()
	unauthenticated.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || backend.createCalls != 0 {
		t.Fatalf("unauthenticated response = %d/%s, calls=%d", response.Code, response.Body.String(), backend.createCalls)
	}

	tests := []struct {
		name        string
		body        string
		contentType string
		key         string
	}{
		{name: "unknown field", body: `{"question":"safe","sql":"select secret"}`, contentType: "application/json", key: "request-0003"},
		{name: "trailing object", body: `{"question":"safe"}{}`, contentType: "application/json", key: "request-0004"},
		{name: "wrong media type", body: `{"question":"safe"}`, contentType: "text/plain", key: "request-0005"},
		{name: "missing key", body: `{"question":"safe"}`, contentType: "application/json"},
		{name: "oversized", body: `{"question":"` + strings.Repeat("问", maxQuestionBodyBytes) + `"}`, contentType: "application/json", key: "request-0006"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := testHandler(backend, identity)
			request := httptest.NewRequest(http.MethodPost, "/api/v1/questions", strings.NewReader(test.body))
			request.Header.Set("Content-Type", test.contentType)
			if test.key != "" {
				request.Header.Set("Idempotency-Key", test.key)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
		})
	}
	if backend.createCalls != 0 {
		t.Fatalf("backend received invalid requests: %d", backend.createCalls)
	}
}

func TestQuestionHandlerRequiresBearerTokenBeforeBackend(t *testing.T) {
	backend := &fakeBackend{}
	handler := NewHandler(nil, backend)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/questions/"+uuid.NewString(), nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized || backend.getCalls != 0 ||
		!strings.Contains(response.Body.String(), "ACCESS_TOKEN_REQUIRED") {
		t.Fatalf("auth response = %d/%s, backend calls=%d", response.Code, response.Body.String(), backend.getCalls)
	}
}

func TestGetQuestionPublishesOnlyBoundedCompletionContract(t *testing.T) {
	identity := testIdentity()
	snapshot := testSnapshot(orchestrator.StateClarificationRequired, 2)
	completionHash := askdata.HashBytes([]byte("completion"))
	snapshot.Run.Disposition = orchestrator.DispositionClarify
	snapshot.Run.CompletionCode = "AMBIGUOUS_METRIC"
	snapshot.Run.CompletionArtifact = completionHash
	completedAt := time.Now().UTC()
	snapshot.Run.CompletedAt = &completedAt
	snapshot.Artifacts = []orchestrator.Artifact{{
		Hash: completionHash, Type: orchestrator.ArtifactClarification,
		EvidenceIDs: []askdata.ID{"evidence-safe"},
		Payload: json.RawMessage(`{
			"conflictCode":"AMBIGUOUS_METRIC",
			"clarificationQuestion":"请选择统计口径",
			"options":[{"optionId":"metric:revenue","label":"销售收入"}],
			"prompt":"DO NOT LEAK","sqlText":"SELECT secret","resultRows":["secret"]
		}`),
	}}
	backend := &fakeBackend{getSnapshots: []orchestrator.ReplaySnapshot{snapshot}}
	handler := testHandler(backend, identity)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/questions/"+string(snapshot.Run.ID), nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, secret := range []string{"DO NOT LEAK", "SELECT secret", "resultRows", "sqlText", `"prompt"`} {
		if strings.Contains(body, secret) {
			t.Fatalf("run response leaked %q: %s", secret, body)
		}
	}
	if !strings.Contains(body, "请选择统计口径") || !strings.Contains(body, "metric:revenue") ||
		!strings.Contains(body, "销售收入") {
		t.Fatalf("public clarification missing: %s", body)
	}
}

func TestSubmitClarificationAcceptsOnlyStableOptionID(t *testing.T) {
	identity := testIdentity()
	parent := testSnapshot(orchestrator.StateClarificationRequired, 2)
	child := testSnapshot(orchestrator.StateReceived, 1)
	child.Run.ParentRunID = parent.Run.ID
	child.Run.ConversationID = parent.Run.ConversationID
	backend := &fakeBackend{clarificationResult: OperationResult{Snapshot: child}}
	handler := testHandler(backend, identity)
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/questions/"+string(parent.Run.ID)+"/clarifications",
		strings.NewReader(`{"optionId":"metric:revenue"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "clarification-0001")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted || backend.clarificationCalls != 1 ||
		backend.clarificationInput.RunID != parent.Run.ID ||
		backend.clarificationInput.OptionID != "metric:revenue" ||
		backend.clarificationInput.IdempotencyKeyHash != askdata.HashBytes([]byte(
			clarificationHashDomain+string(parent.Run.ID)+"\x00clarification-0001",
		)) {
		t.Fatalf("clarification = %d/%s, input=%#v", response.Code, response.Body.String(), backend.clarificationInput)
	}

	invalid := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/questions/"+string(parent.Run.ID)+"/clarifications",
		strings.NewReader(`{"optionId":"metric:revenue","answer":"free text"}`),
	)
	invalid.Header.Set("Content-Type", "application/json")
	invalid.Header.Set("Idempotency-Key", "clarification-0002")
	invalidResponse := httptest.NewRecorder()
	handler.ServeHTTP(invalidResponse, invalid)
	if invalidResponse.Code != http.StatusBadRequest || backend.clarificationCalls != 1 {
		t.Fatalf("free-text clarification = %d/%s, calls=%d", invalidResponse.Code, invalidResponse.Body.String(), backend.clarificationCalls)
	}
}

func TestQuestionErrorsUseStableStatusWithoutInternalDetails(t *testing.T) {
	identity := testIdentity()
	runID := askdata.ID(uuid.NewString())
	for _, test := range []struct {
		err  error
		code int
	}{
		{orchestrator.ErrRunNotFound, http.StatusNotFound},
		{orchestrator.ErrPinnedScopeMismatch, http.StatusForbidden},
		{orchestrator.ErrIdempotencyConflict, http.StatusConflict},
		{errors.New("database password=secret"), http.StatusInternalServerError},
	} {
		backend := &fakeBackend{getErr: test.err}
		handler := testHandler(backend, identity)
		request := httptest.NewRequest(http.MethodGet, "/api/v1/questions/"+string(runID), nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.code || strings.Contains(response.Body.String(), "password") ||
			strings.Contains(response.Body.String(), "secret") {
			t.Fatalf("error response = %d/%s", response.Code, response.Body.String())
		}
	}
}

func TestClarificationOptionMustComeFromCompletionArtifact(t *testing.T) {
	snapshot := testSnapshot(orchestrator.StateClarificationRequired, 2)
	snapshot.Run.CompletionArtifact = askdata.HashBytes([]byte("clarification"))
	snapshot.Artifacts = []orchestrator.Artifact{{
		Hash: snapshot.Run.CompletionArtifact, Type: orchestrator.ArtifactClarification,
		Payload: json.RawMessage(`{
			"clarificationQuestion":"请选择",
			"options":[{"optionId":"option:a","label":"口径 A"},{"optionId":"option:b","label":"口径 B"}]
		}`),
	}}
	if !clarificationOptionAllowed(snapshot, "option:a") || clarificationOptionAllowed(snapshot, "option:c") {
		t.Fatal("clarification option allowlist was not enforced")
	}
	snapshot.Artifacts[0].Payload = json.RawMessage(`{"retryable":true}`)
	if !clarificationOptionAllowed(snapshot, "retry") || clarificationOptionAllowed(snapshot, "option:a") {
		t.Fatal("bounded retry option was not enforced")
	}
}

func testHandler(backend Backend, identity RequestIdentity) http.Handler {
	return newProtectedHandler(
		backend,
		func(context.Context) (RequestIdentity, error) { return identity, nil },
		defaultStreamOptions(),
	)
}

func testIdentity() RequestIdentity {
	return RequestIdentity{
		TenantID: askdata.ID(uuid.NewString()), ActorID: askdata.ID(uuid.NewString()),
		DomainID: askdata.ID(uuid.NewString()),
	}
}

func testSnapshot(state orchestrator.State, eventCount int) orchestrator.ReplaySnapshot {
	now := time.Now().UTC()
	run := orchestrator.Run{
		ID: askdata.ID(uuid.NewString()), TenantID: askdata.ID(uuid.NewString()),
		DomainID: askdata.ID(uuid.NewString()), ActorID: askdata.ID(uuid.NewString()),
		ConversationID: askdata.ID(uuid.NewString()), TraceID: askdata.ID(uuid.NewString()),
		Release: askdata.ReleaseRef{ReleaseID: askdata.ID(uuid.NewString()), ContentHash: askdata.HashBytes([]byte("release"))},
		State:   state, Disposition: orchestrator.DispositionPending,
		Limits: orchestrator.DefaultBudgetLimits(), RecordVersion: int64(eventCount),
		CreatedAt: now, UpdatedAt: now,
	}
	events := make([]orchestrator.Event, 0, eventCount)
	for index := 1; index <= eventCount; index++ {
		events = append(events, orchestrator.Event{
			ID: askdata.ID(uuid.NewString()), Index: index, RunVersion: int64(index),
			State: state, Type: orchestrator.EventStateTransition,
			Status: orchestrator.EventSucceeded, Code: "STATE_UPDATED", CreatedAt: now,
		})
	}
	return orchestrator.ReplaySnapshot{Run: run, Events: events}
}
