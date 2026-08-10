package validator

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/materialization"
)

func TestDetermineOutcomePartialConditionsP1ThroughP6(t *testing.T) {
	tests := []struct {
		name    string
		context OutcomeContext
		assert  func(*testing.T, OutcomeEvidence)
	}{
		{
			name:    "P1 time range truncated",
			context: OutcomeContext{Coverage: outcomeTruncatedCoverage(t)},
			assert: func(t *testing.T, evidence OutcomeEvidence) {
				if !evidence.TimeRangeTruncated {
					t.Fatal("timeRangeTruncated was not set")
				}
			},
		},
		{
			name: "P2 metrics filtered by permission",
			context: OutcomeContext{MetricAuthorization: &MetricAuthorizationEvidence{
				RequestedCount: 4, AuthorizedCount: 2,
			}},
			assert: func(t *testing.T, evidence OutcomeEvidence) {
				if evidence.MetricsFilteredByPermission != 2 {
					t.Fatalf("metricsFilteredByPermission = %d", evidence.MetricsFilteredByPermission)
				}
			},
		},
		{
			name: "P3 bundle plans failed and timed out",
			context: OutcomeContext{Bundle: &BundleOutcomeEvidence{
				TotalPlans: 3,
				FailedPlans: []FailedPlanEvidence{
					{PlanID: "p3", FailureCode: "PLAN_TIMEOUT"},
					{PlanID: "p2", FailureCode: "PLAN_EXECUTION_FAILED"},
				},
			}},
			assert: func(t *testing.T, evidence OutcomeEvidence) {
				if len(evidence.FailedPlans) != 2 || evidence.FailedPlans[0].PlanID != "p2" ||
					evidence.FailedPlans[1].FailureCode != "PLAN_TIMEOUT" {
					t.Fatalf("failedPlans = %#v", evidence.FailedPlans)
				}
			},
		},
		{
			name: "P4 row limit applied",
			context: OutcomeContext{RowLimit: &RowLimitEvidence{
				Limit: 500, ReturnedRows: 500, Truncated: true,
			}},
			assert: func(t *testing.T, evidence OutcomeEvidence) {
				if !evidence.RowLimitApplied {
					t.Fatal("rowLimitApplied was not set")
				}
			},
		},
		{
			name: "P5 members filtered by policy",
			context: OutcomeContext{MemberPolicy: &MemberPolicyEvidence{
				EvaluatedCount: 12, FilteredCount: 3,
			}},
			assert: func(t *testing.T, evidence OutcomeEvidence) {
				if evidence.MembersFilteredByPolicy != 3 {
					t.Fatalf("membersFilteredByPolicy = %d", evidence.MembersFilteredByPolicy)
				}
			},
		},
		{
			name: "P6 sources timed out",
			context: OutcomeContext{MultiSource: &MultiSourceOutcomeEvidence{
				TotalSources: 3, TimedOut: []askdata.ID{"source:z", "source:a"},
			}},
			assert: func(t *testing.T, evidence OutcomeEvidence) {
				if len(evidence.SourcesTimedOut) != 2 || evidence.SourcesTimedOut[0] != "source:a" ||
					evidence.SourcesTimedOut[1] != "source:z" {
					t.Fatalf("sourcesTimedOut = %#v", evidence.SourcesTimedOut)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			outcome := DetermineOutcome(test.context)
			if outcome.Status != OutcomePartial || outcome.Validate() != nil {
				t.Fatalf("outcome = %#v", outcome)
			}
			test.assert(t, outcome.Evidence)
		})
	}
}

func TestDetermineOutcomeQualityWarningIsOrthogonalToPartial(t *testing.T) {
	quality := outcomeWarningQuality()
	warningOnly := DetermineOutcome(OutcomeContext{Quality: &quality})
	if warningOnly.Status != OutcomeQualityWarning || len(warningOnly.Evidence.QualityWarnings) != 1 ||
		warningOnly.Validate() != nil {
		t.Fatalf("warning-only outcome = %#v", warningOnly)
	}

	combined := DetermineOutcome(OutcomeContext{
		Coverage: outcomeTruncatedCoverage(t), Quality: &quality,
	})
	if combined.Status != OutcomePartial || !combined.Evidence.TimeRangeTruncated ||
		len(combined.Evidence.QualityWarnings) != 1 || combined.Validate() != nil {
		t.Fatalf("combined outcome = %#v", combined)
	}
}

func TestDetermineOutcomeP2CannotLeakMetricNames(t *testing.T) {
	outcome := DetermineOutcome(OutcomeContext{MetricAuthorization: &MetricAuthorizationEvidence{
		RequestedCount: 3, AuthorizedCount: 1,
	}})
	raw, err := json.Marshal(outcome)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"gross_margin", "secret metric", "metricName", "metricId"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("outcome leaked %q: %s", forbidden, raw)
		}
	}
	if !strings.Contains(string(raw), `"metricsFilteredByPermission":2`) {
		t.Fatalf("count-only P2 evidence missing: %s", raw)
	}
}

func TestDetermineOutcomeFailsClosedForContradictorySubsets(t *testing.T) {
	tests := []OutcomeContext{
		{MetricAuthorization: &MetricAuthorizationEvidence{RequestedCount: 2, AuthorizedCount: 0}},
		{Bundle: &BundleOutcomeEvidence{TotalPlans: 2, FailedPlans: []FailedPlanEvidence{
			{PlanID: "p1", FailureCode: "PLAN_TIMEOUT"}, {PlanID: "p2", FailureCode: "PLAN_TIMEOUT"},
		}}},
		{RowLimit: &RowLimitEvidence{Limit: 100, ReturnedRows: 99, Truncated: true}},
		{MemberPolicy: &MemberPolicyEvidence{EvaluatedCount: 3, FilteredCount: 3}},
		{MultiSource: &MultiSourceOutcomeEvidence{TotalSources: 2, TimedOut: []askdata.ID{"a", "b"}}},
	}
	for index, context := range tests {
		if outcome := DetermineOutcome(context); outcome.SchemaVersion != "" || outcome.Status != "" ||
			outcome.OutcomeHash != "" {
			t.Fatalf("case %d did not fail closed: %#v", index, outcome)
		}
	}
}

func outcomeTruncatedCoverage(t *testing.T) *CoverageVerdict {
	t.Helper()
	location := coverageLocation(t)
	watermark := time.Date(2026, time.August, 5, 18, 0, 0, 0, location)
	verdict := EvaluateCoverage(coverageSpec(location, 1, 11), materialization.MaterializationMeta{
		MaterializationID: "materialization:sales", DataAvailableThrough: &watermark,
	})
	if verdict.Validate() != nil || verdict.Relation != CoverageTruncated {
		t.Fatalf("coverage fixture = %#v", verdict)
	}
	return &verdict
}

func outcomeWarningQuality() QualityEvidence {
	base := askdata.EvidenceRef{
		EvidenceID: "evidence:quality", Kind: askdata.EvidenceKindDataQuality,
		SourceID: "quality:orders", ContentHash: askdata.HashBytes([]byte("quality")),
	}
	check := askdata.EvidenceRef{
		EvidenceID: "evidence:freshness", Kind: askdata.EvidenceKindRule,
		SourceID: "rule:freshness", ContentHash: askdata.HashBytes([]byte("freshness")),
	}
	return QualityEvidence{
		Status: QualityWarning, Evidence: base,
		Checks: []QualityCheckEvidence{{
			Code: "FRESHNESS_NEAR_LIMIT", Severity: RuleWarning, Passed: false, Evidence: check,
		}},
	}
}
