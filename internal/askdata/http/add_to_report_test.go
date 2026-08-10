package askdatahttp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/orchestrator"
	"intelligent-report-generation-system/internal/askdata/validator"
)

func TestAddToReportRejectsPersistedPartialOutcome(t *testing.T) {
	identity := testIdentity()
	outcome := validator.DetermineOutcome(validator.OutcomeContext{
		MetricAuthorization: &validator.MetricAuthorizationEvidence{
			RequestedCount: 3, AuthorizedCount: 2,
		},
	})
	snapshot := addToReportSnapshot(t, outcome)
	backend := &fakeBackend{getSnapshots: []orchestrator.ReplaySnapshot{snapshot}}
	handler := testHandler(backend, identity)

	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/questions/"+string(snapshot.Run.ID)+"/add-to-report",
		strings.NewReader(`{"reportId":"`+uuid.NewString()+`","runVersion":`+
			jsonInteger(snapshot.Run.RecordVersion)+`}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "add-partial-0001")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusConflict ||
		!strings.Contains(response.Body.String(), `"code":"RESULT_PARTIAL_NOT_EXPORTABLE"`) ||
		!strings.Contains(response.Body.String(), "缩小查询范围") || backend.addToReportCalls != 0 {
		t.Fatalf("response = %d %s, add calls=%d", response.Code, response.Body.String(), backend.addToReportCalls)
	}
}

func TestAddToReportAllowsQualityWarningAndPassesTrustedOutcomeHash(t *testing.T) {
	identity := testIdentity()
	qualityEvidence := askdata.EvidenceRef{
		EvidenceID: "evidence:quality", Kind: askdata.EvidenceKindDataQuality,
		SourceID: "quality:sales", ContentHash: askdata.HashBytes([]byte("quality")),
	}
	ruleEvidence := askdata.EvidenceRef{
		EvidenceID: "evidence:rule", Kind: askdata.EvidenceKindRule,
		SourceID: "rule:freshness", ContentHash: askdata.HashBytes([]byte("rule")),
	}
	quality := validator.QualityEvidence{
		Status: validator.QualityWarning, Evidence: qualityEvidence,
		Checks: []validator.QualityCheckEvidence{{
			Code: "FRESHNESS_NEAR_LIMIT", Severity: validator.RuleWarning,
			Passed: false, Evidence: ruleEvidence,
		}},
	}
	outcome := validator.DetermineOutcome(validator.OutcomeContext{Quality: &quality})
	snapshot := addToReportSnapshot(t, outcome)
	reportID := askdata.ID(uuid.NewString())
	intentID := askdata.ID(uuid.NewString())
	backend := &fakeBackend{
		getSnapshots: []orchestrator.ReplaySnapshot{snapshot},
		addToReportResult: AddToReportResult{
			IntentID: intentID, ReportID: reportID, RunID: snapshot.Run.ID, Status: "PENDING",
		},
	}
	handler := testHandler(backend, identity)
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/questions/"+string(snapshot.Run.ID)+"/add-to-report",
		strings.NewReader(`{"reportId":"`+string(reportID)+`","runVersion":`+
			jsonInteger(snapshot.Run.RecordVersion)+`}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "add-warning-0001")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted || backend.addToReportCalls != 1 ||
		backend.addToReportInput.OutcomeHash != outcome.OutcomeHash ||
		backend.addToReportInput.ReportID != reportID || backend.addToReportIdentity != identity ||
		!strings.Contains(response.Body.String(), string(intentID)) {
		t.Fatalf("response = %d %s, backend=%#v", response.Code, response.Body.String(), backend)
	}
}

func TestAddToReportAllowsNarrativeDegradedStructuredOutcome(t *testing.T) {
	identity := testIdentity()
	outcome := validator.DetermineOutcome(validator.OutcomeContext{})
	snapshot := addToReportSnapshot(t, outcome)
	snapshot.Run.CompletionCode = "ANSWER_DEGRADED"
	var envelope map[string]any
	if err := json.Unmarshal(snapshot.Artifacts[0].Payload, &envelope); err != nil {
		t.Fatal(err)
	}
	envelope["answer"] = map[string]any{"narrativeDegraded": true}
	payload, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Artifacts[0].Payload = payload
	reportID := askdata.ID(uuid.NewString())
	backend := &fakeBackend{
		getSnapshots: []orchestrator.ReplaySnapshot{snapshot},
		addToReportResult: AddToReportResult{
			IntentID: askdata.ID(uuid.NewString()), ReportID: reportID,
			RunID: snapshot.Run.ID, Status: "PENDING",
		},
	}
	handler := testHandler(backend, identity)
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/questions/"+string(snapshot.Run.ID)+"/add-to-report",
		strings.NewReader(`{"reportId":"`+string(reportID)+`","runVersion":`+
			jsonInteger(snapshot.Run.RecordVersion)+`}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "add-degraded-0001")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted || backend.addToReportCalls != 1 ||
		backend.addToReportInput.OutcomeHash != outcome.OutcomeHash {
		t.Fatalf("response = %d %s, backend=%#v", response.Code, response.Body.String(), backend)
	}
}

func TestAddToReportUsesServerArtifactRatherThanClientOutcome(t *testing.T) {
	identity := testIdentity()
	outcome := validator.DetermineOutcome(validator.OutcomeContext{
		RowLimit: &validator.RowLimitEvidence{Limit: 100, ReturnedRows: 100, Truncated: true},
	})
	snapshot := addToReportSnapshot(t, outcome)
	backend := &fakeBackend{getSnapshots: []orchestrator.ReplaySnapshot{snapshot}}
	handler := testHandler(backend, identity)
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/questions/"+string(snapshot.Run.ID)+"/add-to-report",
		strings.NewReader(`{"reportId":"`+uuid.NewString()+`","runVersion":`+
			jsonInteger(snapshot.Run.RecordVersion)+`,"outcome":{"status":"ANSWERED"}}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "client-outcome-0001")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest || backend.getCalls != 0 || backend.addToReportCalls != 0 {
		t.Fatalf("response = %d %s, get/add=%d/%d", response.Code, response.Body.String(), backend.getCalls, backend.addToReportCalls)
	}
}

func TestValidateAddToReportOutcomeBlocksOnlyPartial(t *testing.T) {
	partial := validator.DetermineOutcome(validator.OutcomeContext{
		MemberPolicy: &validator.MemberPolicyEvidence{EvaluatedCount: 4, FilteredCount: 1},
	})
	answered := validator.DetermineOutcome(validator.OutcomeContext{})
	if err := ValidateAddToReportOutcome(partial); err != ErrPartialResultNotExportable {
		t.Fatalf("partial error = %v", err)
	}
	if err := ValidateAddToReportOutcome(answered); err != nil {
		t.Fatalf("answered error = %v", err)
	}
}

func addToReportSnapshot(t *testing.T, outcome validator.Outcome) orchestrator.ReplaySnapshot {
	t.Helper()
	if outcome.Validate() != nil {
		t.Fatalf("invalid outcome fixture: %#v", outcome)
	}
	payload, err := json.Marshal(struct {
		Outcome validator.Outcome `json:"outcome"`
	}{Outcome: outcome})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := testSnapshot(orchestrator.StateAnswered, 12)
	snapshot.Run.Disposition = orchestrator.DispositionDirect
	snapshot.Run.CompletionCode = "ANSWER_READY"
	snapshot.Run.CompletionArtifact = askdata.HashBytes([]byte("answer-with-outcome"))
	snapshot.Artifacts = []orchestrator.Artifact{{
		Hash: snapshot.Run.CompletionArtifact, Type: orchestrator.ArtifactAnswer, Payload: payload,
	}}
	return snapshot
}

func jsonInteger(value int64) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}
