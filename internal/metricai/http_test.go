package metricai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/access"
	aiplatform "intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/auth"
)

const metricAIHTTPSessionID = "metric-ai-http-session"

type metricAIHTTPAuthStore struct {
	user    auth.LoginUser
	session auth.Session
}

func (s *metricAIHTTPAuthStore) FindTenantID(context.Context, string) (string, error) {
	return s.user.TenantID, nil
}
func (s *metricAIHTTPAuthStore) FindUserByEmail(context.Context, string, string) (auth.LoginUser, error) {
	return s.user, nil
}
func (s *metricAIHTTPAuthStore) FindUserByID(context.Context, string, string) (auth.LoginUser, error) {
	return s.user, nil
}
func (s *metricAIHTTPAuthStore) CreateSession(_ context.Context, session auth.Session, _, _ string) error {
	s.session = session
	return nil
}
func (s *metricAIHTTPAuthStore) FindSession(context.Context, string, string) (auth.Session, error) {
	return s.session, nil
}
func (*metricAIHTTPAuthStore) RotateSession(context.Context, string, string, []byte, []byte, time.Time) error {
	return nil
}
func (*metricAIHTTPAuthStore) RevokeSession(context.Context, string, string, []byte, string) error {
	return nil
}
func (*metricAIHTTPAuthStore) RecordLoginFailure(context.Context, string, string, string, string, string, string) {
}

type metricAIHTTPPermissionStore struct {
	allowed bool
	checks  []access.Check
}

func (s *metricAIHTTPPermissionStore) Allowed(_ context.Context, check access.Check) (bool, error) {
	s.checks = append(s.checks, check)
	return s.allowed, nil
}

type proposerStub struct {
	result            ProposalResult
	err               error
	calls             int
	tenantID, actorID string
	request           AuthoringRequest
}

func (s *proposerStub) Propose(_ context.Context, tenantID, actorID string, request AuthoringRequest) (ProposalResult, error) {
	s.calls++
	s.tenantID, s.actorID, s.request = tenantID, actorID, request
	return s.result, s.err
}

type metricAIHTTPHarness struct {
	handler     http.Handler
	token       string
	permissions *metricAIHTTPPermissionStore
}

func newMetricAIHTTPHarness(t *testing.T, proposer Proposer, allowed bool) metricAIHTTPHarness {
	t.Helper()
	tokens := auth.NewTokenManager("metric-ai-http-test", "01234567890123456789012345678901", time.Hour)
	token, _, err := tokens.Issue(testActorID, testTenantID, metricAIHTTPSessionID, 1)
	if err != nil {
		t.Fatal(err)
	}
	authStore := &metricAIHTTPAuthStore{
		user: auth.LoginUser{ID: testActorID, TenantID: testTenantID, Status: auth.UserStatusActive, TokenVersion: 1},
		session: auth.Session{
			ID: metricAIHTTPSessionID, TenantID: testTenantID, UserID: testActorID,
			TokenVersion: 1, UserStatus: auth.UserStatusActive, ExpiresAt: time.Now().UTC().Add(time.Hour),
		},
	}
	authService := auth.NewService(authStore, auth.NewPasswordManager(4), tokens, time.Hour)
	permissionStore := &metricAIHTTPPermissionStore{allowed: allowed}
	return metricAIHTTPHarness{
		handler: NewHandler(authService, access.NewService(permissionStore), proposer),
		token:   token, permissions: permissionStore,
	}
}

func metricAIHTTPRequest(t *testing.T, harness metricAIHTTPHarness, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, "/api/v1/metrics/ai/proposals", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+harness.token)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	harness.handler.ServeHTTP(response, request)
	return response
}

func TestMetricAIHTTPReturnsReviewOnlyProposalWithManageFence(t *testing.T) {
	proposer := &proposerStub{result: ProposalResult{
		RequestID: "request-1", RetrievalContextHash: strings.Repeat("a", 64),
		Proposal: MetricAuthoringProposal{
			SchemaVersion: SchemaVersion, Strategy: StrategyDataGap, Summary: "没有足够数据",
			RetrievalEvidence: []RetrievalEvidence{}, ClarificationQuestions: []string{}, Assumptions: []string{}, Warnings: []string{},
		},
	}}
	harness := newMetricAIHTTPHarness(t, proposer, true)
	response := metricAIHTTPRequest(t, harness, http.MethodPost, `{"requirement":"创建销售额指标，汇总已支付订单金额，按支付时间统计到月"}`)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d cache=%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	}
	if proposer.calls != 1 || proposer.tenantID != testTenantID || proposer.actorID != testActorID || proposer.request != validRequest() {
		t.Fatalf("proposer call = %#v", proposer)
	}
	if len(harness.permissions.checks) != 1 {
		t.Fatalf("permission checks = %#v", harness.permissions.checks)
	}
	check := harness.permissions.checks[0]
	if check.ResourceType != "METRIC" || check.Action != "MANAGE" || check.ObjectID != "" {
		t.Fatalf("permission check = %#v", check)
	}
	var body ProposalResult
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body.Proposal.Strategy != StrategyDataGap {
		t.Fatalf("response body = %s, error = %v", response.Body.String(), err)
	}
}

func TestMetricAIHTTPRejectsUnauthorizedAndAmbiguousRequests(t *testing.T) {
	proposer := &proposerStub{}
	denied := newMetricAIHTTPHarness(t, proposer, false)
	response := metricAIHTTPRequest(t, denied, http.MethodPost, `{"requirement":"创建销售额指标，汇总金额并按月统计"}`)
	if response.Code != http.StatusForbidden || proposer.calls != 0 {
		t.Fatalf("denied response=%d body=%s calls=%d", response.Code, response.Body.String(), proposer.calls)
	}

	allowed := newMetricAIHTTPHarness(t, proposer, true)
	for name, body := range map[string]string{
		"unknown field": `{"requirement":"创建销售额指标","save":true}`,
		"trailing json": `{"requirement":"创建销售额指标"}{}`,
		"duplicate key": `{"requirement":"创建销售额指标","requirement":"创建利润指标"}`,
	} {
		t.Run(name, func(t *testing.T) {
			response := metricAIHTTPRequest(t, allowed, http.MethodPost, body)
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "METRIC_AI_REQUEST_INVALID") {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
	if proposer.calls != 0 {
		t.Fatalf("invalid request reached proposer %d times", proposer.calls)
	}
}

func TestMetricAIHTTPMapsStableReviewOnlyErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
		code string
	}{
		{"invalid request", ErrInvalidRequest, http.StatusBadRequest, "METRIC_AI_REQUEST_INVALID"},
		{"tenant forbidden", aiplatform.ErrTenantAIForbidden, http.StatusForbidden, "AI_TENANT_FORBIDDEN"},
		{"quota", aiplatform.ErrQuotaExceeded, http.StatusTooManyRequests, "AI_QUOTA_EXCEEDED"},
		{"provider unavailable", ErrProviderUnavailable, http.StatusServiceUnavailable, "AI_PROVIDER_UNAVAILABLE"},
		{"timeout", context.DeadlineExceeded, http.StatusGatewayTimeout, "AI_TIMEOUT"},
		{"invalid output", ErrInvalidOutput, http.StatusBadGateway, "METRIC_AI_INVALID_OUTPUT"},
		{"context", ErrInvalidRetrievalContext, http.StatusInternalServerError, "METRIC_AI_CONTEXT_INVALID"},
		{"retrieval", errors.New("database failed"), http.StatusInternalServerError, "METRIC_AI_RETRIEVAL_FAILED"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			writeAuthoringError(response, test.err)
			if response.Code != test.want || !strings.Contains(response.Body.String(), test.code) || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if test.code == "AI_TENANT_FORBIDDEN" && !strings.Contains(response.Body.String(), "当前租户未启用通用 AI 能力，或当前账号不可用") {
				t.Fatalf("metric authoring still exposes a per-purpose opt-in message: %s", response.Body.String())
			}
		})
	}
}
