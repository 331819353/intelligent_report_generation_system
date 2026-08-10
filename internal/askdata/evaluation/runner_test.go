package evaluation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/testfixture"
)

type fixturePipelineFunc func(context.Context, testfixture.Set, testfixture.Question) (FixtureActual, error)

func (pipeline fixturePipelineFunc) Execute(
	ctx context.Context,
	fixture testfixture.Set,
	question testfixture.Question,
) (FixtureActual, error) {
	return pipeline(ctx, fixture, question)
}

func TestDeterministicFixtureRunnerPassesStableSyntheticBaseline(t *testing.T) {
	runner, err := NewFixtureRunner(NewDeterministicFixturePipeline())
	if err != nil {
		t.Fatalf("NewFixtureRunner() error = %v", err)
	}
	fixture := testfixture.Standard()
	first, err := runner.Run(context.Background(), fixture)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if first.TotalCases != 6 || first.PassedCases != 6 || first.FailedCases != 0 {
		t.Fatalf("report counts = %#v", first)
	}
	if err := first.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	for _, summary := range first.Stages {
		if summary.Failures != 0 || summary.FailedCaseIDs == nil {
			t.Fatalf("unexpected stage summary = %#v", summary)
		}
	}

	// Input slice order is not semantic and cannot change a regression receipt.
	reverseFixtureSlices(&fixture)
	second, err := runner.Run(context.Background(), fixture)
	if err != nil {
		t.Fatalf("Run(reordered) error = %v", err)
	}
	if second.ContentHash != first.ContentHash {
		t.Fatalf("input ordering changed report hash: %s != %s", first.ContentHash, second.ContentHash)
	}
	raw, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("json.Marshal(report) error = %v", err)
	}
	for _, forbidden := range []string{"今年华东区按月的销售额", "1200.00", "980.00"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("report leaked fixture question/result value %q: %s", forbidden, raw)
		}
	}
	direct := fixtureCaseReport(first, "question-direct")
	if direct.ExpectedIRHash == "" || direct.ActualIRHash == "" ||
		direct.ExpectedResultHash == "" || direct.ActualResultHash == "" ||
		direct.ComparisonHash == "" {
		t.Fatalf("direct case is missing replay hashes: %#v", direct)
	}
}

func TestDeterministicFixtureRunnerDerivesControlledEarlyOutcomes(t *testing.T) {
	runner, _ := NewFixtureRunner(NewDeterministicFixturePipeline())
	report, err := runner.Run(context.Background(), testfixture.Standard())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	tests := []struct {
		caseID      askdata.ID
		disposition testfixture.Disposition
		reason      string
	}{
		{"question-member-ambiguous", testfixture.DispositionClarify, "MEMBER_DIMENSION_AMBIGUOUS"},
		{"question-unauthorized", testfixture.DispositionRefuse, "SEMANTIC_OBJECT_FORBIDDEN"},
		{"question-fanout", testfixture.DispositionClarify, "JOIN_FANOUT_UNSAFE"},
		{"question-empty", testfixture.DispositionNoData, "TIME_RANGE_NO_DATA"},
		{"question-expired-member", testfixture.DispositionClarify, "MEMBER_EXPIRED"},
	}
	for _, test := range tests {
		result := fixtureCaseReport(report, test.caseID)
		if !result.Passed || result.ActualDisposition != test.disposition || result.ActualReasonCode != test.reason {
			t.Fatalf("case %s = %#v", test.caseID, result)
		}
	}
}

func TestFixtureRunnerAttributesEveryStableFailureStage(t *testing.T) {
	for _, stage := range fixtureFailureStages {
		t.Run(string(stage), func(t *testing.T) {
			runner, err := NewFixtureRunner(fixturePipelineFunc(func(
				context.Context,
				testfixture.Set,
				testfixture.Question,
			) (FixtureActual, error) {
				return FixtureActual{}, newFixtureStageFailure(stage, "INJECTED_FAILURE")
			}))
			if err != nil {
				t.Fatalf("NewFixtureRunner() error = %v", err)
			}
			report, err := runner.Run(context.Background(), testfixture.Standard())
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if report.PassedCases != 0 || report.FailedCases != report.TotalCases {
				t.Fatalf("counts = %#v", report)
			}
			for _, result := range report.Cases {
				if result.FailureStage != stage || result.FailureCode != "INJECTED_FAILURE" {
					t.Fatalf("case attribution = %#v", result)
				}
			}
			summary := fixtureStageSummary(report, stage)
			if summary.Failures != report.TotalCases || len(summary.FailedCaseIDs) != report.TotalCases {
				t.Fatalf("stage summary = %#v", summary)
			}
		})
	}
}

func TestFixtureRunnerClassifiesIRResultAndSecurityRegressions(t *testing.T) {
	baseline := NewDeterministicFixturePipeline()
	tests := []struct {
		name   string
		stage  FailureStage
		code   string
		mutate func(*FixtureActual)
	}{
		{
			name: "IR mismatch", stage: FailureStageIR, code: "IR_STRICT_MISMATCH",
			mutate: func(actual *FixtureActual) { actual.IR.Limit = 499 },
		},
		{
			name: "result mismatch", stage: FailureStageValidation, code: "RESULT_NOT_EQUIVALENT",
			mutate: func(actual *FixtureActual) { actual.Result.Rows[0][1] = "9999.00" },
		},
		{
			name: "sensitive leak", stage: FailureStageSecurity, code: "SENSITIVE_DATA_LEAK",
			mutate: func(actual *FixtureActual) { actual.SensitiveLeak = true },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner, _ := NewFixtureRunner(fixturePipelineFunc(func(
				ctx context.Context,
				fixture testfixture.Set,
				question testfixture.Question,
			) (FixtureActual, error) {
				actual, err := baseline.Execute(ctx, fixture, question)
				if err == nil && question.QuestionID == "question-direct" {
					test.mutate(&actual)
				}
				return actual, err
			}))
			report, err := runner.Run(context.Background(), testfixture.Standard())
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			result := fixtureCaseReport(report, "question-direct")
			if result.Passed || result.FailureStage != test.stage || result.FailureCode != test.code {
				t.Fatalf("direct regression = %#v", result)
			}
			if report.FailedCases != 1 || report.PassedCases != 5 {
				t.Fatalf("counts = %#v", report)
			}
		})
	}
}

func TestFixtureRunnerRejectsInvalidFixtureAndReportTampering(t *testing.T) {
	runner, _ := NewFixtureRunner(NewDeterministicFixturePipeline())
	fixture := testfixture.Standard()
	fixture.Questions = append(fixture.Questions, fixture.Questions[0])
	if _, err := runner.Run(context.Background(), fixture); !errors.Is(err, ErrInvalidFixtureRunner) {
		t.Fatalf("duplicate case error = %v", err)
	}

	fixture = testfixture.Standard()
	fixture.Results = append(fixture.Results, fixture.Results[0])
	if _, err := runner.Run(context.Background(), fixture); !errors.Is(err, ErrInvalidFixtureRunner) {
		t.Fatalf("duplicate result error = %v", err)
	}

	report, err := runner.Run(context.Background(), testfixture.Standard())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	report.PassedCases--
	if err := report.Validate(); !errors.Is(err, ErrInvalidFixtureRunner) {
		t.Fatalf("tampered report error = %v", err)
	}
}

func TestFixtureRunnerHonorsCancellation(t *testing.T) {
	runner, _ := NewFixtureRunner(NewDeterministicFixturePipeline())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runner.Run(ctx, testfixture.Standard()); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Run() error = %v", err)
	}
}

func fixtureCaseReport(report FixtureRegressionReport, caseID askdata.ID) FixtureCaseReport {
	for _, result := range report.Cases {
		if result.CaseID == caseID {
			return result
		}
	}
	return FixtureCaseReport{}
}

func fixtureStageSummary(report FixtureRegressionReport, stage FailureStage) FixtureStageSummary {
	for _, summary := range report.Stages {
		if summary.Stage == stage {
			return summary
		}
	}
	return FixtureStageSummary{}
}

func reverseFixtureSlices(fixture *testfixture.Set) {
	reverse := func(length int, swap func(int, int)) {
		for left, right := 0, length-1; left < right; left, right = left+1, right-1 {
			swap(left, right)
		}
	}
	reverse(len(fixture.Users), func(left, right int) {
		fixture.Users[left], fixture.Users[right] = fixture.Users[right], fixture.Users[left]
	})
	reverse(len(fixture.Models), func(left, right int) {
		fixture.Models[left], fixture.Models[right] = fixture.Models[right], fixture.Models[left]
	})
	reverse(len(fixture.Metrics), func(left, right int) {
		fixture.Metrics[left], fixture.Metrics[right] = fixture.Metrics[right], fixture.Metrics[left]
	})
	reverse(len(fixture.Dimensions), func(left, right int) {
		fixture.Dimensions[left], fixture.Dimensions[right] = fixture.Dimensions[right], fixture.Dimensions[left]
	})
	reverse(len(fixture.Members), func(left, right int) {
		fixture.Members[left], fixture.Members[right] = fixture.Members[right], fixture.Members[left]
	})
	reverse(len(fixture.Relationships), func(left, right int) {
		fixture.Relationships[left], fixture.Relationships[right] = fixture.Relationships[right], fixture.Relationships[left]
	})
	reverse(len(fixture.Questions), func(left, right int) {
		fixture.Questions[left], fixture.Questions[right] = fixture.Questions[right], fixture.Questions[left]
	})
	reverse(len(fixture.Results), func(left, right int) {
		fixture.Results[left], fixture.Results[right] = fixture.Results[right], fixture.Results[left]
	})
}
