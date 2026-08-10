package suites

import (
	"context"
	"fmt"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/ir"
	calendarfixture "intelligent-report-generation-system/internal/askdata/testfixture/calendar"
)

type timeResolverFixture struct {
	wrong map[askdata.ID]string
}

func (resolver timeResolverFixture) ResolveTimeCase(_ context.Context, evaluationCase TimeSuiteCase) (ir.ResolvedTimeSpec, error) {
	result := evaluationCase.ExpectedResolution
	switch resolver.wrong[evaluationCase.CaseID] {
	case "resolvedStart":
		result.ResolvedStart = result.ResolvedStart.AddDate(0, 0, 1)
	case "resolvedEndExclusive":
		result.ResolvedEndExclusive = result.ResolvedEndExclusive.AddDate(0, 0, 1)
	case "policyApplied":
		result.PolicyApplied = "WRONG"
	case "comparison":
		result.Comparison = nil
	}
	return result, nil
}

func TestTimeSuiteInventoryAndContractChangeRequiresFullRun(t *testing.T) {
	cases := syntheticTimeCases(t)
	if err := ValidateTimeSuiteInventory(cases); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildTimeSuiteRunPlan(cases, []askdata.ID{cases[0].CaseID}, TimeContractChangedTrigger); err == nil {
		t.Fatal("partial time-contract regression accepted")
	}
	plan, err := BuildTimeSuiteRunPlan(cases, nil, TimeContractChangedTrigger)
	if err != nil || !plan.FullRegression || len(plan.SelectedCaseIDs) != len(cases) {
		t.Fatalf("full plan = %#v, %v", plan, err)
	}
}

func TestTimeSuiteAssertsAllFourFieldsAndNinetyNinePercentGate(t *testing.T) {
	cases := syntheticTimeCases(t)
	plan, err := BuildTimeSuiteRunPlan(cases, nil, TimeContractChangedTrigger)
	if err != nil {
		t.Fatal(err)
	}
	wrong := map[askdata.ID]string{
		cases[0].CaseID: "resolvedStart", cases[1].CaseID: "resolvedEndExclusive",
		cases[2].CaseID: "policyApplied", cases[3].CaseID: "comparison",
	}
	report, err := EvaluateTimeSuite(context.Background(), cases, plan, timeResolverFixture{wrong: wrong})
	if err != nil {
		t.Fatal(err)
	}
	if report.GatePassed || report.Accuracy >= TimeSuiteThreshold || len(report.Failures) != 4 {
		t.Fatalf("time report = %#v", report)
	}
	for index, field := range []string{"resolvedStart", "resolvedEndExclusive", "policyApplied", "comparison"} {
		if len(report.Failures[index].Fields) != 1 || report.Failures[index].Fields[0] != field {
			t.Fatalf("failure %d = %#v", index, report.Failures[index])
		}
	}
}

func TestSyntheticCalendarIsExplicitlyNonBusiness(t *testing.T) {
	fixture := calendarfixture.SyntheticShanghai()
	if err := fixture.Validate(); err != nil || !fixture.Synthetic {
		t.Fatalf("fixture = %#v, %v", fixture, err)
	}
}

func syntheticTimeCases(t *testing.T) []TimeSuiteCase {
	t.Helper()
	fixture := calendarfixture.SyntheticShanghai()
	loc, err := time.LoadLocation(fixture.Timezone)
	if err != nil {
		t.Fatal(err)
	}
	type categoryCount struct {
		category TimeSuiteCategory
		count    int
	}
	distribution := []categoryCount{
		{TimeIncompletePeriod, 40}, {TimeIncompleteComparison, 40}, {TimeCompleteComparison, 40},
		{TimeMonthEndOverflow, 20}, {TimeFiscalCalendar, 40}, {TimeDataAvailability, 30},
		{TimeCrossTimezoneYear, 20},
	}
	cases := make([]TimeSuiteCase, 0, TimeSuiteCaseCount)
	for _, group := range distribution {
		for index := 0; index < group.count; index++ {
			start := time.Date(2026, time.January, 1, 0, 0, 0, 0, loc).AddDate(0, 0, index)
			comparison := &ir.ResolvedComparison{
				Type: "YEAR_OVER_YEAR", Periods: 1, Alignment: "SAME_DAY_COUNT",
				ResolvedStart: start.AddDate(-1, 0, 0), ResolvedEndExclusive: start.AddDate(-1, 0, 2),
			}
			cases = append(cases, TimeSuiteCase{
				CaseID: askdata.ID(fmt.Sprintf("synthetic-%s-%03d", group.category, index)), Category: group.category,
				Synthetic: true, CalendarFixtureID: fixture.ID, TimeContractHash: fixture.ContentHash,
				ExpectedResolution: ir.ResolvedTimeSpec{
					RequestedPeriod: "SYNTHETIC", Grain: "DAY", PolicyApplied: "MTD", PolicySource: "TIME_CONTRACT",
					ResolvedStart: start, ResolvedEndExclusive: start.AddDate(0, 0, 2),
					DataAvailableThrough: start.AddDate(0, 0, 1), Timezone: fixture.Timezone,
					CalendarVersionID: string(fixture.ID), Comparison: comparison,
				},
			})
		}
	}
	return cases
}
