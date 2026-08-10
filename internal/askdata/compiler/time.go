package compiler

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"intelligent-report-generation-system/internal/askdata/ir"
	"intelligent-report-generation-system/internal/askdata/registry"
)

var (
	ErrTimeUnsupportedGrain     = errors.New(registry.TimeUnsupportedGrain)
	ErrTimeComparisonUndefined  = errors.New("TIME_COMPARISON_UNDEFINED")
	ErrTimeRangeEmpty           = errors.New("TIME_RANGE_EMPTY")
	ErrTimeCalendarLookupFailed = errors.New("TIME_CALENDAR_LOOKUP_FAILED")
)

// MaterializationMeta is the bounded runtime input needed by the time
// compiler. PolicyApplied/PolicySource carry the already-resolved TIME-001
// precedence (metric > time contract > domain > platform default). When they
// are empty, Resolve falls back only to the contract and platform default.
type MaterializationMeta struct {
	DataAvailableThrough time.Time
	PolicyApplied        registry.IncompletePeriodPolicy
	PolicySource         registry.PolicySource
	Calendar             FiscalCalendarResolver
}

// Resolve compiles the relative time request retained in Semantic IR into one
// deterministic half-open interval and, when requested, its comparison range.
func Resolve(
	ctx context.Context,
	semanticIR ir.SemanticIR,
	contract registry.TimeContractVersion,
	meta MaterializationMeta,
) (ir.ResolvedTimeSpec, error) {
	if err := ctx.Err(); err != nil {
		return ir.ResolvedTimeSpec{}, err
	}
	if semanticIR.TimeRange == nil || meta.DataAvailableThrough.IsZero() {
		return ir.ResolvedTimeSpec{}, ErrTimeRangeEmpty
	}
	if semanticIR.Comparison != nil &&
		contract.ComparisonAlignment != registry.ComparisonSameDayCount &&
		contract.ComparisonAlignment != registry.ComparisonSameCalendarRange {
		return ir.ResolvedTimeSpec{}, ErrTimeComparisonUndefined
	}
	loc, err := time.LoadLocation(contract.Timezone)
	if err != nil {
		return ir.ResolvedTimeSpec{}, fmt.Errorf("%w: business timezone", ErrTimeRangeEmpty)
	}
	rangeValue := *semanticIR.TimeRange
	start, err := parseBusinessDate(rangeValue.Start, loc)
	if err != nil {
		return ir.ResolvedTimeSpec{}, ErrTimeRangeEmpty
	}
	end, err := parseBusinessDate(rangeValue.EndExclusive, loc)
	if err != nil || !end.After(start) {
		return ir.ResolvedTimeSpec{}, ErrTimeRangeEmpty
	}
	requested := rangeValue.RequestedPeriod
	if requested == "" {
		requested = "ABSOLUTE"
	}
	grain, err := requestedGrain(requested, rangeValue.Grain)
	if err != nil || !supportsGrain(contract.SupportedGrains, grain) {
		return ir.ResolvedTimeSpec{}, ErrTimeUnsupportedGrain
	}

	period := FiscalCalendarPeriod{Start: start, EndExclusive: end}
	if isFiscalGrain(grain) {
		period, err = resolveFiscalPeriod(ctx, meta.Calendar, contract, requested, grain, start, end, 0, loc)
		if err != nil {
			return ir.ResolvedTimeSpec{}, err
		}
	}

	policy, source, err := effectiveTimePolicy(contract, meta)
	if err != nil {
		return ir.ResolvedTimeSpec{}, err
	}
	dataAvailableThrough := meta.DataAvailableThrough.In(loc)
	resolvedStart, resolvedEnd := period.Start, period.EndExclusive
	fallback, truncated := false, false
	if isCurrentPeriod(requested) {
		switch policy {
		case registry.IncompletePeriodMTD:
			availableEnd := nextBusinessDay(dataAvailableThrough, loc)
			if availableEnd.Before(resolvedEnd) {
				resolvedEnd, truncated = availableEnd, true
			}
		case registry.IncompletePeriodFull:
		case registry.IncompletePeriodLastComplete:
			if isFiscalGrain(grain) {
				period, err = resolveFiscalPeriod(ctx, meta.Calendar, contract, requested, grain, start, end, -1, loc)
			} else {
				period, err = previousNaturalPeriod(period, grain)
			}
			if err != nil {
				return ir.ResolvedTimeSpec{}, err
			}
			resolvedStart, resolvedEnd, fallback = period.Start, period.EndExclusive, true
		default:
			return ir.ResolvedTimeSpec{}, ErrTimeRangeEmpty
		}
	}
	if !resolvedEnd.After(resolvedStart) {
		return ir.ResolvedTimeSpec{}, ErrTimeRangeEmpty
	}

	result := ir.ResolvedTimeSpec{
		RequestedPeriod: requested, Grain: string(grain),
		PolicyApplied: string(policy), PolicySource: string(source),
		ResolvedStart: resolvedStart, ResolvedEndExclusive: resolvedEnd,
		DataAvailableThrough:        dataAvailableThrough,
		TruncatedByDataAvailability: truncated, PeriodFallbackApplied: fallback,
		Timezone: contract.Timezone,
	}
	if isFiscalGrain(grain) {
		result.CalendarVersionID = contract.CalendarDatasetVersionID
	}
	if semanticIR.Comparison != nil {
		comparison, err := resolveComparison(ctx, *semanticIR.Comparison, result, period, contract, meta, grain, loc)
		if err != nil {
			return ir.ResolvedTimeSpec{}, err
		}
		result.Comparison = &comparison
	}
	if err := validateResolvedTimeSpec(result); err != nil {
		return ir.ResolvedTimeSpec{}, err
	}
	return result, nil
}

// ApplyDataAvailability clips an already-resolved current interval to the
// control-plane watermark. Callers must classify a fully out-of-range request
// before invoking this helper; producing an empty interval is never valid.
// Comparison ranges are shortened with the same alignment contract so the
// compiled CURRENT and BASELINE plans continue to compare like-for-like.
func ApplyDataAvailability(
	spec ir.ResolvedTimeSpec,
	dataAvailableThrough time.Time,
) (ir.ResolvedTimeSpec, error) {
	if validateResolvedTimeSpec(spec) != nil || dataAvailableThrough.IsZero() {
		return ir.ResolvedTimeSpec{}, ErrTimeRangeEmpty
	}
	loc, err := time.LoadLocation(spec.Timezone)
	if err != nil {
		return ir.ResolvedTimeSpec{}, ErrTimeRangeEmpty
	}
	availableEnd := nextBusinessDay(dataAvailableThrough, loc)
	if !availableEnd.After(spec.ResolvedStart) {
		return ir.ResolvedTimeSpec{}, ErrTimeRangeEmpty
	}

	result := spec
	result.DataAvailableThrough = dataAvailableThrough.In(loc)
	if !result.ResolvedEndExclusive.After(availableEnd) {
		if err := validateResolvedTimeSpec(result); err != nil {
			return ir.ResolvedTimeSpec{}, err
		}
		return result, nil
	}

	previousEnd := result.ResolvedEndExclusive
	result.ResolvedEndExclusive = availableEnd
	result.TruncatedByDataAvailability = true
	if result.Comparison != nil {
		comparison := *result.Comparison
		switch comparison.Alignment {
		case string(registry.ComparisonSameDayCount):
			dayCount := calendarDayCount(result.ResolvedStart, result.ResolvedEndExclusive)
			comparison.ResolvedEndExclusive = addBusinessDays(comparison.ResolvedStart, dayCount, loc)
		case string(registry.ComparisonSameCalendarRange):
			clippedDays := calendarDayCount(result.ResolvedEndExclusive, previousEnd)
			comparison.ResolvedEndExclusive = addBusinessDays(comparison.ResolvedEndExclusive, -clippedDays, loc)
		default:
			return ir.ResolvedTimeSpec{}, ErrTimeComparisonUndefined
		}
		if !comparison.ResolvedEndExclusive.After(comparison.ResolvedStart) {
			return ir.ResolvedTimeSpec{}, ErrTimeComparisonUndefined
		}
		result.Comparison = &comparison
	}
	if err := validateResolvedTimeSpec(result); err != nil {
		return ir.ResolvedTimeSpec{}, err
	}
	return result, nil
}

func resolveComparison(
	ctx context.Context,
	comparison ir.Comparison,
	current ir.ResolvedTimeSpec,
	currentPeriod FiscalCalendarPeriod,
	contract registry.TimeContractVersion,
	meta MaterializationMeta,
	grain registry.TimeGrain,
	loc *time.Location,
) (ir.ResolvedComparison, error) {
	if comparison.Periods < 1 || comparison.Periods > 120 {
		return ir.ResolvedComparison{}, ErrTimeComparisonUndefined
	}
	typeName, shift, err := comparisonShift(comparison.Type, comparison.Periods, grain, current)
	if err != nil {
		return ir.ResolvedComparison{}, err
	}
	var previousStart, previousEnd time.Time
	overflow := false
	if isFiscalGrain(grain) {
		offset, offsetErr := fiscalComparisonOffset(comparison.Type, comparison.Periods, grain)
		if offsetErr != nil {
			return ir.ResolvedComparison{}, offsetErr
		}
		previous, lookupErr := resolveFiscalPeriod(
			ctx, meta.Calendar, contract, current.RequestedPeriod, grain,
			currentPeriod.Start, currentPeriod.EndExclusive, offset, loc,
		)
		if lookupErr != nil {
			return ir.ResolvedComparison{}, lookupErr
		}
		previousStart = previous.Start
		if contract.ComparisonAlignment == registry.ComparisonSameDayCount {
			previousEnd = addBusinessDays(previousStart, calendarDayCount(current.ResolvedStart, current.ResolvedEndExclusive), loc)
		} else {
			startOffset := calendarDayCount(currentPeriod.Start, current.ResolvedStart)
			endOffset := calendarDayCount(currentPeriod.Start, current.ResolvedEndExclusive)
			previousStart = addBusinessDays(previous.Start, startOffset, loc)
			previousEnd = addBusinessDays(previous.Start, endOffset, loc)
			if previousEnd.After(previous.EndExclusive) {
				previousEnd = previous.EndExclusive
			}
		}
	} else {
		previousStart, overflow, err = shiftNaturalBoundary(current.ResolvedStart, shift, contract.MonthEndOverflowRule, loc)
		if err != nil {
			return ir.ResolvedComparison{}, err
		}
		if contract.ComparisonAlignment == registry.ComparisonSameDayCount {
			previousEnd = addBusinessDays(previousStart, calendarDayCount(current.ResolvedStart, current.ResolvedEndExclusive), loc)
		} else {
			var endOverflow bool
			previousEnd, endOverflow, err = shiftNaturalBoundary(current.ResolvedEndExclusive, shift, contract.MonthEndOverflowRule, loc)
			overflow = overflow || endOverflow
			if err != nil {
				return ir.ResolvedComparison{}, err
			}
		}
	}
	if !previousEnd.After(previousStart) {
		return ir.ResolvedComparison{}, ErrTimeComparisonUndefined
	}
	return ir.ResolvedComparison{
		Type: typeName, Periods: comparison.Periods,
		Alignment:     string(contract.ComparisonAlignment),
		ResolvedStart: previousStart, ResolvedEndExclusive: previousEnd,
		OverflowApplied: overflow,
	}, nil
}

func fiscalComparisonOffset(comparison ir.ComparisonType, periods int, grain registry.TimeGrain) (int, error) {
	switch comparison {
	case ir.ComparisonPeriodOverPeriod:
		return -periods, nil
	case ir.ComparisonMonthOverMonth:
		if grain != registry.TimeGrainFiscalMonth {
			return 0, ErrTimeComparisonUndefined
		}
		return -periods, nil
	case ir.ComparisonYearOverYear:
		switch grain {
		case registry.TimeGrainFiscalMonth:
			return -12 * periods, nil
		case registry.TimeGrainFiscalQuarter:
			return -4 * periods, nil
		case registry.TimeGrainFiscalYear:
			return -periods, nil
		default:
			return 0, ErrTimeComparisonUndefined
		}
	default:
		return 0, ErrTimeComparisonUndefined
	}
}

type naturalShift struct{ months, days int }

func comparisonShift(comparison ir.ComparisonType, periods int, grain registry.TimeGrain, current ir.ResolvedTimeSpec) (string, naturalShift, error) {
	switch comparison {
	case ir.ComparisonYearOverYear:
		return "YEAR_OVER_YEAR", naturalShift{months: -12 * periods}, nil
	case ir.ComparisonMonthOverMonth:
		return "MONTH_OVER_MONTH", naturalShift{months: -periods}, nil
	case ir.ComparisonPeriodOverPeriod:
		switch grain {
		case registry.TimeGrainWeek:
			return "WEEK_OVER_WEEK", naturalShift{days: -7 * periods}, nil
		case registry.TimeGrainMonth, registry.TimeGrainFiscalMonth:
			return "MONTH_OVER_MONTH", naturalShift{months: -periods}, nil
		case registry.TimeGrainQuarter, registry.TimeGrainFiscalQuarter:
			return "QUARTER_OVER_QUARTER", naturalShift{months: -3 * periods}, nil
		case registry.TimeGrainYear, registry.TimeGrainFiscalYear:
			return "YEAR_OVER_YEAR", naturalShift{months: -12 * periods}, nil
		case registry.TimeGrainDay:
			days := calendarDayCount(current.ResolvedStart, current.ResolvedEndExclusive)
			return "PERIOD_OVER_PERIOD", naturalShift{days: -days * periods}, nil
		default:
			return "", naturalShift{}, ErrTimeUnsupportedGrain
		}
	default:
		return "", naturalShift{}, ErrTimeComparisonUndefined
	}
}

func effectiveTimePolicy(contract registry.TimeContractVersion, meta MaterializationMeta) (registry.IncompletePeriodPolicy, registry.PolicySource, error) {
	policy, source := meta.PolicyApplied, meta.PolicySource
	if policy == "" {
		if contract.IncompletePeriodPolicy != "" {
			policy, source = contract.IncompletePeriodPolicy, registry.PolicySourceTimeContract
		} else {
			policy, source = registry.PlatformDefaultIncompletePeriodPolicy, registry.PolicySourcePlatformDefault
		}
	}
	if source != registry.PolicySourceMetric && source != registry.PolicySourceTimeContract &&
		source != registry.PolicySourceDomain && source != registry.PolicySourcePlatformDefault {
		return "", "", ErrTimeRangeEmpty
	}
	if policy != registry.IncompletePeriodMTD && policy != registry.IncompletePeriodFull && policy != registry.IncompletePeriodLastComplete {
		return "", "", ErrTimeRangeEmpty
	}
	return policy, source, nil
}

func requestedGrain(requested string, grain ir.TimeGrain) (registry.TimeGrain, error) {
	switch {
	case strings.Contains(requested, "FISCAL_MONTH"):
		return registry.TimeGrainFiscalMonth, nil
	case strings.Contains(requested, "FISCAL_QUARTER"):
		return registry.TimeGrainFiscalQuarter, nil
	case strings.Contains(requested, "FISCAL_YEAR"):
		return registry.TimeGrainFiscalYear, nil
	case grain != "":
		return registry.TimeGrain(grain), nil
	default:
		return "", ErrTimeUnsupportedGrain
	}
}

func resolveFiscalPeriod(
	ctx context.Context,
	resolver FiscalCalendarResolver,
	contract registry.TimeContractVersion,
	requested string,
	grain registry.TimeGrain,
	startHint, endHint time.Time,
	offset int,
	loc *time.Location,
) (FiscalCalendarPeriod, error) {
	if resolver == nil || contract.CalendarDatasetVersionID == "" {
		return FiscalCalendarPeriod{}, ErrTimeCalendarLookupFailed
	}
	period, err := resolver.ResolveFiscalPeriod(ctx, FiscalCalendarRequest{
		CalendarVersionID: contract.CalendarDatasetVersionID, RequestedPeriod: requested,
		Grain: grain, StartHint: startHint, EndExclusiveHint: endHint, Offset: offset,
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return FiscalCalendarPeriod{}, err
		}
		return FiscalCalendarPeriod{}, ErrTimeCalendarLookupFailed
	}
	period.Start, period.EndExclusive = period.Start.In(loc), period.EndExclusive.In(loc)
	if period.PeriodKey == "" || !period.EndExclusive.After(period.Start) {
		return FiscalCalendarPeriod{}, ErrTimeCalendarLookupFailed
	}
	return period, nil
}

func previousNaturalPeriod(period FiscalCalendarPeriod, grain registry.TimeGrain) (FiscalCalendarPeriod, error) {
	var start, end time.Time
	switch grain {
	case registry.TimeGrainDay:
		start, end = period.Start.AddDate(0, 0, -1), period.EndExclusive.AddDate(0, 0, -1)
	case registry.TimeGrainWeek:
		start, end = period.Start.AddDate(0, 0, -7), period.EndExclusive.AddDate(0, 0, -7)
	case registry.TimeGrainMonth:
		start, end = period.Start.AddDate(0, -1, 0), period.EndExclusive.AddDate(0, -1, 0)
	case registry.TimeGrainQuarter:
		start, end = period.Start.AddDate(0, -3, 0), period.EndExclusive.AddDate(0, -3, 0)
	case registry.TimeGrainYear:
		start, end = period.Start.AddDate(-1, 0, 0), period.EndExclusive.AddDate(-1, 0, 0)
	default:
		return FiscalCalendarPeriod{}, ErrTimeUnsupportedGrain
	}
	if !end.After(start) {
		return FiscalCalendarPeriod{}, ErrTimeRangeEmpty
	}
	return FiscalCalendarPeriod{Start: start, EndExclusive: end}, nil
}

func shiftNaturalBoundary(value time.Time, shift naturalShift, rule registry.MonthEndOverflowRule, loc *time.Location) (time.Time, bool, error) {
	if shift.months == 0 {
		return value.AddDate(0, 0, shift.days), false, nil
	}
	targetYear, targetMonth := shiftedYearMonth(value.Year(), value.Month(), shift.months)
	lastDay := time.Date(targetYear, targetMonth+1, 0, 0, 0, 0, 0, loc).Day()
	day, overflow := value.Day(), value.Day() > lastDay
	if overflow {
		if rule == registry.MonthEndSkip {
			return time.Time{}, false, ErrTimeComparisonUndefined
		}
		if rule != registry.MonthEndClampToLastDay {
			return time.Time{}, false, ErrTimeComparisonUndefined
		}
		day = lastDay
	}
	return time.Date(targetYear, targetMonth, day, value.Hour(), value.Minute(), value.Second(), value.Nanosecond(), loc), overflow, nil
}

func shiftedYearMonth(year int, month time.Month, delta int) (int, time.Month) {
	index := year*12 + int(month) - 1 + delta
	targetYear, targetMonth := index/12, index%12
	if targetMonth < 0 {
		targetYear--
		targetMonth += 12
	}
	return targetYear, time.Month(targetMonth + 1)
}

func parseBusinessDate(value string, loc *time.Location) (time.Time, error) {
	parsed, err := time.ParseInLocation("2006-01-02", value, loc)
	if err != nil {
		return time.Time{}, err
	}
	return parsed, nil
}

func nextBusinessDay(value time.Time, loc *time.Location) time.Time {
	local := value.In(loc)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, 1)
}

func addBusinessDays(value time.Time, days int, loc *time.Location) time.Time {
	local := value.In(loc)
	return time.Date(local.Year(), local.Month(), local.Day(), local.Hour(), local.Minute(), local.Second(), local.Nanosecond(), loc).AddDate(0, 0, days)
}

func calendarDayCount(start, end time.Time) int {
	startUTC := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
	endUTC := time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, time.UTC)
	return int(endUTC.Sub(startUTC) / (24 * time.Hour))
}

func isCurrentPeriod(requested string) bool {
	return strings.HasPrefix(requested, "CURRENT_") || requested == "TODAY"
}

func isFiscalGrain(grain registry.TimeGrain) bool {
	return grain == registry.TimeGrainFiscalMonth || grain == registry.TimeGrainFiscalQuarter || grain == registry.TimeGrainFiscalYear
}

func supportsGrain(values []registry.TimeGrain, target registry.TimeGrain) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func validateResolvedTimeSpec(value ir.ResolvedTimeSpec) error {
	if value.RequestedPeriod == "" || value.Grain == "" || value.PolicyApplied == "" || value.PolicySource == "" ||
		value.Timezone == "" || value.ResolvedStart.IsZero() || value.ResolvedEndExclusive.IsZero() ||
		value.DataAvailableThrough.IsZero() || !value.ResolvedEndExclusive.After(value.ResolvedStart) {
		return ErrTimeRangeEmpty
	}
	if _, err := time.LoadLocation(value.Timezone); err != nil {
		return ErrTimeRangeEmpty
	}
	if value.PolicyApplied != string(registry.IncompletePeriodMTD) && value.PolicyApplied != string(registry.IncompletePeriodFull) &&
		value.PolicyApplied != string(registry.IncompletePeriodLastComplete) {
		return ErrTimeRangeEmpty
	}
	if value.PolicySource != string(registry.PolicySourceMetric) && value.PolicySource != string(registry.PolicySourceTimeContract) &&
		value.PolicySource != string(registry.PolicySourceDomain) && value.PolicySource != string(registry.PolicySourcePlatformDefault) {
		return ErrTimeRangeEmpty
	}
	grain := registry.TimeGrain(value.Grain)
	if !validResolvedGrain(grain) || isFiscalGrain(grain) != (value.CalendarVersionID != "") ||
		(value.TruncatedByDataAvailability && value.PeriodFallbackApplied) {
		return ErrTimeRangeEmpty
	}
	if value.Comparison != nil {
		comparison := value.Comparison
		if !validResolvedComparisonType(comparison.Type) || comparison.Periods < 1 ||
			(comparison.Alignment != string(registry.ComparisonSameDayCount) && comparison.Alignment != string(registry.ComparisonSameCalendarRange)) ||
			comparison.ResolvedStart.IsZero() || !comparison.ResolvedEndExclusive.After(comparison.ResolvedStart) {
			return ErrTimeComparisonUndefined
		}
	}
	return nil
}

func validResolvedGrain(value registry.TimeGrain) bool {
	switch value {
	case registry.TimeGrainDay, registry.TimeGrainWeek, registry.TimeGrainMonth, registry.TimeGrainQuarter,
		registry.TimeGrainYear, registry.TimeGrainFiscalMonth, registry.TimeGrainFiscalQuarter, registry.TimeGrainFiscalYear:
		return true
	default:
		return false
	}
}

func validResolvedComparisonType(value string) bool {
	switch value {
	case "YEAR_OVER_YEAR", "MONTH_OVER_MONTH", "QUARTER_OVER_QUARTER", "WEEK_OVER_WEEK", "PERIOD_OVER_PERIOD":
		return true
	default:
		return false
	}
}
