// Package calendar contains synthetic-only evaluation fixtures. These values
// exercise calendar mechanics and must never be represented as a real domain's
// fiscal policy.
package calendar

import (
	"errors"
	"fmt"
	"time"

	"intelligent-report-generation-system/internal/askdata"
)

const SyntheticFixtureVersion = "synthetic-calendar-v1"

type Fixture struct {
	ID              askdata.ID
	Version         string
	Synthetic       bool
	Timezone        string
	FiscalYearStart time.Month
	WeekStart       time.Weekday
	ContentHash     askdata.ContentHash
}

// Synthetic builds a deterministic calendar fixture. The content hash covers
// every parameter that changes a resolved boundary, so two fixtures that would
// resolve periods differently can never share an identity.
func Synthetic(id askdata.ID, timezone string, fiscalYearStart time.Month, weekStart time.Weekday) Fixture {
	fixture := Fixture{
		ID: id, Version: SyntheticFixtureVersion, Synthetic: true,
		Timezone: timezone, FiscalYearStart: fiscalYearStart, WeekStart: weekStart,
	}
	fixture.ContentHash = askdata.HashBytes([]byte(fmt.Sprintf(
		"%s|%s|%d|%d", SyntheticFixtureVersion, timezone, int(fiscalYearStart), int(weekStart),
	)))
	return fixture
}

func SyntheticShanghai() Fixture {
	fixture := Fixture{
		ID: "synthetic-calendar-shanghai", Version: SyntheticFixtureVersion, Synthetic: true,
		Timezone: "Asia/Shanghai", FiscalYearStart: time.April, WeekStart: time.Monday,
	}
	fixture.ContentHash = askdata.HashBytes([]byte("synthetic-calendar-v1|Asia/Shanghai|4|1"))
	return fixture
}

func (fixture Fixture) Validate() error {
	if fixture.ID.Validate() != nil || fixture.Version != SyntheticFixtureVersion || !fixture.Synthetic ||
		fixture.ContentHash.Validate() != nil || fixture.FiscalYearStart < time.January ||
		fixture.FiscalYearStart > time.December || fixture.WeekStart < time.Sunday || fixture.WeekStart > time.Saturday {
		return errors.New("synthetic calendar fixture is invalid")
	}
	_, err := time.LoadLocation(fixture.Timezone)
	return err
}

// Location resolves the fixture timezone once so callers never silently fall
// back to UTC when the zone database is unavailable.
func (fixture Fixture) Location() (*time.Location, error) {
	if err := fixture.Validate(); err != nil {
		return nil, err
	}
	return time.LoadLocation(fixture.Timezone)
}
