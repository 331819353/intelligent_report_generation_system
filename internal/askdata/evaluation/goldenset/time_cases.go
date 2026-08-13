package goldenset

import (
	"fmt"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/compiler"
	"intelligent-report-generation-system/internal/askdata/evaluation/suites"
	"intelligent-report-generation-system/internal/askdata/ir"
	"intelligent-report-generation-system/internal/askdata/registry"
	"intelligent-report-generation-system/internal/askdata/testfixture/calendar"
)

// goldenCalendarVersionID names the synthetic calendar dataset version the
// fiscal cases resolve against. It is a fixture identity, never a real dataset.
const goldenCalendarVersionID = "dataset-version-golden-calendar-v1"

var naturalGrains = []ir.TimeGrain{
	ir.TimeGrainDay, ir.TimeGrainWeek, ir.TimeGrainMonth, ir.TimeGrainQuarter, ir.TimeGrainYear,
}

type timeScenarioBuilder struct {
	scenarios []timeScenario
	err       error
}

func (builder *timeScenarioBuilder) fail(err error) {
	if builder.err == nil && err != nil {
		builder.err = err
	}
}

func (builder *timeScenarioBuilder) add(
	id string,
	category suites.TimeSuiteCategory,
	fixture calendar.Fixture,
	contract registry.TimeContractVersion,
	query ir.SemanticIR,
	meta compiler.MaterializationMeta,
	fiscal *FiscalCalendar,
	expected ir.ResolvedTimeSpec,
) {
	if builder.err != nil {
		return
	}
	expected.Timezone = contract.Timezone
	builder.scenarios = append(builder.scenarios, timeScenario{
		public: suites.TimeSuiteCase{
			CaseID: askdata.ID(id), Category: category, Synthetic: true,
			CalendarFixtureID: fixture.ID, TimeContractHash: contract.ContentHash,
			ExpectedResolution: expected,
		},
		contract: contract, query: query, meta: meta, calendar: fiscal,
	})
}

func buildTimeScenarios() ([]timeScenario, error) {
	builder := &timeScenarioBuilder{}
	shanghai := calendar.SyntheticShanghai()
	fiscal, err := NewFiscalCalendar(shanghai, goldenCalendarVersionID, 2022, 2030)
	if err != nil {
		return nil, err
	}
	builder.incompletePeriods(shanghai)
	builder.incompletePeriodComparisons(shanghai)
	builder.completePeriodComparisons(shanghai)
	builder.monthEndOverflow(shanghai)
	builder.fiscalCalendar(shanghai, fiscal)
	builder.dataAvailability(shanghai)
	builder.crossTimezoneYear()
	if builder.err != nil {
		return nil, builder.err
	}
	return builder.scenarios, nil
}

// incompletePeriods covers the three incomplete-period policies and, on the
// third anchor, the metric-level override that must beat the time contract.
func (builder *timeScenarioBuilder) incompletePeriods(fixture calendar.Fixture) {
	location, err := fixture.Location()
	if err != nil {
		builder.fail(err)
		return
	}
	policies := []registry.IncompletePeriodPolicy{
		registry.IncompletePeriodMTD, registry.IncompletePeriodFull, registry.IncompletePeriodLastComplete,
	}
	anchors := []time.Time{
		time.Date(2026, 8, 13, 0, 0, 0, 0, location),
		time.Date(2026, 2, 27, 0, 0, 0, 0, location),
		time.Date(2026, 12, 31, 0, 0, 0, 0, location),
	}
	index := 0
	for anchorIndex, anchor := range anchors {
		for _, policy := range policies {
			for _, grain := range naturalGrains {
				index++
				// The third anchor pins the contract to FULL_PERIOD and overrides it
				// at the metric level, so a broken precedence order fails here
				// instead of surfacing as a wrong number in production.
				contractPolicy, meta := policy, compiler.MaterializationMeta{DataAvailableThrough: anchor}
				if anchorIndex == 2 {
					contractPolicy = registry.IncompletePeriodFull
					meta.PolicyApplied, meta.PolicySource = policy, registry.PolicySourceMetric
				}
				contract := syntheticTimeContract(
					fixture, contractPolicy, registry.ComparisonSameDayCount,
					registry.MonthEndClampToLastDay, goldenCalendarVersionID,
				)
				start, end, periodErr := naturalPeriod(grain, anchor, fixture.WeekStart, location)
				if periodErr != nil {
					builder.fail(periodErr)
					return
				}
				expectedStart, expectedEnd, expectErr := expectedNaturalResolution(
					policy, grain, start, end, anchor, fixture.WeekStart, location,
				)
				if expectErr != nil {
					builder.fail(expectErr)
					return
				}
				builder.add(
					fmt.Sprintf("time-golden-incomplete-%03d", index),
					suites.TimeIncompletePeriod, fixture, contract,
					timeQuery(fixture, currentPeriodCode(grain), grain, start, end, nil),
					meta, nil,
					ir.ResolvedTimeSpec{
						ResolvedStart: expectedStart, ResolvedEndExclusive: expectedEnd,
						PolicyApplied: string(policy),
					},
				)
			}
		}
	}
}

// incompletePeriodComparisons pairs an unfinished period with a comparison. The
// baseline must be shortened by the same alignment contract, or the answer
// compares a partial period against a whole one.
func (builder *timeScenarioBuilder) incompletePeriodComparisons(fixture calendar.Fixture) {
	location, err := fixture.Location()
	if err != nil {
		builder.fail(err)
		return
	}
	grains := []ir.TimeGrain{ir.TimeGrainWeek, ir.TimeGrainMonth, ir.TimeGrainQuarter, ir.TimeGrainYear}
	policies := []registry.IncompletePeriodPolicy{registry.IncompletePeriodMTD, registry.IncompletePeriodFull}
	alignments := []registry.ComparisonAlignment{
		registry.ComparisonSameDayCount, registry.ComparisonSameCalendarRange,
	}
	anchors := []time.Time{
		time.Date(2026, 8, 13, 0, 0, 0, 0, location),
		time.Date(2026, 2, 27, 0, 0, 0, 0, location),
		time.Date(2026, 5, 20, 0, 0, 0, 0, location),
	}
	index := 0
	for _, anchor := range anchors {
		for _, grain := range grains {
			for _, policy := range policies {
				for _, alignment := range alignments {
					index++
					contract := syntheticTimeContract(
						fixture, policy, alignment, registry.MonthEndClampToLastDay, goldenCalendarVersionID,
					)
					start, end, periodErr := naturalPeriod(grain, anchor, fixture.WeekStart, location)
					if periodErr != nil {
						builder.fail(periodErr)
						return
					}
					resolvedStart, resolvedEnd, expectErr := expectedNaturalResolution(
						policy, grain, start, end, anchor, fixture.WeekStart, location,
					)
					if expectErr != nil {
						builder.fail(expectErr)
						return
					}
					comparison, comparisonErr := expectedNaturalComparison(
						ir.ComparisonYearOverYear, 1, grain, resolvedStart, resolvedEnd,
						alignment, registry.MonthEndClampToLastDay, location,
					)
					if comparisonErr != nil {
						builder.fail(comparisonErr)
						return
					}
					builder.add(
						fmt.Sprintf("time-golden-incomplete-comparison-%03d", index),
						suites.TimeIncompleteComparison, fixture, contract,
						timeQuery(fixture, currentPeriodCode(grain), grain, start, end,
							&ir.Comparison{Type: ir.ComparisonYearOverYear, Periods: 1}),
						compiler.MaterializationMeta{DataAvailableThrough: anchor}, nil,
						ir.ResolvedTimeSpec{
							ResolvedStart: resolvedStart, ResolvedEndExclusive: resolvedEnd,
							PolicyApplied: string(policy), Comparison: &comparison,
						},
					)
				}
			}
		}
	}
}

// completePeriodComparisons uses closed periods, where the incomplete-period
// policy must not move a boundary at all.
func (builder *timeScenarioBuilder) completePeriodComparisons(fixture calendar.Fixture) {
	location, err := fixture.Location()
	if err != nil {
		builder.fail(err)
		return
	}
	type combination struct {
		grain      ir.TimeGrain
		comparison ir.ComparisonType
		periods    int
	}
	combinations := []combination{
		{ir.TimeGrainMonth, ir.ComparisonYearOverYear, 1},
		{ir.TimeGrainMonth, ir.ComparisonMonthOverMonth, 1},
		{ir.TimeGrainMonth, ir.ComparisonPeriodOverPeriod, 2},
		{ir.TimeGrainQuarter, ir.ComparisonYearOverYear, 1},
		{ir.TimeGrainQuarter, ir.ComparisonPeriodOverPeriod, 1},
		{ir.TimeGrainYear, ir.ComparisonYearOverYear, 2},
		{ir.TimeGrainYear, ir.ComparisonPeriodOverPeriod, 1},
		{ir.TimeGrainWeek, ir.ComparisonPeriodOverPeriod, 1},
	}
	alignments := []registry.ComparisonAlignment{
		registry.ComparisonSameDayCount, registry.ComparisonSameCalendarRange,
	}
	anchors := []time.Time{
		time.Date(2026, 8, 13, 0, 0, 0, 0, location),
		time.Date(2026, 3, 5, 0, 0, 0, 0, location),
		time.Date(2025, 11, 19, 0, 0, 0, 0, location),
	}
	index := 0
	for _, anchor := range anchors {
		for _, item := range combinations {
			for _, alignment := range alignments {
				index++
				contract := syntheticTimeContract(
					fixture, registry.IncompletePeriodMTD, alignment,
					registry.MonthEndClampToLastDay, goldenCalendarVersionID,
				)
				current, _, periodErr := naturalPeriod(item.grain, anchor, fixture.WeekStart, location)
				if periodErr != nil {
					builder.fail(periodErr)
					return
				}
				// One completed period back, so the request can never be current.
				start, end, previousErr := naturalPeriod(
					item.grain, current.AddDate(0, 0, -1), fixture.WeekStart, location,
				)
				if previousErr != nil {
					builder.fail(previousErr)
					return
				}
				comparison, comparisonErr := expectedNaturalComparison(
					item.comparison, item.periods, item.grain, start, end,
					alignment, registry.MonthEndClampToLastDay, location,
				)
				if comparisonErr != nil {
					builder.fail(comparisonErr)
					return
				}
				builder.add(
					fmt.Sprintf("time-golden-complete-comparison-%03d", index),
					suites.TimeCompleteComparison, fixture, contract,
					timeQuery(fixture, closedPeriodCode(item.grain), item.grain, start, end,
						&ir.Comparison{Type: item.comparison, Periods: item.periods}),
					compiler.MaterializationMeta{DataAvailableThrough: anchor}, nil,
					ir.ResolvedTimeSpec{
						ResolvedStart: start, ResolvedEndExclusive: end,
						PolicyApplied: string(registry.IncompletePeriodMTD), Comparison: &comparison,
					},
				)
			}
		}
	}
}

// monthEndOverflow anchors day-grain ranges on the 29th to 31st so every
// comparison shift lands on a month that may be too short.
func (builder *timeScenarioBuilder) monthEndOverflow(fixture calendar.Fixture) {
	location, err := fixture.Location()
	if err != nil {
		builder.fail(err)
		return
	}
	// Month ends only: a range that both starts and ends on a day the target
	// month lacks would clamp to a single instant, and "compare this day with
	// nothing" is a refusal in the compiler, not a resolvable expectation.
	anchors := []time.Time{
		time.Date(2026, 1, 31, 0, 0, 0, 0, location),
		time.Date(2026, 3, 31, 0, 0, 0, 0, location),
		time.Date(2026, 4, 30, 0, 0, 0, 0, location),
		time.Date(2026, 5, 31, 0, 0, 0, 0, location),
		time.Date(2026, 6, 30, 0, 0, 0, 0, location),
		time.Date(2026, 7, 31, 0, 0, 0, 0, location),
		time.Date(2026, 8, 31, 0, 0, 0, 0, location),
		time.Date(2026, 9, 30, 0, 0, 0, 0, location),
		time.Date(2026, 10, 31, 0, 0, 0, 0, location),
		time.Date(2026, 12, 31, 0, 0, 0, 0, location),
		time.Date(2024, 2, 29, 0, 0, 0, 0, location),
		time.Date(2028, 2, 29, 0, 0, 0, 0, location),
	}
	alignments := []registry.ComparisonAlignment{
		registry.ComparisonSameDayCount, registry.ComparisonSameCalendarRange,
	}
	index := 0
	for _, anchor := range anchors {
		for _, alignment := range alignments {
			for _, comparisonType := range []ir.ComparisonType{
				ir.ComparisonMonthOverMonth, ir.ComparisonYearOverYear,
			} {
				index++
				contract := syntheticTimeContract(
					fixture, registry.IncompletePeriodMTD, alignment,
					registry.MonthEndClampToLastDay, goldenCalendarVersionID,
				)
				start := anchor
				end := anchor.AddDate(0, 0, 1)
				comparison, comparisonErr := expectedNaturalComparison(
					comparisonType, 1, ir.TimeGrainDay, start, end,
					alignment, registry.MonthEndClampToLastDay, location,
				)
				if comparisonErr != nil {
					builder.fail(comparisonErr)
					return
				}
				builder.add(
					fmt.Sprintf("time-golden-month-end-%03d", index),
					suites.TimeMonthEndOverflow, fixture, contract,
					timeQuery(fixture, "ABSOLUTE", ir.TimeGrainDay, start, end,
						&ir.Comparison{Type: comparisonType, Periods: 1}),
					compiler.MaterializationMeta{DataAvailableThrough: anchor.AddDate(0, 0, 30)}, nil,
					ir.ResolvedTimeSpec{
						ResolvedStart: start, ResolvedEndExclusive: end,
						PolicyApplied: string(registry.IncompletePeriodMTD), Comparison: &comparison,
					},
				)
			}
		}
	}
}

// fiscalCalendar resolves against the materialized period table. The compiler
// has no fallback fiscal arithmetic, so a missing period must fail rather than
// be guessed — these cases stay inside the table on purpose.
func (builder *timeScenarioBuilder) fiscalCalendar(fixture calendar.Fixture, table *FiscalCalendar) {
	location, err := fixture.Location()
	if err != nil {
		builder.fail(err)
		return
	}
	grains := []registry.TimeGrain{
		registry.TimeGrainFiscalMonth, registry.TimeGrainFiscalQuarter, registry.TimeGrainFiscalYear,
	}
	anchors := []time.Time{
		time.Date(2026, 8, 13, 0, 0, 0, 0, location),
		time.Date(2026, 2, 27, 0, 0, 0, 0, location),
	}
	index := 0
	for _, anchor := range anchors {
		for _, grain := range grains {
			period, lookupErr := table.Period(grain, anchor, 0)
			if lookupErr != nil {
				builder.fail(lookupErr)
				return
			}
			for _, policy := range []registry.IncompletePeriodPolicy{
				registry.IncompletePeriodFull, registry.IncompletePeriodMTD, registry.IncompletePeriodLastComplete,
			} {
				index++
				contract := syntheticTimeContract(
					fixture, policy, registry.ComparisonSameDayCount,
					registry.MonthEndClampToLastDay, goldenCalendarVersionID,
				)
				resolvedStart, resolvedEnd := period.Start, period.EndExclusive
				switch policy {
				case registry.IncompletePeriodMTD:
					if available := midnight(anchor, location).AddDate(0, 0, 1); available.Before(resolvedEnd) {
						resolvedEnd = available
					}
				case registry.IncompletePeriodLastComplete:
					previous, previousErr := table.Period(grain, anchor, -1)
					if previousErr != nil {
						builder.fail(previousErr)
						return
					}
					resolvedStart, resolvedEnd = previous.Start, previous.EndExclusive
				}
				builder.add(
					fmt.Sprintf("time-golden-fiscal-%03d", index),
					suites.TimeFiscalCalendar, fixture, contract,
					timeQuery(fixture, "CURRENT_"+string(grain), "", period.Start, period.EndExclusive, nil),
					compiler.MaterializationMeta{DataAvailableThrough: anchor}, table,
					ir.ResolvedTimeSpec{
						ResolvedStart: resolvedStart, ResolvedEndExclusive: resolvedEnd,
						PolicyApplied: string(policy),
					},
				)
			}
		}
	}
	for _, anchor := range anchors {
		for _, grain := range grains {
			period, lookupErr := table.Period(grain, anchor, 0)
			if lookupErr != nil {
				builder.fail(lookupErr)
				return
			}
			for _, comparisonType := range []ir.ComparisonType{
				ir.ComparisonPeriodOverPeriod, ir.ComparisonYearOverYear,
			} {
				for _, alignment := range []registry.ComparisonAlignment{
					registry.ComparisonSameDayCount, registry.ComparisonSameCalendarRange,
				} {
					index++
					contract := syntheticTimeContract(
						fixture, registry.IncompletePeriodFull, alignment,
						registry.MonthEndClampToLastDay, goldenCalendarVersionID,
					)
					comparison, comparisonErr := expectedFiscalComparison(
						table, comparisonType, 1, grain, period,
						period.Start, period.EndExclusive, alignment,
					)
					if comparisonErr != nil {
						builder.fail(comparisonErr)
						return
					}
					builder.add(
						fmt.Sprintf("time-golden-fiscal-%03d", index),
						suites.TimeFiscalCalendar, fixture, contract,
						timeQuery(fixture, "CURRENT_"+string(grain), "", period.Start, period.EndExclusive,
							&ir.Comparison{Type: comparisonType, Periods: 1}),
						compiler.MaterializationMeta{DataAvailableThrough: anchor}, table,
						ir.ResolvedTimeSpec{
							ResolvedStart: period.Start, ResolvedEndExclusive: period.EndExclusive,
							PolicyApplied: string(registry.IncompletePeriodFull), Comparison: &comparison,
						},
					)
				}
			}
		}
	}
}

// dataAvailability walks the watermark across each period. The boundary case is
// the watermark landing exactly on the last day: the period is then complete
// and must NOT be reported as truncated.
func (builder *timeScenarioBuilder) dataAvailability(fixture calendar.Fixture) {
	location, err := fixture.Location()
	if err != nil {
		builder.fail(err)
		return
	}
	anchors := []time.Time{
		time.Date(2026, 8, 13, 0, 0, 0, 0, location),
		time.Date(2026, 2, 27, 0, 0, 0, 0, location),
	}
	index := 0
	for _, anchor := range anchors {
		for _, grain := range naturalGrains {
			start, end, periodErr := naturalPeriod(grain, anchor, fixture.WeekStart, location)
			if periodErr != nil {
				builder.fail(periodErr)
				return
			}
			length := dayCount(start, end)
			seen := map[int]struct{}{}
			for _, offset := range []int{0, length / 2, length - 1, length + 5} {
				if offset < 0 {
					continue
				}
				if _, duplicate := seen[offset]; duplicate {
					continue
				}
				seen[offset] = struct{}{}
				index++
				contract := syntheticTimeContract(
					fixture, registry.IncompletePeriodMTD, registry.ComparisonSameDayCount,
					registry.MonthEndClampToLastDay, goldenCalendarVersionID,
				)
				watermark := start.AddDate(0, 0, offset)
				resolvedEnd := end
				if available := watermark.AddDate(0, 0, 1); available.Before(end) {
					resolvedEnd = available
				}
				builder.add(
					fmt.Sprintf("time-golden-availability-%03d", index),
					suites.TimeDataAvailability, fixture, contract,
					timeQuery(fixture, currentPeriodCode(grain), grain, start, end, nil),
					compiler.MaterializationMeta{DataAvailableThrough: watermark}, nil,
					ir.ResolvedTimeSpec{
						ResolvedStart: start, ResolvedEndExclusive: resolvedEnd,
						PolicyApplied: string(registry.IncompletePeriodMTD),
					},
				)
			}
		}
	}
}

// crossTimezoneYear proves the year boundary follows the business timezone.
// The same instant belongs to different years in Shanghai and New York, so a
// resolver that silently normalizes to UTC produces the wrong year here.
func (builder *timeScenarioBuilder) crossTimezoneYear() {
	fixtures := []calendar.Fixture{
		calendar.Synthetic("synthetic-calendar-shanghai", "Asia/Shanghai", time.April, time.Monday),
		calendar.Synthetic("synthetic-calendar-utc", "UTC", time.January, time.Monday),
		calendar.Synthetic("synthetic-calendar-new-york", "America/New_York", time.October, time.Sunday),
		calendar.Synthetic("synthetic-calendar-london", "Europe/London", time.April, time.Monday),
		calendar.Synthetic("synthetic-calendar-kolkata", "Asia/Kolkata", time.April, time.Monday),
	}
	index := 0
	for _, fixture := range fixtures {
		location, err := fixture.Location()
		if err != nil {
			builder.fail(err)
			return
		}
		for _, anchor := range []time.Time{
			time.Date(2026, 1, 1, 0, 0, 0, 0, location),
			time.Date(2026, 12, 31, 0, 0, 0, 0, location),
		} {
			for _, policy := range []registry.IncompletePeriodPolicy{
				registry.IncompletePeriodMTD, registry.IncompletePeriodFull,
			} {
				index++
				contract := syntheticTimeContract(
					fixture, policy, registry.ComparisonSameCalendarRange,
					registry.MonthEndClampToLastDay, goldenCalendarVersionID,
				)
				start, end, periodErr := naturalPeriod(ir.TimeGrainYear, anchor, fixture.WeekStart, location)
				if periodErr != nil {
					builder.fail(periodErr)
					return
				}
				resolvedStart, resolvedEnd, expectErr := expectedNaturalResolution(
					policy, ir.TimeGrainYear, start, end, anchor, fixture.WeekStart, location,
				)
				if expectErr != nil {
					builder.fail(expectErr)
					return
				}
				comparison, comparisonErr := expectedNaturalComparison(
					ir.ComparisonYearOverYear, 1, ir.TimeGrainYear, resolvedStart, resolvedEnd,
					registry.ComparisonSameCalendarRange, registry.MonthEndClampToLastDay, location,
				)
				if comparisonErr != nil {
					builder.fail(comparisonErr)
					return
				}
				builder.add(
					fmt.Sprintf("time-golden-timezone-%03d", index),
					suites.TimeCrossTimezoneYear, fixture, contract,
					timeQuery(fixture, "CURRENT_YEAR", ir.TimeGrainYear, start, end,
						&ir.Comparison{Type: ir.ComparisonYearOverYear, Periods: 1}),
					compiler.MaterializationMeta{DataAvailableThrough: anchor}, nil,
					ir.ResolvedTimeSpec{
						ResolvedStart: resolvedStart, ResolvedEndExclusive: resolvedEnd,
						PolicyApplied: string(policy), Comparison: &comparison,
					},
				)
			}
		}
	}
}
