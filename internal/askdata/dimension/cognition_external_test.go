package dimension_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	askdatacognition "intelligent-report-generation-system/internal/askdata/cognition"
	"intelligent-report-generation-system/internal/askdata/dimension"
	"intelligent-report-generation-system/internal/askdata/registry"
)

type externalProfileStore struct {
	claim        *dimension.ScanClaim
	profile      dimension.Profile
	observations []dimension.MemberObservation
}

func (store *externalProfileStore) ListTenantIDs(context.Context) ([]string, error) {
	return []string{store.claim.TenantID}, nil
}

func (store *externalProfileStore) Claim(
	context.Context, string, string, time.Duration, dimension.WorkerOptions,
) (*dimension.ScanClaim, error) {
	claim := store.claim
	store.claim = nil
	return claim, nil
}

func (store *externalProfileStore) Complete(
	_ context.Context,
	_ dimension.ScanClaim,
	_ string,
	profile dimension.Profile,
	_ dimension.PolicyDecision,
	observations []dimension.MemberObservation,
) error {
	store.profile = profile
	store.observations = append([]dimension.MemberObservation(nil), observations...)
	return nil
}

func (*externalProfileStore) Fail(context.Context, dimension.ScanClaim, string, string) error {
	return nil
}

type externalScanner struct{}

func (externalScanner) Scan(context.Context, dimension.ScanClaim) (dimension.ScanResult, error) {
	return dimension.ScanResult{
		RowCount: 2, RawDistinct: 2, SampleBytes: int64(len("ACME") + len("acme")),
		Members: []dimension.RawMember{{Value: "ACME", Count: 1}, {Value: "acme", Count: 1}},
	}, nil
}

type externalReviewer struct{ calls int }

func (reviewer *externalReviewer) ReviewGeneration(
	context.Context,
	askdatacognition.PromptFact,
) ([]dimension.AnomalyProposal, error) {
	reviewer.calls++
	return nil, errors.New("member reviewer must remain disabled")
}

func TestExternalScannerCannotRouteManufacturedMemberLabelsToReviewer(t *testing.T) {
	options := dimension.DefaultWorkerOptions()
	claim := dimension.ScanClaim{
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
		PublishedSchema:        "warehouse_published",
		PublishedName:          "dws_sales",
		FieldCode:              "region",
		InputHash:              string(askdata.HashBytes([]byte("input"))),
		LeaseToken:             "99999999-9999-4999-8999-999999999999",
		Generation:             1,
		ExpectedRowCount:       2,
		Sensitivity:            registry.SensitivityInternal,
		MemberIndexPolicy:      registry.MemberIndexFull,
		Budget:                 options.Budget,
		Attempt:                1,
		MaxAttempts:            options.MaxAttempts,
	}
	store := &externalProfileStore{claim: &claim}
	worker, err := dimension.NewWorker(store, externalScanner{}, options)
	if err != nil {
		t.Fatal(err)
	}
	processed, err := worker.ProcessNext(
		context.Background(), claim.TenantID, "external-worker", dimension.DefaultDimensionProfileLease,
	)
	if err != nil || !processed {
		t.Fatalf("processed=%v err=%v", processed, err)
	}

	reviewer := &externalReviewer{}
	result, err := dimension.ReviewProfileGeneration(
		context.Background(), claim, store.profile, store.observations, reviewer,
	)
	if err != nil {
		t.Fatal(err)
	}
	if reviewer.calls != 0 || len(result.DeterministicProposals) != 1 || len(result.LLMProposals) != 0 {
		t.Fatalf("calls=%d deterministic=%d llm=%d", reviewer.calls, len(result.DeterministicProposals), len(result.LLMProposals))
	}
	fact, err := result.Evidence.PromptFact()
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range [][]byte{
		[]byte("ACME"), []byte("acme"), []byte("canonicalLabel"),
		[]byte("normalizedValue"), []byte("memberKeyHash"), []byte("memberEvidenceId"),
	} {
		if bytes.Contains(fact.Payload, forbidden) {
			t.Fatalf("prompt payload leaked %q: %s", forbidden, fact.Payload)
		}
	}
}
