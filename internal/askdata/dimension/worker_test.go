package dimension

import (
	"context"
	"errors"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/registry"
)

type profileStoreFixture struct {
	claim        *ScanClaim
	completed    bool
	failedCode   string
	profile      Profile
	decision     PolicyDecision
	observations []MemberObservation
}

func (store *profileStoreFixture) ListTenantIDs(context.Context) ([]string, error) {
	return []string{"11111111-1111-4111-8111-111111111111"}, nil
}

func (store *profileStoreFixture) Claim(
	context.Context, string, string, time.Duration, WorkerOptions,
) (*ScanClaim, error) {
	claim := store.claim
	store.claim = nil
	return claim, nil
}

func (store *profileStoreFixture) Complete(
	_ context.Context,
	_ ScanClaim,
	_ string,
	profile Profile,
	decision PolicyDecision,
	observations []MemberObservation,
) error {
	store.completed = true
	store.profile = profile
	store.decision = decision
	store.observations = append([]MemberObservation(nil), observations...)
	return nil
}

func (store *profileStoreFixture) Fail(
	_ context.Context, _ ScanClaim, _ string, code string,
) error {
	store.failedCode = code
	return nil
}

type warehouseScannerFixture struct {
	result ScanResult
	err    error
	calls  int
}

func (scanner *warehouseScannerFixture) Scan(context.Context, ScanClaim) (ScanResult, error) {
	scanner.calls++
	return scanner.result, scanner.err
}

func TestDimensionProfileWorkerPersistsNormalizedGeneration(t *testing.T) {
	t.Parallel()
	claim := validScanClaim()
	store := &profileStoreFixture{claim: &claim}
	scanner := &warehouseScannerFixture{result: ScanResult{
		RowCount: 4, NullCount: 1, RawDistinct: 2,
		SampleBytes: int64(len("华东") + len("UNKNOWN")),
		Members:     []RawMember{{Value: "华东", Count: 2}, {Value: "UNKNOWN", Count: 1}},
	}}
	worker, err := NewWorker(store, scanner, DefaultWorkerOptions())
	if err != nil {
		t.Fatal(err)
	}
	processed, err := worker.ProcessNext(
		context.Background(), claim.TenantID, "worker-1", DefaultDimensionProfileLease,
	)
	if err != nil || !processed {
		t.Fatalf("processed=%v err=%v", processed, err)
	}
	if scanner.calls != 1 || !store.completed || store.failedCode != "" {
		t.Fatalf("scanner calls=%d completed=%v failure=%q", scanner.calls, store.completed, store.failedCode)
	}
	if err := store.profile.Validate(); err != nil {
		t.Fatalf("profile: %v", err)
	}
	if err := store.decision.Validate(); err != nil {
		t.Fatalf("decision: %v", err)
	}
	if store.profile.Generation != claim.Generation || len(store.profile.ReservedValues) != 1 ||
		store.profile.ReservedValues[0].Code != "UNKNOWN" {
		t.Fatalf("unexpected profile: %#v", store.profile)
	}
	if len(store.observations) != 1 || store.observations[0].CanonicalLabel != "华东" ||
		store.observations[0].ObservedCount != 2 || !store.observations[0].EligibleForLLM {
		t.Fatalf("unexpected observations: %#v", store.observations)
	}
	if store.decision.RecommendedPolicy != registry.MemberIndexFull {
		t.Fatalf("policy=%q, want FULL", store.decision.RecommendedPolicy)
	}
}

func TestDimensionProfileWorkerRecordsTimeoutForRetry(t *testing.T) {
	t.Parallel()
	claim := validScanClaim()
	store := &profileStoreFixture{claim: &claim}
	scanner := &warehouseScannerFixture{err: ErrWarehouseTimeout}
	worker, err := NewWorker(store, scanner, DefaultWorkerOptions())
	if err != nil {
		t.Fatal(err)
	}
	processed, err := worker.ProcessNext(
		context.Background(), claim.TenantID, "worker-1", DefaultDimensionProfileLease,
	)
	if !processed || !errors.Is(err, ErrWarehouseTimeout) {
		t.Fatalf("processed=%v err=%v", processed, err)
	}
	if store.completed || store.failedCode != "WAREHOUSE_STATEMENT_TIMEOUT" {
		t.Fatalf("completed=%v failure=%q", store.completed, store.failedCode)
	}
}

func TestDimensionProfileGenerationComputesMemberSetChange(t *testing.T) {
	t.Parallel()
	claim := validScanClaim()
	claim.Generation = 2
	previousDistinct := int64(2)
	claim.PreviousDistinctCount = &previousDistinct
	claim.PreviousComplete = true
	east, _, err := NormalizeMember(
		askdata.ID(claim.DimensionVersionID), "华东", nil,
		registry.SensitivityInternal, DefaultReservedValueCatalog(),
	)
	if err != nil {
		t.Fatal(err)
	}
	claim.PreviousMemberKeyHashes = []askdata.ContentHash{
		east.MemberKeyHash, askdata.HashBytes([]byte("unknown")),
	}
	result := ScanResult{
		RowCount: 4, RawDistinct: 2, SampleBytes: int64(len("华东") + len("华南")),
		Members: []RawMember{{Value: "华东", Count: 2}, {Value: "华南", Count: 2}},
	}
	profile, _, _, err := buildProfileResult(claim, result, DefaultPolicyConfig())
	if err != nil {
		t.Fatal(err)
	}
	if profile.PreviousDistinctCount == nil || *profile.PreviousDistinctCount != 2 ||
		profile.AddedDistinctCount != 1 || profile.RemovedDistinctCount != 1 || profile.DistinctCount != 2 {
		t.Fatalf("unexpected change evidence: %#v", profile)
	}
}

func validScanClaim() ScanClaim {
	options := DefaultWorkerOptions()
	return ScanClaim{
		ID:                     "11111111-1111-4111-8111-111111111111",
		TenantID:               "22222222-2222-4222-8222-222222222222",
		DomainID:               "33333333-3333-4333-8333-333333333333",
		DimensionVersionID:     "44444444-4444-4444-8444-444444444444",
		SemanticModelVersionID: "55555555-5555-4555-8555-555555555555",
		DatasetID:              "66666666-6666-4666-8666-666666666666",
		DatasetVersionID:       "77777777-7777-4777-8777-777777777777",
		MaterializationID:      "88888888-8888-4888-8888-888888888888",
		SourceSnapshotHash:     string(askdata.HashBytes([]byte("snapshot"))),
		DatasetSchemaHash:      string(askdata.HashBytes([]byte("schema"))),
		PublishedSchema:        "warehouse_published", PublishedName: "dws_sales",
		FieldCode: "region", InputHash: string(askdata.HashBytes([]byte("input"))),
		LeaseToken: "99999999-9999-4999-8999-999999999999",
		Generation: 1, ExpectedRowCount: 4,
		Sensitivity:       registry.SensitivityInternal,
		MemberIndexPolicy: registry.MemberIndexExactOnly,
		Budget:            options.Budget, Attempt: 1, MaxAttempts: options.MaxAttempts,
	}
}
