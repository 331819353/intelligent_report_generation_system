package metadataai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	aiplatform "intelligent-report-generation-system/internal/ai"
)

func TestWriteServiceErrorMapsTenantPolicyAndQuota(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"租户未授权", aiplatform.ErrTenantAIForbidden, http.StatusForbidden, "AI_TENANT_FORBIDDEN"},
		{"租户配额耗尽", aiplatform.ErrQuotaExceeded, http.StatusTooManyRequests, "AI_QUOTA_EXCEEDED"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			writeServiceError(response, test.err, GenerateResult{Job: Job{ID: "job-1", Status: "FAILED"}})
			if response.Code != test.wantStatus {
				t.Fatalf("status=%d", response.Code)
			}
			var body struct {
				Code string `json:"code"`
				Job  Job    `json:"job"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Code != test.wantCode || body.Job.ID != "job-1" {
				t.Fatalf("body=%s", response.Body.String())
			}
		})
	}
}
