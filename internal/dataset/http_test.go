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
	httpTestTenantID  = "tenant-http-test"
	httpTestUserID    = "user-http-test"
	httpTestSessionID = "session-http-test"
	httpTestDatasetID = "11111111-1111-4111-8111-111111111111"
	httpTestVersionID = "22222222-2222-4222-8222-222222222222"
	httpTestDraftID   = "33333333-3333-4333-8333-333333333333"
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
	result    PreviewResult
	err       error
	tenantID  string
	actorID   string
	datasetID string
	versionID string
	input     PreviewInput
}

func (p *httpPreviewer) Preview(context.Context, string, string, string, PreviewInput) (PreviewResult, error) {
	return p.result, p.err
}

func (p *httpPreviewer) PreviewVersion(_ context.Context, tenantID, actorID, datasetID, versionID string, input PreviewInput) (PreviewResult, error) {
	p.tenantID = tenantID
	p.actorID = actorID
	p.datasetID = datasetID
	p.versionID = versionID
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

func TestPublishRejectsMissingAndControlIdempotencyKey(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string]string
	}{
		{name: "缺少请求头", headers: nil},
		{name: "包含控制字符", headers: map[string]string{"Idempotency-Key": "publish\u007fkey"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &httpDatasetStore{}
			harness := newDatasetHTTPHarness(t, store, nil, nil)
			response := performDatasetHTTPRequest(t, harness, http.MethodPost, "/api/v1/datasets/"+httpTestDatasetID+"/publish", []byte(`{}`), test.headers)
			if response.Code != http.StatusBadRequest || readDatasetErrorCode(t, response) != "INVALID_IDEMPOTENCY_KEY" {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if store.replayCalls != 0 || store.publishCalls != 0 {
				t.Fatalf("无效幂等键仍访问了发布存储：replay=%d publish=%d", store.replayCalls, store.publishCalls)
			}
		})
	}
}

func TestPublishReturnsCreatedVersion(t *testing.T) {
	prepared, err := Prepare(readExample(t))
	if err != nil {
		t.Fatal(err)
	}
	store := &httpDatasetStore{
		record: Record{
			ID: httpTestDatasetID, Version: 3, DraftVersionID: httpTestDraftID, DraftRecordVersion: 2,
			DSLHash: prepared.DSLHash, PlanHash: prepared.PlanHash, DSL: prepared.DSLJSON,
		},
		published: VersionRecord{
			ID: httpTestVersionID, DatasetID: httpTestDatasetID, DatasetRecordVersion: 4,
			DraftVersionID: httpTestDraftID, DraftRecordVersion: 2, VersionNo: 1,
			Status: "PUBLISHED", DSLVersion: DSLVersion, DSLHash: prepared.DSLHash, PlanHash: prepared.PlanHash,
			DSL: prepared.DSLJSON, LogicalPlan: prepared.LogicalPlanJSON,
		},
	}
	harness := newDatasetHTTPHarness(t, store, nil, nil)
	input := PublishInput{
		DraftVersionID: httpTestDraftID, ExpectedVersion: 3, ExpectedDraftRecordVersion: 2,
		ExpectedDSLHash: prepared.DSLHash, ValidationParameters: map[string]any{"start_date": "2026-01-01"},
	}
	response := performDatasetHTTPRequest(t, harness, http.MethodPost, "/api/v1/datasets/"+httpTestDatasetID+"/publish", mustDatasetJSON(t, input), map[string]string{"Idempotency-Key": "publish-http-1"})
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var body VersionRecord
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.ID != httpTestVersionID || body.DatasetID != httpTestDatasetID || body.Status != "PUBLISHED" {
		t.Fatalf("body=%#v", body)
	}
	if store.replayCalls != 1 || store.publishCalls != 1 || store.publishPlan.IdempotencyKey != "publish-http-1" {
		t.Fatalf("replay=%d publish=%d plan=%#v", store.replayCalls, store.publishCalls, store.publishPlan)
	}
	if store.publishTenantID != httpTestTenantID || store.publishActorID != httpTestUserID || store.publishDatasetID != httpTestDatasetID {
		t.Fatalf("发布作用域不正确：tenant=%q actor=%q dataset=%q", store.publishTenantID, store.publishActorID, store.publishDatasetID)
	}
	if harness.validator.candidate.DraftVersionID != httpTestDraftID || harness.validator.candidate.Parameters["start_date"] != "2026-01-01" {
		t.Fatalf("candidate=%#v", harness.validator.candidate)
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
		if response.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
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
		{name: "版本不可用", err: ErrVersionUnavailable, wantStatus: http.StatusConflict, wantCode: "DATASET_VERSION_UNAVAILABLE"},
		{name: "乐观锁冲突", err: ErrConflict, wantStatus: http.StatusConflict, wantCode: "DATASET_VERSION_CONFLICT"},
		{name: "幂等键冲突", err: ErrIdempotencyConflict, wantStatus: http.StatusConflict, wantCode: "DATASET_IDEMPOTENCY_CONFLICT"},
		{name: "领域权限拒绝", err: ErrForbidden, wantStatus: http.StatusForbidden, wantCode: "PERMISSION_DENIED"},
		{name: "发布服务不可用", err: ErrPublishUnavailable, wantStatus: http.StatusServiceUnavailable, wantCode: "DATASET_PUBLISH_UNAVAILABLE"},
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
