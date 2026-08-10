package askdatahttp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	askdataobservability "intelligent-report-generation-system/internal/askdata/observability"
)

func TestQuotaExceededResponseIncludesRemainingRestoreAndRequestEntry(t *testing.T) {
	reset := time.Now().UTC().Add(15 * time.Minute)
	response := httptest.NewRecorder()
	writeServiceError(response, &QuestionQuotaExceededError{Decision: askdataobservability.QuotaDecision{
		Status: askdataobservability.QuotaExceeded, Allowed: false,
		Limiters: []askdataobservability.QuotaLimiter{{
			Scope: askdataobservability.QuotaScopeDomain, ScopeID: askdata.ID("domain-a"),
			Period: askdataobservability.QuotaPeriodDay, Dimension: askdataobservability.QuotaDimensionRuns,
			Used: 100, Limit: 100, Remaining: 0, PercentUsed: 100, ResetAt: reset, Exceeded: true,
		}},
	}})
	if response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") == "" ||
		!strings.Contains(response.Body.String(), `"code":"QUOTA_EXCEEDED"`) ||
		!strings.Contains(response.Body.String(), `"remaining":0`) ||
		!strings.Contains(response.Body.String(), `"requestPath":"/api/v1/data-requests"`) ||
		!strings.Contains(response.Body.String(), `"restoreAt"`) {
		t.Fatalf("quota response=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
}
