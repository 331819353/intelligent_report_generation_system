package semanticqa

import (
	"net/http"
	"testing"

	aiplatform "intelligent-report-generation-system/internal/ai"
)

func TestResponseErrorKeepsEvidenceLoopFailureStable(t *testing.T) {
	status, code, _ := responseError(&aiplatform.ProviderError{
		Code: aiplatform.ErrorCodeToolNoProgress,
	})
	if status != http.StatusBadGateway ||
		code != string(aiplatform.ErrorCodeToolNoProgress) {
		t.Fatalf("status=%d code=%q", status, code)
	}
}
