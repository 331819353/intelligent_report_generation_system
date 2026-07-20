package dataset

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/access"
	"intelligent-report-generation-system/internal/auth"
)

const (
	httpTestTenantID   = "tenant-http-test"
	httpTestUserID     = "user-http-test"
	httpTestSessionID  = "session-http-test"
	httpTestDatasetID  = "11111111-1111-4111-8111-111111111111"
	httpTestVersionID  = "22222222-2222-4222-8222-222222222222"
	httpTestDraftID    = "33333333-3333-4333-8333-333333333333"
	httpTestRevisionID = "44444444-4444-4444-8444-444444444444"
)

// httpAuthStore 只提供 HTTP 合同测试所需的有效用户和会话。
type httpAuthStore struct {
	user    auth.LoginUser
	session auth.Session
}

func (s *httpAuthStore) FindTenantID(context.Context, string) (string, error) {
	return s.user.TenantID, nil
}

func (s *httpAuthStore) FindUserByEmail(context.Context, string, string) (auth.LoginUser, error) {
	return s.user, nil
}

func (s *httpAuthStore) FindUserByID(context.Context, string, string) (auth.LoginUser, error) {
	return s.user, nil
}

func (s *httpAuthStore) CreateSession(_ context.Context, session auth.Session, _, _ string) error {
	s.session = session
	return nil
}

func (s *httpAuthStore) FindSession(context.Context, string, string) (auth.Session, error) {
	return s.session, nil
}

func (s *httpAuthStore) RotateSession(context.Context, string, string, []byte, []byte, time.Time) error {
	return nil
}

func (s *httpAuthStore) RevokeSession(context.Context, string, string, []byte, string) error {
	return nil
}

func (s *httpAuthStore) RecordLoginFailure(context.Context, string, string, string, string, string, string) {
}

// httpPermissionStore 记录每次权限判定，便于验证路由使用的动作和对象范围。
type httpPermissionStore struct {
	allow  func(access.Check) bool
	checks []access.Check
}

func (s *httpPermissionStore) Allowed(_ context.Context, check access.Check) (bool, error) {
	s.checks = append(s.checks, check)
	if s.allow == nil {
		return true, nil
	}
	return s.allow(check), nil
}

// httpDatasetStore 保存路由传入的精确标识和发布参数，不模拟数据库实现细节。
type httpDatasetStore struct {
	record Record
	getErr error

	replayRecord      VersionRecord
	replayFound       bool
	replayErr         error
	replayCalls       int
	replayTenantID    string
	replayActorID     string
	replayDatasetID   string
	replayKey         string
	replayRequestHash string

	published        VersionRecord
	publishErr       error
	publishCalls     int
	publishTenantID  string
	publishActorID   string
	publishDatasetID string
	publishPlan      PublishPlan

	versions              []VersionSummary
	versionsTotal         int
	listVersionsErr       error
	listVersionsTenantID  string
	listVersionsDatasetID string
	listVersionsLimit     int
	listVersionsOffset    int

	version             VersionRecord
	getVersionErr       error
	getVersionTenantID  string
	getVersionDatasetID string
	getVersionVersionID string

	sourceRevision         RevisionRecord
	sourceRevisionErr      error
	resolveSourceCalls     int
	resolveSourceTenantID  string
	resolveSourceDatasetID string
	resolveSourceVersionID string

	usage          VersionUsage
	usageErr       error
	usageCalls     int
	usageTenantID  string
	usageDatasetID string
	usageVersionID string

	transition          VersionRecord
	transitionErr       error
	transitionCalls     int
	transitionTenantID  string
	transitionActorID   string
	transitionDatasetID string
	transitionVersionID string
	transitionInput     VersionTransitionInput

	revisions              []RevisionSummary
	revisionsTotal         int
	listRevisionsErr       error
	listRevisionsTenantID  string
	listRevisionsDatasetID string
	listRevisionsLimit     int
	listRevisionsOffset    int

	revision             RevisionRecord
	getRevisionErr       error
	getRevisionTenantID  string
	getRevisionDatasetID string
	getRevisionID        string

	rollbackRecord    Record
	rollbackErr       error
	rollbackCalls     int
	rollbackTenantID  string
	rollbackActorID   string
	rollbackDatasetID string
	rollbackInput     RollbackRevisionInput
	rollbackRevision  RevisionRecord
	rollbackPrepared  Prepared

	disableCalls   int
	restoreCalls   int
	deleteCalls    int
	lifecycleInput LifecycleInput
	disableErr     error
	restoreErr     error
	deleteErr      error
}

func (s *httpDatasetStore) Create(context.Context, string, string, CreateInput, Prepared) (Record, error) {
	return s.record, nil
}

func (s *httpDatasetStore) Get(context.Context, string, string) (Record, error) {
	return s.record, s.getErr
}

func (s *httpDatasetStore) List(context.Context, string, int, int) ([]Summary, int, error) {
	return nil, 0, nil
}

func (s *httpDatasetStore) Update(context.Context, string, string, string, UpdateInput, Prepared) (Record, error) {
	return s.record, nil
}

func (s *httpDatasetStore) Disable(_ context.Context, _, _, _ string, input LifecycleInput) (Record, error) {
	s.disableCalls++
	s.lifecycleInput = input
	return s.record, s.disableErr
}

func (s *httpDatasetStore) Restore(_ context.Context, _, _, _ string, input LifecycleInput) (Record, error) {
	s.restoreCalls++
	s.lifecycleInput = input
	return s.record, s.restoreErr
}

func (s *httpDatasetStore) Delete(_ context.Context, _, _, _ string, input LifecycleInput) error {
	s.deleteCalls++
	s.lifecycleInput = input
	return s.deleteErr
}

func (s *httpDatasetStore) ReplayPublication(_ context.Context, tenantID, actorID, datasetID, key, requestHash string) (VersionRecord, bool, error) {
	s.replayCalls++
	s.replayTenantID = tenantID
	s.replayActorID = actorID
	s.replayDatasetID = datasetID
	s.replayKey = key
	s.replayRequestHash = requestHash
	return s.replayRecord, s.replayFound, s.replayErr
}

func (s *httpDatasetStore) Publish(_ context.Context, tenantID, actorID, datasetID string, plan PublishPlan) (VersionRecord, error) {
	s.publishCalls++
	s.publishTenantID = tenantID
	s.publishActorID = actorID
	s.publishDatasetID = datasetID
	s.publishPlan = plan
	return s.published, s.publishErr
}

func (s *httpDatasetStore) GetVersion(_ context.Context, tenantID, datasetID, versionID string) (VersionRecord, error) {
	s.getVersionTenantID = tenantID
	s.getVersionDatasetID = datasetID
	s.getVersionVersionID = versionID
	return s.version, s.getVersionErr
}

func (s *httpDatasetStore) ResolveVersionSourceRevision(_ context.Context, tenantID, datasetID, versionID string) (RevisionRecord, error) {
	s.resolveSourceCalls++
	s.resolveSourceTenantID = tenantID
	s.resolveSourceDatasetID = datasetID
	s.resolveSourceVersionID = versionID
	return s.sourceRevision, s.sourceRevisionErr
}

func (s *httpDatasetStore) ListVersions(_ context.Context, tenantID, datasetID string, limit, offset int) ([]VersionSummary, int, error) {
	s.listVersionsTenantID = tenantID
	s.listVersionsDatasetID = datasetID
	s.listVersionsLimit = limit
	s.listVersionsOffset = offset
	return s.versions, s.versionsTotal, s.listVersionsErr
}

func (s *httpDatasetStore) GetVersionUsage(_ context.Context, tenantID, datasetID, versionID string) (VersionUsage, error) {
	s.usageCalls++
	s.usageTenantID = tenantID
	s.usageDatasetID = datasetID
	s.usageVersionID = versionID
	return s.usage, s.usageErr
}

func (s *httpDatasetStore) TransitionVersion(_ context.Context, tenantID, actorID, datasetID, versionID string, input VersionTransitionInput) (VersionRecord, error) {
	s.transitionCalls++
	s.transitionTenantID = tenantID
	s.transitionActorID = actorID
	s.transitionDatasetID = datasetID
	s.transitionVersionID = versionID
	s.transitionInput = input
	return s.transition, s.transitionErr
}

func (s *httpDatasetStore) GetRevision(_ context.Context, tenantID, datasetID, revisionID string) (RevisionRecord, error) {
	s.getRevisionTenantID = tenantID
	s.getRevisionDatasetID = datasetID
	s.getRevisionID = revisionID
	return s.revision, s.getRevisionErr
}

func (s *httpDatasetStore) ListRevisions(_ context.Context, tenantID, datasetID string, limit, offset int) ([]RevisionSummary, int, error) {
	s.listRevisionsTenantID = tenantID
	s.listRevisionsDatasetID = datasetID
	s.listRevisionsLimit = limit
	s.listRevisionsOffset = offset
	return s.revisions, s.revisionsTotal, s.listRevisionsErr
}

func (s *httpDatasetStore) RollbackRevision(_ context.Context, tenantID, actorID, datasetID string, input RollbackRevisionInput, revision RevisionRecord, prepared Prepared) (Record, error) {
	s.rollbackCalls++
	s.rollbackTenantID = tenantID
	s.rollbackActorID = actorID
	s.rollbackDatasetID = datasetID
	s.rollbackInput = input
	s.rollbackRevision = revision
	s.rollbackPrepared = prepared
	return s.rollbackRecord, s.rollbackErr
}

type httpPublicationValidator struct {
	result    PreviewResult
	err       error
	candidate PublicationCandidate
}

func (v *httpPublicationValidator) ValidatePublication(_ context.Context, _, _ string, candidate PublicationCandidate) (PreviewResult, error) {
	v.candidate = candidate
	return v.result, v.err
}

// httpPreviewer 记录版本预览是否绑定到请求中的精确版本。
type httpPreviewer struct {
	result      PreviewResult
	draftResult DraftPreviewResult
	err         error
	tenantID    string
	actorID     string
	datasetID   string
	versionID   string
	revisionID  string
	input       PreviewInput
	draftInput  DraftPreviewInput
	draftCalls  int
}

func (p *httpPreviewer) Preview(context.Context, string, string, string, PreviewInput) (PreviewResult, error) {
	return p.result, p.err
}

func (p *httpPreviewer) PreviewDraft(_ context.Context, tenantID, actorID, datasetID string, input DraftPreviewInput) (DraftPreviewResult, error) {
	p.draftCalls++
	p.tenantID = tenantID
	p.actorID = actorID
	p.datasetID = datasetID
	p.draftInput = input
	return p.draftResult, p.err
}

func (p *httpPreviewer) PreviewVersion(_ context.Context, tenantID, actorID, datasetID, versionID string, input PreviewInput) (PreviewResult, error) {
	p.tenantID = tenantID
	p.actorID = actorID
	p.datasetID = datasetID
	p.versionID = versionID
	p.input = input
	return p.result, p.err
}

func (p *httpPreviewer) PreviewRevision(_ context.Context, tenantID, actorID, datasetID, revisionID string, input PreviewInput) (PreviewResult, error) {
	p.tenantID = tenantID
	p.actorID = actorID
	p.datasetID = datasetID
	p.revisionID = revisionID
	p.input = input
	return p.result, p.err
}

func (p *httpPreviewer) Cancel(context.Context, string, string, string, string) error {
	return nil
}

type datasetHTTPHarness struct {
	handler     http.Handler
	token       string
	permissions *httpPermissionStore
	validator   *httpPublicationValidator
}

// newDatasetHTTPHarness 使用真实认证和权限中间件组装数据集路由。
func newDatasetHTTPHarness(t *testing.T, store *httpDatasetStore, previewer Previewer, allow func(access.Check) bool) datasetHTTPHarness {
	t.Helper()
	tokenManager := auth.NewTokenManager("dataset-http-test", "01234567890123456789012345678901", time.Hour)
	token, _, err := tokenManager.Issue(httpTestUserID, httpTestTenantID, httpTestSessionID, 1)
	if err != nil {
		t.Fatal(err)
	}
	authStore := &httpAuthStore{
		user: auth.LoginUser{
			ID: httpTestUserID, TenantID: httpTestTenantID, Status: auth.UserStatusActive, TokenVersion: 1,
		},
		session: auth.Session{
			ID: httpTestSessionID, TenantID: httpTestTenantID, UserID: httpTestUserID,
			TokenVersion: 1, UserStatus: auth.UserStatusActive, ExpiresAt: time.Now().UTC().Add(time.Hour),
		},
	}
	authService := auth.NewService(authStore, auth.NewPasswordManager(4), tokenManager, time.Hour)
	permissionStore := &httpPermissionStore{allow: allow}
	validator := &httpPublicationValidator{}
	service := NewService(store, validator)
	var handler http.Handler
	if previewer == nil {
		handler = NewHandler(authService, access.NewService(permissionStore), service)
	} else {
		handler = NewHandler(authService, access.NewService(permissionStore), service, previewer)
	}
	return datasetHTTPHarness{handler: handler, token: token, permissions: permissionStore, validator: validator}
}

func performDatasetHTTPRequest(t *testing.T, harness datasetHTTPHarness, method, target string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, strings.NewReader(string(body)))
	request.Header.Set("Authorization", "Bearer "+harness.token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response := httptest.NewRecorder()
	harness.handler.ServeHTTP(response, request)
	return response
}

func mustDatasetJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func readDatasetErrorCode(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是有效 JSON：%v，body=%s", err, response.Body.String())
	}
	return body.Code
}

func TestGetDatasetDisablesIntermediateCaching(t *testing.T) {
	store := &httpDatasetStore{record: Record{ID: httpTestDatasetID, Version: 7}}
	harness := newDatasetHTTPHarness(t, store, nil, nil)
	response := performDatasetHTTPRequest(t, harness, http.MethodGet, "/api/v1/datasets/"+httpTestDatasetID, nil, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if value := response.Header().Get("Cache-Control"); value != "no-store" {
		t.Fatalf("Cache-Control=%q，期望 no-store", value)
	}
}

func TestDraftPreviewRouteUsesCandidateContractAndAllRequiredPermissions(t *testing.T) {
	prepared, err := Prepare(readExample(t))
	if err != nil {
		t.Fatal(err)
	}
	previewer := &httpPreviewer{draftResult: DraftPreviewResult{
		PreviewResult: PreviewResult{
			QueryID: "d7567ac1-dd36-4d16-aac4-65d48d491d74", Columns: []string{"region"},
			Rows: [][]any{{"华东"}}, RowCount: 1, DurationMS: 8,
		},
		DSLHash: prepared.DSLHash, PlanHash: prepared.PlanHash, BaseVersion: 7,
	}}
	harness := newDatasetHTTPHarness(t, &httpDatasetStore{}, previewer, func(check access.Check) bool {
		return check.ResourceType == "DATASET" && check.ObjectID == httpTestDatasetID && (check.Action == "READ" || check.Action == "MANAGE") ||
			check.ResourceType == "DATA_ASSET" && check.Action == "READ" && check.ObjectID == ""
	})
	input := DraftPreviewInput{
		QueryID: "d7567ac1-dd36-4d16-aac4-65d48d491d74", ExpectedVersion: 7,
		DSL: prepared.DSLJSON, Parameters: map[string]any{"region": "华东"}, MaxRows: 5,
	}
	response := performDatasetHTTPRequest(t, harness, http.MethodPost,
		"/api/v1/datasets/"+httpTestDatasetID+"/draft/preview", mustDatasetJSON(t, input), nil)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	var body DraftPreviewResult
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.QueryID != input.QueryID || body.DSLHash != prepared.DSLHash || body.PlanHash != prepared.PlanHash || body.BaseVersion != 7 || body.RowCount != 1 {
		t.Fatalf("body=%#v", body)
	}
	if previewer.draftCalls != 1 || previewer.tenantID != httpTestTenantID || previewer.actorID != httpTestUserID ||
		previewer.datasetID != httpTestDatasetID || previewer.draftInput.ExpectedVersion != 7 ||
		string(previewer.draftInput.DSL) != string(prepared.DSLJSON) || previewer.draftInput.MaxRows != 5 {
		t.Fatalf("previewer=%#v", previewer)
	}
	wantChecks := []access.Check{
		{ResourceType: "DATASET", Action: "MANAGE", ObjectID: httpTestDatasetID},
		{ResourceType: "DATASET", Action: "READ", ObjectID: httpTestDatasetID},
		{ResourceType: "DATA_ASSET", Action: "READ"},
	}
	if len(harness.permissions.checks) != len(wantChecks) {
		t.Fatalf("permission checks=%#v", harness.permissions.checks)
	}
	for index, want := range wantChecks {
		got := harness.permissions.checks[index]
		if got.ResourceType != want.ResourceType || got.Action != want.Action || got.ObjectID != want.ObjectID {
			t.Fatalf("permission check[%d]=%#v want=%#v", index, got, want)
		}
	}
}

func TestDraftPreviewRouteFailsClosedOnEachPermissionAndDisablesCaching(t *testing.T) {
	tests := []struct {
		name         string
		deniedType   string
		deniedAction string
		wantChecks   int
	}{
		{name: "数据集管理权限", deniedType: "DATASET", deniedAction: "MANAGE", wantChecks: 1},
		{name: "数据集读取权限", deniedType: "DATASET", deniedAction: "READ", wantChecks: 2},
		{name: "数据资产读取权限", deniedType: "DATA_ASSET", deniedAction: "READ", wantChecks: 3},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			previewer := &httpPreviewer{}
			harness := newDatasetHTTPHarness(t, &httpDatasetStore{}, previewer, func(check access.Check) bool {
				return check.ResourceType != test.deniedType || check.Action != test.deniedAction
			})
			response := performDatasetHTTPRequest(t, harness, http.MethodPost,
				"/api/v1/datasets/"+httpTestDatasetID+"/draft/preview", []byte(`{}`), nil)
			if response.Code != http.StatusForbidden || readDatasetErrorCode(t, response) != "PERMISSION_DENIED" ||
				response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
			}
			if previewer.draftCalls != 0 || len(harness.permissions.checks) != test.wantChecks {
				t.Fatalf("draftCalls=%d permission checks=%#v", previewer.draftCalls, harness.permissions.checks)
			}
		})
	}
}

func TestDraftPreviewRouteKeepsNoStoreOnDomainError(t *testing.T) {
	previewer := &httpPreviewer{err: ErrConflict}
	harness := newDatasetHTTPHarness(t, &httpDatasetStore{}, previewer, nil)
	response := performDatasetHTTPRequest(t, harness, http.MethodPost,
		"/api/v1/datasets/"+httpTestDatasetID+"/draft/preview",
		mustDatasetJSON(t, DraftPreviewInput{ExpectedVersion: 7, DSL: json.RawMessage(`{}`), Parameters: map[string]any{}, MaxRows: 5}), nil)
	if response.Code != http.StatusConflict || readDatasetErrorCode(t, response) != "DATASET_VERSION_CONFLICT" ||
		response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
}

func TestCurrentDraftPreviewDisablesIntermediateCaching(t *testing.T) {
	previewer := &httpPreviewer{result: PreviewResult{
		QueryID: "d7567ac1-dd36-4d16-aac4-65d48d491d74", Columns: []string{"region"},
		Rows: [][]any{{"华东"}}, RowCount: 1,
	}}
	harness := newDatasetHTTPHarness(t, &httpDatasetStore{}, previewer, func(check access.Check) bool {
		return check.ResourceType == "DATASET" && check.Action == "READ" && check.ObjectID == httpTestDatasetID
	})
	response := performDatasetHTTPRequest(t, harness, http.MethodPost,
		"/api/v1/datasets/"+httpTestDatasetID+"/preview",
		mustDatasetJSON(t, PreviewInput{Parameters: map[string]any{}, MaxRows: 5}), nil)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
}

func TestDatasetLifecycleRoutesUseManagePermissionAndExpectedVersion(t *testing.T) {
	store := &httpDatasetStore{record: Record{ID: httpTestDatasetID, Version: 8, Status: "DEPRECATED"}}
	harness := newDatasetHTTPHarness(t, store, nil, func(check access.Check) bool {
		return check.Action == "MANAGE" && check.ObjectID == httpTestDatasetID
	})

	disable := performDatasetHTTPRequest(t, harness, http.MethodPost, "/api/v1/datasets/"+httpTestDatasetID+"/disable", mustDatasetJSON(t, LifecycleInput{ExpectedVersion: 7}), nil)
	if disable.Code != http.StatusOK || store.disableCalls != 1 || store.lifecycleInput.ExpectedVersion != 7 {
		t.Fatalf("disable status=%d calls=%d input=%#v body=%s", disable.Code, store.disableCalls, store.lifecycleInput, disable.Body.String())
	}
	restore := performDatasetHTTPRequest(t, harness, http.MethodPost, "/api/v1/datasets/"+httpTestDatasetID+"/restore", mustDatasetJSON(t, LifecycleInput{ExpectedVersion: 8}), nil)
	if restore.Code != http.StatusOK || store.restoreCalls != 1 || store.lifecycleInput.ExpectedVersion != 8 {
		t.Fatalf("restore status=%d calls=%d input=%#v body=%s", restore.Code, store.restoreCalls, store.lifecycleInput, restore.Body.String())
	}
	remove := performDatasetHTTPRequest(t, harness, http.MethodDelete, "/api/v1/datasets/"+httpTestDatasetID, mustDatasetJSON(t, LifecycleInput{ExpectedVersion: 8}), nil)
	if remove.Code != http.StatusNoContent || store.deleteCalls != 1 || store.lifecycleInput.ExpectedVersion != 8 {
		t.Fatalf("delete status=%d calls=%d input=%#v body=%s", remove.Code, store.deleteCalls, store.lifecycleInput, remove.Body.String())
	}
	if len(harness.permissions.checks) != 3 {
		t.Fatalf("permission checks=%#v", harness.permissions.checks)
	}
}

func TestDatasetHandlerDoesNotExposeDirectPublish(t *testing.T) {
	store := &httpDatasetStore{}
	harness := newDatasetHTTPHarness(t, store, nil, nil)
	response := performDatasetHTTPRequest(
		t, harness, http.MethodPost, "/api/v1/datasets/"+httpTestDatasetID+"/publish", []byte(`{}`), nil,
	)
	if response.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if store.replayCalls != 0 || store.publishCalls != 0 {
		t.Fatalf("基础处理器不应触发直接发布：replay=%d publish=%d", store.replayCalls, store.publishCalls)
	}
	if len(harness.permissions.checks) != 0 {
		t.Fatalf("未注册路由不应进入权限或业务处理：checks=%#v", harness.permissions.checks)
	}
}

func TestPublishedVersionRoutesUseExactIdentifiers(t *testing.T) {
	t.Run("版本列表", func(t *testing.T) {
		store := &httpDatasetStore{
			versions: []VersionSummary{{
				ID: httpTestVersionID, DatasetID: httpTestDatasetID, VersionNo: 2, Status: "PUBLISHED",
			}},
			versionsTotal: 3,
		}
		harness := newDatasetHTTPHarness(t, store, nil, nil)
		response := performDatasetHTTPRequest(t, harness, http.MethodGet, "/api/v1/datasets/"+httpTestDatasetID+"/versions?limit=1&offset=2", nil, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("cache-control=%q", response.Header().Get("Cache-Control"))
		}
		var body struct {
			Items  []VersionSummary `json:"items"`
			Total  int              `json:"total"`
			Limit  int              `json:"limit"`
			Offset int              `json:"offset"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Items) != 1 || body.Items[0].ID != httpTestVersionID || body.Total != 3 || body.Limit != 1 || body.Offset != 2 {
			t.Fatalf("body=%#v", body)
		}
		if store.listVersionsTenantID != httpTestTenantID || store.listVersionsDatasetID != httpTestDatasetID || store.listVersionsLimit != 1 || store.listVersionsOffset != 2 {
			t.Fatalf("列表参数不正确：store=%#v", store)
		}
	})

	t.Run("精确版本加载", func(t *testing.T) {
		store := &httpDatasetStore{version: VersionRecord{
			ID: httpTestVersionID, DatasetID: httpTestDatasetID, VersionNo: 2, Status: "PUBLISHED",
		}}
		harness := newDatasetHTTPHarness(t, store, nil, nil)
		response := performDatasetHTTPRequest(t, harness, http.MethodGet, "/api/v1/datasets/"+httpTestDatasetID+"/versions/"+httpTestVersionID, nil, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("cache-control=%q", response.Header().Get("Cache-Control"))
		}
		var body VersionRecord
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.ID != httpTestVersionID || store.getVersionTenantID != httpTestTenantID || store.getVersionDatasetID != httpTestDatasetID || store.getVersionVersionID != httpTestVersionID {
			t.Fatalf("body=%#v dataset=%q version=%q", body, store.getVersionDatasetID, store.getVersionVersionID)
		}
	})

	t.Run("版本占用统计", func(t *testing.T) {
		store := &httpDatasetStore{usage: VersionUsage{
			ReportDraftReferences: 2, DownstreamDraftReferences: 1,
			DownstreamPublishedReferences: 3, ActiveQueryRuns: 4,
		}}
		harness := newDatasetHTTPHarness(t, store, nil, func(check access.Check) bool {
			return check.Action == "READ"
		})
		response := performDatasetHTTPRequest(t, harness, http.MethodGet, "/api/v1/datasets/"+httpTestDatasetID+"/versions/"+httpTestVersionID+"/usage", nil, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("cache-control=%q", response.Header().Get("Cache-Control"))
		}
		if strings.Contains(response.Body.String(), httpTestDatasetID) || strings.Contains(response.Body.String(), httpTestVersionID) {
			t.Fatalf("占用统计泄露了资源标识：body=%s", response.Body.String())
		}
		var body VersionUsage
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body != store.usage || store.usageCalls != 1 || store.usageTenantID != httpTestTenantID ||
			store.usageDatasetID != httpTestDatasetID || store.usageVersionID != httpTestVersionID {
			t.Fatalf("body=%#v store=%#v", body, store)
		}
		if len(harness.permissions.checks) != 1 || harness.permissions.checks[0].Action != "READ" || harness.permissions.checks[0].ObjectID != httpTestDatasetID {
			t.Fatalf("checks=%#v", harness.permissions.checks)
		}
	})

	t.Run("精确版本预览", func(t *testing.T) {
		previewer := &httpPreviewer{result: PreviewResult{
			QueryID: "query-version-1", Columns: []string{"region"}, Rows: [][]any{{"华东"}}, RowCount: 1,
		}}
		harness := newDatasetHTTPHarness(t, &httpDatasetStore{}, previewer, nil)
		input := PreviewInput{QueryID: "query-version-1", Parameters: map[string]any{"region": "华东"}, MaxRows: 25}
		response := performDatasetHTTPRequest(t, harness, http.MethodPost, "/api/v1/datasets/"+httpTestDatasetID+"/versions/"+httpTestVersionID+"/preview", mustDatasetJSON(t, input), nil)
		if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
		}
		var body PreviewResult
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.QueryID != "query-version-1" || previewer.tenantID != httpTestTenantID || previewer.actorID != httpTestUserID || previewer.datasetID != httpTestDatasetID || previewer.versionID != httpTestVersionID {
			t.Fatalf("body=%#v previewer=%#v", body, previewer)
		}
		if previewer.input.MaxRows != 25 || previewer.input.Parameters["region"] != "华东" {
			t.Fatalf("input=%#v", previewer.input)
		}
	})
}

func TestDraftRevisionRoutesAndRollbackPermissions(t *testing.T) {
	prepared, err := Prepare(readExample(t))
	if err != nil {
		t.Fatal(err)
	}
	revision := RevisionRecord{RevisionSummary: RevisionSummary{
		ID: httpTestRevisionID, DatasetID: httpTestDatasetID, VersionNo: 3, OperationType: "SAVE",
		Name: prepared.Document.Dataset.Name, Description: prepared.Document.Dataset.Description,
		Type: prepared.Document.Dataset.Type, DraftVersionID: httpTestDraftID, DraftRecordVersion: 2,
		DSLVersion: DSLVersion, DSLHash: prepared.DSLHash, PlanHash: prepared.PlanHash,
		CreatedAt: "2026-07-19T00:00:00Z", CreatedBy: httpTestUserID,
	}, DSL: prepared.DSLJSON, LogicalPlan: prepared.LogicalPlanJSON}

	t.Run("修订目录和精确加载使用读取权限", func(t *testing.T) {
		store := &httpDatasetStore{revisions: []RevisionSummary{revision.RevisionSummary}, revisionsTotal: 4, revision: revision}
		harness := newDatasetHTTPHarness(t, store, nil, func(check access.Check) bool { return check.Action == "READ" })
		listResponse := performDatasetHTTPRequest(t, harness, http.MethodGet,
			"/api/v1/datasets/"+httpTestDatasetID+"/revisions?limit=1&offset=2", nil, nil)
		if listResponse.Code != http.StatusOK || listResponse.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("list status=%d headers=%v body=%s", listResponse.Code, listResponse.Header(), listResponse.Body.String())
		}
		var page RevisionPage
		if err := json.Unmarshal(listResponse.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 || page.Items[0].ID != httpTestRevisionID || page.Total != 4 || page.Limit != 1 || page.Offset != 2 ||
			store.listRevisionsTenantID != httpTestTenantID || store.listRevisionsDatasetID != httpTestDatasetID ||
			store.listRevisionsLimit != 1 || store.listRevisionsOffset != 2 {
			t.Fatalf("page=%#v store=%#v", page, store)
		}

		detailResponse := performDatasetHTTPRequest(t, harness, http.MethodGet,
			"/api/v1/datasets/"+httpTestDatasetID+"/revisions/"+httpTestRevisionID, nil, nil)
		if detailResponse.Code != http.StatusOK || detailResponse.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("detail status=%d headers=%v body=%s", detailResponse.Code, detailResponse.Header(), detailResponse.Body.String())
		}
		var detail RevisionRecord
		if err := json.Unmarshal(detailResponse.Body.Bytes(), &detail); err != nil {
			t.Fatal(err)
		}
		if detail.ID != httpTestRevisionID || detail.DSLHash != prepared.DSLHash ||
			store.getRevisionTenantID != httpTestTenantID || store.getRevisionDatasetID != httpTestDatasetID || store.getRevisionID != httpTestRevisionID {
			t.Fatalf("detail=%#v store=%#v", detail, store)
		}
	})

	t.Run("修订数据预览使用读取权限和精确修订标识", func(t *testing.T) {
		previewer := &httpPreviewer{result: PreviewResult{
			QueryID: "query-revision-1", Columns: []string{"region"}, Rows: [][]any{{"华东"}}, RowCount: 1,
		}}
		harness := newDatasetHTTPHarness(t, &httpDatasetStore{}, previewer, func(check access.Check) bool {
			return check.Action == "READ" && check.ObjectID == httpTestDatasetID
		})
		input := PreviewInput{QueryID: "query-revision-1", Parameters: map[string]any{}, MaxRows: 5}
		response := performDatasetHTTPRequest(t, harness, http.MethodPost,
			"/api/v1/datasets/"+httpTestDatasetID+"/revisions/"+httpTestRevisionID+"/preview",
			mustDatasetJSON(t, input), nil)
		if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
		}
		if previewer.tenantID != httpTestTenantID || previewer.actorID != httpTestUserID ||
			previewer.datasetID != httpTestDatasetID || previewer.revisionID != httpTestRevisionID || previewer.input.MaxRows != 5 {
			t.Fatalf("previewer=%#v", previewer)
		}
		if len(harness.permissions.checks) != 1 || harness.permissions.checks[0].Action != "READ" {
			t.Fatalf("checks=%#v", harness.permissions.checks)
		}
	})

	t.Run("回滚要求管理权限并返回新的草稿基线", func(t *testing.T) {
		store := &httpDatasetStore{revision: revision, rollbackRecord: Record{
			ID: httpTestDatasetID, Version: 8, DraftVersionID: httpTestDraftID,
			DraftRecordVersion: 5, DSLHash: prepared.DSLHash, DSL: prepared.DSLJSON,
		}}
		harness := newDatasetHTTPHarness(t, store, nil, func(check access.Check) bool { return check.Action == "MANAGE" })
		response := performDatasetHTTPRequest(t, harness, http.MethodPost,
			"/api/v1/datasets/"+httpTestDatasetID+"/revisions/"+httpTestRevisionID+"/rollback",
			mustDatasetJSON(t, RollbackRevisionInput{ExpectedVersion: 7}), nil)
		if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
		}
		if store.rollbackCalls != 1 || store.rollbackTenantID != httpTestTenantID || store.rollbackActorID != httpTestUserID ||
			store.rollbackDatasetID != httpTestDatasetID || store.rollbackInput.ExpectedVersion != 7 ||
			store.rollbackRevision.ID != httpTestRevisionID || store.rollbackPrepared.DSLHash != prepared.DSLHash {
			t.Fatalf("store=%#v", store)
		}
		if len(harness.permissions.checks) != 1 || harness.permissions.checks[0].Action != "MANAGE" || harness.permissions.checks[0].ObjectID != httpTestDatasetID {
			t.Fatalf("checks=%#v", harness.permissions.checks)
		}
	})

	t.Run("只有读取权限不能回滚", func(t *testing.T) {
		store := &httpDatasetStore{revision: revision}
		harness := newDatasetHTTPHarness(t, store, nil, func(check access.Check) bool { return check.Action == "READ" })
		response := performDatasetHTTPRequest(t, harness, http.MethodPost,
			"/api/v1/datasets/"+httpTestDatasetID+"/revisions/"+httpTestRevisionID+"/rollback",
			mustDatasetJSON(t, RollbackRevisionInput{ExpectedVersion: 7}), nil)
		if response.Code != http.StatusForbidden || readDatasetErrorCode(t, response) != "PERMISSION_DENIED" || store.rollbackCalls != 0 || store.getRevisionID != "" {
			t.Fatalf("status=%d body=%s store=%#v", response.Code, response.Body.String(), store)
		}
	})
}

func TestPublishedVersionRollbackRouteIsExactAndFailClosed(t *testing.T) {
	prepared, err := Prepare(readExample(t))
	if err != nil {
		t.Fatal(err)
	}
	sourceRevision := RevisionRecord{RevisionSummary: RevisionSummary{
		ID: httpTestRevisionID, DatasetID: httpTestDatasetID, VersionNo: 3, OperationType: "SAVE",
		Name: prepared.Document.Dataset.Name, Description: prepared.Document.Dataset.Description,
		Type: prepared.Document.Dataset.Type, DraftVersionID: httpTestDraftID, DraftRecordVersion: 2,
		DSLVersion: DSLVersion, DSLHash: prepared.DSLHash, PlanHash: prepared.PlanHash,
	}, DSL: prepared.DSLJSON, LogicalPlan: prepared.LogicalPlanJSON}
	target := "/api/v1/datasets/" + httpTestDatasetID + "/versions/" + httpTestVersionID + "/rollback"

	t.Run("管理权限按发布版本解析唯一来源并返回新草稿", func(t *testing.T) {
		store := &httpDatasetStore{sourceRevision: sourceRevision, rollbackRecord: Record{
			ID: httpTestDatasetID, Version: 8, DraftVersionID: httpTestDraftID,
			DraftRecordVersion: 5, DSLHash: prepared.DSLHash, DSL: prepared.DSLJSON,
		}}
		harness := newDatasetHTTPHarness(t, store, nil, func(check access.Check) bool {
			return check.Action == "MANAGE" && check.ObjectID == httpTestDatasetID
		})
		response := performDatasetHTTPRequest(t, harness, http.MethodPost, target,
			mustDatasetJSON(t, RollbackRevisionInput{ExpectedVersion: 7}), nil)
		if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
		}
		if store.resolveSourceCalls != 1 || store.resolveSourceTenantID != httpTestTenantID ||
			store.resolveSourceDatasetID != httpTestDatasetID || store.resolveSourceVersionID != httpTestVersionID ||
			store.rollbackCalls != 1 || store.rollbackRevision.ID != httpTestRevisionID ||
			store.rollbackInput.ExpectedVersion != 7 || store.rollbackPrepared.DSLHash != prepared.DSLHash {
			t.Fatalf("store=%#v", store)
		}
		if len(harness.permissions.checks) != 1 || harness.permissions.checks[0].Action != "MANAGE" {
			t.Fatalf("checks=%#v", harness.permissions.checks)
		}
	})

	t.Run("来源缺失或重复时返回稳定冲突且不写草稿", func(t *testing.T) {
		store := &httpDatasetStore{sourceRevisionErr: ErrVersionRollbackUnavailable}
		harness := newDatasetHTTPHarness(t, store, nil, func(check access.Check) bool { return check.Action == "MANAGE" })
		response := performDatasetHTTPRequest(t, harness, http.MethodPost, target,
			mustDatasetJSON(t, RollbackRevisionInput{ExpectedVersion: 7}), nil)
		if response.Code != http.StatusConflict || readDatasetErrorCode(t, response) != "DATASET_VERSION_ROLLBACK_UNAVAILABLE" ||
			store.resolveSourceCalls != 1 || store.rollbackCalls != 0 {
			t.Fatalf("status=%d body=%s store=%#v", response.Code, response.Body.String(), store)
		}
	})

	t.Run("只有读取权限时在解析来源前拒绝", func(t *testing.T) {
		store := &httpDatasetStore{sourceRevision: sourceRevision}
		harness := newDatasetHTTPHarness(t, store, nil, func(check access.Check) bool { return check.Action == "READ" })
		response := performDatasetHTTPRequest(t, harness, http.MethodPost, target,
			mustDatasetJSON(t, RollbackRevisionInput{ExpectedVersion: 7}), nil)
		if response.Code != http.StatusForbidden || readDatasetErrorCode(t, response) != "PERMISSION_DENIED" ||
			store.resolveSourceCalls != 0 || store.rollbackCalls != 0 {
			t.Fatalf("status=%d body=%s store=%#v", response.Code, response.Body.String(), store)
		}
	})
}

func TestVersionUsageRequiresReadAction(t *testing.T) {
	store := &httpDatasetStore{}
	harness := newDatasetHTTPHarness(t, store, nil, func(check access.Check) bool {
		return check.Action == "PUBLISH"
	})
	response := performDatasetHTTPRequest(t, harness, http.MethodGet, "/api/v1/datasets/"+httpTestDatasetID+"/versions/"+httpTestVersionID+"/usage", nil, nil)
	if response.Code != http.StatusForbidden || readDatasetErrorCode(t, response) != "PERMISSION_DENIED" {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if store.usageCalls != 0 {
		t.Fatalf("权限拒绝后仍读取了占用统计：calls=%d", store.usageCalls)
	}
}

func TestVersionTransitionRequiresPublishAction(t *testing.T) {
	input := VersionTransitionInput{ExpectedVersion: 1, ExpectedStatus: "published", TargetStatus: "stale"}
	target := "/api/v1/datasets/" + httpTestDatasetID + "/versions/" + httpTestVersionID + "/status"

	t.Run("只有读取权限时拒绝", func(t *testing.T) {
		store := &httpDatasetStore{}
		harness := newDatasetHTTPHarness(t, store, nil, func(check access.Check) bool {
			return check.Action == "READ"
		})
		response := performDatasetHTTPRequest(t, harness, http.MethodPost, target, mustDatasetJSON(t, input), nil)
		if response.Code != http.StatusForbidden || readDatasetErrorCode(t, response) != "PERMISSION_DENIED" {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		if len(harness.permissions.checks) != 1 || harness.permissions.checks[0].Action != "PUBLISH" || harness.permissions.checks[0].ObjectID != httpTestDatasetID {
			t.Fatalf("checks=%#v", harness.permissions.checks)
		}
		if store.transitionCalls != 0 {
			t.Fatalf("权限拒绝后仍执行了状态迁移：%d", store.transitionCalls)
		}
	})

	t.Run("发布权限允许迁移", func(t *testing.T) {
		store := &httpDatasetStore{transition: VersionRecord{
			ID: httpTestVersionID, DatasetID: httpTestDatasetID, VersionNo: 1, Status: "STALE",
		}}
		harness := newDatasetHTTPHarness(t, store, nil, func(check access.Check) bool {
			return check.Action == "PUBLISH"
		})
		response := performDatasetHTTPRequest(t, harness, http.MethodPost, target, mustDatasetJSON(t, input), nil)
		if response.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		if store.transitionCalls != 1 || store.transitionTenantID != httpTestTenantID || store.transitionActorID != httpTestUserID || store.transitionDatasetID != httpTestDatasetID || store.transitionVersionID != httpTestVersionID {
			t.Fatalf("迁移作用域不正确：store=%#v", store)
		}
		if store.transitionInput.ExpectedStatus != "PUBLISHED" || store.transitionInput.TargetStatus != "STALE" {
			t.Fatalf("迁移状态未规范化：%#v", store.transitionInput)
		}
	})
}

func TestWriteDatasetErrorMapsPublicationAndVersionErrors(t *testing.T) {
	publicationError := &PublicationValidationError{Issues: []PublicationIssue{{
		Path: "joins[0]", Code: "JOIN_CONFIRMATION_REQUIRED", Reason: "发布前必须确认 Join 字段和基数",
	}}}
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{name: "发布校验失败", err: publicationError, wantStatus: http.StatusUnprocessableEntity, wantCode: "DATASET_PUBLISH_VALIDATION_FAILED"},
		{name: "数据集不存在", err: ErrNotFound, wantStatus: http.StatusNotFound, wantCode: "DATASET_NOT_FOUND"},
		{name: "版本不存在", err: ErrVersionNotFound, wantStatus: http.StatusNotFound, wantCode: "DATASET_VERSION_NOT_FOUND"},
		{name: "草稿修订不存在", err: ErrRevisionNotFound, wantStatus: http.StatusNotFound, wantCode: "DATASET_REVISION_NOT_FOUND"},
		{name: "发布版本回滚来源不可用", err: ErrVersionRollbackUnavailable, wantStatus: http.StatusConflict, wantCode: "DATASET_VERSION_ROLLBACK_UNAVAILABLE"},
		{name: "版本不可用", err: ErrVersionUnavailable, wantStatus: http.StatusConflict, wantCode: "DATASET_VERSION_UNAVAILABLE"},
		{name: "乐观锁冲突", err: ErrConflict, wantStatus: http.StatusConflict, wantCode: "DATASET_VERSION_CONFLICT"},
		{name: "幂等键冲突", err: ErrIdempotencyConflict, wantStatus: http.StatusConflict, wantCode: "DATASET_IDEMPOTENCY_CONFLICT"},
		{name: "领域权限拒绝", err: ErrForbidden, wantStatus: http.StatusForbidden, wantCode: "PERMISSION_DENIED"},
		{name: "发布服务不可用", err: ErrPublishUnavailable, wantStatus: http.StatusServiceUnavailable, wantCode: "DATASET_PUBLISH_UNAVAILABLE"},
		{name: "数据集仍被占用", err: ErrInUse, wantStatus: http.StatusConflict, wantCode: "DATASET_IN_USE"},
		{name: "状态迁移无效", err: ErrInvalidTransition, wantStatus: http.StatusConflict, wantCode: "DATASET_VERSION_TRANSITION_INVALID"},
		{name: "文档无效", err: ErrInvalidDocument, wantStatus: http.StatusBadRequest, wantCode: "DSL-002-INVALID-DOCUMENT"},
		{name: "未知存储错误", err: errors.New("storage failed"), wantStatus: http.StatusInternalServerError, wantCode: "DATASET_PERSISTENCE_FAILED"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			writeDatasetError(response, test.err)
			if response.Code != test.wantStatus || readDatasetErrorCode(t, response) != test.wantCode {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if test.err == publicationError {
				var body struct {
					Details []PublicationIssue `json:"details"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if len(body.Details) != 1 || body.Details[0].Code != "JOIN_CONFIRMATION_REQUIRED" || body.Details[0].Path != "joins[0]" {
					t.Fatalf("details=%#v", body.Details)
				}
			}
		})
	}
}
