package datasource

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/access"
	"intelligent-report-generation-system/internal/auth"
)

const (
	dataSourceHTTPActorID  = "22222222-2222-4222-8222-222222222222"
	dataSourceHTPTenantID  = "11111111-1111-4111-8111-111111111111"
	dataSourceHTTPSession  = "data-source-http-session"
	dataSourceHTTPSourceID = "33333333-3333-4333-8333-333333333333"
)

type dataSourceHTTPAuthStore struct {
	user    auth.LoginUser
	session auth.Session
}

func (s *dataSourceHTTPAuthStore) FindTenantID(context.Context, string) (string, error) {
	return s.user.TenantID, nil
}
func (s *dataSourceHTTPAuthStore) FindUserByEmail(context.Context, string, string) (auth.LoginUser, error) {
	return s.user, nil
}
func (s *dataSourceHTTPAuthStore) FindUserByID(context.Context, string, string) (auth.LoginUser, error) {
	return s.user, nil
}
func (s *dataSourceHTTPAuthStore) CreateSession(_ context.Context, session auth.Session, _, _ string) error {
	s.session = session
	return nil
}
func (s *dataSourceHTTPAuthStore) FindSession(context.Context, string, string) (auth.Session, error) {
	return s.session, nil
}
func (*dataSourceHTTPAuthStore) RotateSession(context.Context, string, string, []byte, []byte, time.Time) error {
	return nil
}
func (*dataSourceHTTPAuthStore) RevokeSession(context.Context, string, string, []byte, string) error {
	return nil
}
func (*dataSourceHTTPAuthStore) RecordLoginFailure(context.Context, string, string, string, string, string, string) {
}

type dataSourceHTTPPermissionStore struct{}

func (dataSourceHTTPPermissionStore) Allowed(context.Context, access.Check) (bool, error) {
	return true, nil
}

func dataSourceHTTPHarness(t *testing.T, service *Service) (http.Handler, string) {
	t.Helper()
	tokens := auth.NewTokenManager("data-source-http-test", "01234567890123456789012345678901", time.Hour)
	token, _, err := tokens.Issue(dataSourceHTTPActorID, dataSourceHTPTenantID, dataSourceHTTPSession, 1)
	if err != nil {
		t.Fatal(err)
	}
	store := &dataSourceHTTPAuthStore{
		user: auth.LoginUser{
			ID: dataSourceHTTPActorID, TenantID: dataSourceHTPTenantID,
			Status: auth.UserStatusActive, TokenVersion: 1,
		},
		session: auth.Session{
			ID: dataSourceHTTPSession, TenantID: dataSourceHTPTenantID, UserID: dataSourceHTTPActorID,
			UserStatus: auth.UserStatusActive, TokenVersion: 1, ExpiresAt: time.Now().Add(time.Hour),
		},
	}
	authService := auth.NewService(store, auth.NewPasswordManager(4), tokens, time.Hour)
	return NewHandler(authService, access.NewService(dataSourceHTTPPermissionStore{}), service), token
}

func performDataSourcePublish(t *testing.T, handler http.Handler, token string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/data-sources/"+dataSourceHTTPSourceID+"/publish", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestDataSourceHTTPCreateDoesNotRequireExpectedVersion(t *testing.T) {
	service := NewService(&repo{quota: Quota{MaxDataSources: 10}})
	handler, token := dataSourceHTTPHarness(t, service)
	body := strings.NewReader(`{
		"code":"sales_file",
		"name":"Sales",
		"type":"EXCEL",
		"fileAssetId":"44444444-4444-4444-8444-444444444444"
	}`)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/data-sources", body)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestDataSourceHTTPUpdateRequiresExpectedVersion(t *testing.T) {
	service := NewService(&repo{quota: Quota{MaxDataSources: 10}})
	handler, token := dataSourceHTTPHarness(t, service)
	body := strings.NewReader(`{
		"code":"sales_file",
		"name":"Sales",
		"type":"EXCEL",
		"fileAssetId":"44444444-4444-4444-8444-444444444444"
	}`)
	request := httptest.NewRequest(http.MethodPut, "/api/v1/data-sources/"+dataSourceHTTPSourceID, body)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest ||
		!strings.Contains(response.Body.String(), "DATA_SOURCE_EXPECTED_VERSION_REQUIRED") {
		t.Fatalf("update status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestDataSourceHTTPRejectsClientSuppliedSecretReference(t *testing.T) {
	service := NewService(&repo{quota: Quota{MaxDataSources: 10}})
	handler, token := dataSourceHTTPHarness(t, service)
	for _, requestCase := range []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{
			name: "create", method: http.MethodPost, path: "/api/v1/data-sources",
			body: `{"code":"sales","name":"Sales","type":"MYSQL","secretRef":"env://TENANT_A_DB"}`,
		},
		{
			name: "update", method: http.MethodPut,
			path: "/api/v1/data-sources/" + dataSourceHTTPSourceID,
			body: `{"code":"sales","name":"Sales","type":"MYSQL","expectedVersion":1,"secretRef":"env://TENANT_A_DB"}`,
		},
	} {
		t.Run(requestCase.name, func(t *testing.T) {
			request := httptest.NewRequest(requestCase.method, requestCase.path, strings.NewReader(requestCase.body))
			request.Header.Set("Authorization", "Bearer "+token)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != http.StatusBadRequest ||
				!strings.Contains(response.Body.String(), "INVALID_REQUEST") {
				t.Fatalf("%s status=%d body=%s", requestCase.name, response.Code, response.Body.String())
			}
		})
	}
}

func TestDataSourceHTTPPublishReturnsVersionedSourceWithoutCredential(t *testing.T) {
	now := time.Date(2026, 7, 24, 8, 0, 0, 0, time.UTC)
	expiresAt := now.Add(30 * time.Minute)
	draft := Source{
		ID: dataSourceHTTPSourceID, TenantID: dataSourceHTPTenantID, Code: "sales", Name: "Sales",
		Type: TypeMySQL, Status: StatusDraft, Config: map[string]any{"host": "db.internal"},
		SecretRef: "encrypted://must-never-leak", ConfigVersionID: "44444444-4444-4444-8444-444444444444",
		ConfigVersion: 1, ValidationStatus: ValidationPassed, PublicationStatus: PublicationUnpublished,
		TestExpiresAt: &expiresAt,
	}
	draft.ConfigHash, _ = sourceConfigurationHash(draft)
	repository := &versionedRepo{
		repo:  repo{quota: Quota{MaxDataSources: 10}},
		draft: draft,
		testRuns: []ConnectionTestRun{{
			ConfigVersion: draft.ConfigVersionID, ConfigHash: draft.ConfigHash,
			Status: ValidationPassed, ExpiresAt: &expiresAt,
		}},
	}
	service := NewService(repository)
	service.now = func() time.Time { return now }
	handler, token := dataSourceHTTPHarness(t, service)

	response := performDataSourcePublish(t, handler, token)
	if response.Code != http.StatusOK {
		t.Fatalf("publish status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "must-never-leak") || strings.Contains(response.Body.String(), "secretRef") {
		t.Fatalf("publication response leaked credential: %s", response.Body.String())
	}
	var published Source
	if err := json.Unmarshal(response.Body.Bytes(), &published); err != nil {
		t.Fatal(err)
	}
	if published.PublicationStatus != PublicationPublished || published.PublishedVersionID != draft.ConfigVersionID {
		t.Fatalf("published=%#v", published)
	}
}

func TestDataSourceHTTPPublishMapsMissingTestToConflict(t *testing.T) {
	draft := Source{
		ID: dataSourceHTTPSourceID, TenantID: dataSourceHTPTenantID, Code: "sales", Name: "Sales",
		Type: TypeMySQL, Status: StatusDraft, Config: map[string]any{"host": "db.internal"},
		SecretRef: "encrypted://internal", ConfigVersionID: "44444444-4444-4444-8444-444444444444",
		ConfigVersion: 1, ValidationStatus: ValidationUntested,
	}
	draft.ConfigHash, _ = sourceConfigurationHash(draft)
	service := NewService(&versionedRepo{repo: repo{quota: Quota{MaxDataSources: 10}}, draft: draft})
	handler, token := dataSourceHTTPHarness(t, service)

	response := performDataSourcePublish(t, handler, token)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "DATA_SOURCE_TEST_REQUIRED") {
		t.Fatalf("publish status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestDataSourceHTTPUpdateRejectsStaleExpectedVersion(t *testing.T) {
	draft := Source{
		ID: dataSourceHTTPSourceID, TenantID: dataSourceHTPTenantID, Code: "sales_file", Name: "Sales",
		Type: TypeExcel, Status: StatusDraft, FileAssetID: "44444444-4444-4444-8444-444444444444",
		ConfigVersionID: "55555555-5555-4555-8555-555555555555", ConfigVersion: 1,
		ValidationStatus: ValidationUntested, PublicationStatus: PublicationUnpublished, Version: 4,
	}
	service := NewService(&versionedRepo{repo: repo{quota: Quota{MaxDataSources: 10}}, draft: draft})
	handler, token := dataSourceHTTPHarness(t, service)
	body := strings.NewReader(`{
		"code":"sales_file",
		"name":"Sales",
		"type":"EXCEL",
		"fileAssetId":"44444444-4444-4444-8444-444444444444",
		"expectedVersion":3
	}`)
	request := httptest.NewRequest(http.MethodPut, "/api/v1/data-sources/"+dataSourceHTTPSourceID, body)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "DATA_SOURCE_VERSION_CONFLICT") {
		t.Fatalf("update status=%d body=%s", response.Code, response.Body.String())
	}
}
