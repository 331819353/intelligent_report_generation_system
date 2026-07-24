package datasource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeConnectionTestJobRepository struct {
	job                ConnectionTestJob
	latest             *ConnectionTestJob
	tenantID           string
	sourceID           string
	requestedBy        string
	idempotencyKeyHash string
}

func (r *fakeConnectionTestJobRepository) EnqueueConnectionTest(
	_ context.Context,
	tenantID, sourceID, requestedBy, idempotencyKeyHash string,
) (ConnectionTestJob, error) {
	r.tenantID = tenantID
	r.sourceID = sourceID
	r.requestedBy = requestedBy
	r.idempotencyKeyHash = idempotencyKeyHash
	return r.job, nil
}

func (r *fakeConnectionTestJobRepository) GetConnectionTest(
	context.Context, string, string, string,
) (ConnectionTestJob, error) {
	if r.job.ID == "" {
		return ConnectionTestJob{}, ErrConnectionTestNotFound
	}
	return r.job, nil
}

func (r *fakeConnectionTestJobRepository) LatestConnectionTest(
	context.Context, string, string, string, string,
) (*ConnectionTestJob, error) {
	return r.latest, nil
}

func TestQueueConnectionTestHashesIdempotencyKey(t *testing.T) {
	repository := &fakeConnectionTestJobRepository{job: ConnectionTestJob{
		ID: "job-1", DataSourceID: "source-1",
		ConfigVersionID: "version-1", Status: ConnectionTestQueued,
	}}
	service := NewService(&repo{})
	service.SetConnectionTestJobRepository(repository)

	job, err := service.QueueConnectionTest(
		context.Background(), "tenant-1", "actor-1", "source-1",
		"browser-request-1",
	)
	if err != nil || job.ID != "job-1" {
		t.Fatalf("job=%#v err=%v", job, err)
	}
	sum := sha256.Sum256([]byte("browser-request-1"))
	if repository.idempotencyKeyHash != hex.EncodeToString(sum[:]) {
		t.Fatalf("idempotency hash=%q", repository.idempotencyKeyHash)
	}
	if strings.Contains(repository.idempotencyKeyHash, "browser-request") {
		t.Fatal("raw idempotency key reached persistence")
	}
}

func TestQueueConnectionTestRejectsOversizedIdempotencyKey(t *testing.T) {
	service := NewService(&repo{})
	service.SetConnectionTestJobRepository(&fakeConnectionTestJobRepository{})
	if _, err := service.QueueConnectionTest(
		context.Background(), "tenant-1", "actor-1", "source-1",
		strings.Repeat("x", 257),
	); err != ErrIdempotencyKeyInvalid {
		t.Fatalf("error=%v", err)
	}
}

func TestDataSourceHTTPConnectionTestReturnsAcceptedAndCanBePolled(t *testing.T) {
	requestedAt := time.Date(2026, 7, 24, 1, 0, 0, 0, time.UTC)
	repository := &fakeConnectionTestJobRepository{job: ConnectionTestJob{
		ID:              "55555555-5555-4555-8555-555555555555",
		DataSourceID:    dataSourceHTTPSourceID,
		ConfigVersionID: "44444444-4444-4444-8444-444444444444",
		Status:          ConnectionTestQueued, RequestedAt: requestedAt,
	}}
	service := NewService(&repo{})
	service.SetConnectionTestJobRepository(repository)
	handler, token := dataSourceHTTPHarness(t, service)

	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/data-sources/"+dataSourceHTTPSourceID+"/test",
		strings.NewReader(`{}`),
	)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Idempotency-Key", "request-key-1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("enqueue status=%d body=%s", response.Code, response.Body.String())
	}
	expectedLocation := "/api/v1/data-sources/" + dataSourceHTTPSourceID +
		"/connection-tests/" + repository.job.ID
	if response.Header().Get("Location") != expectedLocation {
		t.Fatalf("location=%q", response.Header().Get("Location"))
	}
	if repository.tenantID != dataSourceHTPTenantID ||
		repository.requestedBy != dataSourceHTTPActorID {
		t.Fatalf("enqueue identity tenant=%s actor=%s", repository.tenantID, repository.requestedBy)
	}
	if strings.Contains(response.Body.String(), "configHash") ||
		strings.Contains(response.Body.String(), "lease") {
		t.Fatalf("enqueue response leaked protected fields: %s", response.Body.String())
	}

	pollRequest := httptest.NewRequest(http.MethodGet, expectedLocation, nil)
	pollRequest.Header.Set("Authorization", "Bearer "+token)
	pollResponse := httptest.NewRecorder()
	handler.ServeHTTP(pollResponse, pollRequest)
	if pollResponse.Code != http.StatusOK ||
		!strings.Contains(pollResponse.Body.String(), `"status":"QUEUED"`) {
		t.Fatalf("poll status=%d body=%s", pollResponse.Code, pollResponse.Body.String())
	}
}

func TestDataSourceHTTPPublishMapsActiveConnectionTestToPending(t *testing.T) {
	draft := Source{
		ID: dataSourceHTTPSourceID, TenantID: dataSourceHTPTenantID,
		Code: "sales", Name: "Sales", Type: TypeMySQL, Status: StatusDraft,
		SecretRef:       "encrypted://internal",
		ConfigVersionID: "44444444-4444-4444-8444-444444444444",
		ConfigVersion:   1, ValidationStatus: ValidationUntested,
	}
	draft.ConfigHash, _ = sourceConfigurationHash(draft)
	service := NewService(&versionedRepo{
		repo: repo{quota: Quota{MaxDataSources: 10}}, draft: draft,
	})
	service.SetConnectionTestJobRepository(&fakeConnectionTestJobRepository{
		latest: &ConnectionTestJob{
			ID: "job-1", DataSourceID: draft.ID,
			ConfigVersionID: draft.ConfigVersionID,
			Status:          ConnectionTestRunning,
		},
	})
	handler, token := dataSourceHTTPHarness(t, service)

	response := performDataSourcePublish(t, handler, token)
	if response.Code != http.StatusConflict ||
		!strings.Contains(response.Body.String(), "DATA_SOURCE_TEST_PENDING") {
		t.Fatalf("publish status=%d body=%s", response.Code, response.Body.String())
	}
}
