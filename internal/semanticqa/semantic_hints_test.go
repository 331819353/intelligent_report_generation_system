package semanticqa

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	aiplatform "intelligent-report-generation-system/internal/ai"
)

func TestQuerySemanticHintsAreBoundedAndContainNoPredicates(t *testing.T) {
	valid := QuerySemanticHints{
		Intent:      "METRIC",
		MetricNames: []string{"总订单数"},
		DimensionValues: []QuerySemanticDimensionHint{
			{
				SourceToken: "华东", Value: "华东",
				DimensionName: "配送区域名称",
				DimensionCode: "zone_name",
				DimensionType: "GEOGRAPHY",
			},
		},
	}
	if !validQuerySemanticHints(valid) {
		t.Fatal("bounded names and values must be accepted")
	}
	valid.DimensionValues[0].Value = "华东\nwhere 1=1"
	if validQuerySemanticHints(valid) {
		t.Fatal("control characters must be rejected")
	}
	valid.DimensionValues[0].Value = "华东"
	valid.Intent = "DROP"
	if validQuerySemanticHints(valid) {
		t.Fatal("unknown intents must be rejected")
	}
}

func TestContainsControlRejectsFeedbackControlCharacters(t *testing.T) {
	if !containsControl("bad\tfeedback") || !containsControl("bad\nfeedback") {
		t.Fatal("all control characters must be rejected")
	}
	if containsControl("指标结果不准确") {
		t.Fatal("ordinary feedback text must remain valid")
	}
}

func TestQueryWithSemanticHintsCarriesOnlyIntentNamesAndValues(t *testing.T) {
	result := queryWithSemanticHints(
		"华东区域订单量",
		QuerySemanticHints{
			Intent:      "METRIC",
			MetricNames: []string{"总订单数"},
			DimensionValues: []QuerySemanticDimensionHint{
				{
					Value: "华东", DimensionName: "配送区域名称",
					DimensionCode: "zone_name",
				},
			},
		},
	)
	for _, expected := range []string{
		"华东区域订单量", "意图：METRIC", "指标：总订单数",
		"维度：配送区域名称=华东",
	} {
		if !strings.Contains(result, expected) {
			t.Fatalf("expected %q in %q", expected, result)
		}
	}
}

func TestSemanticHintsFromTokenizationUsesOnlyValidatedCompletion(t *testing.T) {
	tokenization := QueryTokenization{
		LLMCompletion: QueryTokenLLMCompletion{
			Status:      "SUCCEEDED",
			Intent:      "METRIC",
			MetricNames: []string{"总订单数"},
			DimensionValues: []QueryLLMDimensionValue{
				{
					SourceToken: "华东", Value: "华东",
					DimensionName: "配送区域名称",
					DimensionCode: "zone_name",
					DimensionType: "GEOGRAPHY",
				},
			},
		},
	}
	hints, ready := semanticHintsFromTokenization(tokenization)
	if !ready || hints.Intent != "METRIC" ||
		len(hints.MetricNames) != 1 ||
		len(hints.DimensionValues) != 1 ||
		hints.DimensionValues[0].DimensionCode != "zone_name" {
		t.Fatalf("expected validated semantic hints, got %#v", hints)
	}
	tokenization.LLMCompletion.Status = "FAILED_VALIDATION"
	if _, ready := semanticHintsFromTokenization(tokenization); ready {
		t.Fatal("failed token completion must not influence query planning")
	}
}

func TestMemberFiltersAcceptServerValidatedLLMSemanticHints(t *testing.T) {
	filters, complete := memberFiltersFromResolvedLookups(
		[]QueryDimensionValueLookupTrace{
			{
				Term:               "Beijing East Zone 08",
				DimensionCode:      "zone_name",
				SelectedMemberKeys: []string{"Beijing East Zone 08"},
				Selected:           true,
				Source:             "LLM_INTENT_COMPLETION",
			},
		},
	)
	if !complete || len(filters) != 1 ||
		filters[0].DimensionCode != "zone_name" ||
		len(filters[0].MemberValues) != 1 {
		t.Fatalf("expected one validated member filter, got %#v", filters)
	}
}

func TestReconcilePlanningTraceKeepsCanonicalMemberCasing(t *testing.T) {
	plan := QueryPlan{
		Conditions: QueryConditionDocument{
			MetricCode: "order_count",
			Dimensions: []QueryDimensionClause{
				{
					DimensionCode: "zone_name",
					MemberKey:     "Beijing East Zone 08",
				},
			},
		},
		PlanningTrace: []QueryDimensionValueLookupTrace{
			{
				Term:                "beijing east zone 08",
				DimensionCode:       "zone_name",
				DimensionFieldName:  "zone_name",
				CandidateMemberKeys: []string{"beijing east zone 08"},
			},
		},
	}
	reconcilePlanningTrace(&plan)
	trace := plan.PlanningTrace[0]
	if !trace.Selected || len(trace.SelectedMemberKeys) != 1 ||
		trace.SelectedMemberKeys[0] != "Beijing East Zone 08" {
		t.Fatalf("expected canonical plan member casing, got %#v", trace)
	}
}

func TestHighConfidenceHintDecisionFallbackRequiresUniqueVectorWinner(
	t *testing.T,
) {
	lookup := QueryDimensionValueLookupTrace{Term: "北京"}
	candidates := []dimensionDecisionCandidate{
		{
			DecisionID: "beijing", CanonicalValue: "Beijing",
			MemberValue: "beijing", SelectedMemberCount: 1, Score: 0.9785,
		},
		{
			DecisionID: "shanghai", CanonicalValue: "Shanghai",
			MemberValue: "shanghai", SelectedMemberCount: 1, Score: 0.8836,
		},
	}
	index, ok := highConfidenceHintDecisionFallback(lookup, candidates)
	if !ok || index != 0 {
		t.Fatalf("expected unique high-confidence winner, index=%d ok=%v",
			index, ok)
	}
	candidates[1].Score = 0.94
	if _, ok := highConfidenceHintDecisionFallback(
		lookup, candidates,
	); ok {
		t.Fatal("an ambiguous vector result must still fail closed")
	}
	candidates[0].Score = 0.989
	candidates[1].Score = 0.972
	if index, ok := highConfidenceHintDecisionFallback(
		lookup, candidates,
	); !ok || index != 0 {
		t.Fatalf(
			"expected near-exact cross-language winner, index=%d ok=%v",
			index, ok,
		)
	}
	candidates[0].Score = 0.984
	if _, ok := highConfidenceHintDecisionFallback(
		lookup, candidates,
	); ok {
		t.Fatal("sub-threshold close vector result must fail closed")
	}
}

func TestSelectExactDimensionDecisionRequiresOneGovernedMember(t *testing.T) {
	candidates := []dimensionDecisionCandidate{
		{
			DecisionID: "profile-beijing", CanonicalValue: "Beijing",
			MemberValue: "Beijing", SelectedMemberCount: 1,
			WhereCondition: "城市 = Beijing", CompiledCondition: "zone_city = $1",
		},
		{
			DecisionID: "observed-beijing", CanonicalValue: "Beijing",
			MemberValue: "Beijing", SelectedMemberCount: 1,
			WhereCondition: "城市 = Beijing", CompiledCondition: "zone_city = $1",
		},
	}
	selected, ok := selectExactDimensionDecision(candidates)
	if !ok || selected.DecisionID != "profile-beijing" {
		t.Fatalf("equivalent exact decisions should be reusable: %#v", selected)
	}
	candidates = append(candidates, dimensionDecisionCandidate{
		DecisionID: "merchant-beijing", CanonicalValue: "Beijing",
		MemberValue: "Beijing", SelectedMemberCount: 1,
		WhereCondition:    "商户城市 = Beijing",
		CompiledCondition: "merchant_city = $1",
	})
	if _, ok := selectExactDimensionDecision(candidates); ok {
		t.Fatal("one term resolving to different predicates must remain ambiguous")
	}
	candidates = []dimensionDecisionCandidate{{
		DecisionID: "unsafe", MemberValue: "Beijing",
		SelectedMemberCount: 2, CompiledCondition: "zone_city = $1",
	}}
	if _, ok := selectExactDimensionDecision(candidates); ok {
		t.Fatal("a multi-member or incomplete decision must fail closed")
	}
}

func TestActionableSemanticDimensionHintExcludesTimeOnlyHints(t *testing.T) {
	if hasActionableSemanticDimensionHint([]QuerySemanticDimensionHint{
		{Value: "当前", DimensionType: "TIME"},
	}) {
		t.Fatal("time-only hints are compiled separately from member decisions")
	}
	if !hasActionableSemanticDimensionHint([]QuerySemanticDimensionHint{
		{Value: "北京", DimensionType: "STANDARD"},
	}) {
		t.Fatal("a non-time dimension value must fail closed until resolved")
	}
}

func TestDimensionClarificationChoicesExcludeLowRelevanceCandidates(
	t *testing.T,
) {
	lookup := QueryDimensionValueLookupTrace{
		Term: "月球", DimensionCode: "city", DimensionName: "城市",
		VectorTopScore:    0.908,
		WhereDesignStatus: "NO_SAFE_DECISION_SELECTED",
		DecisionCandidates: []QueryDecisionCandidate{
			{
				DecisionID: "beijing", CanonicalValue: "Beijing",
				MemberValue: "Beijing", Score: 0.908,
			},
		},
	}
	if choices := dimensionClarificationChoices(
		[]QueryDimensionValueLookupTrace{lookup}, "complaint_count",
	); len(choices) != 0 {
		t.Fatalf("low-relevance choices must be suppressed: %#v", choices)
	}
	lookup.VectorTopScore = 0.952
	lookup.DecisionCandidates[0].Score = 0.952
	if choices := dimensionClarificationChoices(
		[]QueryDimensionValueLookupTrace{lookup}, "complaint_count",
	); len(choices) != 1 || choices[0].Term != "月球" {
		t.Fatalf("close ambiguous choices should remain confirmable: %#v",
			choices)
	}
}

func TestConfirmedDecisionsRejectTwoDimensionsForOneTerm(t *testing.T) {
	lookups := []QueryDimensionValueLookupTrace{
		{
			Term: "北京", DimensionCode: "city",
			DecisionCandidates: []QueryDecisionCandidate{{
				DecisionID: "decision-city", MemberValue: "beijing",
			}},
		},
		{
			Term: "北京", DimensionCode: "region",
			DecisionCandidates: []QueryDecisionCandidate{{
				DecisionID: "decision-region", MemberValue: "north",
			}},
		},
	}
	if _, valid := applyConfirmedDecisions(
		lookups,
		[]QueryConfirmedDecision{
			{MetricCode: "complaint_count", DecisionID: "decision-city"},
			{MetricCode: "complaint_count", DecisionID: "decision-region"},
		},
		"complaint_count",
	); valid {
		t.Fatal("user confirmation must preserve one decision per dimension term")
	}
}

func TestDeterministicTokenCompletionHandlesExplicitMetricAndCity(t *testing.T) {
	completion, ok := deterministicTokenSemanticCompletion(
		"帮我查询北京市的投诉总量是什么？",
		"Asia/Shanghai",
		time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC),
		[]QueryToken{
			{Text: "帮我", EntityType: "TEXT"},
			{Text: "查询", EntityType: "QUERY_WORD"},
			{Text: "北京市", EntityType: "LOCATION"},
			{
				Text: "投诉", EntityType: "METRIC",
				EntityName: "投诉数量", EntityCode: "complaint_count",
				Confidence: 0.98,
			},
			{Text: "总量", EntityType: "NOUN_CANDIDATE"},
			{Text: "是什么", EntityType: "TEXT"},
		},
		false, testSemanticParsingRules(),
	)
	if !ok || completion.Status != "SUCCEEDED" ||
		completion.Model != "DETERMINISTIC_SEMANTIC_CATALOG" ||
		len(completion.MetricNames) != 1 ||
		completion.MetricNames[0] != "投诉数量" {
		t.Fatalf("unexpected deterministic completion: %#v", completion)
	}
}

func TestDeterministicTokenCompletionDefersTimeAndUnknownDimensions(
	t *testing.T,
) {
	for _, tokens := range [][]QueryToken{
		{
			{
				Text: "投诉", EntityType: "METRIC",
				EntityName: "投诉数量", EntityCode: "complaint_count",
			},
			{Text: "今年", EntityType: "TIME"},
		},
		{
			{
				Text: "投诉", EntityType: "METRIC",
				EntityName: "投诉数量", EntityCode: "complaint_count",
			},
			{Text: "华东", EntityType: "LOCATION"},
		},
	} {
		if _, ok := deterministicTokenSemanticCompletion(
			"测试问题", "Asia/Shanghai", time.Now(), tokens, false,
			testSemanticParsingRules(),
		); ok {
			t.Fatalf("ambiguous tokens must still use bounded completion: %#v",
				tokens)
		}
	}
}

func TestDeterministicTokenCompletionUsesContextForAdministrativeFollowUp(
	t *testing.T,
) {
	tokens := []QueryToken{
		{Text: "上海市", EntityType: "LOCATION"},
		{Text: "呢", EntityType: "TEXT"},
		{Text: "？", EntityType: "PUNCTUATION"},
	}
	if _, ok := deterministicTokenSemanticCompletion(
		"上海市呢？", "Asia/Shanghai", time.Now(), tokens, false,
		testSemanticParsingRules(),
	); ok {
		t.Fatal("a metricless turn without a plan context must not fast-path")
	}
	completion, ok := deterministicTokenSemanticCompletion(
		"上海市呢？", "Asia/Shanghai", time.Now(), tokens, true,
		testSemanticParsingRules(),
	)
	if !ok || len(completion.MetricNames) != 0 ||
		completion.Model != "DETERMINISTIC_SEMANTIC_CATALOG" {
		t.Fatalf("contextual administrative follow-up = %#v", completion)
	}
}

func TestDeterministicTokenCompletionSkipsModelForBroadMetricQuestion(
	t *testing.T,
) {
	rules := testSemanticParsingRules()
	tokenization := tokenizeQueryWithRules(
		"北京市经营情况怎么样？", nil, rules,
	)
	completion, ok := deterministicTokenSemanticCompletion(
		"北京市经营情况怎么样？", "Asia/Shanghai", time.Now(),
		tokenization.Tokens, false, rules,
	)
	if !ok || completion.Model != "DETERMINISTIC_SEMANTIC_CATALOG" {
		t.Fatalf("confirmed metric anchor tokens = %#v", tokenization.Tokens)
	}
}

func TestSemanticToolLoopFallbackOnlyHandlesModelFailures(t *testing.T) {
	if !shouldRetrySemanticToolLoop(ErrUnprovenPath) {
		t.Fatal("an invalid model selection should retry on the next model")
	}
	if !shouldRetrySemanticToolLoop(
		aiplatform.NormalizeProviderError(context.DeadlineExceeded),
	) {
		t.Fatal("a provider timeout should retry on the next model")
	}
	if shouldRetrySemanticToolLoop(ErrInvalidRequest) ||
		shouldRetrySemanticToolLoop(errors.New("database unavailable")) {
		t.Fatal("request and tool execution errors must not switch providers")
	}
	if !semanticDeterministicFallbackAllowed(
		aiplatform.NormalizeProviderError(context.DeadlineExceeded),
	) {
		t.Fatal("provider timeout should permit deterministic catalog fallback")
	}
	if semanticDeterministicFallbackAllowed(&aiplatform.ProviderError{
		Code: aiplatform.ErrorCodeToolNoProgress,
	}) || semanticDeterministicFallbackAllowed(ErrUnprovenPath) {
		t.Fatal("protocol and evidence failures must not be silently downgraded")
	}
}

func TestBroadMetricQuestionAlwaysRequiresUserSelection(t *testing.T) {
	rules := testSemanticParsingRules()
	for _, question := range []string{
		"北京市经营情况怎么样？",
		"看下整体情况",
		"最近业务如何",
	} {
		if !rules.requestsBroadMetricSelection(question) {
			t.Fatalf("expected broad metric question: %q", question)
		}
	}
	if rules.requestsBroadMetricSelection("北京市投诉总量是多少？") {
		t.Fatal("an explicit metric question is not broad")
	}
}

func TestTypedTimeHintCarriesExplicitRangeIntoPlanning(t *testing.T) {
	hints := QuerySemanticHints{
		Intent:      "METRIC",
		MetricNames: []string{"投诉数量"},
		DimensionValues: []QuerySemanticDimensionHint{
			{
				SourceToken: "月底", Value: "2026-07-31",
				DimensionName: "统计日期",
				DimensionCode: "stat_date",
				DimensionType: "TIME", ValueType: "DATE",
				TimeRange: &QueryTimeRange{
					Start: "1970-01-01", EndExclusive: "2026-08-01",
				},
			},
		},
	}
	if !validQuerySemanticHints(hints) {
		t.Fatal("typed LLM time normalization must be accepted")
	}
	timeRange, found := semanticHintTimeRange(hints.DimensionValues)
	if !found || timeRange.Start != "1970-01-01" ||
		timeRange.EndExclusive != "2026-08-01" {
		t.Fatalf("expected explicit cutoff range, got %#v", timeRange)
	}
	hints.DimensionValues[0].TimeRange = nil
	if validQuerySemanticHints(hints) {
		t.Fatal("a typed relative time value must not reach planning unnormalized")
	}
}

func TestQueryTurnTraceKeepsDecisionRecallWhenWhereFilterBlocks(t *testing.T) {
	pendingLookup := QueryDimensionValueLookupTrace{
		Term:                 "业务值",
		MetricCode:           "order_count",
		DimensionCode:        "business_region",
		DimensionName:        "业务区域",
		Source:               "LLM_INTENT_COMPLETION",
		VectorSearchStatus:   "SUCCEEDED",
		VectorCandidateCount: 3,
		DecisionCandidates: []QueryDecisionCandidate{
			{DecisionID: "decision-a", CanonicalValue: "A"},
			{DecisionID: "decision-b", CanonicalValue: "B"},
			{DecisionID: "decision-c", CanonicalValue: "C"},
		},
		WhereDesignStatus: "NO_SAFE_DECISION_SELECTED",
		Selected:          false,
	}
	turn := QueryTurnPlan{
		Intent:      "METRIC",
		MetricCodes: []string{"order_count"},
		Plans:       []QueryPlan{},
		Trace: QueryTurnTrace{
			DimensionValueLookups: []QueryDimensionValueLookupTrace{
				pendingLookup,
			},
		},
	}
	trace := buildQueryTurnTrace(
		[]string{"查询业务值的订单量"},
		"CURRENT_TURN_OVERRIDES_SAME_DIMENSION_THEN_LATEST_VERIFIED_PLAN",
		QueryTurnSlots{
			Intent:      "METRIC",
			MetricCodes: []string{"order_count"},
			MetricCandidates: []QueryMetricCandidateTrace{
				{
					Code: "order_count", Label: "订单量",
					MatchMethod: "CATALOG_RERANK",
				},
			},
		},
		nil,
		turn,
	)
	if len(trace.DimensionValueLookups) != 1 ||
		len(trace.DimensionValueLookups[0].DecisionCandidates) != 3 {
		t.Fatalf("decision recall must remain visible: %#v",
			trace.DimensionValueLookups)
	}
	if len(trace.Extraction.DimensionValueTerms) != 1 ||
		trace.Extraction.DimensionValueTerms[0] != "业务值" {
		t.Fatalf("unselected extracted value must remain visible: %#v",
			trace.Extraction.DimensionValueTerms)
	}
	statuses := map[string]string{}
	for _, assessment := range trace.Assessments {
		statuses[assessment.Step] = assessment.Status
	}
	if statuses["DIMENSION_VALUE_RETRIEVAL"] != "PASS" {
		t.Fatalf("candidate recall must pass independently: %#v", statuses)
	}
	if statuses["WHERE_FILTER"] != "BLOCKED" ||
		statuses["FINAL_PLAN"] != "BLOCKED" {
		t.Fatalf("filter and execution plan must remain blocked: %#v",
			statuses)
	}
}

func TestConfirmedDecisionUsesOnlyRecalledGovernedCandidate(t *testing.T) {
	decisionID := "10000000-0000-4000-8000-000000000001"
	lookups := []QueryDimensionValueLookupTrace{{
		Term: "华东", MetricCode: "sales_amount",
		DimensionCode: "region", DimensionName: "区域",
		DecisionCandidates: []QueryDecisionCandidate{{
			DecisionID: decisionID, CanonicalValue: "华东",
			MemberValue: "EAST", MetricCode: "sales_amount",
			TableSchema: "warehouse_published", TableName: "dws_sales",
			WhereCondition:    "region_code = 'EAST'",
			CompiledCondition: "field_region IN (:region_1)",
			PredicateOperator: "EQUALS",
		}},
	}}
	applied, valid := applyConfirmedDecisions(
		lookups,
		[]QueryConfirmedDecision{{
			MetricCode: "sales_amount", DecisionID: decisionID,
		}},
		"sales_amount",
	)
	if !valid || !applied[0].Selected ||
		len(applied[0].SelectedMemberKeys) != 1 ||
		applied[0].SelectedMemberKeys[0] != "EAST" ||
		applied[0].WhereDesignStatus !=
			"USER_CONFIRMED_DECISION_GRAPH" {
		t.Fatalf("confirmed lookup = %#v", applied[0])
	}
	untrustedInput := append(
		[]QueryDimensionValueLookupTrace(nil), lookups...,
	)
	untrustedInput[0].Selected = false
	untrustedInput[0].DecisionID = ""
	untrustedInput[0].SelectedMemberKeys = nil
	untrustedInput[0].DecisionCandidates = append(
		[]QueryDecisionCandidate(nil),
		lookups[0].DecisionCandidates...,
	)
	untrustedInput[0].DecisionCandidates[0].Selected = false
	if _, valid := applyConfirmedDecisions(
		untrustedInput,
		[]QueryConfirmedDecision{{
			MetricCode: "sales_amount",
			DecisionID: "20000000-0000-4000-8000-000000000002",
		}},
		"sales_amount",
	); valid {
		t.Fatal("unknown decision id must be rejected")
	}
}

func TestSelectMetricDimensionHintLookupUsesUniqueCompatibleAlias(t *testing.T) {
	selected, ok := selectMetricDimensionHintLookup(
		[]rankedMetricDimensionHintLookup{{
			value: QueryDimensionValueLookupTrace{
				DimensionCode: "zone_city",
			},
			rank: 2,
		}},
	)
	if !ok || selected.DimensionCode != "zone_city" {
		t.Fatalf("selected=%#v ok=%v", selected, ok)
	}

	if _, accepted := selectMetricDimensionHintLookup(
		[]rankedMetricDimensionHintLookup{
			{
				value: QueryDimensionValueLookupTrace{
					DimensionCode: "billing_city",
				},
				rank: 2,
			},
			{
				value: QueryDimensionValueLookupTrace{
					DimensionCode: "zone_city",
				},
				rank: 2,
			},
		},
	); accepted {
		t.Fatal("same-rank relaxed dimension aliases must remain ambiguous")
	}
}

func TestFinalizeQueryTurnStatusRejectsEmptyPlan(t *testing.T) {
	result := QueryTurnPlan{Status: "PLANNING", Plans: []QueryPlan{}}
	finalizeQueryTurnStatus(&result)
	if result.Status != "SEMANTIC_GAP" || result.Clarification == nil ||
		result.Clarification.Type != "SEMANTIC_GAP" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestFinalizeQueryTurnStatusRejectsNonReadyLeafPlan(t *testing.T) {
	result := QueryTurnPlan{Status: "PLANNING", Plans: []QueryPlan{{
		Status: "REJECTED", FailureCode: "SOURCE_LINEAGE_NOT_PROVEN",
	}}}
	finalizeQueryTurnStatus(&result)
	if result.Status != "SEMANTIC_GAP" || result.Clarification == nil ||
		!strings.Contains(
			result.Clarification.Message, "SOURCE_LINEAGE_NOT_PROVEN",
		) {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestAdministrativeLocationHintSurvivesLLMValidationFailure(t *testing.T) {
	rules := testSemanticParsingRules()
	hints := supplementAdministrativeLocationHints(
		tokenizeQueryWithRules("月球市", nil, rules),
		QuerySemanticHints{},
	)
	if len(hints.DimensionValues) != 1 {
		t.Fatalf("dimension hints = %#v", hints.DimensionValues)
	}
	hint := hints.DimensionValues[0]
	if hint.SourceToken != "月球市" || hint.Value != "月球" ||
		hint.DimensionCode != "city" || hint.DimensionName != "城市" {
		t.Fatalf("unexpected location hint: %#v", hint)
	}

	hints = supplementAdministrativeLocationHints(
		tokenizeQueryWithRules("北京市", nil, rules),
		QuerySemanticHints{DimensionValues: []QuerySemanticDimensionHint{{
			SourceToken: "北京", Value: "北京",
			DimensionCode: "city", DimensionName: "城市",
		}}},
	)
	if len(hints.DimensionValues) != 1 {
		t.Fatalf("existing governed location was duplicated: %#v", hints)
	}
}

func TestExactMetricCodesAcceptDistinctiveMeasureStem(t *testing.T) {
	candidates := []recallCandidate{
		{
			SubjectType: "METRIC", Code: "complaint_count",
			Label: "投诉数量",
		},
		{
			SubjectType: "METRIC", Code: "order_count",
			Label: "总订单数",
		},
	}
	codes := exactMetricCodes(
		"投诉总量是什么？", candidates, 8, testSemanticParsingRules(),
	)
	if len(codes) != 1 || codes[0] != "complaint_count" {
		t.Fatalf("metric codes = %#v", codes)
	}
	if codes := exactMetricCodes(
		"经营情况怎么样？", candidates, 8, testSemanticParsingRules(),
	); len(codes) != 0 {
		t.Fatalf("broad wording selected arbitrary metrics: %#v", codes)
	}
}

func TestResolveSupersededMetricTurnRequiresUniqueEquivalentSuccessor(
	t *testing.T,
) {
	items := []supersededMetricCandidate{{
		Candidate: recallCandidate{
			SubjectType: "METRIC", Code: "current_discount",
			Label: "订单商品折扣金额合计", Domain: "orders",
			DatasetVersionID: "current-version", Score: 1,
		},
		SupersededCode: "legacy_discount", MatchedTerm: "商品折扣",
		ReplacementCount: 1,
	}}
	turn, ok := resolveSupersededMetricTurn("商品折扣是多少？", items)
	if !ok || len(turn.MetricCodes) != 1 ||
		turn.MetricCodes[0] != "current_discount" ||
		turn.MetricMatchMethod != "SUPERSEDED_CATALOG_ALIAS" ||
		!turn.GovernedMetricOnly ||
		len(turn.MetricCandidates) != 1 ||
		turn.MetricCandidates[0].MatchedTerm != "商品折扣" {
		t.Fatalf("unexpected inherited metric turn: %#v", turn)
	}
	items[0].ReplacementCount = 2
	if _, ok := resolveSupersededMetricTurn("商品折扣是多少？", items); ok {
		t.Fatal("ambiguous successors must fall back to clarification")
	}
	items[0].ReplacementCount = 1
	turn, ok = resolveSupersededMetricTurn("Beijing的商品折扣是多少？", items)
	if !ok || turn.GovernedMetricOnly {
		t.Fatal("unresolved dimension text must not enter the metric-only fast path")
	}
}
