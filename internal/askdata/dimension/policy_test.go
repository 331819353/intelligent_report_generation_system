package dimension

import (
	"testing"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/registry"
)

func TestProfileMathHashAndBudgetValidation(t *testing.T) {
	previous := int64(100)
	profile, err := NewProfile(Profile{
		TenantID: "tenant-1", DomainID: "domain-1", DimensionVersionID: "dimension-region-v1",
		Generation: 2, SourceSnapshotHash: askdata.HashBytes([]byte("snapshot")),
		Sensitivity: registry.SensitivityInternal,
		RowCount:    1_000, NullCount: 100, DistinctCount: 110,
		PreviousDistinctCount: &previous, AddedDistinctCount: 20, RemovedDistinctCount: 10,
		ReservedValues: []ReservedValueObservation{{
			Code: "UNKNOWN", NormalizedValueHash: askdata.HashBytes([]byte("unknown")), Count: 50,
		}},
		Budget: ScanBudget{MaxRows: 10_000, MaxDistinctValues: 1_000, MaxSampleBytes: 1 << 20, StatementTimeoutMS: 5_000},
		Usage:  ScanUsage{RowsScanned: 1_000, DistinctCaptured: 110, SampleBytes: 4_096},
	})
	if err != nil {
		t.Fatalf("NewProfile() error = %v", err)
	}
	if rate, comparable := profile.ChangeRate(); !comparable || rate != 0.25 {
		t.Fatalf("ChangeRate() = %v,%v", rate, comparable)
	}
	if profile.NullRatio() != 0.1 || profile.ReservedRatio() != float64(50)/900 {
		t.Fatalf("ratios = null %v reserved %v", profile.NullRatio(), profile.ReservedRatio())
	}
	profile.NullCount++
	if err := profile.Validate(); err == nil {
		t.Fatal("tampered profile must fail its content hash")
	}
}

func TestDecidePolicyCoversSensitivityCardinalityCompletenessAndStability(t *testing.T) {
	config := DefaultPolicyConfig()
	for _, test := range []struct {
		name       string
		mutate     func(*Profile)
		wantPolicy registry.MemberIndexPolicy
		wantReason string
		wantEmbed  bool
	}{
		{name: "low stable public", wantPolicy: registry.MemberIndexFull, wantReason: ReasonLowStableCardinality, wantEmbed: true},
		{name: "confidential exact only", mutate: func(profile *Profile) { profile.Sensitivity = registry.SensitivityConfidential }, wantPolicy: registry.MemberIndexExactOnly, wantReason: ReasonConfidentialSensitivity},
		{name: "restricted none", mutate: func(profile *Profile) { profile.Sensitivity = registry.SensitivityRestricted }, wantPolicy: registry.MemberIndexNone, wantReason: ReasonRestrictedSensitivity},
		{name: "high cardinality on demand", mutate: func(profile *Profile) { profile.HighCardinalityHint = true }, wantPolicy: registry.MemberIndexOnDemand, wantReason: ReasonHighCardinality},
		{name: "sensitive high cardinality none", mutate: func(profile *Profile) {
			profile.Sensitivity = registry.SensitivityConfidential
			profile.HighCardinalityHint = true
		}, wantPolicy: registry.MemberIndexNone, wantReason: ReasonSensitiveHighCardinality},
		{name: "incomplete scan exact only", mutate: func(profile *Profile) { profile.Usage.Truncated = true; profile.Usage.DistinctCaptured-- }, wantPolicy: registry.MemberIndexExactOnly, wantReason: ReasonIncompleteScan},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := baseProfileInput()
			if test.mutate != nil {
				test.mutate(&input)
			}
			profile, err := NewProfile(input)
			if err != nil {
				t.Fatal(err)
			}
			decision, err := DecidePolicy(profile, config)
			if err != nil {
				t.Fatal(err)
			}
			if decision.RecommendedPolicy != test.wantPolicy || decision.EligibleForEmbedding != test.wantEmbed || !containsReason(decision.ReasonCodes, test.wantReason) {
				t.Fatalf("decision = %#v", decision)
			}
		})
	}
}

func baseProfileInput() Profile {
	previous := int64(100)
	return Profile{
		TenantID: "tenant-1", DomainID: "domain-1", DimensionVersionID: "dimension-region-v1",
		Generation: 2, SourceSnapshotHash: askdata.HashBytes([]byte("snapshot")),
		Sensitivity: registry.SensitivityPublic,
		RowCount:    10_000, NullCount: 100, DistinctCount: 105,
		PreviousDistinctCount: &previous, AddedDistinctCount: 5,
		Budget: ScanBudget{MaxRows: 20_000, MaxDistinctValues: 20_000, MaxSampleBytes: 1 << 20, StatementTimeoutMS: 5_000},
		Usage:  ScanUsage{RowsScanned: 10_000, DistinctCaptured: 105, SampleBytes: 8_192},
	}
}

func containsReason(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
