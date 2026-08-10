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
	"intelligent-report-generation-system/internal/askdata/registry"
)

type fakeAdminBackend struct {
	listPage registry.AdminPage
	getValue any
	result   registry.AdminWriteResult
	err      error

	lastScope       registry.AdminScope
	lastResource    registry.AdminResource
	lastResourceID  string
	lastMutation    registry.AdminMutation
	lastDelete      registry.DeleteDraftInput
	lastRelease     registry.ReleaseDraftInput
	lastCommand     registry.AdminCommand
	listCalls       int
	getCalls        int
	createCalls     int
	updateCalls     int
	deleteCalls     int
	releaseCalls    int
	additivityPage  registry.AdditivityCandidatePage
	readiness       registry.AdditivityReadiness
	confirmation    registry.BulkAdditivityConfirmationResult
	lastGroup       registry.Additivity
	lastConfirm     registry.BulkAdditivityConfirmation
	additivityCalls int
	readinessCalls  int
	confirmCalls    int
}

func (backend *fakeAdminBackend) ListDrafts(
	_ context.Context,
	scope registry.AdminScope,
	resource registry.AdminResource,
	_ string,
	_ int,
) (registry.AdminPage, error) {
	backend.listCalls++
	backend.lastScope, backend.lastResource = scope, resource
	return backend.listPage, backend.err
}

func (backend *fakeAdminBackend) GetDraft(
	_ context.Context,
	scope registry.AdminScope,
	resource registry.AdminResource,
	resourceID string,
) (any, error) {
	backend.getCalls++
	backend.lastScope, backend.lastResource, backend.lastResourceID = scope, resource, resourceID
	return backend.getValue, backend.err
}

func (backend *fakeAdminBackend) CreateDraft(
	_ context.Context,
	scope registry.AdminScope,
	resource registry.AdminResource,
	mutation registry.AdminMutation,
	command registry.AdminCommand,
) (registry.AdminWriteResult, error) {
	backend.createCalls++
	backend.lastScope, backend.lastResource = scope, resource
	backend.lastMutation, backend.lastCommand = mutation, command
	return backend.result, backend.err
}

func (backend *fakeAdminBackend) UpdateDraft(
	_ context.Context,
	scope registry.AdminScope,
	resource registry.AdminResource,
	resourceID string,
	mutation registry.AdminMutation,
	command registry.AdminCommand,
) (registry.AdminWriteResult, error) {
	backend.updateCalls++
	backend.lastScope, backend.lastResource, backend.lastResourceID = scope, resource, resourceID
	backend.lastMutation, backend.lastCommand = mutation, command
	return backend.result, backend.err
}

func (backend *fakeAdminBackend) DeleteDraft(
	_ context.Context,
	scope registry.AdminScope,
	resource registry.AdminResource,
	resourceID string,
	input registry.DeleteDraftInput,
	command registry.AdminCommand,
) (registry.AdminWriteResult, error) {
	backend.deleteCalls++
	backend.lastScope, backend.lastResource, backend.lastResourceID = scope, resource, resourceID
	backend.lastDelete, backend.lastCommand = input, command
	return backend.result, backend.err
}

func (backend *fakeAdminBackend) CreateAdminReleaseDraft(
	_ context.Context,
	scope registry.AdminScope,
	input registry.ReleaseDraftInput,
	command registry.AdminCommand,
) (registry.AdminWriteResult, error) {
	backend.releaseCalls++
	backend.lastScope, backend.lastRelease, backend.lastCommand = scope, input, command
	return backend.result, backend.err
}

func (backend *fakeAdminBackend) ListUnconfirmedAdditivity(
	_ context.Context,
	scope registry.AdminScope,
	group registry.Additivity,
	_ string,
	_ int,
) (registry.AdditivityCandidatePage, error) {
	backend.additivityCalls++
	backend.lastScope, backend.lastGroup = scope, group
	return backend.additivityPage, backend.err
}

func (backend *fakeAdminBackend) GetAdditivityReadiness(
	_ context.Context,
	scope registry.AdminScope,
) (registry.AdditivityReadiness, error) {
	backend.readinessCalls++
	backend.lastScope = scope
	return backend.readiness, backend.err
}

func (backend *fakeAdminBackend) BulkConfirmAdditivity(
	_ context.Context,
	scope registry.AdminScope,
	input registry.BulkAdditivityConfirmation,
	command registry.AdminCommand,
) (registry.BulkAdditivityConfirmationResult, error) {
	backend.confirmCalls++
	backend.lastScope, backend.lastConfirm, backend.lastCommand = scope, input, command
	return backend.confirmation, backend.err
}

func TestSemanticAdminAdditivityListReadinessAndBulkConfirmation(t *testing.T) {
	scope := testAdminScope()
	metricVersionID := uuid.NewString()
	backend := &fakeAdminBackend{
		additivityPage: registry.AdditivityCandidatePage{Items: []registry.AdditivityCandidate{{
			MetricVersionID: metricVersionID,
			Suggestion: registry.AdditivitySuggestion{
				Value: registry.NonAdditive, RuleID: registry.AdditivityRuleRatioLexicon,
			},
		}}},
		readiness: registry.AdditivityReadiness{
			DomainID: scope.DomainID, MetricCount: 4, ConfirmedCount: 3,
			UnconfirmedCount: 1, ConfirmationRate: .75,
		},
		confirmation: registry.BulkAdditivityConfirmationResult{
			MetricVersionIDs: []string{metricVersionID}, ConfirmedCount: 1,
		},
	}
	handler := testAdminHandler(backend, scope)

	list := httptest.NewRequest(http.MethodGet,
		"/api/v1/askdata/semantic/metrics?additivityStatus=UNCONFIRMED&suggestion=NON_ADDITIVE", nil)
	listResponse := httptest.NewRecorder()
	handler.ServeHTTP(listResponse, list)
	if listResponse.Code != http.StatusOK || backend.additivityCalls != 1 || backend.listCalls != 0 ||
		backend.lastGroup != registry.NonAdditive || !strings.Contains(listResponse.Body.String(), metricVersionID) {
		t.Fatalf("additivity list = %d/%s calls=%d generic=%d group=%s",
			listResponse.Code, listResponse.Body.String(), backend.additivityCalls,
			backend.listCalls, backend.lastGroup)
	}

	readiness := httptest.NewRequest(http.MethodGet,
		"/api/v1/askdata/semantic/domains/"+scope.DomainID+"/readiness", nil)
	readinessResponse := httptest.NewRecorder()
	handler.ServeHTTP(readinessResponse, readiness)
	if readinessResponse.Code != http.StatusOK || backend.readinessCalls != 1 ||
		!strings.Contains(readinessResponse.Body.String(), `"confirmationRate":0.75`) {
		t.Fatalf("readiness = %d/%s calls=%d",
			readinessResponse.Code, readinessResponse.Body.String(), backend.readinessCalls)
	}

	confirm := httptest.NewRequest(http.MethodPost,
		"/api/v1/askdata/semantic/metrics/additivity/confirm",
		strings.NewReader(`{"metricVersionIds":["`+metricVersionID+`"],"suggestion":"NON_ADDITIVE"}`))
	confirm.Header.Set("Content-Type", "application/json")
	confirm.Header.Set("Idempotency-Key", "additivity-confirm-0001")
	confirmResponse := httptest.NewRecorder()
	handler.ServeHTTP(confirmResponse, confirm)
	if confirmResponse.Code != http.StatusOK || backend.confirmCalls != 1 ||
		backend.lastConfirm.Suggestion != registry.NonAdditive || backend.lastConfirm.MetricVersionIDs[0] != metricVersionID {
		t.Fatalf("confirmation = %d/%s calls=%d input=%+v",
			confirmResponse.Code, confirmResponse.Body.String(), backend.confirmCalls, backend.lastConfirm)
	}
	if err := backend.lastCommand.Validate(); err != nil {
		t.Fatalf("confirmation command = %v", err)
	}

	wrongDomain := httptest.NewRequest(http.MethodGet,
		"/api/v1/askdata/semantic/domains/"+uuid.NewString()+"/readiness", nil)
	wrongDomainResponse := httptest.NewRecorder()
	handler.ServeHTTP(wrongDomainResponse, wrongDomain)
	if wrongDomainResponse.Code != http.StatusBadRequest || backend.readinessCalls != 1 {
		t.Fatalf("cross-domain readiness = %d/%s calls=%d",
			wrongDomainResponse.Code, wrongDomainResponse.Body.String(), backend.readinessCalls)
	}
}

func TestSemanticAdminCreateDraftUsesAuthenticatedScopeAndDurableCommand(t *testing.T) {
	scope := testAdminScope()
	backend := &fakeAdminBackend{result: registry.AdminWriteResult{
		ResourceType: registry.AdminResourceBusinessTerm,
		ResourceID:   uuid.NewString(),
		ObjectID:     uuid.NewString(),
		ContentHash:  askdata.HashBytes([]byte("term")),
		Status:       "DRAFT",
	}}
	handler := testAdminHandler(backend, scope)
	request := httptest.NewRequest(http.MethodPost,
		"/api/v1/askdata/semantic/terms", strings.NewReader(`{
			"versionNo":1,
			"term":"毛利率",
			"termType":"METRIC",
			"targetObjectType":"METRIC",
			"targetVersionId":"`+uuid.NewString()+`",
			"targetCode":"gross_margin",
			"matchMode":"EXACT",
			"priority":100,
			"negativeContexts":["成本率"],
			"applicableRoleIds":[],
			"source":"FEEDBACK",
			"code":"gross_margin",
			"name":"毛利率",
			"definition":"销售毛利占销售收入的比例",
			"aliases":["毛利率","GM Rate"]
		}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "term-create-0001")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if backend.createCalls != 1 || backend.lastScope != scope ||
		backend.lastResource != registry.AdminResourceBusinessTerm ||
		backend.lastMutation.BusinessTerm == nil ||
		backend.lastMutation.BusinessTerm.Code != "gross_margin" ||
		backend.lastMutation.BusinessTerm.Term != "毛利率" ||
		backend.lastMutation.BusinessTerm.Source != registry.TermSourceFeedback {
		t.Fatalf("create dispatch = calls:%d scope:%#v resource:%s mutation:%#v",
			backend.createCalls, backend.lastScope, backend.lastResource, backend.lastMutation)
	}
	if err := backend.lastCommand.Validate(); err != nil {
		t.Fatalf("admin command validation = %v", err)
	}
	if strings.Contains(response.Body.String(), scope.TenantID) ||
		strings.Contains(response.Body.String(), scope.ActorID) {
		t.Fatalf("write result leaks request scope: %s", response.Body.String())
	}
}

func TestSemanticAdminCreatesKPIBundleDraftWithoutLifecycleBypass(t *testing.T) {
	scope := testAdminScope()
	backend := &fakeAdminBackend{result: registry.AdminWriteResult{
		ResourceType: registry.AdminResourceKPIBundle,
		ResourceID:   uuid.NewString(), ObjectID: uuid.NewString(),
		ContentHash: askdata.HashBytes([]byte("kpi-bundle")), Status: "DRAFT",
	}}
	handler := testAdminHandler(backend, scope)
	request := httptest.NewRequest(http.MethodPost,
		"/api/v1/askdata/semantic/kpi-bundles", strings.NewReader(`{
			"versionNo":1,
			"code":"sales_overview",
			"name":"销售经营概览",
			"items":[{
				"metricVersionId":"`+uuid.NewString()+`",
				"role":"HEADLINE",
				"groupByDimensionVersionIds":[],
				"chartType":"metric-card",
				"order":1
			}],
			"defaultDimensionVersionIds":[],
			"defaultTimeExpression":"CURRENT_MONTH",
			"defaultChartTypes":["metric-card"],
			"roleMapping":{},
			"applicableQuestionPatterns":["经营情况"]
		}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "kpi-bundle-create-0001")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusCreated || backend.createCalls != 1 ||
		backend.lastResource != registry.AdminResourceKPIBundle || backend.lastMutation.KPIBundle == nil ||
		backend.lastMutation.KPIBundle.Code != "sales_overview" ||
		backend.lastMutation.KPIBundle.Items[0].Role != registry.KPIBundleRoleHeadline {
		t.Fatalf("KPI bundle dispatch = status:%d body:%s calls:%d resource:%s mutation:%#v",
			response.Code, response.Body.String(), backend.createCalls,
			backend.lastResource, backend.lastMutation)
	}
}

func TestSemanticAdminRejectsScopeInjectionMalformedWritesAndLifecycleBypass(t *testing.T) {
	scope := testAdminScope()
	backend := &fakeAdminBackend{}
	handler := testAdminHandler(backend, scope)
	tests := []struct {
		name   string
		method string
		path   string
		body   string
		key    string
		status int
	}{
		{
			name: "server scope field", method: http.MethodPost,
			path: "/api/v1/askdata/semantic/metrics",
			body: `{"tenantId":"` + scope.TenantID + `","code":"sales","name":"销售额","description":""}`,
			key:  "metric-create-0001", status: http.StatusBadRequest,
		},
		{
			name: "unknown status field", method: http.MethodPost,
			path: "/api/v1/askdata/semantic/metrics",
			body: `{"code":"sales","name":"销售额","description":"","status":"ACTIVE"}`,
			key:  "metric-create-0002", status: http.StatusBadRequest,
		},
		{
			name: "missing idempotency key", method: http.MethodPost,
			path:   "/api/v1/askdata/semantic/metrics",
			body:   `{"code":"sales","name":"销售额","description":""}`,
			status: http.StatusBadRequest,
		},
		{
			name: "validate lifecycle is not exposed", method: http.MethodPost,
			path: "/api/v1/askdata/semantic/releases/" + uuid.NewString() + "/validate",
			body: `{}`, key: "release-validate-0001", status: http.StatusNotFound,
		},
		{
			name: "activate lifecycle is not exposed", method: http.MethodPost,
			path: "/api/v1/askdata/semantic/releases/" + uuid.NewString() + "/activate",
			body: `{}`, key: "release-activate-0001", status: http.StatusNotFound,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			if test.key != "" {
				request.Header.Set("Idempotency-Key", test.key)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
		})
	}
	if backend.createCalls != 0 || backend.releaseCalls != 0 {
		t.Fatalf("invalid or lifecycle requests reached backend: create=%d release=%d",
			backend.createCalls, backend.releaseCalls)
	}
}

func TestSemanticAdminDispatchesDraftReadUpdateDeleteAndReleaseManifest(t *testing.T) {
	scope := testAdminScope()
	resourceID := uuid.NewString()
	updatedAt := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	backend := &fakeAdminBackend{
		listPage: registry.AdminPage{Items: []registry.Metric{}},
		getValue: registry.Metric{ID: resourceID, Status: "DRAFT"},
		result: registry.AdminWriteResult{
			ResourceType: registry.AdminResourceMetric,
			ResourceID:   resourceID, Status: "DRAFT", RecordVersion: 2,
		},
	}
	handler := testAdminHandler(backend, scope)

	list := httptest.NewRequest(http.MethodGet, "/api/v1/askdata/semantic/metrics?limit=25", nil)
	listResponse := httptest.NewRecorder()
	handler.ServeHTTP(listResponse, list)
	if listResponse.Code != http.StatusOK || backend.listCalls != 1 {
		t.Fatalf("list response = %d/%s calls=%d", listResponse.Code, listResponse.Body.String(), backend.listCalls)
	}

	get := httptest.NewRequest(http.MethodGet, "/api/v1/askdata/semantic/metrics/"+resourceID, nil)
	getResponse := httptest.NewRecorder()
	handler.ServeHTTP(getResponse, get)
	if getResponse.Code != http.StatusOK || backend.getCalls != 1 || backend.lastResourceID != resourceID {
		t.Fatalf("get response = %d/%s calls=%d id=%s", getResponse.Code, getResponse.Body.String(), backend.getCalls, backend.lastResourceID)
	}

	update := httptest.NewRequest(http.MethodPut, "/api/v1/askdata/semantic/metrics/"+resourceID,
		strings.NewReader(`{"code":"sales","name":"销售额","description":"已支付","expectedVersion":1}`))
	update.Header.Set("Content-Type", "application/json")
	update.Header.Set("Idempotency-Key", "metric-update-0001")
	updateResponse := httptest.NewRecorder()
	handler.ServeHTTP(updateResponse, update)
	if updateResponse.Code != http.StatusOK || backend.updateCalls != 1 ||
		backend.lastMutation.Metric == nil || backend.lastMutation.Metric.ExpectedVersion != 1 {
		t.Fatalf("update response = %d/%s mutation=%#v", updateResponse.Code, updateResponse.Body.String(), backend.lastMutation)
	}

	deleteRequest := httptest.NewRequest(http.MethodDelete, "/api/v1/askdata/semantic/dimensions/"+resourceID,
		strings.NewReader(`{"expectedUpdatedAt":"`+updatedAt.Format(time.RFC3339)+`"}`))
	deleteRequest.Header.Set("Content-Type", "application/json")
	deleteRequest.Header.Set("Idempotency-Key", "dimension-delete-0001")
	deleteResponse := httptest.NewRecorder()
	handler.ServeHTTP(deleteResponse, deleteRequest)
	if deleteResponse.Code != http.StatusOK || backend.deleteCalls != 1 ||
		backend.lastDelete.ExpectedUpdatedAt == nil ||
		!backend.lastDelete.ExpectedUpdatedAt.Equal(updatedAt) {
		t.Fatalf("delete response = %d/%s input=%#v", deleteResponse.Code, deleteResponse.Body.String(), backend.lastDelete)
	}

	backend.result = registry.AdminWriteResult{
		ResourceType: registry.AdminResourceRelease, ResourceID: uuid.NewString(),
		Status: "DRAFT", SemanticVersion: "sales-v1",
	}
	release := httptest.NewRequest(http.MethodPost, "/api/v1/askdata/semantic/releases",
		strings.NewReader(`{"semanticVersion":"sales-v1","objects":[]}`))
	release.Header.Set("Content-Type", "application/json")
	release.Header.Set("Idempotency-Key", "release-create-0001")
	releaseResponse := httptest.NewRecorder()
	handler.ServeHTTP(releaseResponse, release)
	if releaseResponse.Code != http.StatusCreated || backend.releaseCalls != 1 ||
		backend.lastRelease.SemanticVersion != "sales-v1" {
		t.Fatalf("release response = %d/%s input=%#v", releaseResponse.Code, releaseResponse.Body.String(), backend.lastRelease)
	}
}

func TestSemanticAdminMapsStableErrorsAndRequiresBearerBeforeBackend(t *testing.T) {
	scope := testAdminScope()
	tests := []struct {
		err    error
		status int
		code   string
	}{
		{registry.ErrRegistryPermissionDenied, http.StatusForbidden, "REG_PERMISSION_DENIED"},
		{registry.ErrRegistryNotFound, http.StatusNotFound, "REG_NOT_FOUND"},
		{registry.ErrRegistryVersionConflict, http.StatusConflict, "REG_VERSION_CONFLICT"},
		{registry.ErrRegistryIdempotencyConflict, http.StatusConflict, "REG_IDEMPOTENCY_CONFLICT"},
		{registry.ErrRegistryDraftInUse, http.StatusConflict, "REG_DRAFT_IN_USE"},
		{errors.New("database unavailable"), http.StatusInternalServerError, "REG_SERVICE_FAILED"},
	}
	for _, test := range tests {
		backend := &fakeAdminBackend{err: test.err}
		handler := testAdminHandler(backend, scope)
		request := httptest.NewRequest(http.MethodGet, "/api/v1/askdata/semantic/models", nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.status || !strings.Contains(response.Body.String(), test.code) {
			t.Fatalf("error %v response = %d/%s", test.err, response.Code, response.Body.String())
		}
	}

	backend := &fakeAdminBackend{}
	handler := NewAdminHandler(nil, backend)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/askdata/semantic/models", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || backend.listCalls != 0 ||
		!strings.Contains(response.Body.String(), "ACCESS_TOKEN_REQUIRED") {
		t.Fatalf("auth response = %d/%s calls=%d", response.Code, response.Body.String(), backend.listCalls)
	}
}

func testAdminScope() registry.AdminScope {
	return registry.AdminScope{
		TenantID: uuid.NewString(), DomainID: uuid.NewString(), ActorID: uuid.NewString(),
	}
}

func testAdminHandler(backend registry.AdminBackend, scope registry.AdminScope) http.Handler {
	return newProtectedAdminHandler(backend, func(context.Context) (registry.AdminScope, error) {
		return scope, nil
	})
}

func decodeAdminResult(t *testing.T, response *httptest.ResponseRecorder) registry.AdminWriteResult {
	t.Helper()
	var result registry.AdminWriteResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	return result
}
