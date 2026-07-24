package semanticmanagement

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

const testSurveyCandidateID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
const testSurveyRefreshJobID = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"

type fakeDimensionSurveyStore struct {
	*fakeDimensionStore
	candidate DimensionSurveyCandidate
	prepared  PreparedDimension
	updated   bool
	accepted  bool
	rejected  bool
}

func newFakeDimensionSurveyStore(
	candidate DimensionSurveyCandidate,
) *fakeDimensionSurveyStore {
	return &fakeDimensionSurveyStore{
		fakeDimensionStore: &fakeDimensionStore{},
		candidate:          candidate,
	}
}

func (s *fakeDimensionSurveyStore) ListDimensionSurveyCandidates(
	context.Context,
	string,
	DimensionSurveyFilter,
) ([]DimensionSurveyCandidate, int, error) {
	return []DimensionSurveyCandidate{s.candidate}, 1, s.err
}

func (s *fakeDimensionSurveyStore) GetDimensionSurveyCandidate(
	context.Context,
	string,
	string,
) (DimensionSurveyCandidate, error) {
	return s.candidate, s.err
}

func (s *fakeDimensionSurveyStore) UpdateDimensionSurveyCandidate(
	_ context.Context,
	_, _, _ string,
	_ int64,
	prepared PreparedDimension,
) (DimensionSurveyCandidate, error) {
	s.prepared, s.updated = prepared, true
	s.candidate.ProposedCode = prepared.Code
	s.candidate.ProposedName = prepared.Name
	s.candidate.ProposedDescription = prepared.Description
	s.candidate.ProposedDimensionType = prepared.DimensionType
	s.candidate.ProposedMemberIndexPolicy = prepared.MemberIndexPolicy
	s.candidate.ProposedHighCardinality = prepared.HighCardinality
	s.candidate.ProposedSensitive = prepared.Sensitive
	s.candidate.Version++
	return s.candidate, s.err
}

func (s *fakeDimensionSurveyStore) AcceptDimensionSurveyCandidate(
	_ context.Context,
	_, _, _ string,
	_ int64,
	prepared PreparedDimension,
) (DimensionSurveyAcceptance, error) {
	s.prepared, s.accepted = prepared, true
	s.candidate.Status = "ACCEPTED"
	return DimensionSurveyAcceptance{
		Candidate: s.candidate,
		Dimension: Dimension{
			ID: testDimensionID, Status: "PUBLISHED",
		},
		MemberRefreshJob: &RefreshJob{
			ID: testSurveyRefreshJobID, Status: "QUEUED",
		},
		NextAction: "WAIT_FOR_MEMBER_REFRESH",
	}, s.err
}

func (s *fakeDimensionSurveyStore) RejectDimensionSurveyCandidate(
	context.Context,
	string,
	string,
	string,
	int64,
	string,
) (DimensionSurveyCandidate, error) {
	s.rejected = true
	s.candidate.Status = "REJECTED"
	return s.candidate, s.err
}

func testDimensionSurveyCandidate() DimensionSurveyCandidate {
	return DimensionSurveyCandidate{
		ID:        testSurveyCandidateID,
		DatasetID: testDatasetID, DatasetVersionID: testVersionID,
		FieldID: "customer_id", FieldCode: "customer_id",
		FieldRole: "IDENTIFIER", CanonicalType: "STRING",
		RiskHighCardinality:       true,
		ProposedCode:              "customer_id",
		ProposedName:              "客户",
		ProposedDescription:       "客户维度",
		ProposedDimensionType:     "CUSTOMER",
		ProposedMemberIndexPolicy: "EXACT_ONLY",
		ProposedHighCardinality:   true,
		Status:                    "SUGGESTED",
		Version:                   1,
	}
}

func TestDimensionSurveyCandidateRiskCanOnlyTighten(t *testing.T) {
	store := newFakeDimensionSurveyStore(testDimensionSurveyCandidate())
	service := NewDimensionService(store)

	_, err := service.UpdateDimensionSurveyCandidate(
		context.Background(), testTenantID, testActorID,
		testSurveyCandidateID,
		UpdateDimensionSurveyCandidateInput{
			ExpectedVersion: 1, Code: "customer", Name: "客户",
			DimensionType: "CUSTOMER", MemberIndexPolicy: "FULL",
		},
	)
	if !errors.Is(err, ErrConflict) || store.updated {
		t.Fatalf("high-cardinality risk was relaxed: updated=%v err=%v", store.updated, err)
	}

	updated, err := service.UpdateDimensionSurveyCandidate(
		context.Background(), testTenantID, testActorID,
		testSurveyCandidateID,
		UpdateDimensionSurveyCandidateInput{
			ExpectedVersion: 1, Code: "customer", Name: "客户",
			Description: "受治理的客户维度", DimensionType: "CUSTOMER",
			MemberIndexPolicy: "NONE", HighCardinality: true,
		},
	)
	if err != nil || !store.updated ||
		updated.ProposedMemberIndexPolicy != "NONE" ||
		store.prepared.Status != "PUBLISHED" {
		t.Fatalf("tightened candidate=%+v prepared=%+v err=%v",
			updated, store.prepared, err)
	}
}

func TestDimensionSurveyAcceptPublishesOnlyReviewedCandidate(t *testing.T) {
	candidate := testDimensionSurveyCandidate()
	candidate.RiskHighCardinality = false
	candidate.ProposedHighCardinality = false
	candidate.ProposedMemberIndexPolicy = "FULL"
	store := newFakeDimensionSurveyStore(candidate)
	result, err := NewDimensionService(store).AcceptDimensionSurveyCandidate(
		context.Background(), testTenantID, testActorID,
		testSurveyCandidateID, candidate.Version,
	)
	if err != nil || !store.accepted ||
		result.Dimension.Status != "PUBLISHED" ||
		result.MemberRefreshJob == nil ||
		result.MemberRefreshJob.Status != "QUEUED" ||
		result.MemberSearchReady ||
		result.NextAction != "WAIT_FOR_MEMBER_REFRESH" ||
		store.prepared.DatasetVersionID != testVersionID ||
		store.prepared.FieldID != candidate.FieldID ||
		store.prepared.Status != "PUBLISHED" {
		t.Fatalf("result=%+v prepared=%+v accepted=%v err=%v",
			result, store.prepared, store.accepted, err)
	}
}

func TestDimensionServiceForbidsFullIndexForHighCardinalityDimension(t *testing.T) {
	service := NewDimensionService(&fakeDimensionStore{})
	_, err := service.CreateDimension(
		context.Background(), testTenantID, testActorID,
		CreateDimensionInput{
			DatasetID: testDatasetID, DatasetVersionID: testVersionID,
			FieldID: "customer_id", Code: "customer", Name: "客户",
			DimensionType: "CUSTOMER", MemberIndexPolicy: "FULL",
			HighCardinality: true, Status: "DRAFT",
		},
	)
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("high-cardinality FULL dimension error=%v", err)
	}
}

func TestDimensionSurveyHTTPListsAndAcceptsWithGovernedPermissions(t *testing.T) {
	candidate := testDimensionSurveyCandidate()
	candidate.RiskHighCardinality = false
	candidate.ProposedHighCardinality = false
	candidate.ProposedMemberIndexPolicy = "FULL"
	store := newFakeDimensionSurveyStore(candidate)
	harness := newDimensionHTTPHarness(t, store, true)

	response := semanticRequest(
		t, harness, http.MethodGet,
		"/api/v1/semantic/dimension-survey-candidates?status=SUGGESTED", "",
	)
	if response.Code != http.StatusOK ||
		response.Header().Get("Cache-Control") != "no-store" ||
		!strings.Contains(response.Body.String(), testSurveyCandidateID) {
		t.Fatalf("list status=%d body=%s", response.Code, response.Body.String())
	}
	response = semanticRequest(
		t, harness, http.MethodPost,
		"/api/v1/semantic/dimension-survey-candidates/"+
			testSurveyCandidateID+"/accept",
		`{"expectedVersion":1}`,
	)
	if response.Code != http.StatusOK || !store.accepted ||
		!strings.Contains(response.Body.String(), `"status":"PUBLISHED"`) ||
		!strings.Contains(response.Body.String(), `"memberSearchReady":false`) ||
		!strings.Contains(response.Body.String(), `"nextAction":"WAIT_FOR_MEMBER_REFRESH"`) {
		t.Fatalf("accept status=%d body=%s", response.Code, response.Body.String())
	}
	if len(harness.permissions.checks) != 2 ||
		harness.permissions.checks[0].Action != "READ" ||
		harness.permissions.checks[1].Action != "MANAGE" {
		t.Fatalf("permission checks=%+v", harness.permissions.checks)
	}
}

var _ DimensionStore = (*fakeDimensionSurveyStore)(nil)
var _ DimensionSurveyStore = (*fakeDimensionSurveyStore)(nil)
