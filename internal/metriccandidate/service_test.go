package metriccandidate

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/metric"
)

const (
	testCandidateID = "55555555-5555-4555-8555-555555555555"
	testTenantID    = "66666666-6666-4666-8666-666666666666"
	testActorID     = "77777777-7777-4777-8777-777777777777"
	testMetricID    = "88888888-8888-4888-8888-888888888888"
	testJobID       = "99999999-9999-4999-8999-999999999999"
)

type candidateStoreStub struct {
	getFn    func(context.Context, string, string) (Candidate, error)
	listFn   func(context.Context, string, ListFilter) ([]Candidate, int, error)
	rejectFn func(context.Context, string, string, string, RejectInput) (Candidate, error)
}

func (store *candidateStoreStub) List(ctx context.Context, tenantID string, filter ListFilter) ([]Candidate, int, error) {
	if store.listFn == nil {
		return nil, 0, errors.New("unexpected List call")
	}
	return store.listFn(ctx, tenantID, filter)
}

func (store *candidateStoreStub) Get(ctx context.Context, tenantID, id string) (Candidate, error) {
	if store.getFn == nil {
		return Candidate{}, errors.New("unexpected Get call")
	}
	return store.getFn(ctx, tenantID, id)
}

func (store *candidateStoreStub) Reject(ctx context.Context, tenantID, actorID, id string, input RejectInput) (Candidate, error) {
	if store.rejectFn == nil {
		return Candidate{}, errors.New("unexpected Reject call")
	}
	return store.rejectFn(ctx, tenantID, actorID, id, input)
}

type metricCreatorStub struct {
	calls    int
	createFn func(context.Context, string, string, string, int64, metric.CreateInput) (metric.Record, error)
}

func (creator *metricCreatorStub) CreateFromCandidate(
	ctx context.Context,
	tenantID, actorID, candidateID string,
	expectedVersion int64,
	input metric.CreateInput,
) (metric.Record, error) {
	creator.calls++
	if creator.createFn == nil {
		return metric.Record{}, errors.New("unexpected CreateFromCandidate call")
	}
	return creator.createFn(ctx, tenantID, actorID, candidateID, expectedVersion, input)
}

func TestServiceAcceptUsesAtomicMetricCreationAndReturnsAcceptedState(t *testing.T) {
	reviewable := reviewCandidate(t, CandidateStatusReady)
	accepted := reviewable
	accepted.Status = CandidateStatusAccepted
	accepted.Version++
	accepted.AcceptedMetricID = testMetricID
	record := metric.Record{
		ID: testMetricID, Code: reviewable.Code, Name: reviewable.Name, Status: "DRAFT",
		DatasetID: reviewable.DatasetID, DatasetVersionID: reviewable.DatasetVersionID,
		Definition: reviewable.ProposedDefinition,
	}

	getCalls := 0
	store := &candidateStoreStub{getFn: func(_ context.Context, tenantID, id string) (Candidate, error) {
		getCalls++
		if tenantID != testTenantID || id != testCandidateID {
			t.Fatalf("Get scope = (%q, %q)", tenantID, id)
		}
		if getCalls == 1 {
			return reviewable, nil
		}
		return accepted, nil
	}}
	creator := &metricCreatorStub{createFn: func(
		_ context.Context,
		tenantID, actorID, candidateID string,
		expectedVersion int64,
		input metric.CreateInput,
	) (metric.Record, error) {
		if tenantID != testTenantID || actorID != testActorID || candidateID != testCandidateID {
			t.Fatalf("CreateFromCandidate scope = (%q, %q, %q)", tenantID, actorID, candidateID)
		}
		if expectedVersion != reviewable.Version {
			t.Fatalf("expected candidate version = %d", expectedVersion)
		}
		if string(input.Definition) != string(reviewable.ProposedDefinition) {
			t.Fatalf("definition changed before atomic creation: %s", input.Definition)
		}
		return record, nil
	}}

	result, err := NewService(store, creator).Accept(
		context.Background(), testTenantID, testActorID, testCandidateID,
		AcceptInput{ExpectedVersion: reviewable.Version},
	)
	if err != nil {
		t.Fatalf("Accept() error = %v", err)
	}
	if creator.calls != 1 || getCalls != 2 {
		t.Fatalf("calls: creator=%d get=%d", creator.calls, getCalls)
	}
	if result.Candidate.Status != CandidateStatusAccepted || result.Candidate.AcceptedMetricID != testMetricID || result.Metric.ID != testMetricID {
		t.Fatalf("Accept() result = %#v", result)
	}
}

func TestServiceAcceptRejectsBlockedCandidateBeforeMetricCreation(t *testing.T) {
	blocked := reviewCandidate(t, CandidateStatusBlocked)
	blocked.BlockReasons = []string{BlockReasonAggregatedDataset}
	store := &candidateStoreStub{getFn: func(context.Context, string, string) (Candidate, error) {
		return blocked, nil
	}}
	creator := &metricCreatorStub{createFn: func(context.Context, string, string, string, int64, metric.CreateInput) (metric.Record, error) {
		return metric.Record{}, nil
	}}

	_, err := NewService(store, creator).Accept(
		context.Background(), testTenantID, testActorID, testCandidateID,
		AcceptInput{ExpectedVersion: blocked.Version},
	)
	if !errors.Is(err, ErrBlocked) || creator.calls != 0 {
		t.Fatalf("blocked Accept() error=%v creatorCalls=%d", err, creator.calls)
	}
}

func TestServiceRejectTrimsReasonAndDelegatesOptimisticVersion(t *testing.T) {
	candidate := reviewCandidate(t, CandidateStatusNeedsReview)
	store := &candidateStoreStub{rejectFn: func(
		_ context.Context,
		tenantID, actorID, id string,
		input RejectInput,
	) (Candidate, error) {
		if tenantID != testTenantID || actorID != testActorID || id != testCandidateID {
			t.Fatalf("Reject scope = (%q, %q, %q)", tenantID, actorID, id)
		}
		if input.ExpectedVersion != candidate.Version || input.Reason != "口径不适用" {
			t.Fatalf("Reject input = %#v", input)
		}
		candidate.Status = CandidateStatusRejected
		candidate.Version++
		candidate.DecisionReason = input.Reason
		return candidate, nil
	}}

	result, err := NewService(store, nil).Reject(
		context.Background(), testTenantID, testActorID, testCandidateID,
		RejectInput{ExpectedVersion: candidate.Version, Reason: "  口径不适用\n"},
	)
	if err != nil {
		t.Fatalf("Reject() error = %v", err)
	}
	if result.Status != CandidateStatusRejected || result.DecisionReason != "口径不适用" {
		t.Fatalf("Reject() result = %#v", result)
	}
}

func TestServiceAcceptMapsAtomicOptimisticLockConflict(t *testing.T) {
	candidate := reviewCandidate(t, CandidateStatusReady)
	store := &candidateStoreStub{getFn: func(context.Context, string, string) (Candidate, error) {
		return candidate, nil
	}}
	creator := &metricCreatorStub{createFn: func(context.Context, string, string, string, int64, metric.CreateInput) (metric.Record, error) {
		return metric.Record{}, metric.ErrOriginCandidateConflict
	}}

	_, err := NewService(store, creator).Accept(
		context.Background(), testTenantID, testActorID, testCandidateID,
		AcceptInput{ExpectedVersion: candidate.Version},
	)
	if !errors.Is(err, ErrConflict) || creator.calls != 1 {
		t.Fatalf("optimistic conflict error=%v creatorCalls=%d", err, creator.calls)
	}
}

type jobStoreStub struct {
	claims                    []*JobClaim
	loadVersion               dataset.VersionRecord
	loadDependencyUnavailable bool
	loadErr                   error
	finishErr                 error
	claimCalls                int
	loadCalls                 int
	finishCalls               int
	failCalls                 int
	results                   []ExtractionResult
	failCodes                 []string
	failReasons               []string
}

func (store *jobStoreStub) ListJobTenantIDs(context.Context) ([]string, error) {
	return []string{testTenantID}, nil
}

func (store *jobStoreStub) ClaimJob(context.Context, string, string, time.Duration) (*JobClaim, error) {
	if store.claimCalls >= len(store.claims) {
		return nil, nil
	}
	claim := store.claims[store.claimCalls]
	store.claimCalls++
	return claim, nil
}

func (store *jobStoreStub) LoadExactDatasetVersion(context.Context, JobClaim) (LoadedDatasetVersion, error) {
	store.loadCalls++
	return LoadedDatasetVersion{Version: store.loadVersion, DependencyUnavailable: store.loadDependencyUnavailable}, store.loadErr
}

func (store *jobStoreStub) FinishJob(_ context.Context, _ JobClaim, _ string, result ExtractionResult) error {
	store.finishCalls++
	store.results = append(store.results, result)
	return store.finishErr
}

func (store *jobStoreStub) FailJob(_ context.Context, _ JobClaim, _ string, code, reason string) error {
	store.failCalls++
	store.failCodes = append(store.failCodes, code)
	store.failReasons = append(store.failReasons, reason)
	return nil
}

func TestWorkerFinishesSuccessfulExtraction(t *testing.T) {
	version := publishedDatasetVersion(t, candidateDatasetDocument())
	claim := claimForVersion(version)
	store := &jobStoreStub{claims: []*JobClaim{&claim}, loadVersion: version}

	handled, err := NewWorker(store).ProcessNext(context.Background(), testTenantID, "worker-1", time.Minute)
	if err != nil || !handled {
		t.Fatalf("ProcessNext() handled=%v error=%v", handled, err)
	}
	if store.loadCalls != 1 || store.finishCalls != 1 || store.failCalls != 0 || len(store.results) != 1 {
		t.Fatalf("worker calls: load=%d finish=%d fail=%d results=%d", store.loadCalls, store.finishCalls, store.failCalls, len(store.results))
	}
	result := store.results[0]
	if result.DatasetVersionID != version.ID || len(result.Candidates) == 0 || result.Status != TaskStatusPartial {
		t.Fatalf("finished extraction result = %#v", result)
	}
}

func TestWorkerReportsEachStoreManagedRetryFailure(t *testing.T) {
	version := publishedDatasetVersion(t, candidateDatasetDocument())
	claim := claimForVersion(version)
	loadErr := errors.New("exact dataset version unavailable")
	store := &jobStoreStub{
		claims:      []*JobClaim{&claim, &claim, &claim},
		loadVersion: version,
		loadErr:     loadErr,
	}
	worker := NewWorker(store)

	// FailJob owns the PENDING/PENDING/FAILED transition. The worker must report every
	// claimed failure so the store can apply its three-attempt retry policy.
	for attempt := 1; attempt <= 3; attempt++ {
		handled, err := worker.ProcessNext(context.Background(), testTenantID, "worker-1", time.Minute)
		if !handled || !errors.Is(err, loadErr) {
			t.Fatalf("attempt %d: handled=%v error=%v", attempt, handled, err)
		}
	}
	handled, err := worker.ProcessNext(context.Background(), testTenantID, "worker-1", time.Minute)
	if handled || err != nil {
		t.Fatalf("after retry budget: handled=%v error=%v", handled, err)
	}
	if store.loadCalls != 3 || store.finishCalls != 0 || store.failCalls != 3 {
		t.Fatalf("retry calls: load=%d finish=%d fail=%d", store.loadCalls, store.finishCalls, store.failCalls)
	}
	for index := range store.failCodes {
		if store.failCodes[index] != "METRIC_EXTRACTION_FAILED" || store.failReasons[index] != loadErr.Error() {
			t.Fatalf("failure %d = (%q, %q)", index+1, store.failCodes[index], store.failReasons[index])
		}
	}
}

func TestWorkerPersistsBlockedCandidatesWhenPublishedDatasetDependencyIsUnavailable(t *testing.T) {
	version := publishedDatasetVersion(t, candidateDatasetDocument())
	claim := claimForVersion(version)
	store := &jobStoreStub{
		claims:                    []*JobClaim{&claim},
		loadVersion:               version,
		loadDependencyUnavailable: true,
	}

	handled, err := NewWorker(store).ProcessNext(context.Background(), testTenantID, "worker-1", time.Minute)
	if err != nil || !handled {
		t.Fatalf("ProcessNext() handled=%v error=%v", handled, err)
	}
	if store.finishCalls != 1 || store.failCalls != 0 || len(store.results) != 1 {
		t.Fatalf("worker calls: finish=%d fail=%d results=%d", store.finishCalls, store.failCalls, len(store.results))
	}
	result := store.results[0]
	if result.Status != TaskStatusPartial || len(result.Candidates) == 0 {
		t.Fatalf("blocked extraction result = %#v", result)
	}
	for _, candidate := range result.Candidates {
		if candidate.Status != CandidateStatusBlocked || !containsString(candidate.BlockReasons, BlockReasonDatasetUnavailable) {
			t.Fatalf("candidate was not dependency-blocked: %#v", candidate)
		}
	}
	if !extractionBlockedByUnavailable(result) {
		t.Fatal("dependency-blocked result was not recognized by persistence guard")
	}
}

func reviewCandidate(t *testing.T, status CandidateStatus) Candidate {
	t.Helper()
	result, err := Extract(publishedDatasetVersion(t, candidateDatasetDocument()))
	if err != nil {
		t.Fatal(err)
	}
	draft := result.Candidates[0]
	raw, err := json.Marshal(draft.Definition)
	if err != nil {
		t.Fatal(err)
	}
	return Candidate{
		ID: testCandidateID, DatasetID: draft.DatasetID, DatasetVersionID: draft.DatasetVersionID,
		DSLHash: result.DSLHash, Name: draft.Definition.Metric.Name, Code: draft.Definition.Metric.Code,
		Description: draft.Definition.Metric.Description, Status: status, Method: "RULE",
		Confidence: 0.95, ProposedDefinition: raw, SourceFieldIDs: []string{draft.SourceFieldID},
		Evidence: []CandidateEvidence{}, Assumptions: []string{}, Warnings: draft.Warnings,
		BlockReasons: []string{}, Fingerprint: draft.Fingerprint, Version: 4,
		CreatedAt: "2026-07-20T00:00:00Z", UpdatedAt: "2026-07-20T00:00:00Z",
	}
}

func claimForVersion(version dataset.VersionRecord) JobClaim {
	return JobClaim{
		ID: testJobID, TenantID: testTenantID, DatasetID: version.DatasetID,
		DatasetVersionID: version.ID, DSLHash: version.DSLHash, RequestedBy: testActorID,
	}
}
