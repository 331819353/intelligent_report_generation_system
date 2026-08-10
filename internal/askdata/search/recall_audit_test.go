package search

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"intelligent-report-generation-system/internal/askdata"
)

type recallAuditStoreFixture struct {
	lastRun      time.Time
	hasLastRun   bool
	samples      []QueryVectorSample
	ann          []askdata.ID
	exact        []askdata.ID
	saved        []RecallAuditResult
	purgedBefore time.Time
	annCalls     int
	exactCalls   int
}

func (store *recallAuditStoreFixture) ListTenantIDs(context.Context) ([]string, error) {
	return []string{"22222222-2222-4222-8222-222222222222"}, nil
}
func (store *recallAuditStoreFixture) LastRunAt(context.Context, string) (time.Time, bool, error) {
	return store.lastRun, store.hasLastRun, nil
}
func (store *recallAuditStoreFixture) LoadSamples(
	context.Context, string, time.Time, int, string, int,
) ([]QueryVectorSample, error) {
	return append([]QueryVectorSample(nil), store.samples...), nil
}
func (store *recallAuditStoreFixture) SearchANN(
	context.Context, QueryVectorSample, int, int,
) ([]askdata.ID, time.Duration, error) {
	store.annCalls++
	return append([]askdata.ID(nil), store.ann...), 9 * time.Millisecond, nil
}
func (store *recallAuditStoreFixture) SearchExact(
	context.Context, QueryVectorSample, int,
) ([]askdata.ID, time.Duration, error) {
	store.exactCalls++
	return append([]askdata.ID(nil), store.exact...), 25 * time.Millisecond, nil
}
func (store *recallAuditStoreFixture) SaveRecallAudits(
	_ context.Context, _ string, results []RecallAuditResult,
) error {
	store.saved = append([]RecallAuditResult(nil), results...)
	return nil
}
func (store *recallAuditStoreFixture) PurgeSamplesBefore(
	_ context.Context, _ string, before time.Time,
) error {
	store.purgedBefore = before
	return nil
}

func TestRecallAtKComputesExactIntersectionAndRejectsDuplicates(t *testing.T) {
	exact := recallIDs(0, 10)
	ann := append(append([]askdata.ID(nil), exact[:7]...), recallIDs(20, 3)...)
	recall, err := RecallAtK(ann, exact, 10)
	if err != nil || recall != 0.7 {
		t.Fatalf("recall@10 = %v, %v", recall, err)
	}
	duplicated := append([]askdata.ID{exact[0]}, exact...)
	if _, err := RecallAtK(duplicated, exact, 10); err == nil {
		t.Fatal("duplicate ANN IDs must fail closed")
	}
}

func TestRecallAuditorDetectsDegradedANNWithoutChangingConfiguration(t *testing.T) {
	runAt := time.Date(2026, 8, 7, 16, 0, 0, 0, time.UTC)
	tenantID := "22222222-2222-4222-8222-222222222222"
	domainID := "44444444-4444-4444-8444-444444444444"
	exact := recallIDs(0, 30)
	ann := append(append([]askdata.ID(nil), exact[:5]...), recallIDs(100, 25)...)
	store := &recallAuditStoreFixture{
		samples: []QueryVectorSample{{
			ID: uuid.NewString(), TenantID: tenantID, DomainID: domainID,
			Release: askdata.ReleaseRef{
				ReleaseID:   "11111111-1111-4111-8111-111111111111",
				ContentHash: askdata.HashBytes([]byte("release")),
			},
			DocumentType: ObjectMetric, Embedding: make([]float32, SearchEmbeddingDimension),
			EmbeddingModel: "Qwen3-Embedding-4B", EmbeddingDimension: SearchEmbeddingDimension,
			CapturedAt: runAt.Add(-time.Hour),
		}},
		ann: ann, exact: exact,
	}
	alerts := []RecallAuditResult{}
	auditor, err := NewRecallAuditor(
		store, DefaultRecallAuditOptions("Qwen3-Embedding-4B", SearchEmbeddingDimension),
		func(_ context.Context, result RecallAuditResult) { alerts = append(alerts, result) },
	)
	if err != nil {
		t.Fatal(err)
	}
	results, err := auditor.RunTenant(context.Background(), tenantID, runAt)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 || len(store.saved) != 3 || len(alerts) != 3 ||
		store.annCalls != 1 || store.exactCalls != 1 {
		t.Fatalf("results=%#v saved=%d alerts=%d calls=%d/%d",
			results, len(store.saved), len(alerts), store.annCalls, store.exactCalls)
	}
	want := map[int]float64{10: 0.5, 20: 0.25, 30: 5.0 / 30.0}
	for _, result := range results {
		if result.Recall != want[result.K] || !result.BelowThreshold ||
			result.P95LatencyANN != 9*time.Millisecond ||
			result.P95LatencyExact != 25*time.Millisecond || result.EFSearch != 100 {
			t.Fatalf("result = %#v", result)
		}
	}
	if !store.purgedBefore.Equal(runAt.Add(-DefaultRecallSampleRetention)) {
		t.Fatalf("purged before %s", store.purgedBefore)
	}
}

func TestRecallAuditorSkipsUntilIntervalExpires(t *testing.T) {
	runAt := time.Date(2026, 8, 7, 16, 0, 0, 0, time.UTC)
	store := &recallAuditStoreFixture{lastRun: runAt.Add(-time.Hour), hasLastRun: true}
	auditor, err := NewRecallAuditor(
		store, DefaultRecallAuditOptions("Qwen3-Embedding-4B", SearchEmbeddingDimension), nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	results, err := auditor.RunTenant(
		context.Background(), "22222222-2222-4222-8222-222222222222", runAt,
	)
	if err != nil || len(results) != 0 || store.annCalls != 0 || store.exactCalls != 0 {
		t.Fatalf("results=%#v error=%v", results, err)
	}
}

func TestEmbeddingVectorRoundTripParser(t *testing.T) {
	values := make([]float32, SearchEmbeddingDimension)
	values[0], values[1], values[len(values)-1] = 0.5, -0.25, 1
	parsed, err := parseEmbeddingVector(formatEmbeddingVector(values), len(values))
	if err != nil || parsed[0] != 0.5 || parsed[1] != -0.25 || parsed[len(parsed)-1] != 1 {
		t.Fatalf("parsed vector = %v/%v/%v, %v", parsed[0], parsed[1], parsed[len(parsed)-1], err)
	}
}

func recallIDs(offset, count int) []askdata.ID {
	result := make([]askdata.ID, count)
	for index := range result {
		result[index] = askdata.ID(fmt.Sprintf("00000000-0000-4000-8000-%012d", offset+index+1))
	}
	return result
}
