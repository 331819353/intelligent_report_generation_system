package idempotency

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

type memoryRecord struct {
	record    Record
	expiresAt time.Time
}

type memoryRepository struct {
	mu      sync.Mutex
	records map[string]memoryRecord
}

func memoryCoordinate(identity Identity, endpoint, key string) string {
	return identity.TenantID + ":" + identity.ActorID + ":" + endpoint + ":" + key
}

func (repository *memoryRepository) Begin(
	_ context.Context,
	identity Identity,
	endpoint, key, hash string,
	now time.Time,
) (Record, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.records == nil {
		repository.records = map[string]memoryRecord{}
	}
	coordinate := memoryCoordinate(identity, endpoint, key)
	stored, exists := repository.records[coordinate]
	if exists && !now.Before(stored.expiresAt) {
		delete(repository.records, coordinate)
		exists = false
	}
	if !exists {
		repository.records[coordinate] = memoryRecord{
			record: Record{State: StateInFlight, RequestHash: hash}, expiresAt: now.Add(TTL),
		}
		return Record{State: StateAcquired, RequestHash: hash}, nil
	}
	if stored.record.RequestHash != hash {
		stored.record.State = StateReused
		return stored.record, nil
	}
	return stored.record, nil
}

func (repository *memoryRepository) Complete(
	_ context.Context,
	identity Identity,
	endpoint, key, hash string,
	status int,
	body []byte,
) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	coordinate := memoryCoordinate(identity, endpoint, key)
	stored := repository.records[coordinate]
	stored.record = Record{
		State: StateReplay, RequestHash: hash, ResponseStatus: status,
		ResponseBody: append([]byte(nil), body...),
	}
	repository.records[coordinate] = stored
	return nil
}

func (repository *memoryRepository) Release(
	_ context.Context,
	identity Identity,
	endpoint, key, _ string,
) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	delete(repository.records, memoryCoordinate(identity, endpoint, key))
	return nil
}

type identityContextKey struct{}

func testMiddleware(repository Repository, calls *atomic.Int32, next http.Handler) http.Handler {
	if next == nil {
		next = http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			writeTestJSON(writer, http.StatusCreated, map[string]string{"id": "created"})
		})
	}
	return Middleware(MiddlewareOptions{
		Repository: repository,
		ResolveIdentity: func(ctx context.Context) (Identity, error) {
			return ctx.Value(identityContextKey{}).(Identity), nil
		},
		Requires: RequiresGovernedWrite,
		WriteError: func(writer http.ResponseWriter, status int, code, message string) {
			writeTestJSON(writer, status, map[string]string{"code": code, "message": message})
		},
		MaxRequestBytes: 4096,
	}, next)
}

func TestMiddlewareRequiresKeyAndReplaysCanonicalEquivalentBody(t *testing.T) {
	repository := &memoryRepository{}
	identity := testIdentity()
	var calls atomic.Int32
	handler := testMiddleware(repository, &calls, nil)

	missing := httptest.NewRecorder()
	handler.ServeHTTP(missing, testRequest(identity, `{"question":"x"}`, ""))
	if missing.Code != http.StatusBadRequest || !strings.Contains(missing.Body.String(), "IDEMPOTENCY_KEY_REQUIRED") {
		t.Fatalf("missing key = %d/%s", missing.Code, missing.Body.String())
	}

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, testRequest(identity, `{"question":"x","conversationId":"c"}`, "same-key"))
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, testRequest(identity, `{"conversationId":"c","question":"x"}`, "same-key"))
	if first.Code != http.StatusCreated || second.Code != http.StatusCreated || calls.Load() != 1 ||
		second.Header().Get("Idempotency-Replayed") != "true" || first.Body.String() != second.Body.String() {
		t.Fatalf("first=%d/%s second=%d/%s calls=%d", first.Code, first.Body.String(), second.Code, second.Body.String(), calls.Load())
	}
}

func TestMiddlewareRejectsReuseAndConcurrentInFlightRequest(t *testing.T) {
	repository := &memoryRepository{}
	identity := testIdentity()
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	handler := testMiddleware(repository, &calls, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		close(started)
		<-release
		writeTestJSON(writer, http.StatusOK, map[string]bool{"ok": true})
	}))
	first := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(first, testRequest(identity, `{"question":"x"}`, "concurrent-key"))
	}()
	<-started
	concurrent := httptest.NewRecorder()
	handler.ServeHTTP(concurrent, testRequest(identity, `{"question":"x"}`, "concurrent-key"))
	if concurrent.Code != http.StatusConflict ||
		!strings.Contains(concurrent.Body.String(), "IDEMPOTENCY_IN_FLIGHT") || calls.Load() != 1 {
		t.Fatalf("concurrent = %d/%s calls=%d", concurrent.Code, concurrent.Body.String(), calls.Load())
	}
	close(release)
	<-done

	reused := httptest.NewRecorder()
	handler.ServeHTTP(reused, testRequest(identity, `{"question":"different"}`, "concurrent-key"))
	if reused.Code != http.StatusConflict || !strings.Contains(reused.Body.String(), "IDEMPOTENCY_KEY_REUSED") {
		t.Fatalf("reused = %d/%s", reused.Code, reused.Body.String())
	}
}

func TestMiddlewareExpiryAndActorTenantIsolation(t *testing.T) {
	repository := &memoryRepository{}
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	now := base
	var calls atomic.Int32
	handler := Middleware(MiddlewareOptions{
		Repository: repository,
		ResolveIdentity: func(ctx context.Context) (Identity, error) {
			return ctx.Value(identityContextKey{}).(Identity), nil
		},
		Requires: RequiresGovernedWrite,
		WriteError: func(writer http.ResponseWriter, status int, code, message string) {
			writeTestJSON(writer, status, map[string]string{"code": code, "message": message})
		},
		MaxRequestBytes: 4096,
		Now:             func() time.Time { return now },
	}, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writeTestJSON(writer, http.StatusCreated, map[string]bool{"ok": true})
	}))

	identities := []Identity{
		testIdentity(),
		{TenantID: uuid.NewString(), ActorID: uuid.NewString()},
		{},
	}
	identities[2] = Identity{TenantID: identities[0].TenantID, ActorID: uuid.NewString()}
	for _, identity := range identities {
		handler.ServeHTTP(httptest.NewRecorder(), testRequest(identity, `{"question":"x"}`, "isolated-key"))
	}
	if calls.Load() != int32(len(identities)) {
		t.Fatalf("cross-scope calls = %d", calls.Load())
	}

	now = base.Add(TTL)
	handler.ServeHTTP(httptest.NewRecorder(), testRequest(identities[0], `{"question":"x"}`, "isolated-key"))
	if calls.Load() != int32(len(identities)+1) {
		t.Fatalf("expired record did not execute again: %d", calls.Load())
	}
}

func TestCanonicalBodyAndGovernedRouteAllowlist(t *testing.T) {
	left, leftHash, err := CanonicalRequestBody([]byte(`{"b":2,"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	right, rightHash, err := CanonicalRequestBody([]byte(` { "a" : 1, "b" : 2 } `))
	if err != nil || string(left) != string(right) || leftHash != rightHash {
		t.Fatalf("canonical bodies differ: %s/%s %s/%s err=%v", left, leftHash, right, rightHash, err)
	}
	if _, _, err := CanonicalRequestBody([]byte(`{"a":1,"a":2}`)); err == nil {
		t.Fatal("duplicate object key was accepted")
	}

	for _, path := range []string{
		"/api/v1/questions",
		"/api/v1/questions/run-1/clarifications",
		"/api/v1/questions/run-1/feedback",
		"/api/v1/questions/run-1/add-to-report",
		"/api/v1/askdata/semantic/releases/release-1/activate",
		"/api/v1/reports/report-1/operations",
		"/api/v1/reports/report-1/publish",
		"/api/v1/reports/report-1/permissions",
		"/api/v1/reports/report-1/archive",
		"/api/v1/reports/report-1/restore",
		"/api/v1/data-requests",
	} {
		if !RequiresGovernedWrite(httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))) {
			t.Errorf("governed write route not matched: %s", path)
		}
	}
	if !RequiresGovernedWrite(httptest.NewRequest(http.MethodDelete, "/api/v1/reports/report-1/permissions/grant-1", nil)) {
		t.Error("report permission revoke route is not governed")
	}
	for _, path := range []string{
		"/api/v1/questions/run-1/events",
		"/api/v1/data-requests/request-1/submit",
		"/api/v1/reports/report-1/read",
	} {
		if RequiresGovernedWrite(httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))) {
			t.Errorf("non-target route was matched: %s", path)
		}
	}
}

type cleanupStoreFixture struct {
	tenantIDs []string
	tenantID  string
	now       time.Time
	limit     int
	deleted   int
}

func (store *cleanupStoreFixture) TenantIDs(context.Context) ([]string, error) {
	return append([]string(nil), store.tenantIDs...), nil
}

func (store *cleanupStoreFixture) DeleteExpired(
	_ context.Context, tenantID string, now time.Time, limit int,
) (int, error) {
	store.tenantID, store.now, store.limit = tenantID, now, limit
	return store.deleted, nil
}

func TestCleanupWorkerUsesBoundedTenantScopedBatch(t *testing.T) {
	tenantID := uuid.NewString()
	now := time.Date(2026, 8, 10, 1, 2, 3, 0, time.FixedZone("test", 8*60*60))
	store := &cleanupStoreFixture{tenantIDs: []string{tenantID}, deleted: 7}
	worker := &CleanupWorker{store: store}
	tenants, err := worker.TenantIDs(context.Background())
	if err != nil || len(tenants) != 1 || tenants[0] != tenantID {
		t.Fatalf("tenant IDs = %#v, %v", tenants, err)
	}
	deleted, err := worker.ProcessTenant(context.Background(), tenantID, now, 25)
	if err != nil || deleted != 7 || store.tenantID != tenantID ||
		!store.now.Equal(now.UTC()) || store.limit != 25 {
		t.Fatalf("cleanup = %d/%v store=%#v", deleted, err, store)
	}
	if _, err := worker.ProcessTenant(
		context.Background(), tenantID, now, MaxExpiredCleanupBatch+1,
	); err == nil {
		t.Fatal("oversized cleanup batch was accepted")
	}
}

func testIdentity() Identity {
	return Identity{TenantID: uuid.NewString(), ActorID: uuid.NewString()}
}

func testRequest(identity Identity, body, key string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/api/v1/questions", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	return request.WithContext(context.WithValue(request.Context(), identityContextKey{}, identity))
}

func writeTestJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
