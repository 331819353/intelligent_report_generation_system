package reporthttp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	platformidempotency "intelligent-report-generation-system/internal/platform/idempotency"
	"intelligent-report-generation-system/internal/report/operation"
	"intelligent-report-generation-system/internal/report/store"
)

type reportIdempotencyRepository struct{}

func (reportIdempotencyRepository) Begin(
	context.Context, platformidempotency.Identity, string, string, string, time.Time,
) (platformidempotency.Record, error) {
	return platformidempotency.Record{State: platformidempotency.StateAcquired}, nil
}

func TestWriteReportErrorIncludesConflictAndApplyDetails(t *testing.T) {
	t.Run("revision conflict", func(t *testing.T) {
		response := httptest.NewRecorder()
		writeReportError(response, &store.RevisionConflict{Expected: 3, Current: 5, Summaries: []string{"r4:PAGE_UPDATE", "r5:BLOCK_MOVE"}})
		var body map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if response.Code != http.StatusConflict || body["currentRevision"] != float64(5) ||
			len(body["operationSummaries"].([]any)) != 2 {
			t.Fatalf("conflict response = %d/%#v", response.Code, body)
		}
	})

	t.Run("operation failure", func(t *testing.T) {
		response := httptest.NewRecorder()
		writeReportError(response, &operation.ApplyError{Index: 7, Code: "REPORT_OPERATION_APPLY_FAILED", Message: "missing target"})
		var body map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if response.Code != http.StatusUnprocessableEntity || body["operationIndex"] != float64(7) {
			t.Fatalf("apply response = %d/%#v", response.Code, body)
		}
	})

	t.Run("AI scope guard", func(t *testing.T) {
		response := httptest.NewRecorder()
		guardError := &operation.GuardError{Code: operation.CodeOutOfScope, Message: "outside scope"}
		if !shouldAuditAIRejection(guardError) {
			t.Fatal("AI guard rejection was not selected for audit")
		}
		writeReportError(response, guardError)
		if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), operation.CodeOutOfScope) {
			t.Fatalf("guard response = %d/%s", response.Code, response.Body.String())
		}
	})

	if shouldAuditAIRejection(store.ErrAIEditForbidden) ||
		shouldAuditAIRejection(&store.RevisionConflict{Expected: 1, Current: 2}) {
		t.Fatal("authorization/conflict must not be recorded as rejected AI output")
	}
}

func (reportIdempotencyRepository) Complete(
	context.Context, platformidempotency.Identity, string, string, string, int, []byte,
) error {
	return nil
}

func (reportIdempotencyRepository) Release(
	context.Context, platformidempotency.Identity, string, string, string,
) error {
	return nil
}

func TestReportMutationUsesSharedIdempotencyMiddleware(t *testing.T) {
	identity := platformidempotency.Identity{TenantID: uuid.NewString(), ActorID: uuid.NewString()}
	handler := WithIdempotency(
		reportIdempotencyRepository{},
		func(context.Context) (platformidempotency.Identity, error) { return identity, nil },
		http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write([]byte(`{"revisionNo":2}`))
		}),
	)
	request := httptest.NewRequest(
		http.MethodPost, "/api/v1/reports/"+uuid.NewString()+"/operations", strings.NewReader(`{}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "IDEMPOTENCY_KEY_REQUIRED") {
		t.Fatalf("report idempotency response = %d/%s", response.Code, response.Body.String())
	}
}
