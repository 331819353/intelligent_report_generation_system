package suites

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"intelligent-report-generation-system/internal/askdata"
)

const (
	NarrativeReviewSchemaVersion = "narrative-human-review-v1"
	MinimumNarrativeReviewCases  = 150
	MinimumNarrativeKappa        = 0.8
	MaximumNarrativeFailureRate  = 0.02
)

var ErrInvalidNarrativeReview = errors.New("narrative review suite is invalid")

type NarrativeDimension string

const (
	NarrativeNumericConsistency  NarrativeDimension = "NUMERIC_CONSISTENCY"
	NarrativeSemanticConsistency NarrativeDimension = "SEMANTIC_CONSISTENCY"
	NarrativeNoExternalFact      NarrativeDimension = "NO_EXTERNAL_FACT"
	NarrativeNoCausalAssertion   NarrativeDimension = "NO_CAUSAL_ASSERTION"
)

var narrativeDimensions = []NarrativeDimension{
	NarrativeNumericConsistency, NarrativeSemanticConsistency,
	NarrativeNoExternalFact, NarrativeNoCausalAssertion,
}

type DimensionVerdicts map[NarrativeDimension]bool

func (verdicts DimensionVerdicts) Validate() error {
	if len(verdicts) != len(narrativeDimensions) {
		return ErrInvalidNarrativeReview
	}
	for _, dimension := range narrativeDimensions {
		if _, exists := verdicts[dimension]; !exists {
			return ErrInvalidNarrativeReview
		}
	}
	for dimension := range verdicts {
		if !isNarrativeDimension(dimension) {
			return ErrInvalidNarrativeReview
		}
	}
	return nil
}

type HumanNarrativeReview struct {
	ReviewerID askdata.ID          `json:"reviewerId"`
	Slot       int                 `json:"slot"`
	Verdicts   DimensionVerdicts   `json:"verdicts"`
	ReviewHash askdata.ContentHash `json:"reviewHash"`
}

type NarrativeReviewCase struct {
	CaseID          askdata.ID              `json:"caseId"`
	CaseContentHash askdata.ContentHash     `json:"caseContentHash"`
	Reviews         [2]HumanNarrativeReview `json:"reviews"`
	Verifier        DimensionVerdicts       `json:"verifier"`
	VerifierVersion string                  `json:"verifierVersion"`
}

type DimensionAgreement struct {
	Dimension     NarrativeDimension `json:"dimension"`
	Kappa         float64            `json:"kappa"`
	FailureCount  int                `json:"failureCount"`
	FalseNegative int                `json:"falseNegative"`
}

type NarrativeImprovementSample struct {
	CaseID          askdata.ID           `json:"caseId"`
	CaseContentHash askdata.ContentHash  `json:"caseContentHash"`
	Dimensions      []NarrativeDimension `json:"dimensions"`
	VerifierVersion string               `json:"verifierVersion"`
}

type NarrativeReviewReport struct {
	SchemaVersion  string                       `json:"schemaVersion"`
	CaseCount      int                          `json:"caseCount"`
	FailureRate    float64                      `json:"failureRate"`
	MinimumKappa   float64                      `json:"minimumKappa"`
	ReadyForGate   bool                         `json:"readyForGate"`
	Passed         bool                         `json:"passed"`
	Agreement      []DimensionAgreement         `json:"agreement"`
	FalseNegatives []NarrativeImprovementSample `json:"falseNegatives"`
	InputHash      askdata.ContentHash          `json:"inputHash"`
	ReportHash     askdata.ContentHash          `json:"reportHash"`
}

// EvaluateNarrativeReviews consumes only stable IDs, hashes and four boolean
// judgments. Narrative text is intentionally absent from this contract.
func EvaluateNarrativeReviews(cases []NarrativeReviewCase) (NarrativeReviewReport, error) {
	if len(cases) < 1 || len(cases) > 100_000 {
		return NarrativeReviewReport{}, ErrInvalidNarrativeReview
	}
	normalized := append([]NarrativeReviewCase(nil), cases...)
	sort.Slice(normalized, func(i, j int) bool { return normalized[i].CaseID < normalized[j].CaseID })
	seen := map[askdata.ID]struct{}{}
	pairs := make(map[NarrativeDimension][]AgreementPair, len(narrativeDimensions))
	failures := make(map[NarrativeDimension]int, len(narrativeDimensions))
	falseNegativeCount := make(map[NarrativeDimension]int, len(narrativeDimensions))
	improvements := make([]NarrativeImprovementSample, 0)
	failedCases := 0
	for _, evaluationCase := range normalized {
		consensus, err := validateNarrativeReviewCase(evaluationCase)
		if err != nil {
			return NarrativeReviewReport{}, err
		}
		if _, duplicate := seen[evaluationCase.CaseID]; duplicate {
			return NarrativeReviewReport{}, ErrInvalidNarrativeReview
		}
		seen[evaluationCase.CaseID] = struct{}{}
		caseFailed := false
		missed := make([]NarrativeDimension, 0)
		for _, dimension := range narrativeDimensions {
			human, verifier := consensus[dimension], evaluationCase.Verifier[dimension]
			pairs[dimension] = append(pairs[dimension], AgreementPair{Human: human, Verifier: verifier})
			if !human {
				failures[dimension]++
				caseFailed = true
				if verifier {
					falseNegativeCount[dimension]++
					missed = append(missed, dimension)
				}
			}
		}
		if caseFailed {
			failedCases++
		}
		if len(missed) != 0 {
			improvements = append(improvements, NarrativeImprovementSample{
				CaseID: evaluationCase.CaseID, CaseContentHash: evaluationCase.CaseContentHash,
				Dimensions: missed, VerifierVersion: evaluationCase.VerifierVersion,
			})
		}
	}
	agreement := make([]DimensionAgreement, 0, len(narrativeDimensions))
	minimumKappa := 1.0
	for _, dimension := range narrativeDimensions {
		kappa, err := CohensKappa(pairs[dimension])
		if err != nil {
			return NarrativeReviewReport{}, err
		}
		if kappa < minimumKappa {
			minimumKappa = kappa
		}
		agreement = append(agreement, DimensionAgreement{
			Dimension: dimension, Kappa: kappa, FailureCount: failures[dimension],
			FalseNegative: falseNegativeCount[dimension],
		})
	}
	input, err := json.Marshal(normalized)
	if err != nil {
		return NarrativeReviewReport{}, err
	}
	failureRate := float64(failedCases) / float64(len(normalized))
	report := NarrativeReviewReport{
		SchemaVersion: NarrativeReviewSchemaVersion, CaseCount: len(normalized), FailureRate: failureRate,
		MinimumKappa: minimumKappa, ReadyForGate: len(normalized) >= MinimumNarrativeReviewCases,
		Agreement: agreement, FalseNegatives: improvements, InputHash: askdata.HashBytes(input),
	}
	report.Passed = report.ReadyForGate && failureRate <= MaximumNarrativeFailureRate && minimumKappa >= MinimumNarrativeKappa
	report.ReportHash, err = narrativeReportHash(report)
	if err != nil {
		return NarrativeReviewReport{}, err
	}
	return report, report.Validate()
}

func (report NarrativeReviewReport) Validate() error {
	if report.SchemaVersion != NarrativeReviewSchemaVersion || report.CaseCount < 1 ||
		report.CaseCount > 100_000 || report.FailureRate < 0 || report.FailureRate > 1 ||
		report.MinimumKappa < -1 || report.MinimumKappa > 1 || len(report.Agreement) != len(narrativeDimensions) ||
		report.FalseNegatives == nil || report.InputHash.Validate() != nil || report.ReportHash.Validate() != nil ||
		report.ReadyForGate != (report.CaseCount >= MinimumNarrativeReviewCases) ||
		report.Passed != (report.ReadyForGate && report.FailureRate <= MaximumNarrativeFailureRate && report.MinimumKappa >= MinimumNarrativeKappa) {
		return ErrInvalidNarrativeReview
	}
	for index, value := range report.Agreement {
		if value.Dimension != narrativeDimensions[index] || value.Kappa < -1 || value.Kappa > 1 ||
			value.FailureCount < 0 || value.FailureCount > report.CaseCount || value.FalseNegative < 0 ||
			value.FalseNegative > value.FailureCount {
			return ErrInvalidNarrativeReview
		}
	}
	expected, err := narrativeReportHash(report)
	if err != nil || expected != report.ReportHash {
		return ErrInvalidNarrativeReview
	}
	return nil
}

func RequireNarrativeReviewGate(report *NarrativeReviewReport, expectedHash askdata.ContentHash) error {
	if report == nil {
		return fmt.Errorf("%w: report is missing", ErrInvalidNarrativeReview)
	}
	if err := report.Validate(); err != nil {
		return err
	}
	if expectedHash.Validate() != nil || expectedHash != report.ReportHash {
		return fmt.Errorf("%w: report hash mismatch", ErrInvalidNarrativeReview)
	}
	if !report.Passed {
		return fmt.Errorf("%w: review threshold not met", ErrInvalidNarrativeReview)
	}
	return nil
}

func validateNarrativeReviewCase(evaluationCase NarrativeReviewCase) (DimensionVerdicts, error) {
	if evaluationCase.CaseID.Validate() != nil || evaluationCase.CaseContentHash.Validate() != nil ||
		evaluationCase.Verifier.Validate() != nil || evaluationCase.VerifierVersion == "" || len(evaluationCase.VerifierVersion) > 128 {
		return nil, ErrInvalidNarrativeReview
	}
	left, right := evaluationCase.Reviews[0], evaluationCase.Reviews[1]
	if left.Slot != 1 || right.Slot != 2 || left.ReviewerID.Validate() != nil || right.ReviewerID.Validate() != nil ||
		left.ReviewerID == right.ReviewerID || left.ReviewHash.Validate() != nil || right.ReviewHash.Validate() != nil ||
		left.Verdicts.Validate() != nil || right.Verdicts.Validate() != nil {
		return nil, ErrInvalidNarrativeReview
	}
	for _, dimension := range narrativeDimensions {
		if left.Verdicts[dimension] != right.Verdicts[dimension] {
			return nil, fmt.Errorf("%w: reviewers disagree for %s", ErrInvalidNarrativeReview, dimension)
		}
	}
	return left.Verdicts, nil
}

func narrativeReportHash(report NarrativeReviewReport) (askdata.ContentHash, error) {
	report.ReportHash = ""
	payload, err := json.Marshal(report)
	if err != nil {
		return "", err
	}
	return askdata.HashBytes(payload), nil
}

func isNarrativeDimension(dimension NarrativeDimension) bool {
	for _, candidate := range narrativeDimensions {
		if candidate == dimension {
			return true
		}
	}
	return false
}
