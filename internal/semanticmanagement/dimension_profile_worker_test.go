package semanticmanagement

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeDimensionProfileStore struct {
	mu               sync.Mutex
	claim            *DimensionProfileJob
	observation      DimensionProfileObservation
	measure          func(context.Context) error
	heartbeatErr     error
	heartbeatStarted chan struct{}
	completed        bool
	failedCode       string
}

func (s *fakeDimensionProfileStore) ListProfileTenantIDs(context.Context) ([]string, error) {
	return []string{testTenantID}, nil
}

func (s *fakeDimensionProfileStore) ClaimDimensionProfile(
	context.Context,
	string,
	string,
	time.Duration,
) (*DimensionProfileJob, error) {
	return s.claim, nil
}

func (s *fakeDimensionProfileStore) HeartbeatDimensionProfile(
	context.Context,
	DimensionProfileJob,
	time.Duration,
) (DimensionProfileJob, error) {
	if s.heartbeatStarted != nil {
		select {
		case <-s.heartbeatStarted:
		default:
			close(s.heartbeatStarted)
		}
	}
	if s.heartbeatErr != nil {
		return DimensionProfileJob{}, s.heartbeatErr
	}
	return *s.claim, nil
}

func (s *fakeDimensionProfileStore) MeasureDimensionProfile(
	ctx context.Context,
	_ DimensionProfileJob,
) (DimensionProfileObservation, error) {
	if s.measure != nil {
		if err := s.measure(ctx); err != nil {
			return DimensionProfileObservation{}, err
		}
	}
	return s.observation, nil
}

func (s *fakeDimensionProfileStore) CompleteDimensionProfile(
	context.Context,
	DimensionProfileJob,
	DimensionProfileObservation,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completed = true
	return nil
}

func (s *fakeDimensionProfileStore) FailDimensionProfile(
	_ context.Context,
	_ DimensionProfileJob,
	code string,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failedCode = code
	return nil
}

func TestDimensionProfileWorkerCompletesAggregateOnlyObservation(t *testing.T) {
	store := &fakeDimensionProfileStore{
		claim: testDimensionProfileClaim(),
		observation: DimensionProfileObservation{
			RowCount: 100, NonNullCount: 90, NullCount: 10,
			DistinctCount: 12, DistinctRatio: 12.0 / 90.0,
		},
	}
	processed, err := NewDimensionProfileWorker(store).ProcessNext(
		context.Background(), testTenantID, "dimension-profile-test", 3*time.Second,
	)
	if err != nil || !processed || !store.completed || store.failedCode != "" {
		t.Fatalf(
			"processed=%v completed=%v failed=%q err=%v",
			processed, store.completed, store.failedCode, err,
		)
	}
}

func TestDimensionProfileWorkerCancelsWithoutTerminalWriteAfterLeaseLoss(t *testing.T) {
	store := &fakeDimensionProfileStore{
		claim:            testDimensionProfileClaim(),
		heartbeatErr:     ErrProfileLeaseLost,
		heartbeatStarted: make(chan struct{}),
		measure: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}
	worker := NewDimensionProfileWorker(store)
	worker.heartbeatInterval = func(time.Duration) time.Duration {
		return time.Millisecond
	}
	processed, err := worker.ProcessNext(
		context.Background(), testTenantID, "dimension-profile-test", 3*time.Second,
	)
	if !processed || !errors.Is(err, ErrProfileLeaseLost) ||
		store.completed || store.failedCode != "" {
		t.Fatalf(
			"processed=%v completed=%v failed=%q err=%v",
			processed, store.completed, store.failedCode, err,
		)
	}
}

func TestDimensionProfileWorkerMapsTimeoutToStableCode(t *testing.T) {
	claim := testDimensionProfileClaim()
	claim.TimeoutSeconds = 1
	store := &fakeDimensionProfileStore{
		claim: claim,
		measure: func(context.Context) error {
			return context.DeadlineExceeded
		},
	}
	processed, err := NewDimensionProfileWorker(store).ProcessNext(
		context.Background(), testTenantID, "dimension-profile-test", 3*time.Second,
	)
	if !processed || !errors.Is(err, context.DeadlineExceeded) ||
		store.completed || store.failedCode != "PROFILE_TIMEOUT" {
		t.Fatalf(
			"processed=%v completed=%v failed=%q err=%v",
			processed, store.completed, store.failedCode, err,
		)
	}
}

func testDimensionProfileClaim() *DimensionProfileJob {
	return &DimensionProfileJob{
		DimensionProfile: DimensionProfile{
			ID:     "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee",
			Status: "RUNNING", ProfileVersion: DimensionProfileVersion,
			PolicyVersion:               DimensionPolicyVersion,
			MaterializationID:           "ffffffff-ffff-4fff-8fff-ffffffffffff",
			MaterializationSnapshotHash: strings64("a"),
			ExpectedRowCount:            100,
			DistinctCap:                 100000,
			Attempt:                     1,
			MaxAttempts:                 3,
		},
		TenantID: testTenantID, DatasetID: testDatasetID,
		DatasetVersionID: testVersionID, SchemaHash: strings64("b"),
		FieldID: "field_region", FieldCode: "region_code",
		FieldRole: "DIMENSION", CanonicalType: "STRING",
		HighRatioThreshold: 0.2, HighRatioMinNonNull: 10000,
		TimeoutSeconds: 60, WorkMemKB: 16384, TempFileLimitKB: 262144,
		RequestedBy: testActorID, LeaseOwner: "dimension-profile-test",
		LeaseToken:     "abababab-abab-4bab-8bab-abababababab",
		LeaseExpiresAt: time.Now().Add(time.Minute),
	}
}

func strings64(character string) string {
	result := ""
	for range 64 {
		result += character
	}
	return result
}
