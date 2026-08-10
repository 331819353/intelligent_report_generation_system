package understanding

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/datarequest"
)

type scopeExample struct {
	question   string
	typeWanted QuestionType
	metrics    []string
	group      string
	value      string
	ordering   string
	comparison string
}

func TestScopeClassifierCoversFifteenTypesWithThreeExamplesEach(t *testing.T) {
	examples := []scopeExample{
		{"本月销售额是多少", QuestionTypeMetricLookup, []string{"销售额"}, "", "", "", ""},
		{"查询订单量", QuestionTypeMetricLookup, []string{"订单量"}, "", "", "", ""},
		{"看看毛利额", QuestionTypeMetricLookup, []string{"毛利额"}, "", "", "", ""},
		{"列出各区域销售额", QuestionTypeGroupedAnalysis, []string{"销售额"}, "区域", "", "", ""},
		{"按渠道看订单量", QuestionTypeGroupedAnalysis, []string{"订单量"}, "渠道", "", "", ""},
		{"各产品类别的毛利额", QuestionTypeGroupedAnalysis, []string{"毛利额"}, "产品类别", "", "", ""},
		{"华东区销售额", QuestionTypeFilteredAnalysis, []string{"销售额"}, "", "华东区", "", ""},
		{"渠道为电商的订单量", QuestionTypeFilteredAnalysis, []string{"订单量"}, "", "电商", "", ""},
		{"产品是冰箱的毛利额", QuestionTypeFilteredAnalysis, []string{"毛利额"}, "", "冰箱", "", ""},
		{"销售额排名前十的区域", QuestionTypeRanking, []string{"销售额"}, "区域", "", "排名", ""},
		{"订单量最高的渠道", QuestionTypeRanking, []string{"订单量"}, "渠道", "", "最高", ""},
		{"毛利额 TOP 5 产品", QuestionTypeRanking, []string{"毛利额"}, "产品", "", "TOP", ""},
		{"销售额同比变化", QuestionTypeComparison, []string{"销售额"}, "", "", "", "同比"},
		{"订单量环比如何", QuestionTypeComparison, []string{"订单量"}, "", "", "", "环比"},
		{"比较本月和上月毛利额", QuestionTypeComparison, []string{"毛利额"}, "", "", "", "比较"},
		{"销售额和毛利额是多少", QuestionTypeMultiMetric, []string{"销售额", "毛利额"}, "", "", "", ""},
		{"订单量与客户数", QuestionTypeMultiMetric, []string{"订单量", "客户数"}, "", "", "", ""},
		{"收入、利润和成本", QuestionTypeMultiMetric, []string{"收入", "利润", "成本"}, "", "", "", ""},
		{"区域销售额占比", QuestionTypeRatioTarget, []string{"销售额"}, "区域", "", "", ""},
		{"目标完成率", QuestionTypeRatioTarget, []string{"完成率"}, "", "", "", ""},
		{"渠道转化率", QuestionTypeRatioTarget, []string{"转化率"}, "渠道", "", "", ""},
		{"销售额怎么算", QuestionTypeDefinition, []string{"销售额"}, "", "", "", ""},
		{"订单量口径是什么", QuestionTypeDefinition, []string{"订单量"}, "", "", "", ""},
		{"毛利率包含哪些", QuestionTypeDefinition, []string{"毛利率"}, "", "", "", ""},
		{"销售情况怎么样", QuestionTypeBundle, []string{"销售"}, "", "", "", ""},
		{"经营概况", QuestionTypeBundle, nil, "", "", "", ""},
		{"订单总体表现", QuestionTypeBundle, []string{"订单"}, "", "", "", ""},
		{"导出全部订单明细", QuestionTypeDetailList, nil, "", "", "", ""},
		{"给我客户名单", QuestionTypeDetailList, nil, "", "", "", ""},
		{"逐笔交易清单", QuestionTypeDetailList, nil, "", "", "", ""},
		{"预测下月销售额", QuestionTypeForecast, []string{"销售额"}, "", "", "", ""},
		{"订单量未来会怎样", QuestionTypeForecast, []string{"订单量"}, "", "", "", ""},
		{"预计明年毛利额", QuestionTypeForecast, []string{"毛利额"}, "", "", "", ""},
		{"帮我算销售额除以订单量", QuestionTypeAdHocFormula, []string{"销售额", "订单量"}, "", "", "", ""},
		{"用自定义公式算效率", QuestionTypeAdHocFormula, []string{"效率"}, "", "", "", ""},
		{"收入减去临时费用怎么算", QuestionTypeAdHocFormula, []string{"收入"}, "", "", "", ""},
		{"为什么销售额下降", QuestionTypeCausal, []string{"销售额"}, "", "", "", ""},
		{"订单量下降的原因是什么", QuestionTypeCausal, []string{"订单量"}, "", "", "", ""},
		{"什么导致毛利率变化", QuestionTypeCausal, []string{"毛利率"}, "", "", "", ""},
		{"跨领域比较销售和库存", QuestionTypeCrossDomain, []string{"销售", "库存"}, "", "", "", ""},
		{"结合销售和供应链看效率", QuestionTypeCrossDomain, []string{"效率"}, "", "", "", ""},
		{"查询当前领域之外的数据", QuestionTypeCrossDomain, nil, "", "", "", ""},
		{"从本地 Excel 查销售额", QuestionTypeUngovernedSource, []string{"销售额"}, "", "", "", ""},
		{"读取 ERP 原表的订单", QuestionTypeUngovernedSource, []string{"订单"}, "", "", "", ""},
		{"用微信群里的数据算毛利", QuestionTypeUngovernedSource, []string{"毛利"}, "", "", "", ""},
	}

	counts := map[QuestionType]int{}
	for _, example := range examples {
		example := example
		t.Run(string(example.typeWanted)+"/"+example.question, func(t *testing.T) {
			understanding := understandingForScopeExample(t, example)
			questionType, verdict := Classify(understanding)
			if questionType != example.typeWanted || verdict.Type != example.typeWanted {
				t.Fatalf("classification = %s / %#v, want %s", questionType, verdict, example.typeWanted)
			}
			if err := verdict.Validate(); err != nil {
				t.Fatalf("verdict validation: %v\n%#v", err, verdict)
			}
		})
		counts[example.typeWanted]++
	}
	if len(counts) != len(AllowedQuestionTypes()) || len(counts) != 15 {
		t.Fatalf("covered types = %d, whitelist = %d", len(counts), len(AllowedQuestionTypes()))
	}
	for _, questionType := range AllowedQuestionTypes() {
		if counts[questionType] < 3 {
			t.Fatalf("%s examples = %d", questionType, counts[questionType])
		}
	}
}

func TestWeakListVerbDoesNotOverrideGovernedGroupedAnalysis(t *testing.T) {
	understanding := understandingForScopeExample(t, scopeExample{
		question: "列出各区域销售额", typeWanted: QuestionTypeGroupedAnalysis,
		metrics: []string{"销售额"}, group: "区域",
	})
	questionType, verdict := Classify(understanding)
	if questionType != QuestionTypeGroupedAnalysis || verdict.Outcome != ScopeOutcomeExecute {
		t.Fatalf("classification = %s / %#v", questionType, verdict)
	}
}

func TestOutOfScopeNextActionAndParsedContextAreRowFree(t *testing.T) {
	_, verdict := Classify(QuestionUnderstanding{SchemaVersion: SchemaVersion, Question: "导出全部订单明细"})
	if verdict.Outcome != ScopeOutcomeOutOfScope || len(verdict.NextActions) != 1 ||
		verdict.NextActions[0].Kind != NextActionDataRequest ||
		verdict.NextActions[0].Payload.Target != NextActionTargetDataRequestDialog {
		t.Fatalf("detail-list verdict = %#v", verdict)
	}
	withContext, err := WithParsedContext(verdict, datarequest.ParsedContext{
		MetricIDs:    []string{"00000000-0000-4000-8000-000000000101"},
		DimensionIDs: []string{"00000000-0000-4000-8000-000000000102"},
		MemberIDs:    []string{"00000000-0000-4000-8000-000000000103"},
		TimeRange: &datarequest.TimeRange{
			Start:        time.Date(2026, 8, 1, 0, 0, 0, 0, time.FixedZone("CST", 8*60*60)),
			EndExclusive: time.Date(2026, 9, 1, 0, 0, 0, 0, time.FixedZone("CST", 8*60*60)),
			Timezone:     "Asia/Shanghai", Grain: "month",
		},
	})
	if err != nil || withContext.ParsedContext == nil || withContext.ParsedContext.TimeRange.Grain != "MONTH" {
		t.Fatalf("attach parsed context = %#v, %v", withContext, err)
	}
	payload, err := json.Marshal(withContext)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"rows", "result", "sql", "answer"} {
		if strings.Contains(strings.ToLower(string(payload)), forbidden) {
			t.Fatalf("scope payload contains forbidden %q: %s", forbidden, payload)
		}
	}
	if _, err := WithParsedContext(verdict, datarequest.ParsedContext{
		MetricIDs: []string{"00000000-0000-4000-8000-000000000101", "00000000-0000-4000-8000-000000000101"},
	}); err == nil {
		t.Fatal("duplicate bound IDs were accepted")
	}
	metricType, supported := Classify(understandingForScopeExample(t, scopeExample{
		question: "销售额是多少", metrics: []string{"销售额"}, typeWanted: QuestionTypeMetricLookup,
	}))
	if metricType != QuestionTypeMetricLookup {
		t.Fatalf("metric type = %s", metricType)
	}
	if _, err := WithParsedContext(supported, datarequest.ParsedContext{}); err == nil {
		t.Fatal("parsed context was accepted for an executable question")
	}
}

type fixedScopeFallback struct {
	value string
	err   error
	input ScopeFallbackInput
}

func (fallback *fixedScopeFallback) ClassifyQuestionType(_ context.Context, input ScopeFallbackInput) (string, error) {
	fallback.input = input
	return fallback.value, fallback.err
}

func TestLLMFallbackRejectsUnknownEnumAndKeepsRuleCandidate(t *testing.T) {
	fallback := &fixedScopeFallback{value: "DELETE_EVERYTHING"}
	classifier, err := NewScopeClassifier(DefaultScopeLexicon(), fallback, false)
	if err != nil {
		t.Fatal(err)
	}
	questionType, verdict := classifier.Classify(context.Background(), QuestionUnderstanding{
		SchemaVersion: SchemaVersion, Question: "帮我看一下这个问题",
	})
	if questionType != QuestionTypeBundle || verdict.ClassificationSource != ClassificationSourceFallbackRejected {
		t.Fatalf("invalid fallback result = %s / %#v", questionType, verdict)
	}
	if len(fallback.input.AllowedTypes) != 15 || fallback.input.LexiconVersion != DefaultScopeLexiconVersion ||
		fallback.input.LexiconHash.Validate() != nil {
		t.Fatalf("fallback whitelist = %#v", fallback.input)
	}

	fallback.value = string(QuestionTypeForecast)
	questionType, verdict = classifier.Classify(context.Background(), QuestionUnderstanding{
		SchemaVersion: SchemaVersion, Question: "帮我看一下这个问题",
	})
	if questionType != QuestionTypeForecast || verdict.Outcome != ScopeOutcomeOutOfScope ||
		verdict.ClassificationSource != ClassificationSourceLLMFallback {
		t.Fatalf("valid fallback result = %s / %#v", questionType, verdict)
	}
}

func TestCorrectOutOfScopeClassificationEntersRefusalNumerator(t *testing.T) {
	_, detail := Classify(QuestionUnderstanding{SchemaVersion: SchemaVersion, Question: "逐笔订单明细"})
	var evaluation ScopeEvaluation
	evaluation.Add(QuestionTypeDetailList, detail)
	if !IsCorrectRefusal(QuestionTypeDetailList, detail) || evaluation.CorrectRefusals != 1 ||
		evaluation.Correct != 1 || evaluation.FalseRefusals != 0 || evaluation.Total != 1 {
		t.Fatalf("correct refusal evaluation = %#v", evaluation)
	}
	evaluation.Add(QuestionTypeMetricLookup, detail)
	if evaluation.FalseRefusals != 1 || evaluation.CorrectRefusals != 1 {
		t.Fatalf("false refusal evaluation = %#v", evaluation)
	}
}

func TestCausalContributionFlagProducesNonCausalExecuteNotice(t *testing.T) {
	classifier, err := NewScopeClassifier(DefaultScopeLexicon(), nil, true)
	if err != nil {
		t.Fatal(err)
	}
	questionType, verdict := classifier.Classify(context.Background(), understandingForScopeExample(t, scopeExample{
		question: "为什么销售额下降", typeWanted: QuestionTypeCausal, metrics: []string{"销售额"},
	}))
	if questionType != QuestionTypeCausal || verdict.Outcome != ScopeOutcomeExecute ||
		verdict.Reason != ScopeReasonCausalContribution || !strings.Contains(verdict.UserMessage, "不证明因果") {
		t.Fatalf("causal contribution verdict = %#v", verdict)
	}
}

func TestScopeLexiconIsValidatedVersionedAndCloned(t *testing.T) {
	lexicon := DefaultScopeLexicon()
	classifier, err := NewScopeClassifier(lexicon, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	lexicon.ForecastTerms[0] = "被调用方篡改"
	if classifier.lexicon.ForecastTerms[0] == "被调用方篡改" || classifier.LexiconVersion() != DefaultScopeLexiconVersion {
		t.Fatalf("classifier lexicon was mutable: %#v", classifier.lexicon)
	}
	if classifier.lexiconHash.Validate() != nil {
		t.Fatalf("classifier lexicon hash = %q", classifier.lexiconHash)
	}
	invalid := DefaultScopeLexicon()
	invalid.Version = ""
	if _, err := NewScopeClassifier(invalid, nil, false); err == nil {
		t.Fatal("unversioned lexicon was accepted")
	}
}

func understandingForScopeExample(t *testing.T, example scopeExample) QuestionUnderstanding {
	t.Helper()
	understanding := QuestionUnderstanding{SchemaVersion: SchemaVersion, Question: example.question}
	for _, metric := range example.metrics {
		understanding.MetricMentions = append(understanding.MetricMentions, MetricMention{
			Text: metric, Span: scopeSpan(t, example.question, metric), AggregationHint: AggregationDefault,
		})
	}
	if example.group != "" {
		understanding.DimensionMentions = append(understanding.DimensionMentions, DimensionMention{
			Text: example.group, Span: scopeSpan(t, example.question, example.group), Role: DimensionRoleGroupBy,
		})
	}
	if example.value != "" {
		understanding.ValueMentions = append(understanding.ValueMentions, ValueMention{
			Text: example.value, Span: scopeSpan(t, example.question, example.value), OperatorHint: ValueOperatorDefault,
		})
	}
	if example.ordering != "" {
		understanding.Ordering = append(understanding.Ordering, OrderingMention{
			Text: example.ordering, Span: scopeSpan(t, example.question, example.ordering),
			TargetText: example.metrics[0], Direction: SortDescending, RankBy: RankByCurrentValue,
		})
	}
	if example.comparison != "" {
		understanding.Comparisons = append(understanding.Comparisons, ComparisonMention{
			Text: example.comparison, Span: scopeSpan(t, example.question, example.comparison), Type: ComparisonPeriodOverPeriod,
		})
	}
	if err := understanding.Validate(); err != nil {
		t.Fatalf("scope fixture invalid: %v\n%#v", err, understanding)
	}
	return understanding
}

func scopeSpan(t *testing.T, question, text string) Span {
	t.Helper()
	questionRunes, textRunes := []rune(question), []rune(text)
	for start := 0; start+len(textRunes) <= len(questionRunes); start++ {
		if string(questionRunes[start:start+len(textRunes)]) == text {
			return Span{Start: start, End: start + len(textRunes)}
		}
	}
	t.Fatalf("%q not found in %q", text, question)
	return Span{}
}
