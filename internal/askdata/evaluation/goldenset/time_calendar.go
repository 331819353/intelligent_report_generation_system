package goldenset

import (
	"fmt"
	"time"

	"intelligent-report-generation-system/internal/askdata/compiler"
	"intelligent-report-generation-system/internal/askdata/ir"
	"intelligent-report-generation-system/internal/askdata/registry"
)

// The helpers below derive expectations from the scenario itself. They are
// deliberately written against the calendar definition ("the period containing
// the day before this one starts") rather than against the compiler's
// transformations, so a shared mistake cannot cancel out on both sides.

func midnight(value time.Time, location *time.Location) time.Time {
	local := value.In(location)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location)
}

func dayCount(start, end time.Time) int {
	startUTC := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
	endUTC := time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, time.UTC)
	return int(endUTC.Sub(startUTC) / (24 * time.Hour))
}

func naturalPeriod(
	grain ir.TimeGrain,
	anchor time.Time,
	weekStart time.Weekday,
	location *time.Location,
) (time.Time, time.Time, error) {
	day := midnight(anchor, location)
	switch grain {
	case ir.TimeGrainDay:
		return day, day.AddDate(0, 0, 1), nil
	case ir.TimeGrainWeek:
		back := (int(day.Weekday()) - int(weekStart) + 7) % 7
		start := day.AddDate(0, 0, -back)
		return start, start.AddDate(0, 0, 7), nil
	case ir.TimeGrainMonth:
		start := time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, location)
		return start, start.AddDate(0, 1, 0), nil
	case ir.TimeGrainQuarter:
		month := time.Month((int(day.Month())-1)/3*3 + 1)
		start := time.Date(day.Year(), month, 1, 0, 0, 0, 0, location)
		return start, start.AddDate(0, 3, 0), nil
	case ir.TimeGrainYear:
		start := time.Date(day.Year(), time.January, 1, 0, 0, 0, 0, location)
		return start, start.AddDate(1, 0, 0), nil
	default:
		return time.Time{}, time.Time{}, fmt.Errorf("%w: grain %s has no natural period", ErrTimeGoldenSet, grain)
	}
}

func currentPeriodCode(grain ir.TimeGrain) string { return "CURRENT_" + string(grain) }

func closedPeriodCode(grain ir.TimeGrain) string { return "LAST_" + string(grain) }

// expectedNaturalResolution states what the incomplete-period policy must do to
// the requested period. LAST_COMPLETE is expressed as "the period containing
// the day before this one begins", which is the definition rather than a shift.
func expectedNaturalResolution(
	policy registry.IncompletePeriodPolicy,
	grain ir.TimeGrain,
	start, endExclusive, dataAvailableThrough time.Time,
	weekStart time.Weekday,
	location *time.Location,
) (time.Time, time.Time, error) {
	switch policy {
	case registry.IncompletePeriodFull:
		return start, endExclusive, nil
	case registry.IncompletePeriodMTD:
		available := midnight(dataAvailableThrough, location).AddDate(0, 0, 1)
		if available.Before(endExclusive) {
			return start, available, nil
		}
		return start, endExclusive, nil
	case registry.IncompletePeriodLastComplete:
		return naturalPeriod(grain, start.AddDate(0, 0, -1), weekStart, location)
	default:
		return time.Time{}, time.Time{}, fmt.Errorf("%w: policy %s", ErrTimeGoldenSet, policy)
	}
}

func comparisonLabel(comparisonType ir.ComparisonType, grain registry.TimeGrain) (string, error) {
	switch comparisonType {
	case ir.ComparisonYearOverYear:
		return "YEAR_OVER_YEAR", nil
	case ir.ComparisonMonthOverMonth:
		return "MONTH_OVER_MONTH", nil
	case ir.ComparisonPeriodOverPeriod:
		switch grain {
		case registry.TimeGrainWeek:
			return "WEEK_OVER_WEEK", nil
		case registry.TimeGrainMonth, registry.TimeGrainFiscalMonth:
			return "MONTH_OVER_MONTH", nil
		case registry.TimeGrainQuarter, registry.TimeGrainFiscalQuarter:
			return "QUARTER_OVER_QUARTER", nil
		case registry.TimeGrainYear, registry.TimeGrainFiscalYear:
			return "YEAR_OVER_YEAR", nil
		case registry.TimeGrainDay:
			return "PERIOD_OVER_PERIOD", nil
		}
	}
	return "", fmt.Errorf("%w: comparison %s is undefined at grain %s", ErrTimeGoldenSet, comparisonType, grain)
}

// shiftBoundary moves one boundary by whole months or whole days. A day that
// does not exist in the target month is clamped, and the clamp is reported so
// the answer can say the baseline was adjusted.
func shiftBoundary(
	value time.Time,
	months, days int,
	rule registry.MonthEndOverflowRule,
	location *time.Location,
) (time.Time, bool, error) {
	if months == 0 {
		return value.AddDate(0, 0, days), false, nil
	}
	index := value.Year()*12 + int(value.Month()) - 1 + months
	targetYear, monthIndex := index/12, index%12
	if monthIndex < 0 {
		targetYear--
		monthIndex += 12
	}
	targetMonth := time.Month(monthIndex + 1)
	lastDay := time.Date(targetYear, targetMonth+1, 0, 0, 0, 0, 0, location).Day()
	day, overflow := value.Day(), value.Day() > lastDay
	if overflow {
		if rule != registry.MonthEndClampToLastDay {
			return time.Time{}, false, fmt.Errorf("%w: overflow rule %s cannot shift %s", ErrTimeGoldenSet, rule, value)
		}
		day = lastDay
	}
	return time.Date(targetYear, targetMonth, day, 0, 0, 0, 0, location), overflow, nil
}

func expectedNaturalComparison(
	comparisonType ir.ComparisonType,
	periods int,
	grain ir.TimeGrain,
	resolvedStart, resolvedEndExclusive time.Time,
	alignment registry.ComparisonAlignment,
	rule registry.MonthEndOverflowRule,
	location *time.Location,
) (ir.ResolvedComparison, error) {
	registryGrain := registry.TimeGrain(grain)
	label, err := comparisonLabel(comparisonType, registryGrain)
	if err != nil {
		return ir.ResolvedComparison{}, err
	}
	months, days := 0, 0
	switch comparisonType {
	case ir.ComparisonYearOverYear:
		months = -12 * periods
	case ir.ComparisonMonthOverMonth:
		months = -periods
	case ir.ComparisonPeriodOverPeriod:
		switch registryGrain {
		case registry.TimeGrainWeek:
			days = -7 * periods
		case registry.TimeGrainMonth:
			months = -periods
		case registry.TimeGrainQuarter:
			months = -3 * periods
		case registry.TimeGrainYear:
			months = -12 * periods
		case registry.TimeGrainDay:
			days = -dayCount(resolvedStart, resolvedEndExclusive) * periods
		default:
			return ir.ResolvedComparison{}, fmt.Errorf("%w: grain %s", ErrTimeGoldenSet, grain)
		}
	}
	previousStart, overflow, err := shiftBoundary(resolvedStart, months, days, rule, location)
	if err != nil {
		return ir.ResolvedComparison{}, err
	}
	var previousEnd time.Time
	switch alignment {
	case registry.ComparisonSameDayCount:
		previousEnd = previousStart.AddDate(0, 0, dayCount(resolvedStart, resolvedEndExclusive))
	case registry.ComparisonSameCalendarRange:
		shifted, endOverflow, shiftErr := shiftBoundary(resolvedEndExclusive, months, days, rule, location)
		if shiftErr != nil {
			return ir.ResolvedComparison{}, shiftErr
		}
		previousEnd, overflow = shifted, overflow || endOverflow
	default:
		return ir.ResolvedComparison{}, fmt.Errorf("%w: alignment %s", ErrTimeGoldenSet, alignment)
	}
	if !previousEnd.After(previousStart) {
		return ir.ResolvedComparison{}, fmt.Errorf("%w: empty baseline interval", ErrTimeGoldenSet)
	}
	return ir.ResolvedComparison{
		Type: label, Periods: periods, Alignment: string(alignment),
		ResolvedStart: previousStart, ResolvedEndExclusive: previousEnd, OverflowApplied: overflow,
	}, nil
}

// expectedFiscalComparison declares which fiscal period the baseline is. The
// offset (a fiscal year is four quarters or twelve months back) is what the
// compiler has to work out; the fixture only looks the result up.
func expectedFiscalComparison(
	table *FiscalCalendar,
	comparisonType ir.ComparisonType,
	periods int,
	grain registry.TimeGrain,
	currentPeriod compiler.FiscalCalendarPeriod,
	resolvedStart, resolvedEndExclusive time.Time,
	alignment registry.ComparisonAlignment,
) (ir.ResolvedComparison, error) {
	label, err := comparisonLabel(comparisonType, grain)
	if err != nil {
		return ir.ResolvedComparison{}, err
	}
	offset := 0
	switch comparisonType {
	case ir.ComparisonPeriodOverPeriod:
		offset = -periods
	case ir.ComparisonMonthOverMonth:
		if grain != registry.TimeGrainFiscalMonth {
			return ir.ResolvedComparison{}, fmt.Errorf("%w: month comparison at grain %s", ErrTimeGoldenSet, grain)
		}
		offset = -periods
	case ir.ComparisonYearOverYear:
		switch grain {
		case registry.TimeGrainFiscalMonth:
			offset = -12 * periods
		case registry.TimeGrainFiscalQuarter:
			offset = -4 * periods
		case registry.TimeGrainFiscalYear:
			offset = -periods
		default:
			return ir.ResolvedComparison{}, fmt.Errorf("%w: fiscal grain %s", ErrTimeGoldenSet, grain)
		}
	}
	previous, err := table.Period(grain, currentPeriod.Start, offset)
	if err != nil {
		return ir.ResolvedComparison{}, err
	}
	previousStart, previousEnd := previous.Start, time.Time{}
	switch alignment {
	case registry.ComparisonSameDayCount:
		previousEnd = previousStart.AddDate(0, 0, dayCount(resolvedStart, resolvedEndExclusive))
	case registry.ComparisonSameCalendarRange:
		previousStart = previous.Start.AddDate(0, 0, dayCount(currentPeriod.Start, resolvedStart))
		previousEnd = previous.Start.AddDate(0, 0, dayCount(currentPeriod.Start, resolvedEndExclusive))
		if previousEnd.After(previous.EndExclusive) {
			previousEnd = previous.EndExclusive
		}
	default:
		return ir.ResolvedComparison{}, fmt.Errorf("%w: alignment %s", ErrTimeGoldenSet, alignment)
	}
	if !previousEnd.After(previousStart) {
		return ir.ResolvedComparison{}, fmt.Errorf("%w: empty fiscal baseline", ErrTimeGoldenSet)
	}
	return ir.ResolvedComparison{
		Type: label, Periods: periods, Alignment: string(alignment),
		ResolvedStart: previousStart, ResolvedEndExclusive: previousEnd,
	}, nil
}
