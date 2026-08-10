package askdatahttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	platformidempotency "intelligent-report-generation-system/internal/platform/idempotency"
)

type memoryIdempotencyRepository struct {
	mu      sync.Mutex
	records map[string]IdempotencyRecord
}

func (repository *memoryIdempotencyRepository) coordinate(identity platformidempotency.Identity, endpoint, key string) string {
	return identity.TenantID + ":" + identity.ActorID + ":" + endpoint + ":" + key
}

func (repository *memoryIdempotencyRepository) Begin(
	_ context.Context, identity platformidempotency.Identity, endpoint, key string,
	hash string, _ time.Time,
) (IdempotencyRecord, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.records == nil {
		repository.records = map[string]IdempotencyRecord{}
	}
	coordinate := repository.coordinate(identity, endpoint, key)
	stored, ok := repository.records[coordinate]
	if !ok {
		stored = IdempotencyRecord{State: IdempotencyAcquired, RequestHash: hash}
		repository.records[coordinate] = IdempotencyRecord{State: IdempotencyInFlight, RequestHash: hash}
		return stored, nil
	}
	if stored.RequestHash != hash {
		stored.State = IdempotencyReused
		return stored, nil
	}
	return stored, nil
}

func (repository *memoryIdempotencyRepository) Complete(
	_ context.Context, identity platformidempotency.Identity, endpoint, key string,
	hash string, status int, body []byte,
) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	repository.records[repository.coordinate(identity, endpoint, key)] = IdempotencyRecord{
		State: IdempotencyReplay, RequestHash: hash, ResponseStatus: status,
		ResponseBody: append([]byte(nil), body...),
	}
	return nil
}

func (repository *memoryIdempotencyRepository) Release(
	_ context.Context, identity platformidempotency.Identity, endpoint, key string, _ string,
) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	delete(repository.records, repository.coordinate(identity, endpoint, key))
	return nil
}

func TestIdempotencyMiddlewareReplaysCanonicalEquivalentRequestOnce(t *testing.T) {
	repository := &memoryIdempotencyRepository{}
	identity := testIdentity()
	var calls atomic.Int32
	handler := idempotencyMiddleware(repository, func(context.Context) (RequestIdentity, error) {
		return identity, nil
	}, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writeJSON(writer, http.StatusCreated, map[string]string{"id": "created"})
	}))

	first := idempotencyRequest(`{"question":"x","conversationId":"c"}`, "same-key")
	firstRecorder := httptest.NewRecorder()
	handler.ServeHTTP(firstRecorder, first)
	second := idempotencyRequest(`{"conversationId":"c","question":"x"}`, "same-key")
	secondRecorder := httptest.NewRecorder()
	handler.ServeHTTP(secondRecorder, second)
	if firstRecorder.Code != http.StatusCreated || secondRecorder.Code != http.StatusCreated ||
		calls.Load() != 1 || secondRecorder.Header().Get("Idempotency-Replayed") != "true" ||
		firstRecorder.Body.String() != secondRecorder.Body.String() {
		t.Fatalf("first=%d/%s second=%d/%s calls=%d", firstRecorder.Code, firstRecorder.Body.String(), secondRecorder.Code, secondRecorder.Body.String(), calls.Load())
	}
}

func TestIdempotencyMiddlewareRejectsReusedAndInFlightKeys(t *testing.T) {
	identity := testIdentity()
	repository := &memoryIdempotencyRepository{}
	handler := idempotencyMiddleware(repository, func(context.Context) (RequestIdentity, error) {
		return identity, nil
	}, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writeJSON(writer, http.StatusOK, map[string]bool{"ok": true})
	}))
	first := idempotencyRequest(`{"question":"x"}`, "reuse")
	handler.ServeHTTP(httptest.NewRecorder(), first)
	reused := httptest.NewRecorder()
	handler.ServeHTTP(reused, idempotencyRequest(`{"question":"y"}`, "reuse"))
	if reused.Code != http.StatusConflict || !containsBodyCode(reused.Body.String(), "IDEMPOTENCY_KEY_REUSED") {
		t.Fatalf("reused response = %d %s", reused.Code, reused.Body.String())
	}

	_, hash, _ := canonicalRequestBody([]byte(`{"question":"z"}`))
	coordinateIdentity := platformidempotency.Identity{
		TenantID: string(identity.TenantID), ActorID: string(identity.ActorID),
	}
	repository.records[repository.coordinate(coordinateIdentity, "POST /api/v1/questions", "flight")] = IdempotencyRecord{
		State: IdempotencyInFlight, RequestHash: string(hash),
	}
	inFlight := httptest.NewRecorder()
	handler.ServeHTTP(inFlight, idempotencyRequest(`{"question":"z"}`, "flight"))
	if inFlight.Code != http.StatusConflict || !containsBodyCode(inFlight.Body.String(), "IDEMPOTENCY_IN_FLIGHT") {
		t.Fatalf("in-flight response = %d %s", inFlight.Code, inFlight.Body.String())
	}
}

func TestIdempotencyMiddlewareReleasesServerFailureForRetry(t *testing.T) {
	repository := &memoryIdempotencyRepository{}
	identity := testIdentity()
	var calls atomic.Int32
	handler := idempotencyMiddleware(repository, func(context.Context) (RequestIdentity, error) {
		return identity, nil
	}, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			writeJSON(writer, http.StatusInternalServerError, map[string]string{"code": "FAILED"})
			return
		}
		writeJSON(writer, http.StatusOK, map[string]bool{"ok": true})
	}))
	failed := httptest.NewRecorder()
	handler.ServeHTTP(failed, idempotencyRequest(`{"question":"x"}`, "retry"))
	retried := httptest.NewRecorder()
	handler.ServeHTTP(retried, idempotencyRequest(`{"question":"x"}`, "retry"))
	if failed.Code != http.StatusInternalServerError || retried.Code != http.StatusOK || calls.Load() != 2 {
		t.Fatalf("failure/retry = %d/%d calls=%d", failed.Code, retried.Code, calls.Load())
	}
}

func idempotencyRequest(body, key string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/api/v1/questions", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", key)
	return request
}

func containsBodyCode(body, code string) bool { return strings.Contains(body, `"code":"`+code+`"`) }
