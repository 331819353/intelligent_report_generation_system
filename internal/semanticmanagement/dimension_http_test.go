package semanticmanagement

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/access"
	"intelligent-report-generation-system/internal/auth"
)

func newDimensionHTTPHarness(
	t *testing.T,
	store DimensionStore,
	allow bool,
) semanticHTTPHarness {
	t.Helper()
	tokens := auth.NewTokenManager(
		"semantic-dimension-http-test",
		"01234567890123456789012345678901",
		time.Hour,
	)
	token, _, err := tokens.Issue(testActorID, testTenantID, semanticHTTPSessionID, 1)
	if err != nil {
		t.Fatal(err)
	}
	authStore := &semanticHTTPAuthStore{
		user: auth.LoginUser{
			ID: testActorID, TenantID: testTenantID,
			Status: auth.UserStatusActive, TokenVersion: 1,
		},
		session: auth.Session{
			ID: semanticHTTPSessionID, TenantID: testTenantID, UserID: testActorID,
			UserStatus: auth.UserStatusActive, TokenVersion: 1,
			ExpiresAt: time.Now().Add(time.Hour),
		},
	}
	authService := auth.NewService(
		authStore, auth.NewPasswordManager(4), tokens, time.Hour,
	)
	permissionStore := &semanticHTTPPermissionStore{allow: allow}
	accessService := access.NewService(permissionStore)
	return semanticHTTPHarness{
		handler: NewHandler(
			authService, accessService, NewService(&fakeStore{}),
			NewDimensionService(store),
		),
		token: token, permissions: permissionStore,
	}
}

func TestDimensionHTTPPersists690MemberAliasAndUsesManagePermission(t *testing.T) {
	store := &fakeDimensionStore{}
	harness := newDimensionHTTPHarness(t, store, true)
	response := semanticRequest(
		t, harness, http.MethodPost,
		"/api/v1/semantic/dimension-member-aliases",
		`{"dimensionId":"`+testDimensionID+`","dimensionMemberId":"`+
			testDimensionMemberID+`","alias":"690","aliasType":"LEGACY"}`,
	)
	if response.Code != http.StatusCreated ||
		!strings.Contains(response.Body.String(), `"alias":"690"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if store.aliasNormalized != "690" || len(harness.permissions.checks) != 1 ||
		harness.permissions.checks[0].ResourceType != "DATASET" ||
		harness.permissions.checks[0].Action != "MANAGE" {
		t.Fatalf("normalized=%q checks=%+v", store.aliasNormalized, harness.permissions.checks)
	}
}

func TestDimensionHTTPRefreshReplayAndSearchPermissionComposition(t *testing.T) {
	refreshStore := &fakeDimensionStore{refreshCreated: false}
	harness := newDimensionHTTPHarness(t, refreshStore, true)
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/semantic/dimensions/"+testDimensionID+"/member-refresh-jobs",
		strings.NewReader(`{"expectedDimensionVersion":1}`),
	)
	request.Header.Set("Authorization", "Bearer "+harness.token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "refresh-1")
	response := httptest.NewRecorder()
	harness.handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK ||
		response.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("status=%d replay=%q body=%s",
			response.Code, response.Header().Get("Idempotency-Replayed"), response.Body.String())
	}

	searchStore := &fakeDimensionStore{
		searchResults: []MemberMetricSearchResult{{
			MatchedValue: "690", MatchType: "MEMBER_ALIAS",
			DimensionID: testDimensionID, DimensionMemberID: testDimensionMemberID,
			MetricID: testMetricID, MetricVersionID: testMetricVersionID,
		}},
	}
	harness = newDimensionHTTPHarness(t, searchStore, true)
	response = semanticRequest(
		t, harness, http.MethodGet,
		"/api/v1/semantic/member-metric-search?q=690&limit=10", "",
	)
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), `"matchedValue":"690"`) ||
		searchStore.searchQuery != "690" || searchStore.readActorID != testActorID {
		t.Fatalf("status=%d actor=%q query=%q body=%s",
			response.Code, searchStore.readActorID, searchStore.searchQuery, response.Body.String())
	}
	if len(harness.permissions.checks) != 1 ||
		harness.permissions.checks[0].ResourceType != "METRIC" ||
		harness.permissions.checks[0].Action != "READ" {
		t.Fatalf("permission checks=%+v", harness.permissions.checks)
	}
}

func TestDimensionHTTPMemberReadsDelegateActorAndMapPolicyDenial(t *testing.T) {
	store := &fakeDimensionStore{}
	harness := newDimensionHTTPHarness(t, store, false)
	response := semanticRequest(
		t, harness, http.MethodGet,
		"/api/v1/semantic/dimensions/"+testDimensionID+"/members?limit=10", "",
	)
	if response.Code != http.StatusOK || store.readActorID != testActorID ||
		len(harness.permissions.checks) != 0 {
		t.Fatalf(
			"members status=%d actor=%q permissionChecks=%+v body=%s",
			response.Code, store.readActorID, harness.permissions.checks,
			response.Body.String(),
		)
	}
	response = semanticRequest(
		t, harness, http.MethodGet,
		"/api/v1/semantic/dimension-member-aliases?dimensionId="+
			testDimensionID, "",
	)
	if response.Code != http.StatusOK || store.readActorID != testActorID ||
		len(harness.permissions.checks) != 0 {
		t.Fatalf(
			"aliases status=%d actor=%q permissionChecks=%+v body=%s",
			response.Code, store.readActorID, harness.permissions.checks,
			response.Body.String(),
		)
	}

	denied := &fakeDimensionStore{err: ErrMemberAccessDenied}
	harness = newDimensionHTTPHarness(t, denied, true)
	response = semanticRequest(
		t, harness, http.MethodGet,
		"/api/v1/semantic/dimensions/"+testDimensionID+"/members", "",
	)
	if response.Code != http.StatusForbidden ||
		response.Header().Get("Cache-Control") != "no-store" ||
		!strings.Contains(
			response.Body.String(), `"code":"SEMANTIC_MEMBER_ACCESS_DENIED"`,
		) {
		t.Fatalf(
			"denied status=%d cache=%q body=%s",
			response.Code, response.Header().Get("Cache-Control"),
			response.Body.String(),
		)
	}
}
