// Package schedule implements governed, in-app-only report scheduling. A
// delivery carries a link to an exact published version and never an attached
// result, so opening it still crosses the report runtime permission boundary.
package schedule

import (
	"errors"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"intelligent-report-generation-system/internal/askdata"
)

var (
	ErrInvalid     = errors.New("report schedule request is invalid")
	ErrNotFound    = errors.New("report schedule was not found")
	ErrForbidden   = errors.New("report schedule access is forbidden")
	ErrConflict    = errors.New("report schedule changed concurrently")
	ErrUnavailable = errors.New("scheduled report version is unavailable")
)

type Identity struct{ TenantID, DomainID, ActorID askdata.ID }

func (value Identity) Validate() error {
	for _, id := range []askdata.ID{value.TenantID, value.DomainID, value.ActorID} {
		parsed, e := uuid.Parse(string(id))
		if e != nil || parsed.String() != string(id) {
			return ErrInvalid
		}
	}
	return nil
}

type Kind string

const (
	KindDaily   Kind = "DAILY"
	KindWeekly  Kind = "WEEKLY"
	KindMonthly Kind = "MONTHLY"
)

type State string

const (
	StateActive   State = "ACTIVE"
	StatePaused   State = "PAUSED"
	StateDisabled State = "DISABLED"
)

type Schedule struct {
	ID                     askdata.ID `json:"id"`
	ReportID               askdata.ID `json:"reportId"`
	ReportVersionID        askdata.ID `json:"reportVersionId"`
	Name                   string     `json:"name"`
	Kind                   Kind       `json:"scheduleKind"`
	LocalTime              string     `json:"localTime"`
	Weekdays               []int      `json:"weekdays"`
	DayOfMonth             *int       `json:"dayOfMonth,omitempty"`
	Timezone               string     `json:"timezone"`
	BusinessCalendar       string     `json:"businessCalendar"`
	State                  State      `json:"state"`
	NextRunAt              time.Time  `json:"nextRunAt"`
	ConsecutiveFailures    int        `json:"consecutiveFailures"`
	MaxConsecutiveFailures int        `json:"maxConsecutiveFailures"`
	MissAfterSeconds       int        `json:"missAfterSeconds"`
	OwnerUserID            askdata.ID `json:"ownerUserId"`
	RecordVersion          int64      `json:"recordVersion"`
	LastFailureCode        string     `json:"lastFailureCode,omitempty"`
	CreatedAt              time.Time  `json:"createdAt"`
	UpdatedAt              time.Time  `json:"updatedAt"`
}
type Subscription struct {
	ID              askdata.ID `json:"id"`
	ScheduleID      askdata.ID `json:"scheduleId"`
	RecipientUserID askdata.ID `json:"recipientUserId"`
	Channel         string     `json:"channel"`
	State           string     `json:"state"`
	RecordVersion   int64      `json:"recordVersion"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}
type Delivery struct {
	ID              askdata.ID `json:"id"`
	ScheduleID      askdata.ID `json:"scheduleId"`
	SubscriptionID  askdata.ID `json:"subscriptionId"`
	ReportID        askdata.ID `json:"reportId"`
	ReportVersionID askdata.ID `json:"reportVersionId"`
	RecipientUserID askdata.ID `json:"recipientUserId"`
	ScheduledFor    time.Time  `json:"scheduledFor"`
	Channel         string     `json:"channel"`
	State           string     `json:"state"`
	Attempt         int        `json:"attempt"`
	ReportLink      string     `json:"reportLink,omitempty"`
	FailureCode     string     `json:"failureCode,omitempty"`
	AccessCheckedAt *time.Time `json:"accessCheckedAt,omitempty"`
	ReadAt          *time.Time `json:"readAt,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
}

type CreateInput struct {
	ReportVersionID        askdata.ID `json:"reportVersionId"`
	Name                   string     `json:"name"`
	ScheduleKind           Kind       `json:"scheduleKind"`
	LocalTime              string     `json:"localTime"`
	Weekdays               []int      `json:"weekdays"`
	DayOfMonth             *int       `json:"dayOfMonth"`
	Timezone               string     `json:"timezone"`
	BusinessCalendar       string     `json:"businessCalendar"`
	MaxConsecutiveFailures int        `json:"maxConsecutiveFailures"`
	MissAfterSeconds       int        `json:"missAfterSeconds"`
}
type SubscriptionInput struct {
	RecipientUserID askdata.ID `json:"recipientUserId"`
	Channel         string     `json:"channel"`
}
type VersionInput struct {
	ExpectedVersion int64 `json:"expectedVersion"`
}

func (input *CreateInput) Normalize(now time.Time) (time.Time, error) {
	if input == nil || input.ReportVersionID.Validate() != nil || !validText(input.Name, 1, 256) || len(input.Timezone) < 1 || len(input.Timezone) > 128 {
		return time.Time{}, ErrInvalid
	}
	location, err := time.LoadLocation(input.Timezone)
	if err != nil {
		return time.Time{}, ErrInvalid
	}
	parsed, err := time.Parse("15:04", input.LocalTime)
	if err != nil {
		return time.Time{}, ErrInvalid
	}
	input.LocalTime = parsed.Format("15:04:00")
	if input.BusinessCalendar == "" {
		input.BusinessCalendar = "CALENDAR_DAYS"
	}
	if input.BusinessCalendar != "CALENDAR_DAYS" && input.BusinessCalendar != "WEEKDAYS" {
		return time.Time{}, ErrInvalid
	}
	if input.MaxConsecutiveFailures == 0 {
		input.MaxConsecutiveFailures = 3
	}
	if input.MaxConsecutiveFailures < 1 || input.MaxConsecutiveFailures > 20 {
		return time.Time{}, ErrInvalid
	}
	if input.MissAfterSeconds == 0 {
		input.MissAfterSeconds = 86400
	}
	if input.MissAfterSeconds < 60 || input.MissAfterSeconds > 604800 {
		return time.Time{}, ErrInvalid
	}
	sort.Ints(input.Weekdays)
	for index, day := range input.Weekdays {
		if day < 0 || day > 6 || (index > 0 && input.Weekdays[index-1] == day) {
			return time.Time{}, ErrInvalid
		}
	}
	switch input.ScheduleKind {
	case KindDaily:
		if len(input.Weekdays) != 0 || input.DayOfMonth != nil {
			return time.Time{}, ErrInvalid
		}
	case KindWeekly:
		if len(input.Weekdays) == 0 || input.DayOfMonth != nil {
			return time.Time{}, ErrInvalid
		}
	case KindMonthly:
		if len(input.Weekdays) != 0 || input.DayOfMonth == nil || *input.DayOfMonth < 1 || *input.DayOfMonth > 31 {
			return time.Time{}, ErrInvalid
		}
	default:
		return time.Time{}, ErrInvalid
	}
	return NextOccurrence(*input, now.In(location))
}

// NextOccurrence implements an explicit wall-clock policy: a nonexistent DST
// time advances to the first valid instant after it; duplicated fall-back wall
// times execute once at the earlier occurrence. Monthly day 29-31 clamps to
// the last day, and WEEKDAYS moves weekend dates backward.
func NextOccurrence(input CreateInput, after time.Time) (time.Time, error) {
	location, err := time.LoadLocation(input.Timezone)
	if err != nil {
		return time.Time{}, ErrInvalid
	}
	clock, err := time.Parse("15:04:05", input.LocalTime)
	if err != nil {
		return time.Time{}, ErrInvalid
	}
	local := after.In(location)
	for offset := 0; offset <= 370; offset++ {
		date := local.AddDate(0, 0, offset)
		year, month, day := date.Date()
		eligible := false
		switch input.ScheduleKind {
		case KindDaily:
			eligible = input.BusinessCalendar != "WEEKDAYS" ||
				(date.Weekday() != time.Saturday && date.Weekday() != time.Sunday)
		case KindWeekly:
			for _, configured := range input.Weekdays {
				if int(date.Weekday()) == configured {
					eligible = true
					break
				}
				if input.BusinessCalendar == "WEEKDAYS" && date.Weekday() == time.Friday &&
					(configured == int(time.Saturday) || configured == int(time.Sunday)) {
					eligible = true
				}
			}
			if input.BusinessCalendar == "WEEKDAYS" &&
				(date.Weekday() == time.Saturday || date.Weekday() == time.Sunday) {
				eligible = false
			}
		case KindMonthly:
			eligible = sameDate(date, monthlyTarget(location, year, month, *input.DayOfMonth, input.BusinessCalendar))
			// A day-of-month at the start of the following month can move
			// backward into this month under the WEEKDAYS policy.
			if !eligible && input.BusinessCalendar == "WEEKDAYS" {
				nextMonth := time.Date(year, month+1, 1, 12, 0, 0, 0, location)
				nextYear, nextMonthValue, _ := nextMonth.Date()
				eligible = sameDate(date, monthlyTarget(location, nextYear, nextMonthValue, *input.DayOfMonth, input.BusinessCalendar))
			}
		}
		if !eligible {
			continue
		}
		candidate := validWallTime(location, year, month, day, clock.Hour(), clock.Minute(), clock.Second())
		if candidate.After(after) {
			return candidate.UTC(), nil
		}
	}
	return time.Time{}, ErrInvalid
}
func monthlyTarget(location *time.Location, year int, month time.Month, day int, calendar string) time.Time {
	last := time.Date(year, month+1, 0, 12, 0, 0, 0, location).Day()
	if day > last {
		day = last
	}
	target := time.Date(year, month, day, 12, 0, 0, 0, location)
	if calendar == "WEEKDAYS" {
		switch target.Weekday() {
		case time.Saturday:
			target = target.AddDate(0, 0, -1)
		case time.Sunday:
			target = target.AddDate(0, 0, -2)
		}
	}
	return target
}
func sameDate(left, right time.Time) bool {
	leftYear, leftMonth, leftDay := left.Date()
	rightYear, rightMonth, rightDay := right.Date()
	return leftYear == rightYear && leftMonth == rightMonth && leftDay == rightDay
}
func validWallTime(location *time.Location, year int, month time.Month, day, hour, minute, second int) time.Time {
	start := time.Date(year, month, day, 0, 0, 0, 0, location)
	for offset := 0; offset < 27*60; offset++ {
		value := start.Add(time.Duration(offset) * time.Minute)
		local := value.In(location)
		localYear, localMonth, localDay := local.Date()
		if localYear != year || localMonth != month || localDay != day {
			if localYear > year || localMonth != month || localDay > day {
				break
			}
			continue
		}
		h, m, _ := local.Clock()
		if h == hour && m == minute {
			candidate := value.Add(time.Duration(second) * time.Second)
			candidateLocal := candidate.In(location)
			candidateHour, candidateMinute, candidateSecond := candidateLocal.Clock()
			if sameDate(candidateLocal, local) && candidateHour == hour && candidateMinute == minute && candidateSecond == second {
				return candidate
			}
		}
		if h > hour || (h == hour && m >= minute) {
			return value
		}
	}
	return time.Date(year, month, day, hour, minute, second, 0, location)
}
func validText(value string, min, max int) bool {
	if value != strings.TrimSpace(value) || len(value) < min || len(value) > max {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
