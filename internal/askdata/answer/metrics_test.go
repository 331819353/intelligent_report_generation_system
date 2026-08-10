package answer

import (
	"math"
	"testing"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"intelligent-report-generation-system/internal/askdata"
)

func TestNarrativeMetricsUseSharedSafeDenominatorAndStableCodes(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewNarrativeMetrics(registry)
	if err != nil {
		t.Fatal(err)
	}
	domainID := askdata.ID(uuid.NewString())
	empty, err := metrics.Snapshot(domainID, NarrativeRunAskData)
	if err != nil || empty.VerificationFailureRate != 0 || empty.DegradedRate != 0 ||
		empty.RetryRate != 0 || math.IsNaN(empty.DegradedRate) {
		t.Fatalf("empty snapshot=%#v err=%v", empty, err)
	}
	failed := VerifyReport{Failures: []VerifyFailure{{Reason: AnswerNumberUnverified}}}
	for index := 0; index < 20; index++ {
		input := NarrativeMetricInput{
			DomainID: domainID, RunType: NarrativeRunAskData,
			Reports: []VerifyReport{{Passed: true}},
		}
		if index == 0 {
			input.Reports = []VerifyReport{failed, failed}
			input.Degraded = true
		}
		if index == 1 {
			input.Reports = []VerifyReport{failed, VerifyReport{Passed: true}}
		}
		if _, err := metrics.Record(input); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, _ := metrics.Snapshot(domainID, NarrativeRunAskData)
	if snapshot.GeneratedRuns != 20 || snapshot.TerminalFailureRuns != 1 ||
		snapshot.DegradedRuns != 1 || snapshot.RetriedRuns != 2 ||
		snapshot.VerificationFailureRate != .05 || snapshot.DegradedRate != .05 ||
		snapshot.RetryRate != .1 || snapshot.DegradedAlert ||
		snapshot.FailureByCode[AnswerNumberUnverified] != 3 {
		t.Fatalf("snapshot=%#v", snapshot)
	}
	if got := testutil.ToFloat64(metrics.failureRate.WithLabelValues(string(domainID), string(NarrativeRunAskData))); got != .05 {
		t.Fatalf("failure metric=%v", got)
	}
	if _, err := metrics.Record(NarrativeMetricInput{
		DomainID: domainID, RunType: NarrativeRunAskData, Reports: []VerifyReport{{
			Failures: []VerifyFailure{{Reason: "bad-code"}},
		}},
	}); err == nil {
		t.Fatal("invalid failure code accepted")
	}
}

func TestNarrativeMetricsAlertAboveFivePercentAndSeparateRunTypes(t *testing.T) {
	metrics, err := NewNarrativeMetrics(prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	domainID := askdata.ID(uuid.NewString())
	failed := VerifyReport{Failures: []VerifyFailure{{Reason: AnswerExternalFact}}}
	for index := 0; index < 20; index++ {
		input := NarrativeMetricInput{DomainID: domainID, RunType: NarrativeRunReport, Reports: []VerifyReport{{Passed: true}}}
		if index < 2 {
			input.Reports, input.Degraded = []VerifyReport{failed, failed}, true
		}
		if _, err := metrics.Record(input); err != nil {
			t.Fatal(err)
		}
	}
	report, _ := metrics.Snapshot(domainID, NarrativeRunReport)
	askData, _ := metrics.Snapshot(domainID, NarrativeRunAskData)
	if !report.DegradedAlert || report.DegradedRate != .1 || report.VerificationFailureRate != .1 ||
		askData.GeneratedRuns != 0 {
		t.Fatalf("report=%#v askdata=%#v", report, askData)
	}
}
