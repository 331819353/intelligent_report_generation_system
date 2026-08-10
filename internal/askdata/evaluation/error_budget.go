package evaluation

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"

	"intelligent-report-generation-system/internal/askdata"
)

const (
	ErrorBudgetSchemaVersion = "askdata-error-budget-v1"
	DefaultResidualTarget    = 0.038
	MaxErrorBudgetStages     = 32
	MaxFaultInjectionCases   = 100_000
)

var ErrInvalidErrorBudget = errors.New("error budget report is invalid")

type ErrorBudgetStage string

const (
	ErrorStageIntent     ErrorBudgetStage = "INTENT"
	ErrorStageRecall     ErrorBudgetStage = "RECALL"
	ErrorStageBinding    ErrorBudgetStage = "BINDING"
	ErrorStageGraph      ErrorBudgetStage = "GRAPH"
	ErrorStageIR         ErrorBudgetStage = "IR"
	ErrorStagePlan       ErrorBudgetStage = "PLAN"
	ErrorStageExecution  ErrorBudgetStage = "EXECUTION"
	ErrorStageValidation ErrorBudgetStage = "VALIDATION"
	ErrorStageNarrative  ErrorBudgetStage = "NARRATIVE"
	ErrorStageSecurity   ErrorBudgetStage = "SECURITY"
)

var governedErrorStages = map[ErrorBudgetStage]struct{}{
	ErrorStageIntent: {}, ErrorStageRecall: {}, ErrorStageBinding: {}, ErrorStageGraph: {},
	ErrorStageIR: {}, ErrorStagePlan: {}, ErrorStageExecution: {}, ErrorStageValidation: {},
	ErrorStageNarrative: {}, ErrorStageSecurity: {},
}

type RecoveryMeasurement struct {
	Seed          int64 `json:"seed"`
	SampleCount   int   `json:"sampleCount"`
	DetectedCount int   `json:"detectedCount"`
}

func (measurement RecoveryMeasurement) Validate() error {
	if measurement.SampleCount < 1 || measurement.SampleCount > MaxFaultInjectionCases ||
		measurement.DetectedCount < 0 || measurement.DetectedCount > measurement.SampleCount {
		return ErrInvalidErrorBudget
	}
	return nil
}

func (measurement RecoveryMeasurement) Rate() float64 {
	if measurement.Validate() != nil {
		return 0
	}
	return float64(measurement.DetectedCount) / float64(measurement.SampleCount)
}

type ErrorBudgetInput struct {
	Stage        ErrorBudgetStage     `json:"stage"`
	ErrorRate    float64              `json:"errorRate"`
	Budget       float64              `json:"budget"`
	RecoveryTest *RecoveryMeasurement `json:"recoveryTest,omitempty"`
}

type ErrorBudgetLine struct {
	Stage            ErrorBudgetStage `json:"stage"`
	ErrorRate        float64          `json:"errorRate"`
	RecoveryRate     float64          `json:"recoveryRate"`
	RecoveryMeasured bool             `json:"recoveryMeasured"`
	ResidualRate     float64          `json:"residualRate"`
	Budget           float64          `json:"budget"`
	Variance         float64          `json:"variance"`
}

type BudgetRecommendation struct {
	Stage             ErrorBudgetStage `json:"stage"`
	RequiredReduction float64          `json:"requiredReduction"`
	AvailableHeadroom float64          `json:"availableHeadroom"`
}

type ErrorBudgetReport struct {
	SchemaVersion     string                 `json:"schemaVersion"`
	Lines             []ErrorBudgetLine      `json:"lines"`
	ResidualTarget    float64                `json:"residualTarget"`
	TotalResidual     float64                `json:"totalResidual"`
	TotalBudget       float64                `json:"totalBudget"`
	Passed            bool                   `json:"passed"`
	Recommendations   []BudgetRecommendation `json:"recommendations"`
	InputContentHash  askdata.ContentHash    `json:"inputContentHash"`
	ReportContentHash askdata.ContentHash    `json:"reportContentHash"`
}

// EvaluateErrorBudget applies epsilon=e*(1-r). An absent recovery test is
// deliberately interpreted as r=0, so an assumed recovery can never improve
// a release argument.
func EvaluateErrorBudget(inputs []ErrorBudgetInput, residualTarget float64) (ErrorBudgetReport, error) {
	if len(inputs) < 1 || len(inputs) > MaxErrorBudgetStages || !unitRate(residualTarget) || residualTarget == 0 {
		return ErrorBudgetReport{}, ErrInvalidErrorBudget
	}
	normalized := append([]ErrorBudgetInput(nil), inputs...)
	sort.Slice(normalized, func(i, j int) bool { return normalized[i].Stage < normalized[j].Stage })
	seen := make(map[ErrorBudgetStage]struct{}, len(normalized))
	lines := make([]ErrorBudgetLine, 0, len(normalized))
	for _, input := range normalized {
		if _, ok := governedErrorStages[input.Stage]; !ok || !unitRate(input.ErrorRate) || !unitRate(input.Budget) {
			return ErrorBudgetReport{}, ErrInvalidErrorBudget
		}
		if _, duplicate := seen[input.Stage]; duplicate {
			return ErrorBudgetReport{}, ErrInvalidErrorBudget
		}
		seen[input.Stage] = struct{}{}
		recoveryRate, measured := 0.0, false
		if input.RecoveryTest != nil {
			if err := input.RecoveryTest.Validate(); err != nil {
				return ErrorBudgetReport{}, err
			}
			recoveryRate, measured = input.RecoveryTest.Rate(), true
		}
		residual := input.ErrorRate * (1 - recoveryRate)
		lines = append(lines, ErrorBudgetLine{
			Stage: input.Stage, ErrorRate: input.ErrorRate, RecoveryRate: recoveryRate,
			RecoveryMeasured: measured, ResidualRate: residual, Budget: input.Budget,
			Variance: residual - input.Budget,
		})
	}
	inputPayload, err := json.Marshal(normalized)
	if err != nil {
		return ErrorBudgetReport{}, err
	}
	report := ErrorBudgetReport{
		SchemaVersion: ErrorBudgetSchemaVersion, Lines: lines, ResidualTarget: residualTarget,
		InputContentHash: askdata.HashBytes(inputPayload), Recommendations: []BudgetRecommendation{},
	}
	for _, line := range lines {
		report.TotalResidual += line.ResidualRate
		report.TotalBudget += line.Budget
	}
	report.Passed = report.TotalResidual <= residualTarget
	if !report.Passed {
		report.Recommendations = errorBudgetRecommendations(lines, report.TotalResidual-residualTarget)
	}
	hash, err := hashErrorBudgetReport(report)
	if err != nil {
		return ErrorBudgetReport{}, err
	}
	report.ReportContentHash = hash
	return report, report.Validate()
}

func (report ErrorBudgetReport) Validate() error {
	if report.SchemaVersion != ErrorBudgetSchemaVersion || len(report.Lines) < 1 ||
		len(report.Lines) > MaxErrorBudgetStages || !unitRate(report.ResidualTarget) ||
		report.ResidualTarget == 0 || report.InputContentHash.Validate() != nil ||
		report.ReportContentHash.Validate() != nil {
		return ErrInvalidErrorBudget
	}
	seen := map[ErrorBudgetStage]struct{}{}
	totalResidual, totalBudget := 0.0, 0.0
	for index, line := range report.Lines {
		if _, ok := governedErrorStages[line.Stage]; !ok || !unitRate(line.ErrorRate) ||
			!unitRate(line.RecoveryRate) || !unitRate(line.ResidualRate) || !unitRate(line.Budget) ||
			!finiteRate(line.Variance) || !almostEqual(line.ResidualRate, line.ErrorRate*(1-line.RecoveryRate)) ||
			!almostEqual(line.Variance, line.ResidualRate-line.Budget) {
			return ErrInvalidErrorBudget
		}
		if !line.RecoveryMeasured && line.RecoveryRate != 0 {
			return ErrInvalidErrorBudget
		}
		if index > 0 && report.Lines[index-1].Stage >= line.Stage {
			return ErrInvalidErrorBudget
		}
		if _, duplicate := seen[line.Stage]; duplicate {
			return ErrInvalidErrorBudget
		}
		seen[line.Stage] = struct{}{}
		totalResidual += line.ResidualRate
		totalBudget += line.Budget
	}
	if !almostEqual(totalResidual, report.TotalResidual) || !almostEqual(totalBudget, report.TotalBudget) ||
		report.Passed != (report.TotalResidual <= report.ResidualTarget) {
		return ErrInvalidErrorBudget
	}
	if report.Passed && len(report.Recommendations) != 0 {
		return ErrInvalidErrorBudget
	}
	expectedHash, err := hashErrorBudgetReport(report)
	if err != nil || expectedHash != report.ReportContentHash {
		return ErrInvalidErrorBudget
	}
	return nil
}

func RequirePassingErrorBudget(report *ErrorBudgetReport, expectedHash askdata.ContentHash) error {
	if report == nil {
		return fmt.Errorf("%w: release review attachment is missing", ErrInvalidErrorBudget)
	}
	if err := report.Validate(); err != nil {
		return err
	}
	if expectedHash.Validate() != nil || report.ReportContentHash != expectedHash {
		return fmt.Errorf("%w: release review attachment hash mismatch", ErrInvalidErrorBudget)
	}
	if !report.Passed {
		return fmt.Errorf("%w: residual error %.6f exceeds %.6f", ErrInvalidErrorBudget, report.TotalResidual, report.ResidualTarget)
	}
	return nil
}

func errorBudgetRecommendations(lines []ErrorBudgetLine, required float64) []BudgetRecommendation {
	over := make([]ErrorBudgetLine, 0, len(lines))
	headroom := 0.0
	for _, line := range lines {
		if line.Variance > 0 {
			over = append(over, line)
		} else {
			headroom += -line.Variance
		}
	}
	sort.Slice(over, func(i, j int) bool {
		if !almostEqual(over[i].Variance, over[j].Variance) {
			return over[i].Variance > over[j].Variance
		}
		return over[i].Stage < over[j].Stage
	})
	recommendations := make([]BudgetRecommendation, 0, len(over))
	remaining := required
	for _, line := range over {
		if remaining <= 0 {
			break
		}
		reduction := math.Min(line.Variance, remaining)
		recommendations = append(recommendations, BudgetRecommendation{
			Stage: line.Stage, RequiredReduction: reduction, AvailableHeadroom: headroom,
		})
		remaining -= reduction
	}
	return recommendations
}

func hashErrorBudgetReport(report ErrorBudgetReport) (askdata.ContentHash, error) {
	report.ReportContentHash = ""
	payload, err := json.Marshal(report)
	if err != nil {
		return "", err
	}
	return askdata.HashBytes(payload), nil
}

func unitRate(value float64) bool {
	return finiteRate(value) && value >= 0 && value <= 1
}

func finiteRate(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= -1 && value <= 1
}

func almostEqual(left, right float64) bool {
	return math.Abs(left-right) <= 1e-12
}
