package semanticqa

import (
	"testing"
	"time"
)

func TestNormalizeQueryTimeRange(t *testing.T) {
	t.Run("date half open range", func(t *testing.T) {
		value, err := normalizeQueryTimeRange(QueryTimeRange{
			Start: "2026-07-01", EndExclusive: "2026-08-01",
		})
		if err != nil || value.Start != "2026-07-01" ||
			value.EndExclusive != "2026-08-01" {
			t.Fatalf("normalized=%#v error=%v", value, err)
		}
	})
	t.Run("instant range is canonical UTC", func(t *testing.T) {
		value, err := normalizeQueryTimeRange(QueryTimeRange{
			Start:        "2026-07-01T08:00:00+08:00",
			EndExclusive: "2026-07-02T08:00:00+08:00",
		})
		if err != nil ||
			value.Start != "2026-07-01T00:00:00Z" ||
			value.EndExclusive != "2026-07-02T00:00:00Z" {
			t.Fatalf("normalized=%#v error=%v", value, err)
		}
	})
	for name, value := range map[string]QueryTimeRange{
		"reversed": {
			Start: "2026-08-01", EndExclusive: "2026-07-01",
		},
		"mixed precision": {
			Start:        "2026-07-01",
			EndExclusive: "2026-08-01T00:00:00Z",
		},
		"invalid": {
			Start: "last month", EndExclusive: "today",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := normalizeQueryTimeRange(value); err == nil {
				t.Fatal("unsafe time range was accepted")
			}
		})
	}
}

func TestParseQueryBoundaryRejectsTimezoneFreeDatetime(t *testing.T) {
	if _, _, err := parseQueryBoundary("2026-07-01 00:00:00"); err == nil {
		t.Fatal("timezone-free datetime was accepted")
	}
	parsed, dateOnly, err := parseQueryBoundary("2026-07-01")
	if err != nil || !dateOnly || parsed.Format(time.DateOnly) != "2026-07-01" {
		t.Fatalf("parsed=%v dateOnly=%v error=%v", parsed, dateOnly, err)
	}
}
