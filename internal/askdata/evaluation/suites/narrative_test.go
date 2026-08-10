package suites

import (
	"errors"
	"fmt"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
)

func TestCohensKappa(t *testing.T) {
	pairs := []AgreementPair{
		{true, true}, {true, true}, {false, false}, {false, false}, {true, false},
	}
	kappa, err := CohensKappa(pairs)
	if err != nil {
		t.Fatal(err)
	}
	if kappa < .6 || kappa > .62 {
		t.Fatalf("kappa = %v", kappa)
	}
	constant, err := CohensKappa([]AgreementPair{{true, true}, {true, true}})
	if err != nil || constant != 1 {
		t.Fatalf("constant agreement = %v, %v", constant, err)
	}
}

func TestNarrativeReviewRequiresTwoIndependentMatchingReviews(t *testing.T) {
	caseValue := narrativeCase(0, true, true)
	caseValue.Reviews[1].ReviewerID = caseValue.Reviews[0].ReviewerID
	if _, err := EvaluateNarrativeReviews([]NarrativeReviewCase{caseValue, narrativeCase(1, true, true)}); !errors.Is(err, ErrInvalidNarrativeReview) {
		t.Fatalf("same reviewer error = %v", err)
	}
	caseValue = narrativeCase(0, true, true)
	caseValue.Reviews[1].Verdicts[NarrativeNoCausalAssertion] = false
	if _, err := EvaluateNarrativeReviews([]NarrativeReviewCase{caseValue, narrativeCase(1, true, true)}); !errors.Is(err, ErrInvalidNarrativeReview) {
		t.Fatalf("review disagreement error = %v", err)
	}
}

func TestNarrativeReviewGateAndFalseNegativeExport(t *testing.T) {
	cases := make([]NarrativeReviewCase, MinimumNarrativeReviewCases)
	for index := range cases {
		humanOK := index != 0
		verifierOK := true
		cases[index] = narrativeCase(index, humanOK, verifierOK)
	}
	report, err := EvaluateNarrativeReviews(cases)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.FalseNegatives) != 1 || report.FalseNegatives[0].CaseID != cases[0].CaseID {
		t.Fatalf("false negatives = %#v", report.FalseNegatives)
	}
	if report.Passed {
		t.Fatal("low agreement unexpectedly passed")
	}
	if err := RequireNarrativeReviewGate(&report, report.ReportHash); !errors.Is(err, ErrInvalidNarrativeReview) {
		t.Fatalf("gate error = %v", err)
	}
	if fmt.Sprintf("%#v", report.FalseNegatives) == "" {
		t.Fatal("unreachable")
	}
}

func TestNarrativeReviewBelowMinimumCannotGate(t *testing.T) {
	report, err := EvaluateNarrativeReviews([]NarrativeReviewCase{
		narrativeCase(0, true, true), narrativeCase(1, false, false),
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.ReadyForGate || report.Passed {
		t.Fatalf("small report gated: %#v", report)
	}
	copy := report
	copy.FailureRate = 0
	if err := copy.Validate(); !errors.Is(err, ErrInvalidNarrativeReview) {
		t.Fatalf("tamper error = %v", err)
	}
}

func narrativeCase(index int, humanOK, verifierOK bool) NarrativeReviewCase {
	human := verdicts(humanOK)
	verifier := verdicts(verifierOK)
	return NarrativeReviewCase{
		CaseID:          askdata.ID(fmt.Sprintf("narrative-case-%03d", index)),
		CaseContentHash: askdata.HashBytes([]byte(fmt.Sprintf("case-%d", index))),
		Reviews: [2]HumanNarrativeReview{
			{ReviewerID: "reviewer-a", Slot: 1, Verdicts: cloneVerdicts(human), ReviewHash: askdata.HashBytes([]byte(fmt.Sprintf("a-%d", index)))},
			{ReviewerID: "reviewer-b", Slot: 2, Verdicts: cloneVerdicts(human), ReviewHash: askdata.HashBytes([]byte(fmt.Sprintf("b-%d", index)))},
		},
		Verifier: verifier, VerifierVersion: "answer-verifier-v1",
	}
}

func verdicts(value bool) DimensionVerdicts {
	return DimensionVerdicts{
		NarrativeNumericConsistency: value, NarrativeSemanticConsistency: value,
		NarrativeNoExternalFact: value, NarrativeNoCausalAssertion: value,
	}
}

func cloneVerdicts(source DimensionVerdicts) DimensionVerdicts {
	result := make(DimensionVerdicts, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
