package metric

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

const metricHTTPSessionID = "metric-http-session"

type metricHTTPAuthStore struct {
	user    auth.LoginUser
	session auth.Session
}

func (store *metricHTTPAuthStore) FindTenantID(context.Context, string) (string, error) {
	return store.user.TenantID, nil
}
func (store *metricHTTPAuthStore) FindUserByEmail(context.Context, string, string) (auth.LoginUser, error) {
	return store.user, nil
}
func (store *metricHTTPAuthStore) FindUserByID(context.Context, string, string) (auth.LoginUser, error) {
	return store.user, nil
}
func (store *metricHTTPAuthStore) CreateSession(_ context.Context, session auth.Session, _, _ string) error {
	store.session = session
	return nil
}
func (store *metricHTTPAuthStore) FindSession(context.Context, string, string) (auth.Session, error) {
	return store.session, nil
}
func (*metricHTTPAuthStore) RotateSession(context.Context, string, string, []byte, []byte, time.Time) error {
	return nil
}
func (*metricHTTPAuthStore) RevokeSession(context.Context, string, string, []byte, string) error {
	return nil
}
func (*metricHTTPAuthStore) RecordLoginFailure(context.Context, string, string, string, string, string, string) {
}

type metricHTTPPermissionStore struct {
	allow  func(access.Check) bool
	checks []access.Check
}

func (store *metricHTTPPermissionStore) Allowed(_ context.Context, check access.Check) (bool, error) {
	store.checks = append(store.checks, check)
	if store.allow == nil {
		return true, nil
	}
	return store.allow(check), nil
}

type metricHTTPHarness struct {
	handler     http.Handler
	token       string
	permissions *metricHTTPPermissionStore
}

func newMetricHTTPHarness(t *testing.T, store *fakeStore, previewer *fakePreviewer, allow func(access.Check) bool) metricHTTPHarness {
	t.Helper()
	tokens := auth.NewTokenManager("metric-http-test", "01234567890123456789012345678901", time.Hour)
	token, _, err := tokens.Issue(testActorID, testTenantID, metricHTTPSessionID, 1)
	if err != nil {
		t.Fatal(err)
	}
	authStore := &metricHTTPAuthStore{
		user: auth.LoginUser{ID: testActorID, TenantID: testTenantID, Status: auth.UserStatusActive, TokenVersion: 1},
		session: auth.Session{
			ID: metricHTTPSessionID, TenantID: testTenantID, UserID: testActorID,
			TokenVersion: 1, UserStatus: auth.UserStatusActive, ExpiresAt: time.Now().UTC().Add(time.Hour),
		},
	}
	authService := auth.NewService(authStore, auth.NewPasswordManager(4), tokens, time.Hour)
	permissionStore := &metricHTTPPermissionStore{allow: allow}
	accessService := access.NewService(permissionStore)
	service := NewService(store, previewer)
	service.SetPermissionChecker(accessService)
	return metricHTTPHarness{
		handler: NewHandler(authService, accessService, service), token: token,
		permissions: permissionStore,
	}
}

func metricRequest(t *testing.T, harness metricHTTPHarness, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+harness.token)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	harness.handler.ServeHTTP(response, request)
	return response
}

func TestMetricHTTPRequiresReadPermissionAndDisablesCache(t *testing.T) {
	store := &fakeStore{record: baseRecord(t), versionsByID: map[string]VersionRecord{}}
	harness := newMetricHTTPHarness(t, store, &fakePreviewer{}, func(check access.Check) bool {
		return check.ResourceType == "METRIC" && check.Action == "READ" && check.ObjectID == testMetricID
	})
	response := metricRequest(t, harness, http.MethodGet, "/api/v1/metrics/"+testMetricID, "")
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unexpected metric response: status=%d cache=%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	}
	if len(harness.permissions.checks) != 1 || harness.permissions.checks[0].ObjectID != testMetricID {
		t.Fatalf("unexpected permission checks: %#v", harness.permissions.checks)
	}

	denied := newMetricHTTPHarness(t, store, &fakePreviewer{}, func(access.Check) bool { return false })
	response = metricRequest(t, denied, http.MethodGet, "/api/v1/metrics/"+testMetricID, "")
	if response.Code != http.StatusForbidden {
		t.Fatalf("expected forbidden, got %d: %s", response.Code, response.Body.String())
	}
}

func TestMetricHTTPPublishUsesIndependentPermissionAndIdempotency(t *testing.T) {
	record := baseRecord(t)
	replayed := VersionRecord{
		ID: testMetricVersionID, MetricID: testMetricID, Status: "PUBLISHED",
		DatasetID: record.DatasetID, DatasetVersionID: record.DatasetVersionID,
		DefinitionHash: record.DefinitionHash, Definition: record.Definition,
	}
	store := &fakeStore{record: record, replay: replayed, replayFound: true, versionsByID: map[string]VersionRecord{}}
	harness := newMetricHTTPHarness(t, store, &fakePreviewer{}, func(check access.Check) bool {
		return check.ResourceType == "METRIC" && check.Action == "PUBLISH" && check.ObjectID == testMetricID ||
			check.ResourceType == "DATASET" && check.Action == "READ" && check.ObjectID == testDatasetID
	})
	body := `{"draftVersionId":"` + testDraftVersionID + `","expectedVersion":3,"expectedDraftRecordVersion":2,"expectedDefinitionHash":"` + store.record.DefinitionHash + `","validationParameters":{}}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/metrics/"+testMetricID+"/publish", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+harness.token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "metric-publish-retry")
	response := httptest.NewRecorder()
	harness.handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || store.publishCalls != 0 {
		t.Fatalf("unexpected replay response: status=%d body=%s calls=%d", response.Code, response.Body.String(), store.publishCalls)
	}
	if len(harness.permissions.checks) != 2 || harness.permissions.checks[0].Action != "PUBLISH" ||
		harness.permissions.checks[1].ResourceType != "DATASET" {
		t.Fatalf("publish did not use independent permission: %#v", harness.permissions.checks)
	}
}

func TestMetricHTTPRejectsUnknownFields(t *testing.T) {
	store := &fakeStore{record: baseRecord(t), versionsByID: map[string]VersionRecord{}}
	harness := newMetricHTTPHarness(t, store, &fakePreviewer{}, nil)
	response := metricRequest(t, harness, http.MethodPut, "/api/v1/metrics/"+testMetricID+"/draft", `{"unexpected":true}`)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "INVALID_REQUEST") {
		t.Fatalf("unknown request field must be rejected: %d %s", response.Code, response.Body.String())
	}
}

func TestMetricHTTPDeleteRequiresManageAndUsesNoContent(t *testing.T) {
	store := &fakeStore{record: baseRecord(t), versionsByID: map[string]VersionRecord{}}
	harness := newMetricHTTPHarness(t, store, &fakePreviewer{}, func(check access.Check) bool {
		return check.ResourceType == "METRIC" && check.Action == "MANAGE" && check.ObjectID == testMetricID
	})
	response := metricRequest(t, harness, http.MethodDelete, "/api/v1/metrics/"+testMetricID, `{"expectedVersion":3}`)
	if response.Code != http.StatusNoContent || store.deleteCalls != 1 ||
		store.deleteInput.ExpectedVersion != 3 {
		t.Fatalf("delete response=%d body=%s calls=%d input=%#v",
			response.Code, response.Body.String(), store.deleteCalls, store.deleteInput)
	}
	if len(harness.permissions.checks) != 1 || harness.permissions.checks[0].Action != "MANAGE" {
		t.Fatalf("delete permission checks=%#v", harness.permissions.checks)
	}
}

func TestMetricHTTPVersionUsageIsNoStore(t *testing.T) {
	store := &fakeStore{record: baseRecord(t), versionsByID: map[string]VersionRecord{}}
	harness := newMetricHTTPHarness(t, store, &fakePreviewer{}, nil)
	response := metricRequest(t, harness, http.MethodGet, "/api/v1/metrics/"+testMetricID+"/versions/"+testMetricVersionID+"/usage", "")
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" || !strings.Contains(response.Body.String(), "activeQueryRuns") {
		t.Fatalf("unexpected usage response: status=%d cache=%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	}
}

func TestMetricHTTPPreviewIsNoStore(t *testing.T) {
	store := &fakeStore{
		record: baseRecord(t), datasetVersion: baseDatasetVersion(t),
		versionsByID: map[string]VersionRecord{},
	}
	harness := newMetricHTTPHarness(t, store, &fakePreviewer{}, nil)
	response := metricRequest(t, harness, http.MethodPost, "/api/v1/metrics/"+testMetricID+"/preview", `{"parameters":{},"dimensionFieldIds":[],"maxRows":1}`)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("指标试算响应必须禁止缓存: status=%d cache=%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	}
}

func TestMetricHTTPPreviewRequiresDatasetRead(t *testing.T) {
	store := &fakeStore{
		record: baseRecord(t), datasetVersion: baseDatasetVersion(t),
		versionsByID: map[string]VersionRecord{},
	}
	harness := newMetricHTTPHarness(t, store, &fakePreviewer{}, func(check access.Check) bool {
		return check.ResourceType == "METRIC" && check.Action == "READ" && check.ObjectID == testMetricID
	})
	response := metricRequest(t, harness, http.MethodPost, "/api/v1/metrics/"+testMetricID+"/preview", `{"parameters":{},"dimensionFieldIds":[],"maxRows":1}`)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "PERMISSION_DENIED") {
		t.Fatalf("指标试算缺少数据集读取权限时必须拒绝: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestWriteMetricErrorMapsActiveDownstreamUsage(t *testing.T) {
	response := httptest.NewRecorder()
	writeMetricError(response, ErrVersionInUse)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "METRIC_VERSION_IN_USE") {
		t.Fatalf("下游占用错误映射异常: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestWriteMetricErrorMapsMetricUsage(t *testing.T) {
	response := httptest.NewRecorder()
	writeMetricError(response, ErrInUse)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "METRIC_IN_USE") {
		t.Fatalf("metric usage error mapping: status=%d body=%s", response.Code, response.Body.String())
	}
}
