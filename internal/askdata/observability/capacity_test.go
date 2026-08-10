package observability

import (
	"strings"
	"testing"
	"time"
)

func TestCapacityReportRequiresEveryGovernedScenario(t *testing.T) {
	report := completeCapacityReport()
	if err := ValidateCapacityReport(report); err != nil {
		t.Fatal(err)
	}
	report.Scenarios = report.Scenarios[:len(report.Scenarios)-1]
	if err := ValidateCapacityReport(report); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing scenario must fail: %v", err)
	}
}

func TestCapacityAlertsCoverScaleLatencyRecallAndSuccess(t *testing.T) {
	report := completeCapacityReport()
	report.Scale.Tenants = 51
	report.Scale.GraphEdges = 10_000_001
	report.Scenarios[0].P95MS = 9000
	report.Scenarios[0].P99MS = 9000
	report.Scenarios[0].MaxMS = 9000
	report.Scenarios[1].Succeeded = 98
	report.Scenarios[1].Failed = 2
	badRecall := .98
	for index := range report.Scenarios {
		if report.Scenarios[index].Scenario == CapacityVectorRecall {
			report.Scenarios[index].RecallAtK = &badRecall
		}
	}
	alerts, err := EvaluateCapacityAlerts(report)
	if err != nil {
		t.Fatal(err)
	}
	codes := make(map[string]bool, len(alerts))
	for _, alert := range alerts {
		codes[alert.Code] = true
	}
	for _, code := range []string{
		"CAPACITY_TENANT_PARTITION_REVIEW", "CAPACITY_GRAPH_REPLICA_REVIEW",
		"CAPACITY_FAST_PATH_P95", "CAPACITY_SUCCESS_RATE", "CAPACITY_VECTOR_RECALL_RECALL",
	} {
		if !codes[code] {
			t.Fatalf("missing capacity alert %s: %+v", code, alerts)
		}
	}
}

func TestPercentileMillisecondsUsesNearestRank(t *testing.T) {
	samples := make([]time.Duration, 100)
	for index := range samples {
		samples[index] = time.Duration(index+1) * time.Millisecond
	}
	p95, err := PercentileMilliseconds(samples, .95)
	if err != nil || p95 != 95 {
		t.Fatalf("unexpected p95: %d err=%v", p95, err)
	}
}

func completeCapacityReport() CapacityReport {
	recall := .995
	report := CapacityReport{
		SchemaVersion: CapacityReportSchemaVersion, RunID: "capacity-run-1", Seed: 42,
		StartedAt: time.Date(2026, 8, 10, 1, 0, 0, 0, time.UTC), DurationMS: 1000,
		Environment: CapacityEnvironment{
			GOOS: "linux", GOARCH: "arm64", LogicalCPUs: 8, GoVersion: "go1.25",
			ConfigHash: strings.Repeat("a", 64), TargetOrigin: "http://service.invalid",
			DatabaseLabel: "postgres-16-pgvector", GraphLabel: "nebula-3.8.0", LLMLabel: "fault-proxy-v1",
		},
	}
	for _, scenario := range requiredCapacityScenarios {
		result := CapacityScenarioResult{
			Scenario: scenario, Requests: 100, Succeeded: 100,
			P50MS: 1, P95MS: 2, P99MS: 3, MaxMS: 4,
			Thresholds:      CapacityThresholds{MaxP95MS: 8000, MinSuccessRate: .99},
			FailureCodeHash: strings.Repeat("b", 64),
		}
		if scenario == CapacityVectorRecall {
			result.RecallAtK = &recall
			result.Thresholds.MinRecallAtK = .99
		}
		report.Scenarios = append(report.Scenarios, result)
	}
	return report
}
