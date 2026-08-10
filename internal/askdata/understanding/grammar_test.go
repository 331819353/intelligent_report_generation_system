package understanding

import (
	"testing"
	"time"
)

func TestRuleParserExtractsCombinedGrammarWithOriginalSpans(t *testing.T) {
	t.Parallel()
	question := "请问今年按月销售额同比前１０名，按地区降序。"
	result := parseRulesForTest(t, question, referenceForTest(), 0)
	if result.Time == nil || result.Time.Expression != TimeExpressionCurrentYear {
		t.Fatalf("unexpected time: %+v", result.Time)
	}
	if len(result.Comparisons) != 1 || result.Comparisons[0].Type != ComparisonYearOverYear {
		t.Fatalf("unexpected comparisons: %+v", result.Comparisons)
	}
	if result.Ranking == nil || result.Ranking.Limit != 10 || result.Ranking.Direction != SortDescending || result.Ranking.Text != "前１０名" {
		t.Fatalf("unexpected ranking: %+v", result.Ranking)
	}
	if len(result.Sorts) != 1 || result.Sorts[0].Direction != SortDescending {
		t.Fatalf("unexpected sorts: %+v", result.Sorts)
	}
	if len(result.Groupings) != 2 || result.Groupings[0].Grain == nil || *result.Groupings[0].Grain != TimeGrainMonth || result.Groupings[1].Grain != nil {
		t.Fatalf("unexpected groupings: %+v", result.Groupings)
	}
	if len(result.UnresolvedSpans) != 0 {
		t.Fatalf("unexpected unresolved grammar: %+v", result.UnresolvedSpans)
	}
	assertAllResultSpans(t, question, result)
}

func TestRuleParserSupportsComparisonRankingSortingAndGroupingVariants(t *testing.T) {
	t.Parallel()
	tests := []struct {
		question   string
		comparison ComparisonType
		limit      int
		direction  SortDirection
		marker     GroupingMarker
	}{
		{"上月每个地区销售额环比bottom 5升序", ComparisonMonthOverMonth, 5, SortAscending, GroupingEach},
		{"今年按照渠道销售额较上期top10降序", ComparisonPeriodOverPeriod, 10, SortDescending, GroupingBy},
	}
	for _, test := range tests {
		result := parseRulesForTest(t, test.question, referenceForTest(), 0)
		if len(result.Comparisons) != 1 || result.Comparisons[0].Type != test.comparison {
			t.Fatalf("%q: unexpected comparison: %+v", test.question, result.Comparisons)
		}
		if result.Ranking == nil || result.Ranking.Limit != test.limit || result.Ranking.Direction != test.direction {
			t.Fatalf("%q: unexpected ranking: %+v", test.question, result.Ranking)
		}
		if len(result.Sorts) != 1 || result.Sorts[0].Direction != test.direction {
			t.Fatalf("%q: unexpected sorts: %+v", test.question, result.Sorts)
		}
		if len(result.Groupings) != 1 || result.Groupings[0].Marker != test.marker {
			t.Fatalf("%q: unexpected groupings: %+v", test.question, result.Groupings)
		}
	}
}

func TestRuleParserCapturesExplicitTopNRankBasisWithoutInventingGrouping(t *testing.T) {
	t.Parallel()
	tests := []struct {
		question string
		rankBy   RankBy
	}{
		{"销售额同比按当期值前10", RankByCurrentValue},
		{"销售额同比按增长额前10", RankByDelta},
		{"销售额同比按同比增长率前10", RankByRatio},
	}
	for _, test := range tests {
		result := parseRulesForTest(t, test.question, referenceForTest(), 0)
		if result.Ranking == nil || result.Ranking.RankBy != test.rankBy || len(result.Groupings) != 0 {
			t.Fatalf("%q: ranking=%#v groupings=%#v", test.question, result.Ranking, result.Groupings)
		}
	}
}

func TestRuleParserSurfacesGrammarConflictsInsteadOfChoosing(t *testing.T) {
	t.Parallel()
	tests := []struct{ question, reason string }{
		{"销售额同比环比", ReasonMultipleComparisons},
		{"销售额前10后5", ReasonMultipleRankings},
		{"销售额top 10升序", ReasonConflictingOrder},
		{"销售额升序再降序", ReasonConflictingOrder},
		{"销售额前10001", ReasonLimitOutOfRange},
		{"销售额top 10%", ReasonUnsupportedRankRatio},
	}
	for _, test := range tests {
		result := parseRulesForTest(t, test.question, referenceForTest(), 0)
		assertUnresolvedReason(t, result, test.reason)
	}
}

func TestRuleParserAvoidsFalseGroupingMarkers(t *testing.T) {
	t.Parallel()
	for _, question := range []string{"按揭贷款余额", "按摩业务收入", "按钮点击次数", "按键次数", "按压次数"} {
		result := parseRulesForTest(t, question, referenceForTest(), 0)
		if len(result.Groupings) != 0 {
			t.Fatalf("%q produced false grouping: %+v", question, result.Groupings)
		}
	}
}

func TestRuleParserRequiresEnglishRankingWordBoundary(t *testing.T) {
	t.Parallel()
	for _, question := range []string{"desktop10收入", "stopping5次数", "top10x收入"} {
		result := parseRulesForTest(t, question, referenceForTest(), 0)
		if result.Ranking != nil {
			t.Fatalf("%q produced false ranking: %+v", question, result.Ranking)
		}
	}
}

func TestRuleParseResultRejectsTampering(t *testing.T) {
	t.Parallel()
	source := "今年按月销售额同比前10降序"
	question, err := NormalizeQuestion(source)
	if err != nil {
		t.Fatal(err)
	}
	parser, err := NewRuleParser(referenceForTest(), time.April)
	if err != nil {
		t.Fatal(err)
	}
	result, err := parser.Parse(question)
	if err != nil {
		t.Fatal(err)
	}
	result.Ranking.Limit = 9
	if err := result.Validate(question); err == nil {
		t.Fatal("expected replay validation to reject modified ranking")
	}
}

func FuzzRuleParserPreservesOriginalSpans(f *testing.F) {
	for _, seed := range []string{
		"请问今年按月销售额同比前１０降序",
		"上财年每个地区环比bottom 5",
		"3/4/2026销售额",
		"按揭贷款余额",
		"2026年1月到2026年3月销售额",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, source string) {
		question, err := NormalizeQuestion(source)
		if err != nil {
			return
		}
		parser, err := NewRuleParser(referenceForTest(), time.April)
		if err != nil {
			t.Fatal(err)
		}
		result, err := parser.Parse(question)
		if err != nil {
			return
		}
		assertAllResultSpans(t, source, result)
	})
}

func assertAllResultSpans(t *testing.T, question string, result RuleParseResult) {
	t.Helper()
	if result.Time != nil {
		assertExactOriginalSpan(t, question, result.Time.Text, result.Time.Span)
	}
	for _, value := range result.Comparisons {
		assertExactOriginalSpan(t, question, value.Text, value.Span)
	}
	if result.Ranking != nil {
		assertExactOriginalSpan(t, question, result.Ranking.Text, result.Ranking.Span)
	}
	for _, value := range result.Sorts {
		assertExactOriginalSpan(t, question, value.Text, value.Span)
	}
	for _, value := range result.Groupings {
		assertExactOriginalSpan(t, question, value.Text, value.Span)
	}
	for _, value := range result.UnresolvedSpans {
		assertExactOriginalSpan(t, question, value.Text, value.Span)
	}
}
