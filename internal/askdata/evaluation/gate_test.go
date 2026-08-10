package evaluation

import (
	"errors"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
)

func TestWilsonLowerBound(t *testing.T) {
	value := WilsonLowerBound(1940, 2000, ReleaseGateWilsonZ95)
	if value < .961 || value > .963 {
		t.Fatalf("Wilson lower bound = %v", value)
	}
	if WilsonLowerBound(1, 0, ReleaseGateWilsonZ95) != 0 {
		t.Fatal("invalid Wilson input did not fail closed")
	}
}

func TestReleaseGatePassesOnlyDatabaseRecomputedCompleteEvidence(t *testing.T) {
	facts := passingReleaseGateFacts()
	receipt, err := EvaluateReleaseGate(facts)
	if err != nil {
		t.Fatal(err)
	}
	if !receipt.Passed || len(receipt.Failures) != 0 || receipt.WilsonLowerBound < ReleaseGateMinimumWilsonLowerBound {
		t.Fatalf("receipt = %#v", receipt)
	}
	facts.DatabaseRecomputed = false
	receipt, err = EvaluateReleaseGate(facts)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Passed || !hasGateCode(receipt, GateReleasePin) {
		t.Fatalf("client summary bypassed gate: %#v", receipt)
	}
}

func TestReleaseGateReturnsEveryStableFailure(t *testing.T) {
	facts := passingReleaseGateFacts()
	facts.CaseCount = 1999
	facts.ReviewedCaseCount = 1998
	facts.StrictCorrectCount = 1800
	facts.DirectAnswerCount = 1000
	facts.DecisionCorrectCount = 100
	facts.P0CorrectCount = 9
	facts.SecurityPassedCount = 9
	facts.SensitiveLeakCount = 1
	facts.NarrativeCaseCount = 1999
	facts.NarrativeFailureCount = 50
	facts.ErrorBudgetPassed = false
	facts.SealedShardCount = 3
	receipt, err := EvaluateReleaseGate(facts)
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range []ReleaseGateCode{
		GateCaseCount, GateIndependentReview, GateStrictAccuracy, GateWilsonLowerBound,
		GateDirectCoverage, GateDecisionAccuracy, GateP0Accuracy, GateSecurity,
		GateSensitiveLeak, GateNarrativeFailure, GateErrorBudget, GateFourShardConclusion,
	} {
		if !hasGateCode(receipt, code) {
			t.Fatalf("missing %s in %#v", code, receipt.Failures)
		}
	}
	copy := receipt
	copy.StrictAccuracy = 1
	if err := copy.Validate(); !errors.Is(err, ErrInvalidReleaseGate) {
		t.Fatalf("tampered receipt error = %v", err)
	}
}

func passingReleaseGateFacts() ReleaseGateFacts {
	return ReleaseGateFacts{
		TenantID: "tenant-a", DomainID: "domain-a", ReleaseID: "release-a",
		ReleaseContentHash:    hashOf("release"),
		EvaluationSetID:       "set-a",
		EvaluationSetHash:     hashOf("set"),
		EvaluationBatchID:     "batch-a",
		WarehouseSnapshotHash: hashOf("warehouse"), WarehouseFreshnessAt: 1,
		CaseCount: 2000, ReviewedCaseCount: 2000, StrictCorrectCount: 1940,
		DirectExpectedCount: 1700, DirectAnswerCount: 1500,
		DecisionExpectedCount: 300, DecisionCorrectCount: 290,
		P0CaseCount: 10, P0CorrectCount: 10, SecurityCaseCount: 10, SecurityPassedCount: 10,
		NarrativeCaseCount: 2000, NarrativeFailureCount: 20, SealedShardCount: 4,
		ErrorBudgetAttached: true, ErrorBudgetPassed: true, DatabaseRecomputed: true,
	}
}

func hasGateCode(receipt ReleaseGateReceipt, code ReleaseGateCode) bool {
	for _, failure := range receipt.Failures {
		if failure.Code == code {
			return true
		}
	}
	return false
}

func hashOf(value string) askdata.ContentHash {
	return askdata.HashBytes([]byte(value))
}
