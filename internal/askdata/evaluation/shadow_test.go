package evaluation

import (
	"fmt"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"intelligent-report-generation-system/internal/askdata"
)

func TestShadowNeverChangesUserFacingRelease(t *testing.T) {
	config := experimentConfig(ExperimentShadow, 0)
	route, err := RouteExperiment(config, ExperimentSubject{TenantID: config.TenantID, DomainID: config.DomainID, ActorID: "actor-a", Role: "ANALYST"})
	if err != nil {
		t.Fatal(err)
	}
	if route.UserFacingReleaseID != config.ControlReleaseID || route.EvaluationReleaseID != config.CandidateReleaseID || route.ExposeEvaluation {
		t.Fatalf("shadow route = %#v", route)
	}
}

func TestCanaryUsesOnlyGovernedPercentagesAndStableCohorts(t *testing.T) {
	config := experimentConfig(ExperimentCanary, 20)
	candidate := 0
	for index := 0; index < 10_000; index++ {
		subject := ExperimentSubject{TenantID: config.TenantID, DomainID: config.DomainID, ActorID: askdata.ID(fmt.Sprintf("actor-%05d", index)), Role: "ANALYST"}
		left, err := RouteExperiment(config, subject)
		if err != nil {
			t.Fatal(err)
		}
		right, _ := RouteExperiment(config, subject)
		if left != right {
			t.Fatal("cohort changed for the same subject")
		}
		if left.Cohort == CohortCandidate {
			candidate++
		}
	}
	if candidate < 1800 || candidate > 2200 {
		t.Fatalf("candidate count = %d", candidate)
	}
	config.CanaryPercent = 10
	if _, err := RouteExperiment(config, ExperimentSubject{}); err == nil {
		t.Fatal("unsupported canary percentage accepted")
	}
}

func TestExperimentSummaryAndAutomaticStop(t *testing.T) {
	control := ExperimentSummary{ReleaseID: "release-control", DomainID: "domain-a", Role: "ANALYST", Cohort: CohortControl, Count: 100, Accuracy: .98, ClarificationRate: .1, SecurityRate: 1, LatencyP95: time.Second, AverageCostMicros: 100}
	candidate := ExperimentSummary{ReleaseID: "release-candidate", DomainID: "domain-a", Role: "ANALYST", Cohort: CohortCandidate, Count: 100, Accuracy: .90, ClarificationRate: .2, SecurityRate: 1, LatencyP95: 2 * time.Second, AverageCostMicros: 200}
	decision, err := EvaluateAutomaticStop(control, candidate, AutomaticStopThresholds{MinimumSamples: 50, MaximumAccuracyDrop: .02, MaximumClarificationIncrease: .05, MaximumLatencyP95Ratio: 1.5, MaximumCostIncreaseRatio: 1.5})
	if err != nil || !decision.Stop || len(decision.Codes) != 4 {
		t.Fatalf("stop decision = %#v, %v", decision, err)
	}
	candidate.Count = 1
	candidate.SecurityRate = 0
	decision, err = EvaluateAutomaticStop(control, candidate, AutomaticStopThresholds{MinimumSamples: 50, MaximumAccuracyDrop: .02, MaximumClarificationIncrease: .05, MaximumLatencyP95Ratio: 1.5, MaximumCostIncreaseRatio: 1.5})
	if err != nil || !decision.Stop || len(decision.Codes) != 1 || decision.Codes[0] != "CANARY_SECURITY_REGRESSION" {
		t.Fatalf("immediate security stop = %#v, %v", decision, err)
	}
}

func TestExperimentPrometheusMetrics(t *testing.T) {
	registry := prometheus.NewRegistry()
	recorder, err := NewExperimentRecorder(registry)
	if err != nil {
		t.Fatal(err)
	}
	observation := ExperimentObservation{ReleaseID: "release-a", DomainID: "domain-a", Role: "ANALYST", Cohort: CohortCandidate, Accurate: true, SecurityPassed: true, Latency: time.Second, CostMicros: 100}
	if err := recorder.Observe(observation); err != nil {
		t.Fatal(err)
	}
	if value := testutil.ToFloat64(recorder.runs.WithLabelValues("release-a", "domain-a", "ANALYST", "CANDIDATE")); value != 1 {
		t.Fatalf("runs = %v", value)
	}
}

func experimentConfig(mode ExperimentMode, percent int) ExperimentConfig {
	return ExperimentConfig{Mode: mode, TenantID: "tenant-a", DomainID: "domain-a", ControlReleaseID: "release-control", CandidateReleaseID: "release-candidate", CanaryPercent: percent, SaltHash: hashOf("experiment"), Enabled: true}
}
