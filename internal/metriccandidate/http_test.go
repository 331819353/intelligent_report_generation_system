package metriccandidate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/access"
	"intelligent-report-generation-system/internal/auth"
	"intelligent-report-generation-system/internal/metric"
)

const candidateHTTPSessionID = "metric-candidate-http-session"

type candidateHTTPAuthStore struct {
	user    auth.LoginUser
	session auth.Session
}

func (store *candidateHTTPAuthStore) FindTenantID(context.Context, string) (string, error) {
	return store.user.TenantID, nil
}

func (store *candidateHTTPAuthStore) FindUserByEmail(context.Context, string, string) (auth.LoginUser, error) {
	return store.user, nil
}

func (store *candidateHTTPAuthStore) FindUserByID(context.Context, string, string) (auth.LoginUser, error) {
	return store.user, nil
}

func (store *candidateHTTPAuthStore) CreateSession(_ context.Context, session auth.Session, _, _ string) error {
	store.session = session
	return nil
}

func (store *candidateHTTPAuthStore) FindSession(context.Context, string, string) (auth.Session, error) {
	return store.session, nil
}

func (*candidateHTTPAuthStore) RotateSession(context.Context, string, string, []byte, []byte, time.Time) error {
	return nil
}

func (*candidateHTTPAuthStore) RevokeSession(context.Context, string, string, []byte, string) error {
	return nil
}

func (*candidateHTTPAuthStore) RecordLoginFailure(context.Context, string, string, string, string, string, string) {
}

type candidateHTTPPermissionStore struct {
	allow        bool
	denyResource string
	checks       []access.Check
}

func (store *candidateHTTPPermissionStore) Allowed(_ context.Context, check access.Check) (bool, error) {
	store.checks = append(store.checks, check)
	if check.ResourceType == store.denyResource {
		return false, nil
	}
	return store.allow, nil
}

type candidateHTTPHarness struct {
	handler     http.Handler
	token       string
	permissions *candidateHTTPPermissionStore
}

func newCandidateHTTPHarness(t *testing.T, store Store, creator MetricCreator) candidateHTTPHarness {
	t.Helper()
	tokens := auth.NewTokenManager("metric-candidate-http-test", "01234567890123456789012345678901", time.Hour)
	token, _, err := tokens.Issue(testActorID, testTenantID, candidateHTTPSessionID, 1)
	if err != nil {
		t.Fatal(err)
	}
	authStore := &candidateHTTPAuthStore{
		user: auth.LoginUser{ID: testActorID, TenantID: testTenantID, Status: auth.UserStatusActive, TokenVersion: 1},
		session: auth.Session{
			ID: candidateHTTPSessionID, TenantID: testTenantID, UserID: testActorID,
			TokenVersion: 1, UserStatus: auth.UserStatusActive, ExpiresAt: time.Now().UTC().Add(time.Hour),
		},
	}
	authService := auth.NewService(authStore, auth.NewPasswordManager(4), tokens, time.Hour)
	permissionStore := &candidateHTTPPermissionStore{allow: true}
	permissionService := access.NewService(permissionStore)
	service := NewService(store, creator)
	return candidateHTTPHarness{
		handler: NewHandler(authService, permissionService, service), token: token,
		permissions: permissionStore,
	}
}

func candidateHTTPRequest(t *testing.T, harness candidateHTTPHarness, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+harness.token)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	harness.handler.ServeHTTP(response, request)
	return response
}

func TestCandidateHTTPAcceptReturnsStrictCandidateAndMetricContract(t *testing.T) {
	reviewable := reviewCandidate(t, CandidateStatusReady)
	accepted := reviewable
	accepted.Status = CandidateStatusAccepted
	accepted.AcceptedMetricID = testMetricID
	accepted.Version++
	record := metric.Record{
		ID: testMetricID, Code: reviewable.Code, Name: reviewable.Name, Status: "DRAFT",
		DatasetID: reviewable.DatasetID, DatasetVersionID: reviewable.DatasetVersionID,
		Definition: reviewable.ProposedDefinition,
	}
	getCalls := 0
	store := &candidateStoreStub{getFn: func(context.Context, string, string) (Candidate, error) {
		getCalls++
		if getCalls == 1 {
			return reviewable, nil
		}
		return accepted, nil
	}}
	creator := &metricCreatorStub{createFn: func(context.Context, string, string, string, int64, metric.CreateInput) (metric.Record, error) {
		return record, nil
	}}
	harness := newCandidateHTTPHarness(t, store, creator)

	response := candidateHTTPRequest(
		t, harness, http.MethodPost, "/api/v1/metric-candidates/"+testCandidateID+"/accept",
		`{"expectedVersion":4}`,
	)
	if response.Code != http.StatusCreated {
		t.Fatalf("accept status=%d body=%s", response.Code, response.Body.String())
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope) != 2 || envelope["candidate"] == nil || envelope["metric"] == nil {
		t.Fatalf("accept envelope = %s", response.Body.String())
	}
	var responseCandidate Candidate
	var responseMetric metric.Record
	if err := json.Unmarshal(envelope["candidate"], &responseCandidate); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(envelope["metric"], &responseMetric); err != nil {
		t.Fatal(err)
	}
	if responseCandidate.Status != CandidateStatusAccepted || responseCandidate.AcceptedMetricID != testMetricID || responseMetric.ID != testMetricID {
		t.Fatalf("accept response candidate=%#v metric=%#v", responseCandidate, responseMetric)
	}
	if len(harness.permissions.checks) != 2 ||
		harness.permissions.checks[0].ResourceType != "METRIC" || harness.permissions.checks[0].Action != "MANAGE" || harness.permissions.checks[0].ObjectID != "" ||
		harness.permissions.checks[1].ResourceType != "DATASET" || harness.permissions.checks[1].Action != "READ" || harness.permissions.checks[1].ObjectID != "" {
		t.Fatalf("accept permission checks = %#v", harness.permissions.checks)
	}
}

func TestCandidateHTTPRejectReturnsRejectedCandidate(t *testing.T) {
	reviewable := reviewCandidate(t, CandidateStatusNeedsReview)
	store := &candidateStoreStub{rejectFn: func(_ context.Context, _, _, _ string, input RejectInput) (Candidate, error) {
		reviewable.Status = CandidateStatusRejected
		reviewable.Version++
		reviewable.DecisionReason = input.Reason
		return reviewable, nil
	}}
	harness := newCandidateHTTPHarness(t, store, nil)

	response := candidateHTTPRequest(
		t, harness, http.MethodPost, "/api/v1/metric-candidates/"+testCandidateID+"/reject",
		`{"expectedVersion":4,"reason":"重复口径"}`,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("reject status=%d body=%s", response.Code, response.Body.String())
	}
	var candidate Candidate
	if err := json.Unmarshal(response.Body.Bytes(), &candidate); err != nil {
		t.Fatal(err)
	}
	if candidate.Status != CandidateStatusRejected || candidate.DecisionReason != "重复口径" {
		t.Fatalf("reject response = %#v", candidate)
	}
}

func TestCandidateHTTPMapsBlockedAndAtomicVersionConflict(t *testing.T) {
	tests := []struct {
		name       string
		candidate  Candidate
		creatorErr error
		wantStatus int
		wantCode   string
	}{
		{
			name: "blocked", candidate: func() Candidate {
				value := reviewCandidate(t, CandidateStatusBlocked)
				value.BlockReasons = []string{BlockReasonPreAggregation}
				return value
			}(),
			wantStatus: http.StatusUnprocessableEntity, wantCode: "METRIC_CANDIDATE_BLOCKED",
		},
		{
			name: "atomic optimistic conflict", candidate: reviewCandidate(t, CandidateStatusReady),
			creatorErr: metric.ErrOriginCandidateConflict,
			wantStatus: http.StatusConflict, wantCode: "METRIC_CANDIDATE_VERSION_CONFLICT",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &candidateStoreStub{getFn: func(context.Context, string, string) (Candidate, error) {
				return test.candidate, nil
			}}
			creator := &metricCreatorStub{createFn: func(context.Context, string, string, string, int64, metric.CreateInput) (metric.Record, error) {
				return metric.Record{}, test.creatorErr
			}}
			harness := newCandidateHTTPHarness(t, store, creator)
			response := candidateHTTPRequest(
				t, harness, http.MethodPost, "/api/v1/metric-candidates/"+testCandidateID+"/accept",
				`{"expectedVersion":4}`,
			)
			if response.Code != test.wantStatus || !strings.Contains(response.Body.String(), test.wantCode) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestCandidateHTTPRejectsUnknownReviewFields(t *testing.T) {
	store := &candidateStoreStub{}
	harness := newCandidateHTTPHarness(t, store, nil)
	response := candidateHTTPRequest(
		t, harness, http.MethodPost, "/api/v1/metric-candidates/"+testCandidateID+"/reject",
		`{"expectedVersion":4,"reason":"重复口径","unexpected":true}`,
	)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "INVALID_REQUEST") {
		t.Fatalf("unknown field status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestCandidateHTTPRequiresGlobalDatasetReadBeforeListingSourceFacts(t *testing.T) {
	harness := newCandidateHTTPHarness(t, &candidateStoreStub{}, nil)
	harness.permissions.denyResource = "DATASET"
	response := candidateHTTPRequest(t, harness, http.MethodGet, "/api/v1/metric-candidates", "")
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "PERMISSION_DENIED") {
		t.Fatalf("dataset-read denial status=%d body=%s", response.Code, response.Body.String())
	}
	if len(harness.permissions.checks) != 2 || harness.permissions.checks[1].ResourceType != "DATASET" || harness.permissions.checks[1].ObjectID != "" {
		t.Fatalf("dataset-read permission checks = %#v", harness.permissions.checks)
	}
}
