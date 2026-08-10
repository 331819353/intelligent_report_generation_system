// Package calendar contains synthetic-only evaluation fixtures. These values
// exercise calendar mechanics and must never be represented as a real domain's
// fiscal policy.
package calendar

import (
	"errors"
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
