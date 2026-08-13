package goldenset

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/compiler"
	"intelligent-report-generation-system/internal/askdata/evaluation/suites"
	"intelligent-report-generation-system/internal/askdata/ir"
	"intelligent-report-generation-system/internal/askdata/registry"
	"intelligent-report-generation-system/internal/askdata/testfixture/calendar"
)

// TimeSuiteVersion identifies the inventory. Changing a case, a calendar
// fixture or a contract parameter must change this version, because a run
// report is only comparable against another run of the same inventory.
const TimeSuiteVersion = "askdata-time-golden-v1"

var ErrTimeGoldenSet = errors.New("time golden set is invalid")

// timeScenario pairs the public, expectation-only case with the private input
// that produces it. The public case deliberately carries no request, mirroring
// suites.TimeSuiteCase: an evaluation case must not double as a way to read the
// question back out.
type timeScenario struct {
	public   suites.TimeSuiteCase
	contract registry.TimeContractVersion
	query    ir.SemanticIR
	meta     compiler.MaterializationMeta
	calendar *FiscalCalendar
}

// TimeSuite is both the inventory and the suites.TimeSuiteResolver for it. It
// resolves every case through compiler.Resolve — the same function the query
// pipeline calls — so a regression in time policy cannot pass here and fail in
// production.
type TimeSuite struct {
	scenarios []timeScenario
	byID      map[askdata.ID]timeScenario
}

func NewTimeSuite() (*TimeSuite, error) {
	scenarios, err := buildTimeScenarios()
	if err != nil {
		return nil, err
	}
	sort.Slice(scenarios, func(left, right int) bool {
		return scenarios[left].public.CaseID < scenarios[right].public.CaseID
	})
	suite := &TimeSuite{scenarios: scenarios, byID: make(map[askdata.ID]timeScenario, len(scenarios))}
	for _, scenario := range scenarios {
		if _, duplicate := suite.byID[scenario.public.CaseID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate case %s", ErrTimeGoldenSet, scenario.public.CaseID)
		}
		suite.byID[scenario.public.CaseID] = scenario
	}
	if err := suites.ValidateTimeSuiteInventory(suite.Cases()); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTimeGoldenSet, err)
	}
	return suite, nil
}

func (suite *TimeSuite) Cases() []suites.TimeSuiteCase {
	cases := make([]suites.TimeSuiteCase, 0, len(suite.scenarios))
	for _, scenario := range suite.scenarios {
		cases = append(cases, scenario.public)
	}
	return cases
}

// ResolveTimeCase refuses a case it does not own and refuses a case whose
// expectation has been altered. Accepting a caller-supplied expectation would
// turn the suite into an echo of its input.
func (suite *TimeSuite) ResolveTimeCase(
	ctx context.Context,
	evaluationCase suites.TimeSuiteCase,
) (ir.ResolvedTimeSpec, error) {
	scenario, known := suite.byID[evaluationCase.CaseID]
	if !known {
		return ir.ResolvedTimeSpec{}, fmt.Errorf("%w: unknown case %s", ErrTimeGoldenSet, evaluationCase.CaseID)
	}
	if evaluationCase.TimeContractHash != scenario.public.TimeContractHash ||
		evaluationCase.CalendarFixtureID != scenario.public.CalendarFixtureID ||
		evaluationCase.Category != scenario.public.Category {
		return ir.ResolvedTimeSpec{}, fmt.Errorf("%w: case %s was substituted", ErrTimeGoldenSet, evaluationCase.CaseID)
	}
	meta := scenario.meta
	meta.Calendar = scenario.calendar
	return compiler.Resolve(ctx, scenario.query, scenario.contract, meta)
}

// Run evaluates the whole inventory. A contract-change trigger forces the full
// regression, which is the reason BuildTimeSuiteRunPlan exists.
func (suite *TimeSuite) Run(ctx context.Context, trigger string) (suites.TimeSuiteReport, error) {
	cases := suite.Cases()
	plan, err := suites.BuildTimeSuiteRunPlan(cases, nil, trigger)
	if err != nil {
		return suites.TimeSuiteReport{}, err
	}
	return suites.EvaluateTimeSuite(ctx, cases, plan, suite)
}

func syntheticTimeContract(
	fixture calendar.Fixture,
	policy registry.IncompletePeriodPolicy,
	alignment registry.ComparisonAlignment,
	overflow registry.MonthEndOverflowRule,
	calendarVersionID string,
) registry.TimeContractVersion {
	contract := registry.TimeContractVersion{
		ID: "time-contract-" + string(fixture.ID), TenantID: "tenant-golden", DomainID: "domain-golden",
		TimeContractID: "time-contract-" + string(fixture.ID), VersionNo: 1, Status: registry.VersionStatusCertified,
		Timezone: fixture.Timezone, WeekStart: weekStartOf(fixture.WeekStart),
		WeekNumbering: registry.WeekNumberingISO, FiscalYearStartMonth: int(fixture.FiscalYearStart),
		FiscalMonthRule: registry.FiscalMonthCalendar, IncompletePeriodPolicy: policy,
		ComparisonAlignment: alignment, MonthEndOverflowRule: overflow,
		SupportedGrains: []registry.TimeGrain{
			registry.TimeGrainDay, registry.TimeGrainWeek, registry.TimeGrainMonth,
			registry.TimeGrainQuarter, registry.TimeGrainYear, registry.TimeGrainFiscalMonth,
			registry.TimeGrainFiscalQuarter, registry.TimeGrainFiscalYear,
		},
		DataAvailableThroughExpr: "max(business_date)", ExpectedLagHours: 24,
		CalendarDatasetVersionID: calendarVersionID,
	}
	contract.ContentHash = askdata.HashBytes([]byte(fmt.Sprintf(
		"%s|%s|%d|%d|%s|%s|%s|%s",
		TimeSuiteVersion, contract.Timezone, contract.FiscalYearStartMonth, int(fixture.WeekStart),
		policy, alignment, overflow, calendarVersionID,
	)))
	return contract
}

func weekStartOf(day time.Weekday) registry.WeekStart {
	if day == time.Sunday {
		return registry.WeekStartSunday
	}
	return registry.WeekStartMonday
}

func timeQuery(
	fixture calendar.Fixture,
	requested string,
	grain ir.TimeGrain,
	start, endExclusive time.Time,
	comparison *ir.Comparison,
) ir.SemanticIR {
	timeRange := ir.TimeRange{
		DimensionVersionID: "dimension-order-date-v1",
		Start:              start.Format("2006-01-02"),
		EndExclusive:       endExclusive.Format("2006-01-02"),
		Timezone:           fixture.Timezone,
		RequestedPeriod:    requested,
		Grain:              grain,
	}
	return ir.SemanticIR{TimeRange: &timeRange, Comparison: comparison}
}
