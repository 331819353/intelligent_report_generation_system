package evaluation

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
)

type deterministicFaultDetector struct {
	trials []FaultTrial
}

func (detector *deterministicFaultDetector) Detect(_ context.Context, trial FaultTrial) (bool, error) {
	detector.trials = append(detector.trials, trial)
	return int(askdata.HashBytes([]byte(trial.CaseID))[0])%2 == 0, nil
}

func TestEvaluateErrorBudgetUsesMeasuredRecoveryAndZeroForUnknown(t *testing.T) {
	report, err := EvaluateErrorBudget([]ErrorBudgetInput{
		{Stage: ErrorStageRecall, ErrorRate: 0.02, Budget: 0.012, RecoveryTest: &RecoveryMeasurement{Seed: 7, SampleCount: 10, DetectedCount: 5}},
		{Stage: ErrorStageBinding, ErrorRate: 0.01, Budget: 0.01},
	}, DefaultResidualTarget)
	if err != nil {
		t.Fatal(err)
	}
	if report.TotalResidual != 0.02 {
		t.Fatalf("total residual = %v, want 0.02", report.TotalResidual)
	}
	var binding ErrorBudgetLine
	for _, line := range report.Lines {
		if line.Stage == ErrorStageBinding {
			binding = line
		}
	}
	if binding.RecoveryMeasured || binding.RecoveryRate != 0 || binding.ResidualRate != 0.01 {
		t.Fatalf("unmeasured recovery was credited: %#v", binding)
	}
	if !report.Passed || RequirePassingErrorBudget(&report, report.ReportContentHash) != nil {
		t.Fatalf("passing report rejected: %#v", report)
	}
}

func TestErrorBudgetRejectsMissingTamperedAndOverBudgetAttachments(t *testing.T) {
	if err := RequirePassingErrorBudget(nil, askdata.HashBytes([]byte("missing"))); !errors.Is(err, ErrInvalidErrorBudget) {
		t.Fatalf("missing report error = %v", err)
	}
	report, err := EvaluateErrorBudget([]ErrorBudgetInput{
		{Stage: ErrorStageIntent, ErrorRate: 0.03, Budget: 0.01},
		{Stage: ErrorStageRecall, ErrorRate: 0.02, Budget: 0.01},
	}, 0.038)
	if err != nil {
		t.Fatal(err)
	}
	if report.Passed || len(report.Recommendations) == 0 {
		t.Fatalf("over-budget report = %#v", report)
	}
	if err := RequirePassingErrorBudget(&report, report.ReportContentHash); !errors.Is(err, ErrInvalidErrorBudget) {
		t.Fatalf("over-budget gate error = %v", err)
	}
	tampered := report
	tampered.Lines[0].ResidualRate = 0
	if err := tampered.Validate(); !errors.Is(err, ErrInvalidErrorBudget) {
		t.Fatalf("tampered report error = %v", err)
	}
}

func TestMeasureRecoveryIsStableForTheSameSeed(t *testing.T) {
	cases := make([]FaultInjectionCase, 10)
	for index := range cases {
		cases[index] = FaultInjectionCase{
			CaseID: askdata.ID("case-" + string(rune('a'+index))), EligibleFaults: []FaultType{FaultWrongMetric},
		}
	}
	leftDetector, rightDetector := &deterministicFaultDetector{}, &deterministicFaultDetector{}
	left, err := MeasureRecovery(context.Background(), cases, FaultWrongMetric, 42, 6, leftDetector)
	if err != nil {
		t.Fatal(err)
	}
	right, err := MeasureRecovery(context.Background(), append([]FaultInjectionCase(nil), cases...), FaultWrongMetric, 42, 6, rightDetector)
	if err != nil {
		t.Fatal(err)
	}
	if left != right || !reflect.DeepEqual(leftDetector.trials, rightDetector.trials) {
		t.Fatalf("same seed was not reproducible: %#v %#v", leftDetector.trials, rightDetector.trials)
	}
}

func TestErrorBudgetValidationRejectsAssumedRecoveryAndDuplicateStages(t *testing.T) {
	if _, err := EvaluateErrorBudget([]ErrorBudgetInput{
		{Stage: ErrorStageGraph, ErrorRate: .01, Budget: .01},
		{Stage: ErrorStageGraph, ErrorRate: .01, Budget: .01},
	}, DefaultResidualTarget); !errors.Is(err, ErrInvalidErrorBudget) {
		t.Fatalf("duplicate stage error = %v", err)
	}
	report, err := EvaluateErrorBudget([]ErrorBudgetInput{{
		Stage: ErrorStageGraph, ErrorRate: .01, Budget: .01,
	}}, DefaultResidualTarget)
	if err != nil {
		t.Fatal(err)
	}
	report.Lines[0].RecoveryRate = .5
	report.Lines[0].ResidualRate = .005
	report.Lines[0].Variance = -.005
	report.TotalResidual = .005
	report.ReportContentHash, _ = hashErrorBudgetReport(report)
	if err := report.Validate(); !errors.Is(err, ErrInvalidErrorBudget) {
		t.Fatalf("assumed recovery error = %v", err)
	}
}
