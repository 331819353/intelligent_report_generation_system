package askdatahttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/answer"
	"intelligent-report-generation-system/internal/askdata/orchestrator"
	"intelligent-report-generation-system/internal/askdata/shared"
)

func TestWriteServiceErrorMapsReleaseNotRunnable(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeServiceError(recorder, orchestrator.ErrReleaseNotRunnable)
	if recorder.Code != http.StatusConflict ||
		!strings.Contains(recorder.Body.String(), `"code":"RELEASE_NOT_RUNNABLE"`) {
		t.Fatalf("response = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestWriteServiceErrorMapsProjectionMismatch(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeServiceError(recorder, orchestrator.ErrReleaseProjectionMismatch)
	if recorder.Code != http.StatusConflict ||
		!strings.Contains(recorder.Body.String(), `"code":"RELEASE_PROJECTION_MISMATCH"`) {
		t.Fatalf("response = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestWriteServiceErrorMapsReleaseDriftAndClarificationExpiry(t *testing.T) {
	conversationID := askdata.ID(uuid.NewString())
	previousReleaseID := askdata.ID(uuid.NewString())
	activeReleaseID := askdata.ID(uuid.NewString())
	recorder := httptest.NewRecorder()
	writeServiceError(recorder, &ReleaseDriftRequiredError{Drift: ReleaseDriftView{
		ConversationID: conversationID,
		Previous: ReleaseDescriptorView{
			ReleaseID: previousReleaseID, SemanticVersion: "2026.08", Status: "SUPERSEDED",
		},
		Active: ReleaseDescriptorView{
			ReleaseID: activeReleaseID, SemanticVersion: "2026.08.1", Status: "ACTIVE",
		},
	}})
	if recorder.Code != http.StatusConflict ||
		!strings.Contains(recorder.Body.String(), `"code":"RELEASE_DRIFT_CONFIRM_REQUIRED"`) ||
		!strings.Contains(recorder.Body.String(), `"conversationId":"`+string(conversationID)+`"`) ||
		!strings.Contains(recorder.Body.String(), `"releaseId":"`+string(activeReleaseID)+`"`) {
		t.Fatalf("release drift response = %d %s", recorder.Code, recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	writeServiceError(recorder, orchestrator.ErrClarificationExpired)
	if recorder.Code != http.StatusConflict ||
		!strings.Contains(recorder.Body.String(), `"code":"CLARIFICATION_EXPIRED"`) {
		t.Fatalf("clarification expiry response = %d %s", recorder.Code, recorder.Body.String())
	}
}

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
	confirmResult       ReleasePinResult
	confirmErr          error
	confirmInput        ConfirmReleaseDriftInput
	confirmIdentity     RequestIdentity
	confirmCalls        int
	feedbackResult      FeedbackResult
	feedbackErr         error
	feedbackInput       SubmitFeedbackInput
	feedbackIdentity    RequestIdentity
	feedbackCalls       int
	addToReportResult   AddToReportResult
	addToReportErr      error
	addToReportInput    AddToReportInput
	addToReportIdentity RequestIdentity
	addToReportCalls    int
}

type fakeQuestionRunStore struct {
	createResult   orchestrator.CreateResult
	createErr      error
	createRequests []orchestrator.CreateRunRequest
	resumeByID     map[askdata.ID]orchestrator.ReplaySnapshot
}

func (store *fakeQuestionRunStore) CreateRun(
	_ context.Context,
	request orchestrator.CreateRunRequest,
) (orchestrator.CreateResult, error) {
	store.createRequests = append(store.createRequests, request)
	return store.createResult, store.createErr
}

func (store *fakeQuestionRunStore) Resume(
	_ context.Context,
	request orchestrator.ResumeRequest,
) (orchestrator.ReplaySnapshot, error) {
	snapshot, ok := store.resumeByID[request.RunID]
	if !ok {
		return orchestrator.ReplaySnapshot{}, orchestrator.ErrRunNotFound
	}
	return snapshot, nil
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

func (backend *fakeBackend) ConfirmReleaseDrift(
	_ context.Context,
	identity RequestIdentity,
	input ConfirmReleaseDriftInput,
) (ReleasePinResult, error) {
	backend.confirmCalls++
	backend.confirmIdentity = identity
	backend.confirmInput = input
	if backend.confirmResult.ConversationID == "" {
		backend.confirmResult.ConversationID = input.ConversationID
	}
	return backend.confirmResult, backend.confirmErr
}

func (backend *fakeBackend) AddToReport(
	_ context.Context,
	identity RequestIdentity,
	input AddToReportInput,
) (AddToReportResult, error) {
	backend.addToReportCalls++
	backend.addToReportIdentity = identity
	backend.addToReportInput = input
	return backend.addToReportResult, backend.addToReportErr
}

func TestConfirmReleaseDriftRequiresStableContractAndCallsBackend(t *testing.T) {
	identity := testIdentity()
	conversationID := askdata.ID(uuid.NewString())
	previousReleaseID := askdata.ID(uuid.NewString())
	activeReleaseID := askdata.ID(uuid.NewString())
	backend := &fakeBackend{confirmResult: ReleasePinResult{
		ConversationID: conversationID,
		Release: ReleaseDescriptorView{
			ReleaseID: activeReleaseID, SemanticVersion: "2026.08.1", Status: "ACTIVE",
		},
	}}
	handler := testHandler(backend, identity)
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/conversations/"+string(conversationID)+"/release-drift",
		strings.NewReader(fmt.Sprintf(
			`{"previousReleaseId":"%s","activeReleaseId":"%s"}`,
			previousReleaseID, activeReleaseID,
		)),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "release-drift-0001")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || backend.confirmCalls != 1 ||
		backend.confirmIdentity != identity || backend.confirmInput.ConversationID != conversationID ||
		backend.confirmInput.PreviousReleaseID != previousReleaseID ||
		backend.confirmInput.ActiveReleaseID != activeReleaseID {
		t.Fatalf("release confirmation = %d/%s, input=%#v", response.Code, response.Body.String(), backend.confirmInput)
	}

	missingKey := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/conversations/"+string(conversationID)+"/release-drift",
		strings.NewReader(fmt.Sprintf(
			`{"previousReleaseId":"%s","activeReleaseId":"%s"}`,
			previousReleaseID, activeReleaseID,
		)),
	)
	missingKey.Header.Set("Content-Type", "application/json")
	missingKeyResponse := httptest.NewRecorder()
	handler.ServeHTTP(missingKeyResponse, missingKey)
	if missingKeyResponse.Code != http.StatusBadRequest || backend.confirmCalls != 1 {
		t.Fatalf("missing idempotency key = %d/%s, calls=%d", missingKeyResponse.Code, missingKeyResponse.Body.String(), backend.confirmCalls)
	}
}

func (backend *fakeBackend) SubmitFeedback(
	_ context.Context,
	identity RequestIdentity,
	input SubmitFeedbackInput,
) (FeedbackResult, error) {
	backend.feedbackCalls++
	backend.feedbackIdentity = identity
	backend.feedbackInput = input
	return backend.feedbackResult, backend.feedbackErr
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
	clarificationID := askdata.ID(uuid.NewString())
	ownerID := askdata.ID(uuid.NewString())
	snapshot.Run.Disposition = orchestrator.DispositionClarify
	snapshot.Run.CompletionCode = "AMBIGUOUS_METRIC"
	snapshot.Run.CompletionArtifact = completionHash
	completedAt := time.Now().UTC()
	snapshot.Run.CompletedAt = &completedAt
	snapshot.Artifacts = []orchestrator.Artifact{{
		ID: clarificationID, Hash: completionHash, Type: orchestrator.ArtifactClarification,
		EvidenceIDs: []askdata.ID{"evidence-safe"},
		Payload: json.RawMessage(fmt.Sprintf(`{
			"conflictCode":"AMBIGUOUS_METRIC",
			"clarificationQuestion":"请选择统计口径",
			"options":[{
				"optionId":"metric:revenue",
				"label":"销售收入",
				"difference":"是否扣除已确认退款",
				"evidenceIds":["evidence-safe"],
				"evidence":{
					"definition":"已支付订单金额，扣除取消订单。",
					"owner":{"id":"%s","displayName":"财务数据组"},
					"semanticVersion":"v3.2",
					"semanticStatus":"CERTIFIED",
					"time":{"label":"本月 MTD","start":"2026-08-01","end":"2026-08-06","timezone":"Asia/Shanghai"},
					"quality":{"status":"PASS","scorePermillion":987000,"dataAsOf":"2026-08-06T10:30:00+08:00","rulesPassed":12,"rulesTotal":12}
				}
			}],
			"prompt":"DO NOT LEAK","sqlText":"SELECT secret","resultRows":["secret"]
		}`, ownerID)),
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
		!strings.Contains(body, "销售收入") || !strings.Contains(body, string(clarificationID)) ||
		!strings.Contains(body, "是否扣除已确认退款") || !strings.Contains(body, "987000") {
		t.Fatalf("public clarification missing: %s", body)
	}
	var view RunView
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil || view.Completion == nil ||
		view.Completion.Clarification == nil || view.Completion.Clarification.ClarificationID != clarificationID ||
		len(view.Completion.Clarification.Options) != 1 ||
		view.Completion.Clarification.Options[0].Evidence == nil ||
		view.Completion.Clarification.Options[0].Evidence.Owner.ID != ownerID {
		t.Fatalf("public clarification DTO = %#v, %v", view.Completion, err)
	}
}

func TestGetQuestionPublishesOnlyValidatedResultPresentation(t *testing.T) {
	identity := testIdentity()
	snapshot := testSnapshot(orchestrator.StateAnswered, 12)
	completionHash := askdata.HashBytes([]byte("answer-completion"))
	ownerID := askdata.ID(uuid.NewString())
	snapshot.Run.Disposition = orchestrator.DispositionDirect
	snapshot.Run.CompletionCode = "ANSWER_READY"
	snapshot.Run.CompletionArtifact = completionHash
	completedAt := time.Now().UTC()
	snapshot.Run.CompletedAt = &completedAt
	snapshot.Artifacts = []orchestrator.Artifact{{
		ID: askdata.ID(uuid.NewString()), Hash: completionHash, Type: orchestrator.ArtifactAnswer,
		EvidenceIDs: []askdata.ID{"evidence:metric"},
		Payload: json.RawMessage(fmt.Sprintf(`{
			"result":{
				"schemaVersion":"question-result-v1",
				"title":"本月已支付订单销售额",
				"resolvedTimeSpec":{"requestedPeriod":"CURRENT_MONTH","grain":"MONTH","policyApplied":"MTD","policySource":"TIME_CONTRACT","resolvedStart":"2026-08-01T00:00:00+08:00","resolvedEndExclusive":"2026-08-07T00:00:00+08:00","dataAvailableThrough":"2026-08-06T10:30:00+08:00","truncatedByDataAvailability":true,"periodFallbackApplied":false,"timezone":"Asia/Shanghai","comparison":{"type":"YEAR_OVER_YEAR","periods":1,"alignment":"SAME_DAY_COUNT","resolvedStart":"2025-08-01T00:00:00+08:00","resolvedEndExclusive":"2025-08-07T00:00:00+08:00","overflowApplied":false}},
				"timeSpec":{"rangeLabel":"DO NOT TRUST","asOfLabel":"DO NOT TRUST","policyLabel":"DO NOT TRUST","comparisonLabel":"DO NOT TRUST","truncatedHint":"DO NOT TRUST"},
				"summary":{
					"metricLabel":"已支付订单销售额","value":"12846320","formattedValue":"¥12,846,320","unit":"CNY",
					"comparison":{"label":"较上期","direction":"UP","changePermillion":86000,"formattedChange":"+8.6%%","baselineStart":"2026-07-26","baselineEnd":"2026-07-31"},
					"time":{"label":"本月 MTD","start":"2026-08-01","end":"2026-08-06","timezone":"Asia/Shanghai"}
				},
				"evidenceIds":["evidence:metric"],
				"evidence":{
					"definition":"已支付订单金额，扣除取消订单，不扣除后续退款。",
					"owner":{"id":"%s","displayName":"王敏 · 财务数据组"},
					"semanticVersion":"v3.2","semanticStatus":"CERTIFIED",
					"time":{"label":"本月 MTD","start":"2026-08-01","end":"2026-08-06","timezone":"Asia/Shanghai"},
					"quality":{"status":"PASS","scorePermillion":987000,"dataAsOf":"2026-08-06T10:30:00+08:00","rulesPassed":12,"rulesTotal":12}
				},
				"datasets":[
					{"id":"dataset:trend","label":"每日销售额趋势","columns":[{"key":"day","label":"日期","type":"DATE","role":"DIMENSION"},{"key":"sales","label":"销售额（元）","type":"DECIMAL","role":"MEASURE"}],"rows":[{"day":"2026-08-01","sales":"1976540"},{"day":"2026-08-02","sales":"1982310"}],"page":1,"pageSize":2,"totalRows":2},
					{"id":"dataset:channel","label":"渠道销售额贡献","columns":[{"key":"channel","label":"渠道","type":"STRING","role":"DIMENSION"},{"key":"sales","label":"销售额（元）","type":"DECIMAL","role":"MEASURE"}],"rows":[{"channel":"电商渠道","sales":"6102430"},{"channel":"线下门店","sales":"3248760"}],"page":1,"pageSize":2,"totalRows":2},
					{"id":"dataset:detail","label":"渠道销售额明细","columns":[{"key":"channel","label":"渠道","type":"STRING","role":"DIMENSION"},{"key":"sales","label":"销售额（元）","type":"DECIMAL","role":"MEASURE"}],"rows":[{"channel":"电商渠道","sales":"6102430"},{"channel":"线下门店","sales":"3248760"}],"page":1,"pageSize":5,"totalRows":48}
				],
				"views":[
					{"id":"view:trend","type":"LINE","label":"趋势","datasetId":"dataset:trend","dimensionKeys":["day"],"measureKeys":["sales"]},
					{"id":"view:channel","type":"BAR","label":"渠道","datasetId":"dataset:channel","dimensionKeys":["channel"],"measureKeys":["sales"]},
					{"id":"view:detail","type":"TABLE","label":"明细","datasetId":"dataset:detail","dimensionKeys":["channel"],"measureKeys":["sales"]}
				],
				"defaultViewId":"view:trend","recommendedViewId":"view:trend"
			},
			"prompt":"DO NOT LEAK","sqlText":"SELECT secret","resultRows":["secret"]
		}`, ownerID)),
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
	for _, secret := range []string{"DO NOT LEAK", "DO NOT TRUST", "SELECT secret", "resultRows", "sqlText", `"prompt"`} {
		if strings.Contains(body, secret) {
			t.Fatalf("result response leaked %q: %s", secret, body)
		}
	}
	var view RunView
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil || view.Completion == nil ||
		view.Completion.Result == nil || view.Completion.Result.Title != "本月已支付订单销售额" ||
		len(view.Completion.Result.Datasets) != 3 || len(view.Completion.Result.Views) != 3 ||
		view.Completion.Result.RecommendedViewID != "view:trend" || view.Completion.Result.Evidence == nil ||
		view.Completion.Result.TimeSpec.RangeLabel != "2026-08-01 至 2026-08-06" ||
		view.Completion.Result.TimeSpec.PolicyLabel != "本月至今（MTD）" ||
		view.Completion.Result.Evidence.Owner.ID != ownerID {
		t.Fatalf("public result DTO = %#v, %v", view.Completion, err)
	}
}

func TestPublicResultRejectsIneligibleRecommendedShape(t *testing.T) {
	payload := json.RawMessage(`{"result":{
		"schemaVersion":"question-result-v1","title":"结果",
		"summary":{"metricLabel":"销售额","value":"3","formattedValue":"3 元","unit":"CNY","time":{"label":"本月","start":"2026-08-01","end":"2026-08-02","timezone":"Asia/Shanghai"}},
		"evidenceIds":["evidence:metric"],
		"evidence":{"definition":"定义","owner":{"id":"owner:finance","displayName":"财务"},"semanticVersion":"v1","semanticStatus":"CERTIFIED","time":{"label":"本月","start":"2026-08-01","end":"2026-08-02","timezone":"Asia/Shanghai"},"quality":{"status":"PASS","dataAsOf":"2026-08-02","rulesPassed":1,"rulesTotal":1}},
		"datasets":[{"id":"dataset:bad","label":"错误形状","columns":[{"key":"day","label":"日期","type":"DATE","role":"DIMENSION"},{"key":"sales","label":"销售额","type":"DECIMAL","role":"MEASURE"}],"rows":[{"day":"2026-08-01","sales":"1"},{"day":"2026-08-02","sales":"2"}],"page":1,"pageSize":2,"totalRows":2}],
		"views":[{"id":"view:bad","type":"LINE","label":"趋势","datasetId":"dataset:bad","dimensionKeys":["day"],"measureKeys":["day"]}],
		"defaultViewId":"view:bad","recommendedViewId":"view:bad"
	}}`)
	if result := parsePublicResult(payload); result != nil {
		t.Fatalf("invalid result shape was published: %#v", result)
	}
}

func TestPublicAnswerPublishesOnlyVerifiedNarrativeOrStableFallback(t *testing.T) {
	artifact := publicAnswerFixture(t)
	degraded, err := answer.ToStructured(artifact, 2)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := degraded.MarshalCanonical()
	if err != nil {
		t.Fatal(err)
	}
	view := parsePublicAnswer(raw)
	if view == nil || !view.NarrativeDegraded || view.Narrative != nil ||
		view.Hint != answer.DegradedNarrativeHint || view.Verification.Attempts != 2 ||
		view.Verification.Passed {
		t.Fatalf("degraded public answer = %#v", view)
	}

	verifiedRaw, err := artifact.MarshalCanonical()
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := json.Marshal(map[string]json.RawMessage{"answer": verifiedRaw})
	if err != nil {
		t.Fatal(err)
	}
	verified := parsePublicAnswer(envelope)
	if verified == nil || verified.NarrativeDegraded || verified.Narrative == nil ||
		verified.Narrative.Summary != "销售额为128元" || !verified.Verification.Passed {
		t.Fatalf("verified public answer = %#v", verified)
	}

	leaky := strings.Replace(string(raw), `"summary":""`, `"summary":"未经校验的999元"`, 1)
	if leaked := parsePublicAnswer(json.RawMessage(leaky)); leaked != nil {
		t.Fatalf("invalid degraded narrative was published: %#v", leaked)
	}
}

func publicAnswerFixture(t *testing.T) answer.AnswerArtifact {
	t.Helper()
	rowKey, err := shared.FormatRowKey([]shared.RowKeyPart{{Key: "month", Value: "2026-08"}})
	if err != nil {
		t.Fatal(err)
	}
	text := "销售额为128元"
	policy := answer.DefaultReleaseVerifierPolicy(false)
	return answer.AnswerArtifact{
		SchemaVersion: answer.SchemaVersion, RunID: askdata.ID(uuid.NewString()),
		Layers: answer.AnswerLayers{
			Structured: answer.StructuredLayer{
				Headline: &answer.MetricValue{
					MetricVersionID: "metric:sales@v1", Value: "128", Unit: "CNY",
					Label: "销售额", ColumnKey: "sales_amount",
				},
				Cards: []answer.MetricValue{}, TableRef: "result:artifact:v1",
			},
			Narrative: answer.NarrativeLayer{
				Summary: text, Findings: []string{},
				Citations: []shared.Citation{
					shared.NewContractCitation(shared.TextSpan{Start: 0, End: 3}, "metric:sales@v1"),
					shared.NewResultCellCitation(shared.TextSpan{Start: 4, End: len([]rune(text))}, shared.CellRef{
						RowKey: rowKey, ColumnKey: "sales_amount",
					}),
				},
			},
		},
		Verification: answer.Verification{
			VerifierVersion: policy.VerifierVersion, PolicyWordlistVersion: policy.PolicyWordlistVersion,
			Attempts: 1, Passed: true,
		},
		Provenance: answer.Provenance{
			PromptVersion: "answer-v1", ModelPolicy: "narrative-strict",
			EvidenceHash: askdata.HashBytes([]byte("evidence")), ResultHash: askdata.HashBytes([]byte("result")),
			SemanticReleaseID: "release:v1", ChartRuleVersion: "chart-rules-v1",
		},
	}
}

func TestSubmitClarificationAcceptsOnlyStableOptionID(t *testing.T) {
	identity := testIdentity()
	parent := testSnapshot(orchestrator.StateClarificationRequired, 2)
	clarificationID := askdata.ID(uuid.NewString())
	child := testSnapshot(orchestrator.StateReceived, 1)
	child.Run.ParentRunID = parent.Run.ID
	child.Run.ConversationID = parent.Run.ConversationID
	backend := &fakeBackend{clarificationResult: OperationResult{Snapshot: child}}
	handler := testHandler(backend, identity)
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/questions/"+string(parent.Run.ID)+"/clarifications",
		strings.NewReader(fmt.Sprintf(
			`{"clarificationId":"%s","optionId":"metric:revenue","runVersion":%d}`,
			clarificationID, parent.Run.RecordVersion,
		)),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "clarification-0001")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted || backend.clarificationCalls != 1 ||
		backend.clarificationInput.RunID != parent.Run.ID ||
		backend.clarificationInput.OptionID != "metric:revenue" ||
		backend.clarificationInput.ClarificationID != clarificationID ||
		backend.clarificationInput.RunVersion != parent.Run.RecordVersion {
		t.Fatalf("clarification = %d/%s, input=%#v", response.Code, response.Body.String(), backend.clarificationInput)
	}

	invalid := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/questions/"+string(parent.Run.ID)+"/clarifications",
		strings.NewReader(fmt.Sprintf(
			`{"clarificationId":"%s","optionId":"metric:revenue","runVersion":%d,"answer":"free text"}`,
			clarificationID, parent.Run.RecordVersion,
		)),
	)
	invalid.Header.Set("Content-Type", "application/json")
	invalid.Header.Set("Idempotency-Key", "clarification-0002")
	invalidResponse := httptest.NewRecorder()
	handler.ServeHTTP(invalidResponse, invalid)
	if invalidResponse.Code != http.StatusBadRequest || backend.clarificationCalls != 1 {
		t.Fatalf("free-text clarification = %d/%s, calls=%d", invalidResponse.Code, invalidResponse.Body.String(), backend.clarificationCalls)
	}
}

func TestClarificationConsumptionUsesArtifactIdentityAndRunVersion(t *testing.T) {
	identity := testIdentity()
	release := askdata.ReleaseRef{
		ReleaseID: askdata.ID(uuid.NewString()), ContentHash: askdata.HashBytes([]byte("clarification-release")),
	}
	scope, err := askdata.NewPolicyScope(
		identity.TenantID, identity.ActorID, []askdata.ID{identity.DomainID},
		[]askdata.ID{askdata.ID(uuid.NewString())}, release,
	)
	if err != nil {
		t.Fatal(err)
	}
	parent := testSnapshot(orchestrator.StateClarificationRequired, 12)
	parent.Run.TenantID, parent.Run.DomainID, parent.Run.ActorID = identity.TenantID, identity.DomainID, identity.ActorID
	parent.Run.Release, parent.Run.PolicyScopeHash = release, scope.PolicyHash
	parent.Run.CompletionArtifact = askdata.HashBytes([]byte("clarification-completion"))
	parent.Run.IdempotencyKeyHash = askdata.HashBytes([]byte("clarification-idempotency"))
	parent.Run.QuestionHash = askdata.HashBytes([]byte("clarification-question"))
	parent.Run.Disposition = orchestrator.DispositionClarify
	parent.Run.CompletionCode = "METRIC_DEFINITION_AMBIGUOUS"
	frozenAt := time.Now().UTC()
	deadline := frozenAt.Add(orchestrator.DefaultClarificationTimeout)
	consumed := parent.Run.Usage
	parent.Run.BudgetFrozenAt, parent.Run.ClarificationDeadline = &frozenAt, &deadline
	parent.Run.BudgetConsumed, parent.Run.CompletedAt = &consumed, &frozenAt
	clarificationID := askdata.ID(uuid.NewString())
	parent.Artifacts = []orchestrator.Artifact{{
		ID: clarificationID, Hash: parent.Run.CompletionArtifact, Type: orchestrator.ArtifactClarification,
		Payload: json.RawMessage(`{
			"clarificationQuestion":"请选择",
			"options":[
				{"optionId":"option:a","label":"口径 A"},
				{"optionId":"option:b","label":"口径 B"}
			]
		}`),
	}}
	child := testSnapshot(orchestrator.StateReceived, 1)
	child.Run.TenantID, child.Run.DomainID, child.Run.ActorID = identity.TenantID, identity.DomainID, identity.ActorID
	child.Run.Release, child.Run.PolicyScopeHash = release, scope.PolicyHash
	store := &fakeQuestionRunStore{
		createResult: orchestrator.CreateResult{Run: child.Run},
		resumeByID:   map[askdata.ID]orchestrator.ReplaySnapshot{child.Run.ID: child},
	}
	input := SubmitClarificationInput{
		RunID: parent.Run.ID, ClarificationID: clarificationID,
		OptionID: "option:a", RunVersion: parent.Run.RecordVersion,
	}
	result, err := createClarificationChild(context.Background(), store, scope, identity.DomainID, parent, input, frozenAt.Add(time.Minute))
	if err != nil || result.Snapshot.Run.ID != child.Run.ID || len(store.createRequests) != 1 {
		t.Fatalf("clarification child = %#v, %v, requests=%d", result, err, len(store.createRequests))
	}
	wantConsumptionHash := askdata.HashBytes([]byte(
		clarificationConsumeDomain + string(parent.Run.ID) + "\x00" + string(clarificationID),
	))
	if store.createRequests[0].IdempotencyKeyHash != wantConsumptionHash ||
		store.createRequests[0].ParentRunID != parent.Run.ID {
		t.Fatalf("clarification create request = %#v", store.createRequests[0])
	}

	stale := input
	stale.RunVersion--
	if _, err := createClarificationChild(context.Background(), store, scope, identity.DomainID, parent, stale, frozenAt.Add(time.Minute)); !errors.Is(err, orchestrator.ErrVersionConflict) || len(store.createRequests) != 1 {
		t.Fatalf("stale clarification = %v, requests=%d", err, len(store.createRequests))
	}

	store.createErr = orchestrator.ErrIdempotencyConflict
	conflicting := input
	conflicting.OptionID = "option:b"
	if _, err := createClarificationChild(context.Background(), store, scope, identity.DomainID, parent, conflicting, frozenAt.Add(time.Minute)); !errors.Is(err, ErrClarificationAnswered) || len(store.createRequests) != 2 {
		t.Fatalf("conflicting clarification = %v, requests=%d", err, len(store.createRequests))
	}
	if store.createRequests[1].IdempotencyKeyHash != wantConsumptionHash {
		t.Fatalf("clarification consumption key changed across options: %#v", store.createRequests[1])
	}
}

func TestSubmitFeedbackAcceptsOnlyStructuredTerminalFeedback(t *testing.T) {
	identity := testIdentity()
	run := testSnapshot(orchestrator.StateAnswered, 1).Run
	createdAt := time.Now().UTC().Add(-time.Minute)
	updatedAt := time.Now().UTC()
	feedbackID := askdata.ID(uuid.NewString())
	backend := &fakeBackend{feedbackResult: FeedbackResult{
		FeedbackID: feedbackID, RunID: run.ID, Rating: FeedbackInaccurate,
		IssueType: FeedbackIssueMetric, RecordVersion: 1,
		CreatedAt: createdAt, UpdatedAt: updatedAt,
	}}
	handler := testHandler(backend, identity)
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/questions/"+string(run.ID)+"/feedback",
		strings.NewReader(fmt.Sprintf(
			`{"rating":"INACCURATE","issueType":"METRIC","comment":"  口径未包含退款  ","runVersion":%d}`,
			run.RecordVersion,
		)),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusCreated || backend.feedbackCalls != 1 || backend.feedbackIdentity != identity ||
		backend.feedbackInput.RunID != run.ID || backend.feedbackInput.RunVersion != run.RecordVersion ||
		backend.feedbackInput.Rating != FeedbackInaccurate || backend.feedbackInput.IssueType != FeedbackIssueMetric ||
		backend.feedbackInput.Comment != "口径未包含退款" {
		t.Fatalf("feedback = %d/%s, input=%#v", response.Code, response.Body.String(), backend.feedbackInput)
	}
	var view FeedbackView
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil || view.FeedbackID != feedbackID ||
		view.RunID != run.ID || view.RecordVersion != 1 || view.Replayed {
		t.Fatalf("feedback view = %#v, %v", view, err)
	}

	backend.feedbackResult.Replayed = true
	replay := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/questions/"+string(run.ID)+"/feedback",
		strings.NewReader(fmt.Sprintf(
			`{"rating":"INACCURATE","issueType":"METRIC","comment":"口径未包含退款","runVersion":%d}`,
			run.RecordVersion,
		)),
	)
	replay.Header.Set("Content-Type", "application/json")
	replayResponse := httptest.NewRecorder()
	handler.ServeHTTP(replayResponse, replay)
	if replayResponse.Code != http.StatusOK || backend.feedbackCalls != 2 {
		t.Fatalf("feedback replay = %d/%s, calls=%d", replayResponse.Code, replayResponse.Body.String(), backend.feedbackCalls)
	}
}

func TestSubmitFeedbackRejectsInvalidShapeBeforeBackend(t *testing.T) {
	identity := testIdentity()
	runID := askdata.ID(uuid.NewString())
	backend := &fakeBackend{}
	handler := testHandler(backend, identity)
	tests := []string{
		`{"rating":"ACCURATE","issueType":"METRIC","comment":"","runVersion":1}`,
		`{"rating":"INACCURATE","issueType":"NONE","comment":"","runVersion":1}`,
		`{"rating":"INACCURATE","issueType":"UNKNOWN","comment":"","runVersion":1}`,
		`{"rating":"INACCURATE","issueType":"DATA","comment":"line\nbreak","runVersion":1}`,
		`{"rating":"INACCURATE","issueType":"DATA","comment":"","runVersion":0}`,
		`{"rating":"INACCURATE","issueType":"DATA","comment":"","runVersion":1,"sql":"select secret"}`,
	}
	for index, body := range tests {
		request := httptest.NewRequest(
			http.MethodPost, "/api/v1/questions/"+string(runID)+"/feedback", strings.NewReader(body),
		)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("case %d = %d/%s", index, response.Code, response.Body.String())
		}
	}
	if backend.feedbackCalls != 0 {
		t.Fatalf("backend received invalid feedback: %d", backend.feedbackCalls)
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
		{ErrClarificationAnswered, http.StatusConflict},
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
		ID: askdata.ID(uuid.NewString()), Hash: snapshot.Run.CompletionArtifact, Type: orchestrator.ArtifactClarification,
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
