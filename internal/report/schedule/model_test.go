package schedule

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"intelligent-report-generation-system/internal/askdata"
)

func TestNormalizeRejectsInvalidShapesAndAppliesDefaults(t *testing.T) {
	versionID := askdata.ID(uuid.NewString())
	day := 31
	tests := []CreateInput{
		{ReportVersionID: versionID, Name: "daily", ScheduleKind: KindDaily, LocalTime: "09:00", Weekdays: []int{1}, Timezone: "UTC"},
		{ReportVersionID: versionID, Name: "weekly", ScheduleKind: KindWeekly, LocalTime: "09:00", Timezone: "UTC"},
		{ReportVersionID: versionID, Name: "monthly", ScheduleKind: KindMonthly, LocalTime: "09:00", DayOfMonth: &day, Weekdays: []int{1}, Timezone: "UTC"},
		{ReportVersionID: versionID, Name: "duplicate", ScheduleKind: KindWeekly, LocalTime: "09:00", Weekdays: []int{1, 1}, Timezone: "UTC"},
		{ReportVersionID: versionID, Name: "timezone", ScheduleKind: KindDaily, LocalTime: "09:00", Timezone: "Mars/Olympus"},
	}
	for index := range tests {
		if _, err := tests[index].Normalize(time.Now()); err != ErrInvalid {
			t.Fatalf("case %d error = %v", index, err)
		}
	}
	valid := CreateInput{ReportVersionID: versionID, Name: "daily", ScheduleKind: KindDaily, LocalTime: "09:05", Timezone: "Asia/Shanghai"}
	if _, err := valid.Normalize(time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if valid.LocalTime != "09:05:00" || valid.BusinessCalendar != "CALENDAR_DAYS" || valid.MaxConsecutiveFailures != 3 || valid.MissAfterSeconds != 86400 {
		t.Fatalf("normalized input = %#v", valid)
	}
}

func TestNextOccurrenceCalendarPolicies(t *testing.T) {
	tests := []struct {
		name  string
		input CreateInput
		after time.Time
		want  time.Time
	}{
		{
			name:  "monthly clamps leap February",
			input: CreateInput{ScheduleKind: KindMonthly, LocalTime: "08:30:00", DayOfMonth: intPointer(31), Timezone: "Asia/Shanghai", BusinessCalendar: "CALENDAR_DAYS"},
			after: mustTime(t, "Asia/Shanghai", 2028, time.February, 1, 0, 0),
			want:  mustTime(t, "Asia/Shanghai", 2028, time.February, 29, 8, 30).UTC(),
		},
		{
			name:  "monthly day one can move into previous month",
			input: CreateInput{ScheduleKind: KindMonthly, LocalTime: "08:30:00", DayOfMonth: intPointer(1), Timezone: "Asia/Shanghai", BusinessCalendar: "WEEKDAYS"},
			after: mustTime(t, "Asia/Shanghai", 2026, time.January, 15, 0, 0),
			want:  mustTime(t, "Asia/Shanghai", 2026, time.January, 30, 8, 30).UTC(),
		},
		{
			name:  "weekly weekend moves to Friday",
			input: CreateInput{ScheduleKind: KindWeekly, LocalTime: "17:00:00", Weekdays: []int{int(time.Sunday)}, Timezone: "Asia/Shanghai", BusinessCalendar: "WEEKDAYS"},
			after: mustTime(t, "Asia/Shanghai", 2026, time.August, 10, 0, 0),
			want:  mustTime(t, "Asia/Shanghai", 2026, time.August, 14, 17, 0).UTC(),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := NextOccurrence(test.input, test.after)
			if err != nil || !got.Equal(test.want) {
				t.Fatalf("NextOccurrence() = %s, %v; want %s", got, err, test.want)
			}
		})
	}
}

func TestNextOccurrenceDSTPolicy(t *testing.T) {
	input := CreateInput{ScheduleKind: KindDaily, LocalTime: "02:30:00", Timezone: "America/New_York", BusinessCalendar: "CALENDAR_DAYS"}
	spring, err := NextOccurrence(input, mustTime(t, "America/New_York", 2026, time.March, 8, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	springLocal := spring.In(mustLocation(t, "America/New_York"))
	if springLocal.Hour() != 3 || springLocal.Minute() != 0 || springLocal.Day() != 8 {
		t.Fatalf("nonexistent 02:30 ran at %s", springLocal)
	}

	input.LocalTime = "01:30:00"
	fall, err := NextOccurrence(input, mustTime(t, "America/New_York", 2026, time.November, 1, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, time.November, 1, 5, 30, 0, 0, time.UTC)
	if !fall.Equal(want) {
		t.Fatalf("duplicated 01:30 ran at %s; want earlier occurrence %s", fall, want)
	}
}

func intPointer(value int) *int { return &value }

func mustLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	location, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return location
}

func mustTime(t *testing.T, zone string, year int, month time.Month, day, hour, minute int) time.Time {
	t.Helper()
	return time.Date(year, month, day, hour, minute, 0, 0, mustLocation(t, zone))
}
