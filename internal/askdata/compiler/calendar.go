package compiler

import (
	"context"
	"time"

	"intelligent-report-generation-system/internal/askdata/registry"
)

// FiscalCalendarRequest identifies a governed calendar period relative to the
// requested period retained in IR. Offset zero resolves the requested period;
// negative offsets resolve prior fiscal periods of the same grain.
//
// PostgreSQL implementations must obtain the fiscal_period_key from the
// certified calendar dataset and return min(date), max(date)+1 day. The
// compiler deliberately has no fallback fiscal-month arithmetic.
type FiscalCalendarRequest struct {
	CalendarVersionID string
	RequestedPeriod   string
	Grain             registry.TimeGrain
	StartHint         time.Time
	EndExclusiveHint  time.Time
	Offset            int
}

type FiscalCalendarPeriod struct {
	PeriodKey    string
	Start        time.Time
	EndExclusive time.Time
}

type FiscalCalendarResolver interface {
	ResolveFiscalPeriod(context.Context, FiscalCalendarRequest) (FiscalCalendarPeriod, error)
}
