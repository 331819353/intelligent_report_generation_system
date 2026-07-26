package semanticqa

import (
	"testing"
	"time"
)

func TestInheritQueryContextOnlyFillsMissingSlots(t *testing.T) {
	t.Run("follow-up inherits governed metric and dimension", func(t *testing.T) {
		input := QueryPlanInput{Intent: "UNKNOWN"}
		inheritQueryContext(&input, QuerySlots{
			Intent: "METRIC", MetricCode: "sales_amount", DimensionCode: "region",
		})
		if input.Intent != "METRIC" || input.MetricCode != "sales_amount" ||
			input.DimensionCode != "region" {
			t.Fatalf("input=%#v", input)
		}
	})
	t.Run("current turn wins over prior context", func(t *testing.T) {
		input := QueryPlanInput{
			Intent: "RANKING", MetricCode: "order_count", DimensionCode: "channel",
		}
		inheritQueryContext(&input, QuerySlots{
			Intent: "METRIC", MetricCode: "sales_amount", DimensionCode: "region",
		})
		if input.Intent != "RANKING" || input.MetricCode != "order_count" ||
			input.DimensionCode != "channel" {
			t.Fatalf("input=%#v", input)
		}
	})
}

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

func TestNormalizeQueryMemberFilters(t *testing.T) {
	filters, err := normalizeQueryMemberFilters(
		"region", "华东",
		[]QueryMemberFilterInput{
			{DimensionCode: " channel ", MemberValue: " APP "},
			{DimensionCode: "category", MemberValue: "餐饮"},
		},
	)
	if err != nil || len(filters) != 2 ||
		filters[0].DimensionCode != "channel" ||
		filters[0].MemberValue != "APP" {
		t.Fatalf("filters=%#v error=%v", filters, err)
	}
	if _, err := normalizeQueryMemberFilters(
		"region", "华东",
		[]QueryMemberFilterInput{
			{DimensionCode: "REGION", MemberValue: "华南"},
		},
	); err == nil {
		t.Fatal("duplicate dimension filter was accepted")
	}
	if _, err := normalizeQueryMemberFilters(
		"", "",
		[]QueryMemberFilterInput{{DimensionCode: "", MemberValue: "APP"}},
	); err == nil {
		t.Fatal("filter without dimension was accepted")
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

func TestInferQueryTimePreset(t *testing.T) {
	tests := map[string]string{
		"今天的收入":            "TODAY",
		"昨天订单量":            "YESTERDAY",
		"最近 7 天销售排行":       "LAST_7_DAYS",
		"过去30天用户趋势":        "LAST_30_DAYS",
		"本月各区域收入":          "THIS_MONTH",
		"上个月渠道转化":          "LAST_MONTH",
		"今年累计成交额":          "THIS_YEAR",
		"去年商户增长":           "LAST_YEAR",
		"all time revenue": "",
	}
	for question, expected := range tests {
		if actual := inferQueryTimePreset(question); actual != expected {
			t.Fatalf("question=%q actual=%q expected=%q", question, actual, expected)
		}
	}
}

func TestResolveQueryTimePresetUsesTimezoneAndFieldPrecision(t *testing.T) {
	now := time.Date(2026, 7, 26, 10, 30, 0, 0, time.FixedZone("CST", 8*60*60))
	dateRange, err := resolveQueryTimePreset(
		"LAST_7_DAYS", "Asia/Shanghai", "DATE", now,
	)
	if err != nil ||
		dateRange.Start != "2026-07-20" ||
		dateRange.EndExclusive != "2026-07-27" {
		t.Fatalf("date range=%#v error=%v", dateRange, err)
	}
	instantRange, err := resolveQueryTimePreset(
		"LAST_MONTH", "Asia/Shanghai", "DATETIME", now,
	)
	if err != nil ||
		instantRange.Start != "2026-05-31T16:00:00Z" ||
		instantRange.EndExclusive != "2026-06-30T16:00:00Z" {
		t.Fatalf("instant range=%#v error=%v", instantRange, err)
	}
	if _, err := resolveQueryTimePreset(
		"LAST_MONTH", "Not/AZone", "DATETIME", now,
	); err == nil {
		t.Fatal("invalid timezone was accepted")
	}
}

func TestInferAndDeriveQueryComparisonWindow(t *testing.T) {
	if mode := inferQueryComparisonMode("本月收入同比"); mode != "YEAR_OVER_YEAR" {
		t.Fatalf("mode=%q", mode)
	}
	if mode := inferQueryComparisonMode("最近 7 天环比"); mode != "PREVIOUS_PERIOD" {
		t.Fatalf("mode=%q", mode)
	}
	previous, err := deriveQueryComparisonRange(
		QueryTimeRange{
			Start: "2026-07-20", EndExclusive: "2026-07-27",
		},
		"PREVIOUS_PERIOD", "LAST_7_DAYS", "Asia/Shanghai", "DATE",
	)
	if err != nil ||
		previous.Start != "2026-07-13" ||
		previous.EndExclusive != "2026-07-20" {
		t.Fatalf("previous=%#v error=%v", previous, err)
	}
	yearOverYear, err := deriveQueryComparisonRange(
		QueryTimeRange{
			Start:        "2026-06-30T16:00:00Z",
			EndExclusive: "2026-07-31T16:00:00Z",
		},
		"YEAR_OVER_YEAR", "THIS_MONTH", "Asia/Shanghai", "DATETIME",
	)
	if err != nil ||
		yearOverYear.Start != "2025-06-30T16:00:00Z" ||
		yearOverYear.EndExclusive != "2025-07-31T16:00:00Z" {
		t.Fatalf("yearOverYear=%#v error=%v", yearOverYear, err)
	}
}
