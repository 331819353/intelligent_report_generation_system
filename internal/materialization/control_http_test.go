package materialization

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/access"
	"intelligent-report-generation-system/internal/auth"
)

const controlHTTPSessionID = "materialization-control-http-session"

type controlHTTPAuthStore struct {
	user    auth.LoginUser
	session auth.Session
}

func (store *controlHTTPAuthStore) FindTenantID(context.Context, string) (string, error) {
	return store.user.TenantID, nil
}

func (store *controlHTTPAuthStore) FindUserByEmail(
	context.Context,
	string,
	string,
) (auth.LoginUser, error) {
	return store.user, nil
}

func (store *controlHTTPAuthStore) FindUserByID(
	context.Context,
	string,
	string,
) (auth.LoginUser, error) {
	return store.user, nil
}

func (store *controlHTTPAuthStore) CreateSession(
	_ context.Context,
	session auth.Session,
	_, _ string,
) error {
	store.session = session
	return nil
}

func (store *controlHTTPAuthStore) FindSession(
	context.Context,
	string,
	string,
) (auth.Session, error) {
	return store.session, nil
}

func (*controlHTTPAuthStore) RotateSession(
	context.Context,
	string,
	string,
	[]byte,
	[]byte,
	time.Time,
) error {
	return nil
}

func (*controlHTTPAuthStore) RevokeSession(
	context.Context,
	string,
	string,
	[]byte,
	string,
) error {
	return nil
}

func (*controlHTTPAuthStore) RecordLoginFailure(
	context.Context,
	string,
	string,
	string,
	string,
	string,
	string,
) {
}

type controlHTTPPermissionStore struct {
	allow  bool
	checks []access.Check
}

func (store *controlHTTPPermissionStore) Allowed(
	_ context.Context,
	check access.Check,
) (bool, error) {
	store.checks = append(store.checks, check)
	return store.allow, nil
}

type controlHTTPHarness struct {
	handler     http.Handler
	token       string
	store       *fakeControlStore
	permissions *controlHTTPPermissionStore
}

func newControlHTTPHarness(
	t *testing.T,
	store *fakeControlStore,
	allow bool,
) controlHTTPHarness {
	t.Helper()
	tokens := auth.NewTokenManager(
		"materialization-control-http-test",
		"01234567890123456789012345678901",
		time.Hour,
	)
	token, _, err := tokens.Issue(
		controlTestActorID, controlTestTenantID, controlHTTPSessionID, 1,
	)
	if err != nil {
		t.Fatal(err)
	}
	authStore := &controlHTTPAuthStore{
		user: auth.LoginUser{
			ID: controlTestActorID, TenantID: controlTestTenantID,
			Status: auth.UserStatusActive, TokenVersion: 1,
		},
		session: auth.Session{
			ID: controlHTTPSessionID, TenantID: controlTestTenantID,
			UserID: controlTestActorID, UserStatus: auth.UserStatusActive,
			TokenVersion: 1, ExpiresAt: time.Now().Add(time.Hour),
		},
	}
	authService := auth.NewService(
		authStore, auth.NewPasswordManager(4), tokens, time.Hour,
	)
	permissionStore := &controlHTTPPermissionStore{allow: allow}
	handler := NewControlHandler(
		authService,
		access.NewService(permissionStore),
		NewControlService(store),
	)
	return controlHTTPHarness{
		handler: handler, token: token, store: store, permissions: permissionStore,
	}
}

func performControlRequest(
	t *testing.T,
	harness controlHTTPHarness,
	method, target, body, contentType string,
) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+harness.token)
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response := httptest.NewRecorder()
	harness.handler.ServeHTTP(response, request)
	return response
}

func TestControlHTTPRegisterUsesDatasetManageAndSafeBody(t *testing.T) {
	store := testControlStore()
	harness := newControlHTTPHarness(t, store, true)
	response := performControlRequest(
		t, harness, http.MethodPost,
		"/api/v1/datasets/"+controlTestDatasetID+"/materializations/builds",
		`{"mode":"FULL","maxAttempts":5}`, "application/json",
	)
	if response.Code != http.StatusCreated ||
		response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf(
			"status=%d cache=%q body=%s",
			response.Code, response.Header().Get("Cache-Control"), response.Body.String(),
		)
	}
	if store.request.Mode != RunModeFull || store.request.MaxAttempts != 5 ||
		store.request.PartitionKey != "" {
		t.Fatalf("request=%+v", store.request)
	}
	if strings.Contains(strings.ToLower(response.Body.String()), "sql") ||
		strings.Contains(response.Body.String(), "physical") {
		t.Fatalf("response leaks execution surface: %s", response.Body.String())
	}
	if len(harness.permissions.checks) != 1 {
		t.Fatalf("checks=%+v", harness.permissions.checks)
	}
	check := harness.permissions.checks[0]
	if check.ResourceType != "DATASET" || check.Action != "MANAGE" ||
		check.ObjectID != controlTestDatasetID {
		t.Fatalf("check=%+v", check)
	}
}

func TestControlHTTPRejectsClientPlanSQLInputsAndTrailingJSON(t *testing.T) {
	tests := []string{
		`{"plan":{"nodes":[]}}`,
		`{"sql":"select 1"}`,
		`{"inputs":[]}`,
		`{"physicalName":"warehouse_dwd.attack"}`,
		`{} {}`,
	}
	for _, body := range tests {
		store := testControlStore()
		harness := newControlHTTPHarness(t, store, true)
		response := performControlRequest(
			t, harness, http.MethodPost,
			"/api/v1/datasets/"+controlTestDatasetID+"/materializations/builds",
			body, "application/json",
		)
		if response.Code != http.StatusBadRequest ||
			!strings.Contains(response.Body.String(), "MATERIALIZATION_INVALID_REQUEST") ||
			store.registerCalls != 0 {
			t.Fatalf(
				"body=%s status=%d calls=%d response=%s",
				body, response.Code, store.registerCalls, response.Body.String(),
			)
		}
	}
}

func TestControlHTTPListAndGetUseDatasetRead(t *testing.T) {
	store := testControlStore()
	harness := newControlHTTPHarness(t, store, true)
	list := performControlRequest(
		t, harness, http.MethodGet,
		"/api/v1/datasets/"+controlTestDatasetID+
			"/materializations/builds?limit=25&offset=0",
		"", "",
	)
	if list.Code != http.StatusOK || store.listCalls != 1 ||
		!strings.Contains(list.Body.String(), `"limit":25`) {
		t.Fatalf("status=%d body=%s store=%+v", list.Code, list.Body.String(), store)
	}
	detail := performControlRequest(
		t, harness, http.MethodGet,
		"/api/v1/datasets/"+controlTestDatasetID+
			"/materializations/builds/"+controlTestBuildID,
		"", "",
	)
	if detail.Code != http.StatusOK || store.getCalls != 1 {
		t.Fatalf("status=%d body=%s store=%+v", detail.Code, detail.Body.String(), store)
	}
	if len(harness.permissions.checks) != 2 {
		t.Fatalf("checks=%+v", harness.permissions.checks)
	}
	for _, check := range harness.permissions.checks {
		if check.Action != "READ" || check.ObjectID != controlTestDatasetID {
			t.Fatalf("check=%+v", check)
		}
	}
}

func TestControlHTTPCancelRequiresEmptyBodyAndDatasetManage(t *testing.T) {
	store := testControlStore()
	store.detail.Status = RunCancelled
	harness := newControlHTTPHarness(t, store, true)
	path := "/api/v1/datasets/" + controlTestDatasetID +
		"/materializations/builds/" + controlTestBuildID + "/cancel"
	rejected := performControlRequest(
		t, harness, http.MethodPost, path, `{}`, "application/json",
	)
	if rejected.Code != http.StatusBadRequest || store.cancelCalls != 0 {
		t.Fatalf("status=%d body=%s store=%+v", rejected.Code, rejected.Body.String(), store)
	}
	response := performControlRequest(t, harness, http.MethodPost, path, "", "")
	if response.Code != http.StatusOK || store.cancelCalls != 1 ||
		!strings.Contains(response.Body.String(), `"status":"CANCELLED"`) {
		t.Fatalf("status=%d body=%s store=%+v", response.Code, response.Body.String(), store)
	}
	if harness.permissions.checks[len(harness.permissions.checks)-1].Action != "MANAGE" {
		t.Fatalf("checks=%+v", harness.permissions.checks)
	}
}

func TestControlHTTPStrictMediaTypePaginationAndErrors(t *testing.T) {
	store := testControlStore()
	harness := newControlHTTPHarness(t, store, true)
	response := performControlRequest(
		t, harness, http.MethodPost,
		"/api/v1/datasets/"+controlTestDatasetID+"/materializations/builds",
		`{}`, "text/plain",
	)
	if response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	response = performControlRequest(
		t, harness, http.MethodGet,
		"/api/v1/datasets/"+controlTestDatasetID+
			"/materializations/builds?limit=101&unknown=1",
		"", "",
	)
	if response.Code != http.StatusBadRequest || store.listCalls != 0 {
		t.Fatalf("status=%d body=%s store=%+v", response.Code, response.Body.String(), store)
	}

	store = testControlStore()
	store.err = ErrConflict
	harness = newControlHTTPHarness(t, store, true)
	response = performControlRequest(
		t, harness, http.MethodPost,
		"/api/v1/datasets/"+controlTestDatasetID+"/materializations/builds",
		`{}`, "application/json; charset=utf-8",
	)
	if response.Code != http.StatusConflict ||
		!strings.Contains(response.Body.String(), "MATERIALIZATION_CONFLICT") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestControlHTTPRequiresPermissionAndAuthentication(t *testing.T) {
	store := testControlStore()
	harness := newControlHTTPHarness(t, store, false)
	response := performControlRequest(
		t, harness, http.MethodGet,
		"/api/v1/datasets/"+controlTestDatasetID+"/materializations/builds",
		"", "",
	)
	if response.Code != http.StatusForbidden || store.listCalls != 0 {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}

	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/datasets/"+controlTestDatasetID+"/materializations/builds",
		nil,
	)
	response = httptest.NewRecorder()
	harness.handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized ||
		response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
}
