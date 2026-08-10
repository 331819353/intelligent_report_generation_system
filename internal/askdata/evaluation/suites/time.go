package suites

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/ir"
)

const (
	TimeSuiteThreshold = 0.99
	TimeSuiteCaseCount = 230
)

var ErrInvalidTimeSuite = errors.New("time evaluation suite is invalid")

type TimeSuiteCategory string

const (
	TimeIncompletePeriod       TimeSuiteCategory = "INCOMPLETE_PERIOD"
	TimeIncompleteComparison   TimeSuiteCategory = "INCOMPLETE_PERIOD_COMPARISON"
	TimeCompleteComparison     TimeSuiteCategory = "COMPLETE_PERIOD_COMPARISON"
	TimeMonthEndOverflow       TimeSuiteCategory = "MONTH_END_OVERFLOW"
	TimeFiscalCalendar         TimeSuiteCategory = "FISCAL_CALENDAR"
	TimeDataAvailability       TimeSuiteCategory = "DATA_AVAILABILITY"
	TimeCrossTimezoneYear      TimeSuiteCategory = "CROSS_TIMEZONE_YEAR"
	TimeContractChangedTrigger string            = "TIME_CONTRACT_CHANGED"
)

var minimumTimeCategoryCases = map[TimeSuiteCategory]int{
	TimeIncompletePeriod: 40, TimeIncompleteComparison: 40, TimeCompleteComparison: 40,
	TimeMonthEndOverflow: 20, TimeFiscalCalendar: 40, TimeDataAvailability: 30,
	TimeCrossTimezoneYear: 20,
}

type TimeSuiteCase struct {
	CaseID             askdata.ID
	Category           TimeSuiteCategory
	Synthetic          bool
	CalendarFixtureID  askdata.ID
	TimeContractHash   askdata.ContentHash
	ExpectedResolution ir.ResolvedTimeSpec
}

type TimeSuiteResolver interface {
	ResolveTimeCase(context.Context, TimeSuiteCase) (ir.ResolvedTimeSpec, error)
}

type TimeSuiteRunPlan struct {
	Trigger         string
	AllCaseCount    int
	SelectedCaseIDs []askdata.ID
	FullRegression  bool
}

type TimeSuiteFailure struct {
	CaseID askdata.ID `json:"caseId"`
	Fields []string   `json:"fields"`
	Code   string     `json:"code"`
}

type TimeSuiteReport struct {
	CaseCount      int                `json:"caseCount"`
	PassedCount    int                `json:"passedCount"`
	Accuracy       float64            `json:"accuracy"`
	Threshold      float64            `json:"threshold"`
	FullRegression bool               `json:"fullRegression"`
	GatePassed     bool               `json:"gatePassed"`
	Failures       []TimeSuiteFailure `json:"failures"`
}

func ValidateTimeSuiteInventory(cases []TimeSuiteCase) error {
	if len(cases) < TimeSuiteCaseCount {
		return fmt.Errorf("%w: at least %d cases are required", ErrInvalidTimeSuite, TimeSuiteCaseCount)
	}
	counts := map[TimeSuiteCategory]int{}
	seen := map[askdata.ID]struct{}{}
	for _, evaluationCase := range cases {
		if evaluationCase.CaseID.Validate() != nil || evaluationCase.CalendarFixtureID.Validate() != nil ||
			evaluationCase.TimeContractHash.Validate() != nil || !evaluationCase.Synthetic ||
			validateResolvedTimeSpecForSuite(evaluationCase.ExpectedResolution) != nil {
			return ErrInvalidTimeSuite
		}
		if _, ok := minimumTimeCategoryCases[evaluationCase.Category]; !ok {
			return ErrInvalidTimeSuite
		}
		if _, duplicate := seen[evaluationCase.CaseID]; duplicate {
			return ErrInvalidTimeSuite
		}
		seen[evaluationCase.CaseID] = struct{}{}
		counts[evaluationCase.Category]++
	}
	for category, minimum := range minimumTimeCategoryCases {
		if counts[category] < minimum {
			return fmt.Errorf("%w: %s requires at least %d cases", ErrInvalidTimeSuite, category, minimum)
		}
	}
	return nil
}

func BuildTimeSuiteRunPlan(cases []TimeSuiteCase, selected []askdata.ID, trigger string) (TimeSuiteRunPlan, error) {
	if err := ValidateTimeSuiteInventory(cases); err != nil {
		return TimeSuiteRunPlan{}, err
	}
	available := make(map[askdata.ID]struct{}, len(cases))
	for _, evaluationCase := range cases {
		available[evaluationCase.CaseID] = struct{}{}
	}
	if len(selected) == 0 {
		selected = make([]askdata.ID, 0, len(cases))
		for id := range available {
			selected = append(selected, id)
		}
	}
	seen := map[askdata.ID]struct{}{}
	for _, id := range selected {
		if _, exists := available[id]; !exists {
			return TimeSuiteRunPlan{}, ErrInvalidTimeSuite
		}
		if _, duplicate := seen[id]; duplicate {
			return TimeSuiteRunPlan{}, ErrInvalidTimeSuite
		}
		seen[id] = struct{}{}
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i] < selected[j] })
	full := len(selected) == len(cases)
	if trigger == TimeContractChangedTrigger && !full {
		return TimeSuiteRunPlan{}, fmt.Errorf("%w: time contract changes require a full regression", ErrInvalidTimeSuite)
	}
	return TimeSuiteRunPlan{Trigger: trigger, AllCaseCount: len(cases), SelectedCaseIDs: selected, FullRegression: full}, nil
}

func EvaluateTimeSuite(ctx context.Context, cases []TimeSuiteCase, plan TimeSuiteRunPlan, resolver TimeSuiteResolver) (TimeSuiteReport, error) {
	if ctx == nil || resolver == nil || len(plan.SelectedCaseIDs) == 0 || plan.AllCaseCount != len(cases) ||
		(plan.Trigger == TimeContractChangedTrigger && !plan.FullRegression) {
		return TimeSuiteReport{}, ErrInvalidTimeSuite
	}
	byID := make(map[askdata.ID]TimeSuiteCase, len(cases))
	for _, evaluationCase := range cases {
		byID[evaluationCase.CaseID] = evaluationCase
	}
	report := TimeSuiteReport{
		CaseCount: len(plan.SelectedCaseIDs), Threshold: TimeSuiteThreshold,
		FullRegression: plan.FullRegression, Failures: []TimeSuiteFailure{},
	}
	for _, id := range plan.SelectedCaseIDs {
		evaluationCase, exists := byID[id]
		if !exists {
			return TimeSuiteReport{}, ErrInvalidTimeSuite
		}
		actual, err := resolver.ResolveTimeCase(ctx, evaluationCase)
		if err != nil {
			report.Failures = append(report.Failures, TimeSuiteFailure{CaseID: id, Code: "TIME_RESOLUTION_FAILED", Fields: []string{}})
			continue
		}
		fields := CompareResolvedTimeSpec(evaluationCase.ExpectedResolution, actual)
		if len(fields) == 0 {
			report.PassedCount++
			continue
		}
		report.Failures = append(report.Failures, TimeSuiteFailure{CaseID: id, Code: "TIME_CONTRACT_MISMATCH", Fields: fields})
	}
	report.Accuracy = float64(report.PassedCount) / float64(report.CaseCount)
	report.GatePassed = report.FullRegression && report.Accuracy >= report.Threshold
	return report, nil
}

// CompareResolvedTimeSpec enforces the four governed assertions required by
// the suite. It returns field paths only, never timestamps from sealed cases.
func CompareResolvedTimeSpec(expected, actual ir.ResolvedTimeSpec) []string {
	fields := make([]string, 0, 4)
	if !expected.ResolvedStart.Equal(actual.ResolvedStart) {
		fields = append(fields, "resolvedStart")
	}
	if !expected.ResolvedEndExclusive.Equal(actual.ResolvedEndExclusive) {
		fields = append(fields, "resolvedEndExclusive")
	}
	if expected.PolicyApplied != actual.PolicyApplied {
		fields = append(fields, "policyApplied")
	}
	if !reflect.DeepEqual(expected.Comparison, actual.Comparison) {
		fields = append(fields, "comparison")
	}
	return fields
}

func validateResolvedTimeSpecForSuite(spec ir.ResolvedTimeSpec) error {
	if spec.ResolvedStart.IsZero() || spec.ResolvedEndExclusive.IsZero() ||
		!spec.ResolvedEndExclusive.After(spec.ResolvedStart) || spec.PolicyApplied == "" || spec.Timezone == "" {
		return ErrInvalidTimeSuite
	}
	location, err := time.LoadLocation(spec.Timezone)
	if err != nil || spec.ResolvedStart.Location().String() != location.String() ||
		spec.ResolvedEndExclusive.Location().String() != location.String() {
		return ErrInvalidTimeSuite
	}
	return nil
}
