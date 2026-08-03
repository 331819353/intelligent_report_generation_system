package semanticqa

import (
	"math"
	"testing"
	"time"
)

func TestWilsonLowerBoundRequiresStatisticalMargin(t *testing.T) {
	value := wilsonLowerBound(1920, 2000)
	if math.Abs(value-0.950493) > 0.00001 {
		t.Fatalf("unexpected Wilson lower bound: %.9f", value)
	}
	if evaluationMetric(1900, 2000, 0.95, true).Passed {
		t.Fatal("95%% point estimate must not pass the 95%% Wilson lower-bound gate")
	}
}

func TestCalculateEvaluationReleaseGatePassesOnlyCompleteSealedE2E(t *testing.T) {
	const contentHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	facts := make([]evaluationGateFact, 0, 2020)
	for index := 0; index < 2000; index++ {
		facts = append(facts, evaluationGateFact{
			reviewCount: 2, priority: "P1", answerable: true,
			runID: "run", status: "PASSED", directAnswer: index < 1900,
			semanticVersion: "sem-v7", semanticContentHash: contentHash,
		})
	}
	for index := 0; index < 100; index++ {
		facts[index].priority = "P0"
	}
	for index := 0; index < 20; index++ {
		facts = append(facts, evaluationGateFact{
			reviewCount: 2, priority: "P1", answerable: false,
			securityExpectation: "UNAUTHORIZED_BLOCK", runID: "security-run",
			status: "PASSED", refusal: true, unauthorizedBlocked: true,
			semanticVersion: "sem-v7", semanticContentHash: contentHash,
		})
	}
	gate := calculateEvaluationReleaseGate(EvaluationReleaseGate{
		SetID: "set", SetVersion: 7, DatasetSplit: "SEALED",
		EvaluationMode: "END_TO_END_RESULT_EQUIVALENCE", SetStatus: "ACTIVE",
		SealedContentHash: contentHash,
	}, facts, 0.96, time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC))
	if gate.Decision != "PASSED" || len(gate.Blockers) != 0 {
		t.Fatalf("expected release gate to pass: %+v", gate)
	}
	if gate.StrictAccuracy.PointEstimate != 1 || gate.StrictAccuracy.WilsonLowerBound < 0.95 {
		t.Fatalf("unexpected strict accuracy: %+v", gate.StrictAccuracy)
	}

	facts[0].status = "FAILED"
	facts[0].failureStage = "VALIDATION"
	gate = calculateEvaluationReleaseGate(gate, facts, 0.96, time.Now())
	if gate.Decision != "BLOCKED" || gate.P0Accuracy.Passed {
		t.Fatalf("P0 regression must block release: %+v", gate)
	}
	found := false
	for _, blocker := range gate.Blockers {
		if blocker == "P0_ACCURACY_BELOW_100" {
			found = true
		}
	}
	if !found || gate.FailureStageCounts["VALIDATION"] != 1 {
		t.Fatalf("missing P0 blocker or first-stage attribution: %+v", gate)
	}
}
