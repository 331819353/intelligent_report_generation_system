package reporthttp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"intelligent-report-generation-system/internal/report/compiler"
	"intelligent-report-generation-system/internal/report/publication"
)

func TestWriteReportErrorReturnsPublicationFailureIssues(t *testing.T) {
	recorder := httptest.NewRecorder()
	issues := compiler.ValidationIssues{{
		Code: "REPORT_BINDING_DATASET_NOT_ACTIVE", Path: "dataContexts.sales_context",
		Message: "historical dataset version is no longer active",
	}}
	writeReportError(recorder, &publication.StepError{
		Step: 6, Code: "REPORT_DEPENDENCY_INVALID", Err: issues,
	})
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d", recorder.Code)
	}
	var response struct {
		Code   string                    `json:"code"`
		Issues compiler.ValidationIssues `json:"issues"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Code != "REPORT_DEPENDENCY_INVALID" || len(response.Issues) != 1 || response.Issues[0] != issues[0] {
		t.Fatalf("response = %#v", response)
	}
}
