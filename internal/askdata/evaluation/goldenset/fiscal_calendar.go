package goldenset

import (
	"context"
	"errors"
	"fmt"
	"time"

	"intelligent-report-generation-system/internal/askdata/compiler"
	"intelligent-report-generation-system/internal/askdata/registry"
	"intelligent-report-generation-system/internal/askdata/testfixture/calendar"
)

var errFiscalCalendarLookup = errors.New("synthetic fiscal calendar lookup failed")

// FiscalCalendar is a materialized period table, not an arithmetic engine. A
// certified calendar dataset is a table in production, and the compiler has no
// fallback fiscal arithmetic by design (see compiler.FiscalCalendarRequest), so
// the fixture reproduces the lookup rather than the derivation.
type FiscalCalendar struct {
	versionID string
	location  *time.Location
	periods   map[registry.TimeGrain][]compiler.FiscalCalendarPeriod
}

// NewFiscalCalendar materializes fiscal years [fromYear, toYear] for the
// fixture. A fiscal year is labelled by the calendar year it starts in.
func NewFiscalCalendar(fixture calendar.Fixture, versionID string, fromYear, toYear int) (*FiscalCalendar, error) {
	location, err := fixture.Location()
	if err != nil {
		return nil, err
	}
	if versionID == "" || toYear < fromYear || toYear-fromYear > 100 {
		return nil, errFiscalCalendarLookup
	}
	table := &FiscalCalendar{
		versionID: versionID, location: location,
		periods: map[registry.TimeGrain][]compiler.FiscalCalendarPeriod{
			registry.TimeGrainFiscalYear:    {},
			registry.TimeGrainFiscalQuarter: {},
			registry.TimeGrainFiscalMonth:   {},
		},
	}
	for year := fromYear; year <= toYear; year++ {
		start := time.Date(year, fixture.FiscalYearStart, 1, 0, 0, 0, 0, location)
		table.periods[registry.TimeGrainFiscalYear] = append(
			table.periods[registry.TimeGrainFiscalYear],
			compiler.FiscalCalendarPeriod{
				PeriodKey: fmt.Sprintf("FY%04d", year), Start: start, EndExclusive: start.AddDate(1, 0, 0),
			},
		)
		for quarter := 0; quarter < 4; quarter++ {
			quarterStart := start.AddDate(0, quarter*3, 0)
			table.periods[registry.TimeGrainFiscalQuarter] = append(
				table.periods[registry.TimeGrainFiscalQuarter],
				compiler.FiscalCalendarPeriod{
					PeriodKey:    fmt.Sprintf("FY%04d-Q%d", year, quarter+1),
					Start:        quarterStart,
					EndExclusive: quarterStart.AddDate(0, 3, 0),
				},
			)
		}
		for month := 0; month < 12; month++ {
			monthStart := start.AddDate(0, month, 0)
			table.periods[registry.TimeGrainFiscalMonth] = append(
				table.periods[registry.TimeGrainFiscalMonth],
				compiler.FiscalCalendarPeriod{
					PeriodKey:    fmt.Sprintf("FY%04d-M%02d", year, month+1),
					Start:        monthStart,
					EndExclusive: monthStart.AddDate(0, 1, 0),
				},
			)
		}
	}
	return table, nil
}

func (table *FiscalCalendar) VersionID() string { return table.versionID }

// Period is the fixture-side lookup used to DECLARE an expectation. Callers
// state which period they expect ("the fiscal month twelve periods before the
// one containing this date"); deriving that offset from a comparison contract
// is the compiler's job and is exactly what the suite measures.
func (table *FiscalCalendar) Period(
	grain registry.TimeGrain,
	containing time.Time,
	offset int,
) (compiler.FiscalCalendarPeriod, error) {
	if table == nil {
		return compiler.FiscalCalendarPeriod{}, errFiscalCalendarLookup
	}
	periods, supported := table.periods[grain]
	if !supported {
		return compiler.FiscalCalendarPeriod{}, fmt.Errorf("%w: grain %s", errFiscalCalendarLookup, grain)
	}
	local := containing.In(table.location)
	for index, period := range periods {
		if local.Before(period.Start) || !local.Before(period.EndExclusive) {
			continue
		}
		target := index + offset
		if target < 0 || target >= len(periods) {
			return compiler.FiscalCalendarPeriod{}, fmt.Errorf("%w: offset %d is outside the table", errFiscalCalendarLookup, offset)
		}
		return periods[target], nil
	}
	return compiler.FiscalCalendarPeriod{}, fmt.Errorf("%w: %s is outside the table", errFiscalCalendarLookup, local)
}

func (table *FiscalCalendar) ResolveFiscalPeriod(
	ctx context.Context,
	request compiler.FiscalCalendarRequest,
) (compiler.FiscalCalendarPeriod, error) {
	if err := ctx.Err(); err != nil {
		return compiler.FiscalCalendarPeriod{}, err
	}
	if request.CalendarVersionID != table.versionID {
		return compiler.FiscalCalendarPeriod{}, fmt.Errorf(
			"%w: calendar version %s is not materialized", errFiscalCalendarLookup, request.CalendarVersionID,
		)
	}
	return table.Period(request.Grain, request.StartHint, request.Offset)
}
