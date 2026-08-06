package understanding

import (
	"testing"
	"time"
)

func TestRuleParserResolvesRelativeCalendarRanges(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, question, start, end string
		expression                 TimeExpression
		grain                      TimeGrain
	}{
		{"today", "今天销售额", "2026-08-05", "2026-08-06", TimeExpressionToday, TimeGrainDay},
		{"yesterday", "昨日销售额", "2026-08-04", "2026-08-05", TimeExpressionYesterday, TimeGrainDay},
		{"current natural week", "本自然周销售额", "2026-08-03", "2026-08-10", TimeExpressionCurrentWeek, TimeGrainWeek},
		{"previous natural week", "上周销售额", "2026-07-27", "2026-08-03", TimeExpressionPreviousWeek, TimeGrainWeek},
		{"previous month", "上月销售额", "2026-07-01", "2026-08-01", TimeExpressionPreviousMonth, TimeGrainMonth},
		{"current year", "今年销售额", "2026-01-01", "2027-01-01", TimeExpressionCurrentYear, TimeGrainYear},
		{"previous year", "去年销售额", "2025-01-01", "2026-01-01", TimeExpressionPreviousYear, TimeGrainYear},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			result := parseRulesForTest(t, test.question, referenceForTest(), 0)
			if result.Time == nil {
				t.Fatalf("expected resolved time, got unresolved: %+v", result.UnresolvedSpans)
			}
			if got := *result.Time; got.Start != test.start || got.EndExclusive != test.end || got.Expression != test.expression || got.Grain != test.grain {
				t.Fatalf("unexpected time: %+v", got)
			}
			if result.Time.Timezone != QuestionTimezone || result.Timezone != QuestionTimezone {
				t.Fatalf("timezone is not fixed: %+v", result)
			}
		})
	}
}

func TestRuleParserUsesShanghaiReferenceDate(t *testing.T) {
	t.Parallel()
	// 18:30 UTC is already the next calendar day in Asia/Shanghai.
	reference := time.Date(2026, 8, 5, 18, 30, 0, 0, time.UTC)
	result := parseRulesForTest(t, "今天销售额", reference, 0)
	if result.ReferenceDate != "2026-08-06" || result.Time == nil || result.Time.Start != "2026-08-06" {
		t.Fatalf("reference was not converted to Asia/Shanghai: %+v", result)
	}
}

func TestRuleParserResolvesConfiguredFiscalYear(t *testing.T) {
	t.Parallel()
	tests := []struct{ question, start, end string }{
		{"本财年销售额", "2026-04-01", "2027-04-01"},
		{"上财年销售额", "2025-04-01", "2026-04-01"},
	}
	for _, test := range tests {
		result := parseRulesForTest(t, test.question, referenceForTest(), time.April)
		if result.Time == nil || result.Time.Start != test.start || result.Time.EndExclusive != test.end {
			t.Fatalf("%q: unexpected fiscal range: %+v", test.question, result)
		}
	}
}

func TestRuleParserDoesNotGuessFiscalConfiguration(t *testing.T) {
	t.Parallel()
	result := parseRulesForTest(t, "本财年销售额", referenceForTest(), 0)
	assertUnresolvedReason(t, result, ReasonFiscalStartRequired)
	if result.Time != nil {
		t.Fatalf("fiscal time must remain unresolved: %+v", result.Time)
	}
}

func TestRuleParserResolvesExplicitDatesAndInclusiveRanges(t *testing.T) {
	t.Parallel()
	tests := []struct {
		question, start, end string
		expression           TimeExpression
		grain                TimeGrain
	}{
		{"2026年3月5日销售额", "2026-03-05", "2026-03-06", TimeExpressionExplicitDay, TimeGrainDay},
		{"2026/03销售额", "2026-03-01", "2026-04-01", TimeExpressionExplicitMonth, TimeGrainMonth},
		{"2026年销售额", "2026-01-01", "2027-01-01", TimeExpressionExplicitYear, TimeGrainYear},
		{"2026年1月到2026年3月销售额", "2026-01-01", "2026-04-01", TimeExpressionExplicitRange, TimeGrainMonth},
		{"2026-03-01至2026-03-05销售额", "2026-03-01", "2026-03-06", TimeExpressionExplicitRange, TimeGrainDay},
	}
	for _, test := range tests {
		result := parseRulesForTest(t, test.question, referenceForTest(), 0)
		if result.Time == nil {
			t.Fatalf("%q: expected resolved time, got %+v", test.question, result.UnresolvedSpans)
		}
		if result.Time.Start != test.start || result.Time.EndExclusive != test.end || result.Time.Expression != test.expression || result.Time.Grain != test.grain {
			t.Fatalf("%q: unexpected time: %+v", test.question, result.Time)
		}
	}
}

func TestRuleParserReturnsUnresolvedForAmbiguousOrInvalidTimes(t *testing.T) {
	t.Parallel()
	tests := []struct{ question, reason string }{
		{"3/4/2026销售额", ReasonAmbiguousDate},
		{"3月5日销售额", ReasonAmbiguousDate},
		{"2026-02-30销售额", ReasonInvalidDate},
		{"2026年到2026年3月销售额", ReasonIncompatibleDateRange},
		{"2026年3月到2026年1月销售额", ReasonInvalidDateRange},
		{"财年销售额", ReasonAmbiguousFiscalYear},
		{"自然周销售额", ReasonAmbiguousNaturalWeek},
		{"去年与今年销售额", ReasonMultipleTimes},
	}
	for _, test := range tests {
		result := parseRulesForTest(t, test.question, referenceForTest(), 0)
		if result.Time != nil {
			t.Fatalf("%q: time must not be guessed: %+v", test.question, result.Time)
		}
		assertUnresolvedReason(t, result, test.reason)
	}
}

func TestResolvedTimeMapsBackToExactOriginalRunes(t *testing.T) {
	t.Parallel()
	question := "请问，２０２６年０３月０５日销售额？"
	result := parseRulesForTest(t, question, referenceForTest(), 0)
	if result.Time == nil || result.Time.Text != "２０２６年０３月０５日" {
		t.Fatalf("unexpected original time text: %+v", result.Time)
	}
	assertExactOriginalSpan(t, question, result.Time.Text, result.Time.Span)
}

func parseRulesForTest(t *testing.T, source string, reference time.Time, fiscal time.Month) RuleParseResult {
	t.Helper()
	question, err := NormalizeQuestion(source)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	parser, err := NewRuleParser(reference, fiscal)
	if err != nil {
		t.Fatalf("new parser: %v", err)
	}
	result, err := parser.Parse(question)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := result.Validate(question); err != nil {
		t.Fatalf("validate result: %v", err)
	}
	return result
}

func referenceForTest() time.Time {
	return time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
}

func assertUnresolvedReason(t *testing.T, result RuleParseResult, reason string) {
	t.Helper()
	for _, unresolved := range result.UnresolvedSpans {
		if unresolved.Reason == reason {
			return
		}
	}
	t.Fatalf("missing unresolved reason %q in %+v", reason, result.UnresolvedSpans)
}

func assertExactOriginalSpan(t *testing.T, question, text string, span Span) {
	t.Helper()
	runes := []rune(question)
	if span.Start < 0 || span.End > len(runes) || span.Start >= span.End || string(runes[span.Start:span.End]) != text {
		t.Fatalf("invalid original span %+v for %q in %q", span, text, question)
	}
}
