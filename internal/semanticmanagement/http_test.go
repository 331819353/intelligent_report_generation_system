package semanticmanagement

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

const semanticHTTPSessionID = "semantic-management-http-session"

type semanticHTTPAuthStore struct {
	user    auth.LoginUser
	session auth.Session
}

func (s *semanticHTTPAuthStore) FindTenantID(context.Context, string) (string, error) {
	return s.user.TenantID, nil
}
func (s *semanticHTTPAuthStore) FindUserByEmail(context.Context, string, string) (auth.LoginUser, error) {
	return s.user, nil
}
func (s *semanticHTTPAuthStore) FindUserByID(context.Context, string, string) (auth.LoginUser, error) {
	return s.user, nil
}
func (s *semanticHTTPAuthStore) CreateSession(_ context.Context, session auth.Session, _, _ string) error {
	s.session = session
	return nil
}
func (s *semanticHTTPAuthStore) FindSession(context.Context, string, string) (auth.Session, error) {
	return s.session, nil
}
func (*semanticHTTPAuthStore) RotateSession(context.Context, string, string, []byte, []byte, time.Time) error {
	return nil
}
func (*semanticHTTPAuthStore) RevokeSession(context.Context, string, string, []byte, string) error {
	return nil
}
func (*semanticHTTPAuthStore) RecordLoginFailure(context.Context, string, string, string, string, string, string) {
}

type semanticHTTPPermissionStore struct {
	allow  bool
	checks []access.Check
}

func (s *semanticHTTPPermissionStore) Allowed(_ context.Context, check access.Check) (bool, error) {
	s.checks = append(s.checks, check)
	return s.allow, nil
}

type semanticHTTPHarness struct {
	handler     http.Handler
	token       string
	permissions *semanticHTTPPermissionStore
}

func newSemanticHTTPHarness(t *testing.T, store *fakeStore, allow bool) semanticHTTPHarness {
	t.Helper()
	tokens := auth.NewTokenManager("semantic-http-test", "01234567890123456789012345678901", time.Hour)
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
	authService := auth.NewService(authStore, auth.NewPasswordManager(4), tokens, time.Hour)
	permissionStore := &semanticHTTPPermissionStore{allow: allow}
	accessService := access.NewService(permissionStore)
	return semanticHTTPHarness{
		handler: NewHandler(authService, accessService, NewService(store)),
		token:   token, permissions: permissionStore,
	}
}

func semanticRequest(t *testing.T, harness semanticHTTPHarness, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+harness.token)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	harness.handler.ServeHTTP(response, request)
	return response
}

func TestSemanticHTTPWritesLegacyAliasWithDatasetManagePermission(t *testing.T) {
	store := &fakeStore{}
	harness := newSemanticHTTPHarness(t, store, true)
	response := semanticRequest(t, harness, http.MethodPost, "/api/v1/semantic/tag-aliases",
		`{"tagId":"`+testTagID+`","alias":"690","aliasType":"LEGACY","languageCode":"zh-CN"}`)
	if response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), `"alias":"690"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if store.tenantID != testTenantID || store.actorID != testActorID {
		t.Fatalf("claims not forwarded: tenant=%q actor=%q", store.tenantID, store.actorID)
	}
	if len(harness.permissions.checks) != 1 ||
		harness.permissions.checks[0].ResourceType != "DATASET" ||
		harness.permissions.checks[0].Action != "MANAGE" {
		t.Fatalf("permission checks=%+v", harness.permissions.checks)
	}
}

func TestSemanticHTTPListUsesReadPermissionAndNoStore(t *testing.T) {
	harness := newSemanticHTTPHarness(t, &fakeStore{}, true)
	response := semanticRequest(t, harness, http.MethodGet, "/api/v1/semantic/tags?limit=25", "")
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d cache=%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	}
	if len(harness.permissions.checks) != 1 || harness.permissions.checks[0].Action != "READ" {
		t.Fatalf("permission checks=%+v", harness.permissions.checks)
	}
}

func TestSemanticHTTPRejectsUnknownFieldsAndMapsConflict(t *testing.T) {
	harness := newSemanticHTTPHarness(t, &fakeStore{}, true)
	response := semanticRequest(t, harness, http.MethodPost, "/api/v1/semantic/tag-aliases",
		`{"tagId":"`+testTagID+`","alias":"690","unexpected":true}`)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "INVALID_REQUEST") {
		t.Fatalf("unknown field status=%d body=%s", response.Code, response.Body.String())
	}

	harness = newSemanticHTTPHarness(t, &fakeStore{err: ErrConflict}, true)
	response = semanticRequest(t, harness, http.MethodPost, "/api/v1/semantic/tags/"+testTagID+"/deprecate",
		`{"expectedVersion":2}`)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "SEMANTIC_VERSION_CONFLICT") {
		t.Fatalf("conflict status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestSemanticHTTPRequiresAuthenticationAndPermission(t *testing.T) {
	harness := newSemanticHTTPHarness(t, &fakeStore{}, false)
	response := semanticRequest(t, harness, http.MethodGet, "/api/v1/semantic/tags", "")
	if response.Code != http.StatusForbidden {
		t.Fatalf("denied status=%d body=%s", response.Code, response.Body.String())
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/semantic/tags", nil)
	response = httptest.NewRecorder()
	harness.handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d body=%s", response.Code, response.Body.String())
	}
}
