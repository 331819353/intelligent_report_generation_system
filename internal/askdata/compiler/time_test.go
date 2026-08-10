package compiler

import (
	"context"
	"errors"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/askdata/ir"
	"intelligent-report-generation-system/internal/askdata/registry"
)

func TestResolveNaturalPeriodsPoliciesAndBusinessTimezone(t *testing.T) {
	shanghai := mustLocation(t, "Asia/Shanghai")
	tests := []struct {
		name      string
		requested string
		grain     ir.TimeGrain
		start     string
		end       string
		policy    registry.IncompletePeriodPolicy
		available time.Time
		wantStart string
		wantEnd   string
		truncated bool
		fallback  bool
	}{
		{
			name: "month MTD T plus one", requested: "CURRENT_MONTH", grain: ir.TimeGrainMonth,
			start: "2026-08-01", end: "2026-09-01", policy: registry.IncompletePeriodMTD,
			available: localTime(2026, time.August, 6, 23, shanghai), wantStart: "2026-08-01", wantEnd: "2026-08-07", truncated: true,
		},
		{
			name: "quarter full period", requested: "CURRENT_QUARTER", grain: ir.TimeGrainQuarter,
			start: "2026-07-01", end: "2026-10-01", policy: registry.IncompletePeriodFull,
			available: localTime(2026, time.August, 6, 0, shanghai), wantStart: "2026-07-01", wantEnd: "2026-10-01",
		},
		{
			name: "year last complete", requested: "CURRENT_YEAR", grain: ir.TimeGrainYear,
			start: "2026-01-01", end: "2027-01-01", policy: registry.IncompletePeriodLastComplete,
			available: localTime(2026, time.August, 6, 0, shanghai), wantStart: "2025-01-01", wantEnd: "2026-01-01", fallback: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			contract := timeCompilerContract("Asia/Shanghai", registry.ComparisonSameDayCount, registry.MonthEndClampToLastDay,
				registry.TimeGrainMonth, registry.TimeGrainQuarter, registry.TimeGrainYear)
			result, err := Resolve(context.Background(), timeCompilerIR(test.requested, test.grain, test.start, test.end, nil), contract, MaterializationMeta{
				DataAvailableThrough: test.available, PolicyApplied: test.policy, PolicySource: registry.PolicySourceMetric,
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := result.ResolvedStart.Format("2006-01-02"); got != test.wantStart {
				t.Fatalf("start = %s, want %s", got, test.wantStart)
			}
			if got := result.ResolvedEndExclusive.Format("2006-01-02"); got != test.wantEnd {
				t.Fatalf("end = %s, want %s", got, test.wantEnd)
			}
			if result.Timezone != "Asia/Shanghai" || result.ResolvedStart.Location().String() != shanghai.String() ||
				result.PolicySource != string(registry.PolicySourceMetric) || result.TruncatedByDataAvailability != test.truncated ||
				result.PeriodFallbackApplied != test.fallback {
				t.Fatalf("unexpected resolved contract: %#v", result)
			}
		})
	}
}

func TestResolveComparisonLeapDayAndMonthEndOverflow(t *testing.T) {
	shanghai := mustLocation(t, "Asia/Shanghai")
	for _, test := range []struct {
		name      string
		start     string
		end       string
		grain     ir.TimeGrain
		typeValue ir.ComparisonType
		rule      registry.MonthEndOverflowRule
		wantStart string
		wantEnd   string
		wantErr   error
	}{
		{
			name: "leap day YoY clamps", start: "2024-02-29", end: "2024-03-01", grain: ir.TimeGrainDay,
			typeValue: ir.ComparisonYearOverYear, rule: registry.MonthEndClampToLastDay,
			wantStart: "2023-02-28", wantEnd: "2023-03-01",
		},
		{
			name: "March 31 MoM clamps", start: "2026-03-31", end: "2026-04-01", grain: ir.TimeGrainDay,
			typeValue: ir.ComparisonMonthOverMonth, rule: registry.MonthEndClampToLastDay,
			wantStart: "2026-02-28", wantEnd: "2026-03-01",
		},
		{
			name: "March 31 MoM skips", start: "2026-03-31", end: "2026-04-01", grain: ir.TimeGrainDay,
			typeValue: ir.ComparisonMonthOverMonth, rule: registry.MonthEndSkip, wantErr: ErrTimeComparisonUndefined,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			comparison := &ir.Comparison{Type: test.typeValue, Periods: 1}
			contract := timeCompilerContract("Asia/Shanghai", registry.ComparisonSameCalendarRange, test.rule, registry.TimeGrainDay)
			result, err := Resolve(context.Background(), timeCompilerIR("EXPLICIT_DAY", test.grain, test.start, test.end, comparison), contract,
				MaterializationMeta{DataAvailableThrough: localTime(2026, time.December, 31, 0, shanghai), PolicyApplied: registry.IncompletePeriodFull, PolicySource: registry.PolicySourceTimeContract})
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("error = %v, want %v", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if result.Comparison == nil || result.Comparison.ResolvedStart.Format("2006-01-02") != test.wantStart ||
				result.Comparison.ResolvedEndExclusive.Format("2006-01-02") != test.wantEnd || !result.Comparison.OverflowApplied {
				t.Fatalf("unexpected comparison: %#v", result.Comparison)
			}
		})
	}
}

func TestResolveSameDayCountAcrossDST(t *testing.T) {
	newYork := mustLocation(t, "America/New_York")
	comparison := &ir.Comparison{Type: ir.ComparisonYearOverYear, Periods: 1}
	contract := timeCompilerContract("America/New_York", registry.ComparisonSameDayCount, registry.MonthEndClampToLastDay, registry.TimeGrainMonth)
	result, err := Resolve(context.Background(), timeCompilerIR("CURRENT_MONTH", ir.TimeGrainMonth, "2026-03-01", "2026-04-01", comparison), contract,
		MaterializationMeta{DataAvailableThrough: localTime(2026, time.March, 9, 18, newYork), PolicyApplied: registry.IncompletePeriodMTD, PolicySource: registry.PolicySourceDomain})
	if err != nil {
		t.Fatal(err)
	}
	_, startOffset := result.ResolvedStart.Zone()
	_, endOffset := result.ResolvedEndExclusive.Zone()
	if result.ResolvedEndExclusive.Format("2006-01-02") != "2026-03-10" || startOffset != -5*60*60 || endOffset != -4*60*60 {
		t.Fatalf("DST boundaries were not calculated in business timezone: %#v", result)
	}
	if result.Comparison == nil || calendarDayCount(result.ResolvedStart, result.ResolvedEndExclusive) != 9 ||
		calendarDayCount(result.Comparison.ResolvedStart, result.Comparison.ResolvedEndExclusive) != 9 {
		t.Fatalf("same-day comparison lost calendar-day alignment: %#v", result.Comparison)
	}
}

func TestResolveFiscalQuarterUsesOnlyCalendarResolver(t *testing.T) {
	loc := mustLocation(t, "Asia/Shanghai")
	calendar := &recordingFiscalCalendar{loc: loc}
	contract := timeCompilerContract("Asia/Shanghai", registry.ComparisonSameDayCount, registry.MonthEndClampToLastDay, registry.TimeGrainFiscalQuarter)
	contract.CalendarDatasetVersionID = "calendar-version-v4"
	request := timeCompilerIR("CURRENT_FISCAL_QUARTER", ir.TimeGrainQuarter, "2026-04-01", "2026-07-01", nil)

	result, err := Resolve(context.Background(), request, contract, MaterializationMeta{
		DataAvailableThrough: localTime(2026, time.May, 8, 0, loc), PolicyApplied: registry.IncompletePeriodFull,
		PolicySource: registry.PolicySourceTimeContract, Calendar: calendar,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Grain != string(registry.TimeGrainFiscalQuarter) || result.CalendarVersionID != "calendar-version-v4" ||
		result.ResolvedStart.Format("2006-01-02") != "2026-04-01" || result.ResolvedEndExclusive.Format("2006-01-02") != "2026-07-01" ||
		len(calendar.requests) != 1 || calendar.requests[0].Offset != 0 {
		t.Fatalf("unexpected fiscal resolution: result=%#v requests=%#v", result, calendar.requests)
	}

	calendar.requests = nil
	result, err = Resolve(context.Background(), request, contract, MaterializationMeta{
		DataAvailableThrough: localTime(2026, time.May, 8, 0, loc), PolicyApplied: registry.IncompletePeriodLastComplete,
		PolicySource: registry.PolicySourceTimeContract, Calendar: calendar,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.PeriodFallbackApplied || result.ResolvedStart.Format("2006-01-02") != "2026-01-01" ||
		result.ResolvedEndExclusive.Format("2006-01-02") != "2026-04-01" || len(calendar.requests) != 2 || calendar.requests[1].Offset != -1 {
		t.Fatalf("fiscal LAST_COMPLETE did not use prior calendar key: result=%#v requests=%#v", result, calendar.requests)
	}
}

func TestResolveTimeNegativeContracts(t *testing.T) {
	loc := mustLocation(t, "Asia/Shanghai")
	meta := MaterializationMeta{DataAvailableThrough: localTime(2026, time.August, 8, 0, loc), PolicyApplied: registry.IncompletePeriodMTD, PolicySource: registry.PolicySourcePlatformDefault}
	monthOnly := timeCompilerContract("Asia/Shanghai", registry.ComparisonSameDayCount, registry.MonthEndClampToLastDay, registry.TimeGrainMonth)

	if _, err := Resolve(context.Background(), timeCompilerIR("CURRENT_WEEK", ir.TimeGrainWeek, "2026-08-03", "2026-08-10", nil), monthOnly, meta); !errors.Is(err, ErrTimeUnsupportedGrain) {
		t.Fatalf("unsupported grain error = %v", err)
	}
	if _, err := Resolve(context.Background(), timeCompilerIR("CURRENT_MONTH", ir.TimeGrainMonth, "2026-08-01", "2026-08-01", nil), monthOnly, meta); !errors.Is(err, ErrTimeRangeEmpty) {
		t.Fatalf("empty range error = %v", err)
	}
	meta.DataAvailableThrough = localTime(2026, time.July, 1, 0, loc)
	if _, err := Resolve(context.Background(), timeCompilerIR("CURRENT_MONTH", ir.TimeGrainMonth, "2026-08-01", "2026-09-01", nil), monthOnly, meta); !errors.Is(err, ErrTimeRangeEmpty) {
		t.Fatalf("fully unavailable range error = %v", err)
	}
	fiscal := timeCompilerContract("Asia/Shanghai", registry.ComparisonSameDayCount, registry.MonthEndClampToLastDay, registry.TimeGrainFiscalQuarter)
	fiscal.CalendarDatasetVersionID = "calendar-version-v4"
	meta.DataAvailableThrough = localTime(2026, time.August, 8, 0, loc)
	if _, err := Resolve(context.Background(), timeCompilerIR("CURRENT_FISCAL_QUARTER", ir.TimeGrainQuarter, "2026-04-01", "2026-07-01", nil), fiscal, meta); !errors.Is(err, ErrTimeCalendarLookupFailed) {
		t.Fatalf("missing calendar error = %v", err)
	}
}

func TestResolveTimeProperties(t *testing.T) {
	loc := mustLocation(t, "Asia/Shanghai")
	contract := timeCompilerContract("Asia/Shanghai", registry.ComparisonSameDayCount, registry.MonthEndClampToLastDay, registry.TimeGrainMonth)
	comparison := &ir.Comparison{Type: ir.ComparisonYearOverYear, Periods: 1}
	for month := time.January; month <= time.December; month++ {
		periodStart := time.Date(2024, month, 1, 0, 0, 0, 0, loc)
		periodEnd := periodStart.AddDate(0, 1, 0)
		for day := 1; day <= 24; day += 5 {
			available := periodStart.AddDate(0, 0, day-1)
			result, err := Resolve(context.Background(), timeCompilerIR(
				"CURRENT_MONTH", ir.TimeGrainMonth, periodStart.Format("2006-01-02"), periodEnd.Format("2006-01-02"), comparison,
			), contract, MaterializationMeta{DataAvailableThrough: available, PolicyApplied: registry.IncompletePeriodMTD, PolicySource: registry.PolicySourcePlatformDefault})
			if err != nil {
				t.Fatalf("month=%s day=%d: %v", month, day, err)
			}
			if !result.ResolvedEndExclusive.After(result.ResolvedStart) || result.Comparison == nil ||
				calendarDayCount(result.ResolvedStart, result.ResolvedEndExclusive) != calendarDayCount(result.Comparison.ResolvedStart, result.Comparison.ResolvedEndExclusive) {
				t.Fatalf("property failed for month=%s day=%d: %#v", month, day, result)
			}
		}
	}
}

func TestApplyDataAvailabilityKeepsComparisonAligned(t *testing.T) {
	loc := mustLocation(t, "Asia/Shanghai")
	for _, alignment := range []registry.ComparisonAlignment{
		registry.ComparisonSameDayCount,
		registry.ComparisonSameCalendarRange,
	} {
		t.Run(string(alignment), func(t *testing.T) {
			spec := ir.ResolvedTimeSpec{
				RequestedPeriod: "ABSOLUTE", Grain: string(registry.TimeGrainDay),
				PolicyApplied: string(registry.IncompletePeriodFull), PolicySource: string(registry.PolicySourcePlatformDefault),
				ResolvedStart:        time.Date(2026, time.August, 1, 0, 0, 0, 0, loc),
				ResolvedEndExclusive: time.Date(2026, time.August, 11, 0, 0, 0, 0, loc),
				DataAvailableThrough: time.Date(2026, time.August, 10, 0, 0, 0, 0, loc),
				Timezone:             loc.String(),
				Comparison: &ir.ResolvedComparison{
					Type: "YEAR_OVER_YEAR", Periods: 1, Alignment: string(alignment),
					ResolvedStart:        time.Date(2025, time.August, 1, 0, 0, 0, 0, loc),
					ResolvedEndExclusive: time.Date(2025, time.August, 11, 0, 0, 0, 0, loc),
				},
			}
			adjusted, err := ApplyDataAvailability(spec, time.Date(2026, time.August, 5, 23, 0, 0, 0, loc))
			if err != nil {
				t.Fatal(err)
			}
			if !adjusted.TruncatedByDataAvailability || adjusted.ResolvedEndExclusive.Day() != 6 || adjusted.Comparison == nil ||
				calendarDayCount(adjusted.ResolvedStart, adjusted.ResolvedEndExclusive) !=
					calendarDayCount(adjusted.Comparison.ResolvedStart, adjusted.Comparison.ResolvedEndExclusive) {
				t.Fatalf("coverage clipping lost comparison alignment: %#v", adjusted)
			}
		})
	}
}

type recordingFiscalCalendar struct {
	loc      *time.Location
	requests []FiscalCalendarRequest
}

func (resolver *recordingFiscalCalendar) ResolveFiscalPeriod(_ context.Context, request FiscalCalendarRequest) (FiscalCalendarPeriod, error) {
	resolver.requests = append(resolver.requests, request)
	switch request.Offset {
	case 0:
		return FiscalCalendarPeriod{PeriodKey: "FY2026-Q1", Start: time.Date(2026, time.April, 1, 0, 0, 0, 0, resolver.loc), EndExclusive: time.Date(2026, time.July, 1, 0, 0, 0, 0, resolver.loc)}, nil
	case -1:
		return FiscalCalendarPeriod{PeriodKey: "FY2025-Q4", Start: time.Date(2026, time.January, 1, 0, 0, 0, 0, resolver.loc), EndExclusive: time.Date(2026, time.April, 1, 0, 0, 0, 0, resolver.loc)}, nil
	default:
		return FiscalCalendarPeriod{}, errors.New("calendar row missing")
	}
}

func timeCompilerIR(requested string, grain ir.TimeGrain, start, end string, comparison *ir.Comparison) ir.SemanticIR {
	return ir.SemanticIR{
		TimeRange: &ir.TimeRange{
			DimensionVersionID: "order-date-v1", Start: start, EndExclusive: end,
			Timezone: "user-supplied-zone-is-ignored", RequestedPeriod: requested, Grain: grain,
		},
		Comparison: comparison,
	}
}

func timeCompilerContract(timezone string, alignment registry.ComparisonAlignment, overflow registry.MonthEndOverflowRule, grains ...registry.TimeGrain) registry.TimeContractVersion {
	return registry.TimeContractVersion{
		Timezone: timezone, IncompletePeriodPolicy: registry.IncompletePeriodMTD,
		ComparisonAlignment: alignment, MonthEndOverflowRule: overflow,
		SupportedGrains: append([]registry.TimeGrain(nil), grains...),
	}
}

func mustLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func localTime(year int, month time.Month, day, hour int, loc *time.Location) time.Time {
	return time.Date(year, month, day, hour, 0, 0, 0, loc)
}
