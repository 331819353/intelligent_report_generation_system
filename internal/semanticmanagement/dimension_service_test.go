package semanticmanagement

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

const (
	testDimensionID       = "88888888-8888-4888-8888-888888888888"
	testDimensionMemberID = "99999999-9999-4999-8999-999999999999"
	testMetricID          = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	testMetricVersionID   = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
)

type fakeDimensionStore struct {
	aliasInput      CreateDimensionMemberAliasInput
	aliasNormalized string
	compatibility   ProposeCompatibilityInput
	decision        string
	refreshPrepared PreparedRefreshJob
	refreshCreated  bool
	searchQuery     string
	readActorID     string
	searchResults   []MemberMetricSearchResult
	err             error
}

func (s *fakeDimensionStore) ListDimensions(context.Context, string, DimensionFilter) ([]Dimension, int, error) {
	return []Dimension{}, 0, s.err
}
func (s *fakeDimensionStore) GetDimension(context.Context, string, string) (Dimension, error) {
	return Dimension{
		ID: testDimensionID, DatasetID: testDatasetID, DatasetVersionID: testVersionID,
		FieldID: "circle_code", Version: 1,
	}, s.err
}
func (s *fakeDimensionStore) CreateDimension(context.Context, string, string, PreparedDimension) (Dimension, error) {
	return Dimension{ID: testDimensionID}, s.err
}
func (s *fakeDimensionStore) UpdateDimension(context.Context, string, string, string, int64, PreparedDimension) (Dimension, error) {
	return Dimension{ID: testDimensionID}, s.err
}
func (s *fakeDimensionStore) DeprecateDimension(context.Context, string, string, string, int64) (Dimension, error) {
	return Dimension{ID: testDimensionID, Status: "DEPRECATED"}, s.err
}
func (s *fakeDimensionStore) ListDimensionMembers(_ context.Context, _, actorID string, _ DimensionMemberFilter) ([]DimensionMember, int, error) {
	s.readActorID = actorID
	return []DimensionMember{}, 0, s.err
}
func (s *fakeDimensionStore) ListDimensionMemberAliases(_ context.Context, _, actorID string, _ DimensionMemberAliasFilter) ([]DimensionMemberAlias, int, error) {
	s.readActorID = actorID
	return []DimensionMemberAlias{}, 0, s.err
}
func (s *fakeDimensionStore) CreateDimensionMemberAlias(
	_ context.Context, _, _ string, input CreateDimensionMemberAliasInput, normalized string,
) (DimensionMemberAlias, error) {
	s.aliasInput, s.aliasNormalized = input, normalized
	return DimensionMemberAlias{
		ID: testAliasID, DimensionID: input.DimensionID,
		DimensionMemberID: input.DimensionMemberID, Alias: input.Alias,
		NormalizedAlias: normalized, AliasType: input.AliasType, Version: 1,
	}, s.err
}
func (s *fakeDimensionStore) UpdateDimensionMemberAlias(
	context.Context, string, string, string, UpdateDimensionMemberAliasInput, string,
) (DimensionMemberAlias, error) {
	return DimensionMemberAlias{ID: testAliasID}, s.err
}
func (s *fakeDimensionStore) DeleteDimensionMemberAlias(context.Context, string, string, string, int64) error {
	return s.err
}
func (s *fakeDimensionStore) ListCompatibilities(context.Context, string, CompatibilityFilter) ([]DimensionMetricCompatibility, int, error) {
	return []DimensionMetricCompatibility{}, 0, s.err
}
func (s *fakeDimensionStore) ProposeCompatibility(
	_ context.Context, _, _ string, input ProposeCompatibilityInput,
) (DimensionMetricCompatibility, error) {
	s.compatibility = input
	return DimensionMetricCompatibility{ID: testBindingID, Status: "PROPOSED"}, s.err
}
func (s *fakeDimensionStore) UpdateCompatibility(
	context.Context, string, string, string, UpdateCompatibilityInput,
) (DimensionMetricCompatibility, error) {
	return DimensionMetricCompatibility{ID: testBindingID, Status: "PROPOSED"}, s.err
}
func (s *fakeDimensionStore) DecideCompatibility(
	_ context.Context, _, _, _ string, _ int64, decision string,
) (DimensionMetricCompatibility, error) {
	s.decision = decision
	return DimensionMetricCompatibility{ID: testBindingID, Status: decision}, s.err
}
func (s *fakeDimensionStore) CreateRefreshJob(
	_ context.Context, _, _ string, prepared PreparedRefreshJob,
) (RefreshJob, bool, error) {
	s.refreshPrepared = prepared
	return RefreshJob{
		ID: testBindingID, DimensionID: prepared.DimensionID,
		DimensionVersion: prepared.ExpectedDimensionVersion, Status: "QUEUED",
		MaxMembers: prepared.MaxMembers, TimeoutSeconds: prepared.TimeoutSeconds,
	}, s.refreshCreated, s.err
}
func (s *fakeDimensionStore) ListRefreshJobs(context.Context, string, RefreshJobFilter) ([]RefreshJob, int, error) {
	return []RefreshJob{}, 0, s.err
}
func (s *fakeDimensionStore) SearchMemberMetrics(
	_ context.Context, _, actorID, query string, _ int,
) ([]MemberMetricSearchResult, error) {
	s.readActorID = actorID
	s.searchQuery = query
	return s.searchResults, s.err
}

func TestDimensionServiceTreats690AsGovernedLegacyMemberAlias(t *testing.T) {
	store := &fakeDimensionStore{}
	item, err := NewDimensionService(store).CreateDimensionMemberAlias(
		context.Background(), testTenantID, testActorID,
		CreateDimensionMemberAliasInput{
			DimensionID: testDimensionID, DimensionMemberID: testDimensionMemberID,
			Alias: "690", AliasType: "legacy",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if item.Alias != "690" || store.aliasInput.AliasType != "LEGACY" ||
		store.aliasNormalized != "690" {
		t.Fatalf("legacy member alias was not preserved as data: item=%+v input=%+v normalized=%q",
			item, store.aliasInput, store.aliasNormalized)
	}
}

func TestDimensionServicePreparesBoundedIdempotentRefresh(t *testing.T) {
	store := &fakeDimensionStore{refreshCreated: true}
	item, created, err := NewDimensionService(store).CreateRefreshJob(
		context.Background(), testTenantID, testActorID, testDimensionID,
		"client-request-1", CreateRefreshJobInput{ExpectedDimensionVersion: 7},
	)
	if err != nil {
		t.Fatal(err)
	}
	prepared := store.refreshPrepared
	if !created || item.MaxMembers != defaultRefreshMaxMembers ||
		prepared.MaxMembers != defaultRefreshMaxMembers ||
		prepared.TimeoutSeconds != defaultRefreshTimeout ||
		len(prepared.RequestHash) != 64 || len(prepared.IdempotencyKey) != 64 {
		t.Fatalf("unexpected refresh preparation: item=%+v prepared=%+v", item, prepared)
	}

	_, _, err = NewDimensionService(store).CreateRefreshJob(
		context.Background(), testTenantID, testActorID, testDimensionID,
		"", CreateRefreshJobInput{ExpectedDimensionVersion: 7},
	)
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("missing idempotency key error=%v", err)
	}
}

func TestDimensionServiceForbidsFullIndexForSensitiveDimension(t *testing.T) {
	service := NewDimensionService(&fakeDimensionStore{})
	input := CreateDimensionInput{
		DatasetID: testDatasetID, DatasetVersionID: testVersionID,
		FieldID: "customer_phone", Code: "customer_phone", Name: "客户手机号",
		DimensionType: "CUSTOMER", MemberIndexPolicy: "FULL",
		Sensitive: true, Status: "DRAFT",
	}

	if _, err := service.CreateDimension(
		context.Background(), testTenantID, testActorID, input,
	); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("sensitive FULL dimension error=%v", err)
	}

	input.MemberIndexPolicy = "EXACT_ONLY"
	if _, err := service.CreateDimension(
		context.Background(), testTenantID, testActorID, input,
	); err != nil {
		t.Fatalf("sensitive exact-only dimension was rejected: %v", err)
	}
}

func TestDimensionServiceRejectsExecutableJoinEvidenceAndNormalizesDecision(t *testing.T) {
	store := &fakeDimensionStore{}
	service := NewDimensionService(store)
	_, err := service.ProposeCompatibility(
		context.Background(), testTenantID, testActorID,
		ProposeCompatibilityInput{
			DimensionID: testDimensionID, MetricID: testMetricID,
			MetricVersionID: testMetricVersionID, MetricDatasetVersionID: testVersionID,
			CompatibilityType: "direct", FanoutPolicy: "safe",
			JoinPath: []byte(`[{"sql":"select * from secret"}]`), EvidenceSource: "human",
		},
	)
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("executable join evidence error=%v", err)
	}
	_, err = service.DecideCompatibility(
		context.Background(), testTenantID, testActorID, testBindingID, 1, "verified",
	)
	if err != nil || store.decision != "VERIFIED" {
		t.Fatalf("decision=%q err=%v", store.decision, err)
	}
}

type fakeDimensionRefreshStore struct {
	claim       *DimensionRefreshClaim
	refreshErr  error
	failedCode  string
	failedMsg   string
	failContext error
}

func (s *fakeDimensionRefreshStore) ListRefreshTenantIDs(context.Context) ([]string, error) {
	return []string{testTenantID}, nil
}
func (s *fakeDimensionRefreshStore) ClaimDimensionRefresh(
	context.Context, string, string, time.Duration,
) (*DimensionRefreshClaim, error) {
	return s.claim, nil
}
func (s *fakeDimensionRefreshStore) RefreshDimensionMembers(
	context.Context, DimensionRefreshClaim, string,
) error {
	return s.refreshErr
}
func (s *fakeDimensionRefreshStore) FailDimensionRefresh(
	ctx context.Context, _ DimensionRefreshClaim, code, message string,
) error {
	s.failedCode, s.failedMsg = code, message
	s.failContext = ctx.Err()
	return nil
}

func TestDimensionRefreshWorkerMapsFailuresWithoutLeakingSourceValues(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code string
	}{
		{"cardinality", ErrRefreshCardinality, "CARDINALITY_LIMIT_EXCEEDED"},
		{"timeout", context.DeadlineExceeded, "REFRESH_TIMEOUT"},
		{"unsafe view", ErrRefreshUnsafeView, "PUBLISHED_VIEW_UNTRUSTED"},
		{"source changed", ErrRefreshSourceChanged, "REFRESH_SOURCE_CHANGED"},
		{"invalid member", ErrRefreshInvalidValue, "MEMBER_VALUE_INVALID"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeDimensionRefreshStore{
				claim: &DimensionRefreshClaim{
					RefreshJob: RefreshJob{
						ID: testBindingID, DimensionID: testDimensionID,
						TimeoutSeconds: 10, Attempt: 1, MaxAttempts: 3,
					},
					TenantID: testTenantID, LeaseOwner: "worker-1",
				},
				refreshErr: test.err,
			}
			processed, err := NewDimensionRefreshWorker(store).ProcessNext(
				context.Background(), testTenantID, "worker-1", time.Minute,
			)
			if !processed || !errors.Is(err, test.err) || store.failedCode != test.code ||
				store.failedMsg != "dimension member refresh failed" || store.failContext != nil {
				t.Fatalf("processed=%v err=%v code=%q message=%q failContext=%v",
					processed, err, store.failedCode, store.failedMsg, store.failContext)
			}
		})
	}
}

func TestDimensionRefreshClassifiesDatabaseTimeouts(t *testing.T) {
	for _, code := range []string{"57014", "55P03"} {
		if err := classifyRefreshDatabaseError(
			&pgconn.PgError{Code: code},
		); !errors.Is(err, ErrRefreshTimeout) {
			t.Fatalf("database code %s classified as %v", code, err)
		}
	}
}
