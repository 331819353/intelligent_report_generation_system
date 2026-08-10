package evaluation

import (
	"encoding/json"
	"errors"
	"math"
	"sort"

	"intelligent-report-generation-system/internal/askdata"
)

const (
	ReleaseGateSchemaVersion           = "askdata-release-evaluation-gate-v1"
	ReleaseGateMinimumCases            = 2000
	ReleaseGateMinimumStrictAccuracy   = 0.96
	ReleaseGateMinimumWilsonLowerBound = 0.95
	ReleaseGateMinimumDirectCoverage   = 0.85
	ReleaseGateMinimumDecisionAccuracy = 0.95
	ReleaseGateMaximumNarrativeFailure = 0.02
	ReleaseGateWilsonZ95               = 1.959963984540054
)

var ErrInvalidReleaseGate = errors.New("release evaluation gate is invalid")

type ReleaseGateCode string

const (
	GateCaseCount           ReleaseGateCode = "EVAL_CASE_COUNT"
	GateIndependentReview   ReleaseGateCode = "EVAL_INDEPENDENT_REVIEW"
	GateReleasePin          ReleaseGateCode = "EVAL_RELEASE_PIN"
	GateWarehousePin        ReleaseGateCode = "EVAL_WAREHOUSE_PIN"
	GateStrictAccuracy      ReleaseGateCode = "EVAL_STRICT_ACCURACY"
	GateWilsonLowerBound    ReleaseGateCode = "EVAL_WILSON_LOWER_BOUND"
	GateDirectCoverage      ReleaseGateCode = "EVAL_DIRECT_COVERAGE"
	GateDecisionAccuracy    ReleaseGateCode = "EVAL_CLARIFY_REFUSE_ACCURACY"
	GateP0Accuracy          ReleaseGateCode = "EVAL_P0_ACCURACY"
	GateSecurity            ReleaseGateCode = "EVAL_SECURITY"
	GateSensitiveLeak       ReleaseGateCode = "EVAL_SENSITIVE_LEAK"
	GateNarrativeFailure    ReleaseGateCode = "EVAL_NARRATIVE_FAILURE"
	GateErrorBudget         ReleaseGateCode = "EVAL_ERROR_BUDGET"
	GateFourShardConclusion ReleaseGateCode = "EVAL_FOUR_SHARDS_REQUIRED"
)

type ReleaseGateFacts struct {
	TenantID              askdata.ID
	DomainID              askdata.ID
	ReleaseID             askdata.ID
	ReleaseContentHash    askdata.ContentHash
	EvaluationSetID       askdata.ID
	EvaluationSetHash     askdata.ContentHash
	EvaluationBatchID     askdata.ID
	WarehouseSnapshotHash askdata.ContentHash
	WarehouseFreshnessAt  int64
	CaseCount             int
	ReviewedCaseCount     int
	StrictCorrectCount    int
	DirectExpectedCount   int
	DirectAnswerCount     int
	DecisionExpectedCount int
	DecisionCorrectCount  int
	P0CaseCount           int
	P0CorrectCount        int
	SecurityCaseCount     int
	SecurityPassedCount   int
	SensitiveLeakCount    int
	NarrativeCaseCount    int
	NarrativeFailureCount int
	SealedShardCount      int
	ErrorBudgetAttached   bool
	ErrorBudgetPassed     bool
	DatabaseRecomputed    bool
}

type ReleaseGateFailure struct {
	Code        ReleaseGateCode `json:"code"`
	Numerator   int             `json:"numerator"`
	Denominator int             `json:"denominator"`
	Actual      float64         `json:"actual"`
	Required    float64         `json:"required"`
}

type ReleaseGateReceipt struct {
	SchemaVersion         string               `json:"schemaVersion"`
	ReleaseID             askdata.ID           `json:"releaseId"`
	ReleaseContentHash    askdata.ContentHash  `json:"releaseContentHash"`
	EvaluationSetID       askdata.ID           `json:"evaluationSetId"`
	EvaluationSetHash     askdata.ContentHash  `json:"evaluationSetHash"`
	EvaluationBatchID     askdata.ID           `json:"evaluationBatchId"`
	WarehouseSnapshotHash askdata.ContentHash  `json:"warehouseSnapshotHash"`
	CaseCount             int                  `json:"caseCount"`
	StrictAccuracy        float64              `json:"strictAccuracy"`
	WilsonLowerBound      float64              `json:"wilsonLowerBound"`
	DirectCoverage        float64              `json:"directCoverage"`
	DecisionAccuracy      float64              `json:"decisionAccuracy"`
	NarrativeFailureRate  float64              `json:"narrativeFailureRate"`
	Passed                bool                 `json:"passed"`
	Failures              []ReleaseGateFailure `json:"failures"`
	FactsHash             askdata.ContentHash  `json:"factsHash"`
	ReceiptHash           askdata.ContentHash  `json:"receiptHash"`
}

func EvaluateReleaseGate(facts ReleaseGateFacts) (ReleaseGateReceipt, error) {
	if err := validateReleaseGateFacts(facts); err != nil {
		return ReleaseGateReceipt{}, err
	}
	factsPayload, err := json.Marshal(facts)
	if err != nil {
		return ReleaseGateReceipt{}, err
	}
	receipt := ReleaseGateReceipt{
		SchemaVersion: ReleaseGateSchemaVersion, ReleaseID: facts.ReleaseID,
		ReleaseContentHash: facts.ReleaseContentHash, EvaluationSetID: facts.EvaluationSetID,
		EvaluationSetHash: facts.EvaluationSetHash, EvaluationBatchID: facts.EvaluationBatchID,
		WarehouseSnapshotHash: facts.WarehouseSnapshotHash, CaseCount: facts.CaseCount,
		Failures: []ReleaseGateFailure{}, FactsHash: askdata.HashBytes(factsPayload),
	}
	receipt.StrictAccuracy = rate(facts.StrictCorrectCount, facts.CaseCount)
	receipt.WilsonLowerBound = WilsonLowerBound(facts.StrictCorrectCount, facts.CaseCount, ReleaseGateWilsonZ95)
	receipt.DirectCoverage = rate(facts.DirectAnswerCount, facts.DirectExpectedCount)
	receipt.DecisionAccuracy = rate(facts.DecisionCorrectCount, facts.DecisionExpectedCount)
	receipt.NarrativeFailureRate = rate(facts.NarrativeFailureCount, facts.NarrativeCaseCount)
	addMinimumCountFailure(&receipt, facts.CaseCount, ReleaseGateMinimumCases, GateCaseCount)
	if facts.ReviewedCaseCount != facts.CaseCount {
		addGateFailure(&receipt, GateIndependentReview, facts.ReviewedCaseCount, facts.CaseCount, rate(facts.ReviewedCaseCount, facts.CaseCount), 1)
	}
	if !facts.DatabaseRecomputed {
		addGateFailure(&receipt, GateReleasePin, 0, 1, 0, 1)
	}
	if facts.WarehouseFreshnessAt <= 0 {
		addGateFailure(&receipt, GateWarehousePin, 0, 1, 0, 1)
	}
	addRateFailure(&receipt, GateStrictAccuracy, facts.StrictCorrectCount, facts.CaseCount, ReleaseGateMinimumStrictAccuracy)
	if receipt.WilsonLowerBound < ReleaseGateMinimumWilsonLowerBound {
		addGateFailure(&receipt, GateWilsonLowerBound, facts.StrictCorrectCount, facts.CaseCount, receipt.WilsonLowerBound, ReleaseGateMinimumWilsonLowerBound)
	}
	addRateFailure(&receipt, GateDirectCoverage, facts.DirectAnswerCount, facts.DirectExpectedCount, ReleaseGateMinimumDirectCoverage)
	addRateFailure(&receipt, GateDecisionAccuracy, facts.DecisionCorrectCount, facts.DecisionExpectedCount, ReleaseGateMinimumDecisionAccuracy)
	addRateFailure(&receipt, GateP0Accuracy, facts.P0CorrectCount, facts.P0CaseCount, 1)
	addRateFailure(&receipt, GateSecurity, facts.SecurityPassedCount, facts.SecurityCaseCount, 1)
	if facts.SensitiveLeakCount != 0 {
		addGateFailure(&receipt, GateSensitiveLeak, facts.SensitiveLeakCount, facts.CaseCount, rate(facts.SensitiveLeakCount, facts.CaseCount), 0)
	}
	if receipt.NarrativeFailureRate > ReleaseGateMaximumNarrativeFailure {
		addGateFailure(&receipt, GateNarrativeFailure, facts.NarrativeFailureCount, facts.NarrativeCaseCount, receipt.NarrativeFailureRate, ReleaseGateMaximumNarrativeFailure)
	}
	if !facts.ErrorBudgetAttached || !facts.ErrorBudgetPassed {
		addGateFailure(&receipt, GateErrorBudget, boolInt(facts.ErrorBudgetAttached && facts.ErrorBudgetPassed), 1, float64(boolInt(facts.ErrorBudgetAttached && facts.ErrorBudgetPassed)), 1)
	}
	if facts.SealedShardCount != SealedShardCount {
		addGateFailure(&receipt, GateFourShardConclusion, facts.SealedShardCount, SealedShardCount, float64(facts.SealedShardCount), SealedShardCount)
	}
	sort.Slice(receipt.Failures, func(i, j int) bool { return receipt.Failures[i].Code < receipt.Failures[j].Code })
	receipt.Passed = len(receipt.Failures) == 0
	receipt.ReceiptHash, err = releaseGateReceiptHash(receipt)
	if err != nil {
		return ReleaseGateReceipt{}, err
	}
	return receipt, receipt.Validate()
}

func WilsonLowerBound(successes, total int, z float64) float64 {
	if successes < 0 || total <= 0 || successes > total || math.IsNaN(z) || math.IsInf(z, 0) || z <= 0 {
		return 0
	}
	n := float64(total)
	p := float64(successes) / n
	z2 := z * z
	center := p + z2/(2*n)
	spread := z * math.Sqrt((p*(1-p)+z2/(4*n))/n)
	return (center - spread) / (1 + z2/n)
}

func (receipt ReleaseGateReceipt) Validate() error {
	if receipt.SchemaVersion != ReleaseGateSchemaVersion || receipt.ReleaseID.Validate() != nil ||
		receipt.ReleaseContentHash.Validate() != nil || receipt.EvaluationSetID.Validate() != nil ||
		receipt.EvaluationSetHash.Validate() != nil || receipt.EvaluationBatchID.Validate() != nil ||
		receipt.WarehouseSnapshotHash.Validate() != nil || receipt.CaseCount < 1 || receipt.Failures == nil ||
		receipt.FactsHash.Validate() != nil || receipt.ReceiptHash.Validate() != nil ||
		receipt.Passed != (len(receipt.Failures) == 0) {
		return ErrInvalidReleaseGate
	}
	for _, value := range []float64{receipt.StrictAccuracy, receipt.WilsonLowerBound, receipt.DirectCoverage, receipt.DecisionAccuracy, receipt.NarrativeFailureRate} {
		if value < 0 || value > 1 || math.IsNaN(value) || math.IsInf(value, 0) {
			return ErrInvalidReleaseGate
		}
	}
	for index, failure := range receipt.Failures {
		if failure.Numerator < 0 || failure.Denominator < 0 || math.IsNaN(failure.Actual) || math.IsNaN(failure.Required) ||
			(index > 0 && receipt.Failures[index-1].Code >= failure.Code) {
			return ErrInvalidReleaseGate
		}
	}
	expected, err := releaseGateReceiptHash(receipt)
	if err != nil || expected != receipt.ReceiptHash {
		return ErrInvalidReleaseGate
	}
	return nil
}

func validateReleaseGateFacts(facts ReleaseGateFacts) error {
	if facts.TenantID.Validate() != nil || facts.DomainID.Validate() != nil || facts.ReleaseID.Validate() != nil ||
		facts.ReleaseContentHash.Validate() != nil || facts.EvaluationSetID.Validate() != nil ||
		facts.EvaluationSetHash.Validate() != nil || facts.EvaluationBatchID.Validate() != nil ||
		facts.WarehouseSnapshotHash.Validate() != nil || facts.CaseCount < 1 || facts.CaseCount > 100_000 {
		return ErrInvalidReleaseGate
	}
	counts := []int{
		facts.ReviewedCaseCount, facts.StrictCorrectCount, facts.DirectExpectedCount, facts.DirectAnswerCount,
		facts.DecisionExpectedCount, facts.DecisionCorrectCount, facts.P0CaseCount, facts.P0CorrectCount,
		facts.SecurityCaseCount, facts.SecurityPassedCount, facts.SensitiveLeakCount,
		facts.NarrativeCaseCount, facts.NarrativeFailureCount,
	}
	for _, count := range counts {
		if count < 0 || count > facts.CaseCount {
			return ErrInvalidReleaseGate
		}
	}
	if facts.ReviewedCaseCount > facts.CaseCount || facts.DirectAnswerCount > facts.DirectExpectedCount ||
		facts.DecisionCorrectCount > facts.DecisionExpectedCount || facts.P0CorrectCount > facts.P0CaseCount ||
		facts.SecurityPassedCount > facts.SecurityCaseCount || facts.NarrativeFailureCount > facts.NarrativeCaseCount ||
		facts.DirectExpectedCount == 0 || facts.DecisionExpectedCount == 0 || facts.P0CaseCount == 0 ||
		facts.SecurityCaseCount == 0 || facts.NarrativeCaseCount == 0 || facts.SealedShardCount < 0 ||
		facts.SealedShardCount > SealedShardCount {
		return ErrInvalidReleaseGate
	}
	return nil
}

func addMinimumCountFailure(receipt *ReleaseGateReceipt, actual, required int, code ReleaseGateCode) {
	if actual < required {
		addGateFailure(receipt, code, actual, required, float64(actual), float64(required))
	}
}

func addRateFailure(receipt *ReleaseGateReceipt, code ReleaseGateCode, numerator, denominator int, required float64) {
	actual := rate(numerator, denominator)
	if actual < required {
		addGateFailure(receipt, code, numerator, denominator, actual, required)
	}
}

func addGateFailure(receipt *ReleaseGateReceipt, code ReleaseGateCode, numerator, denominator int, actual, required float64) {
	receipt.Failures = append(receipt.Failures, ReleaseGateFailure{
		Code: code, Numerator: numerator, Denominator: denominator, Actual: actual, Required: required,
	})
}

func rate(numerator, denominator int) float64 {
	if denominator <= 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func releaseGateReceiptHash(receipt ReleaseGateReceipt) (askdata.ContentHash, error) {
	receipt.ReceiptHash = ""
	payload, err := json.Marshal(receipt)
	if err != nil {
		return "", err
	}
	return askdata.HashBytes(payload), nil
}
