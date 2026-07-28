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
			MemberFilters: []QueryMemberFilterInput{
				{DimensionCode: "status", MemberValue: "active"},
			},
		})
		if input.Intent != "METRIC" || input.MetricCode != "sales_amount" ||
			input.DimensionCode != "region" || len(input.MemberFilters) != 1 ||
			input.MemberFilters[0].MemberValue != "active" {
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
	t.Run("current member refinement replaces only the same dimension", func(t *testing.T) {
		input := QueryPlanInput{
			Intent: "UNKNOWN",
			MemberFilters: []QueryMemberFilterInput{
				{DimensionCode: "birth_cohort", MemberValue: "90-95"},
				{DimensionCode: "key_talent", MemberValue: "是"},
			},
		}
		inheritQueryContext(&input, QuerySlots{
			Intent: "METRIC", MetricCode: "employee_count",
			MemberFilters: []QueryMemberFilterInput{
				{
					DimensionCode: "birth_cohort",
					MemberValues:  []string{"80-85", "85-90"},
				},
				{DimensionCode: "employee_status", MemberValue: "在岗"},
			},
		})
		if len(input.MemberFilters) != 3 ||
			input.MemberFilters[0].DimensionCode != "employee_status" ||
			input.MemberFilters[1].DimensionCode != "birth_cohort" ||
			input.MemberFilters[1].MemberValue != "90-95" ||
			input.MemberFilters[2].DimensionCode != "key_talent" {
			t.Fatalf("input=%#v", input)
		}
	})
}

func TestSelectTurnMetricCodesSupportsFollowUpAndExplicitAppend(t *testing.T) {
	contexts := []QuerySlots{
		{MetricCode: "sales_amount"},
		{MetricCode: "order_count"},
	}
	inherited := selectTurnMetricCodes(nil, contexts, false)
	if len(inherited) != 2 || inherited[0] != "sales_amount" ||
		inherited[1] != "order_count" {
		t.Fatalf("inherited=%#v", inherited)
	}
	replaced := selectTurnMetricCodes([]string{"active_people"}, contexts, false)
	if len(replaced) != 1 || replaced[0] != "active_people" {
		t.Fatalf("replaced=%#v", replaced)
	}
	appended := selectTurnMetricCodes([]string{"active_people"}, contexts, true)
	if len(appended) != 3 || appended[0] != "active_people" ||
		appended[1] != "sales_amount" || appended[2] != "order_count" {
		t.Fatalf("appended=%#v", appended)
	}
	if !questionAddsMetrics("同时给我订单量") ||
		questionAddsMetrics("改成订单量") {
		t.Fatal("continuation cue classification is incorrect")
	}
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
	largeSet := make([]string, maxSemanticMemberSetSize)
	for index := range largeSet {
		largeSet[index] = "member-" + string(rune('一'+index))
	}
	if _, err := normalizeQueryMemberFilters(
		"", "",
		[]QueryMemberFilterInput{{
			DimensionCode: "tag", MemberValues: largeSet,
		}},
	); err != nil {
		t.Fatalf("governed set at limit was rejected: %v", err)
	}
	largeSet = append(largeSet, "one-too-many")
	if _, err := normalizeQueryMemberFilters(
		"", "",
		[]QueryMemberFilterInput{{
			DimensionCode: "tag", MemberValues: largeSet,
		}},
	); err == nil {
		t.Fatal("governed set above limit was accepted")
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
		"截止到6月所有的骑手数量是多少":  "THROUGH_06",
		"截至2025年12月底的商户数":  "THROUGH_2025_12",
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
	cutoffDateRange, err := resolveQueryTimePreset(
		"THROUGH_06", "Asia/Shanghai", "DATE", now,
	)
	if err != nil ||
		cutoffDateRange.Start != "1970-01-01" ||
		cutoffDateRange.EndExclusive != "2026-07-01" {
		t.Fatalf("cutoff date range=%#v error=%v", cutoffDateRange, err)
	}
	explicitCutoffRange, err := resolveQueryTimePreset(
		"THROUGH_2025_12", "Asia/Shanghai", "DATETIME", now,
	)
	if err != nil ||
		explicitCutoffRange.Start != "1969-12-31T16:00:00Z" ||
		explicitCutoffRange.EndExclusive != "2025-12-31T16:00:00Z" {
		t.Fatalf("explicit cutoff range=%#v error=%v", explicitCutoffRange, err)
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

func TestResolvedLookupAlternativesCompileToSelectedMemberFilters(t *testing.T) {
	filters, complete := memberFiltersFromResolvedLookups(
		[]QueryDimensionValueLookupTrace{
			{
				Term: "国内公办", Source: "CURRENT_TURN", Selected: true,
				DimensionCode:      "full_time_education_institution_type",
				SelectedMemberKeys: []string{"国内公办"},
			},
			{
				Term: "国内公办", Source: "CURRENT_TURN", Selected: false,
				DimensionCode:       "highest_education_institution_type",
				CandidateMemberKeys: []string{"国内公办"},
			},
			{
				Term: "80后", Source: "CURRENT_TURN", Selected: true,
				DimensionCode:      "birth_cohort",
				SelectedMemberKeys: []string{"80-85", "85-90"},
			},
		},
	)
	if !complete || len(filters) != 2 ||
		filters[0].DimensionCode != "full_time_education_institution_type" ||
		len(filters[0].MemberValues) != 1 ||
		filters[1].DimensionCode != "birth_cohort" ||
		len(filters[1].MemberValues) != 2 {
		t.Fatalf("filters=%#v complete=%v", filters, complete)
	}
}

func TestResolvedLookupCompilationBlocksUnresolvedTerm(t *testing.T) {
	_, complete := memberFiltersFromResolvedLookups(
		[]QueryDimensionValueLookupTrace{
			{
				Term: "国内公办", Source: "CURRENT_TURN", Selected: false,
				DimensionCode: "full_time_education_institution_type",
			},
			{
				Term: "国内公办", Source: "CURRENT_TURN", Selected: false,
				DimensionCode: "highest_education_institution_type",
			},
		},
	)
	if complete {
		t.Fatal("unresolved same-value dimension ambiguity was compiled")
	}
}
