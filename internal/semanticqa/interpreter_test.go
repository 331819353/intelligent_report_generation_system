package semanticqa

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	aiplatform "intelligent-report-generation-system/internal/ai"
)

type whereDesignAIStub struct {
	invocation aiplatform.Invocation
	content    json.RawMessage
	err        error
}

func (*whereDesignAIStub) Configured() bool { return true }

func (stub *whereDesignAIStub) Invoke(
	_ context.Context,
	invocation aiplatform.Invocation,
) (aiplatform.InvocationResult, error) {
	stub.invocation = invocation
	if stub.err != nil {
		return aiplatform.InvocationResult{}, stub.err
	}
	return aiplatform.InvocationResult{
		ProviderResult: aiplatform.ProviderResult{
			Content: stub.content,
			Model:   "where-design-test-model",
		},
	}, nil
}

func TestWhereDesignerReportsQuotaFailure(t *testing.T) {
	stub := &whereDesignAIStub{err: aiplatform.ErrQuotaExceeded}
	interpreter := &SemanticInterpreter{ai: stub}
	lookups := interpreter.designWherePredicates(
		context.Background(), "tenant-1", "actor-1", "employee_count",
		"关键人才有多少？",
		[]QueryDimensionValueLookupTrace{{
			Term:                      "关键人才",
			DimensionFieldName:        "key_talent",
			DimensionFieldDescription: "标识员工是否被评定为关键人才",
			MatchMethod:               "SEMANTIC_TAG",
			SelectedMemberKeys:        []string{"关键人才"},
			Selected:                  true,
		}},
	)
	if lookups[0].WhereDesignStatus != "FAILED_QUOTA" {
		t.Fatalf("status=%q", lookups[0].WhereDesignStatus)
	}
}

func TestDeterministicSlotsExtractsMetricFromPublishedCatalog(t *testing.T) {
	candidates := []recallCandidate{
		{SubjectType: "METRIC", Code: "revenue", Label: "销售额"},
	}
	slots := deterministicSlots("查看华东销售额趋势", candidates)
	if slots.Intent != "TREND" || slots.MetricCode != "revenue" ||
		slots.DimensionCode != "" || slots.MemberValue != "" {
		t.Fatalf("slots=%#v", slots)
	}
}

func TestExactMetricCodesPreservesQuestionOrderAndDeduplicates(t *testing.T) {
	candidates := []recallCandidate{
		{SubjectType: "METRIC", Code: "order_count", Label: "订单量"},
		{
			SubjectType: "METRIC", Code: "sales_amount", Label: "销售额",
			Aliases: []string{"成交金额"},
		},
		{SubjectType: "METRIC", Code: "sales_amount", Label: "成交金额"},
	}
	codes := exactMetricCodes("请给我成交金额和订单量", candidates, 8)
	if len(codes) != 2 || codes[0] != "sales_amount" ||
		codes[1] != "order_count" {
		t.Fatalf("codes=%#v", codes)
	}
}

func TestValidInterpretedTurnSlotsRejectsAnyHallucinatedMetric(t *testing.T) {
	candidates := []recallCandidate{
		{SubjectType: "METRIC", Code: "sales_amount", Label: "销售额"},
		{SubjectType: "METRIC", Code: "order_count", Label: "订单量"},
	}
	if !validInterpretedTurnSlots(interpretedTurnSlots{
		Intent: "METRIC", MetricCodes: []string{"sales_amount", "order_count"},
		Confidence: 0.9,
	}, candidates) {
		t.Fatal("catalog-backed metric set was rejected")
	}
	if validInterpretedTurnSlots(interpretedTurnSlots{
		Intent: "METRIC", MetricCodes: []string{"sales_amount", "profit"},
		Confidence: 0.9,
	}, candidates) {
		t.Fatal("hallucinated metric in a multi-metric set was accepted")
	}
}

func TestValidInterpretedSlotsRejectsHallucinatedCode(t *testing.T) {
	candidates := []recallCandidate{
		{SubjectType: "METRIC", Code: "revenue", Label: "销售额"},
	}
	if validInterpretedSlots(interpretedSlots{
		Intent: "METRIC", MetricCode: "profit", Confidence: 0.9,
	}, candidates) {
		t.Fatal("hallucinated metric code was accepted")
	}
	if !validInterpretedSlots(interpretedSlots{
		Intent: "METRIC", MetricCode: "revenue", Confidence: 0.9,
	}, candidates) {
		t.Fatal("recalled metric code was rejected")
	}
}

func TestExternalRecallCandidatesNeverContainDimensionMemberValues(t *testing.T) {
	candidates := []recallCandidate{
		{SubjectType: "METRIC", Code: "revenue", Label: "销售额"},
		{
			SubjectType: "MEMBER", DimensionCode: "region",
			Label: "华东", MemberValue: "华东",
		},
		{SubjectType: "DIMENSION", Code: "region", Label: "区域"},
	}
	external := externalRecallCandidates(candidates)
	if len(external) != 1 || external[0].SubjectType != "METRIC" {
		t.Fatalf("external candidates=%#v", external)
	}
	for _, candidate := range external {
		if candidate.SubjectType != "METRIC" ||
			candidate.MemberValue != "" || candidate.Label == "华东" {
			t.Fatalf("member value escaped tenant-local recall: %#v", candidate)
		}
	}
}

func TestAggregateQuestionCannotBecomeRecordLookup(t *testing.T) {
	for _, question := range []string{
		"截止到6月所有的骑手数量是多少",
		"本月订单总额",
		"average delivery duration",
	} {
		if !questionRequestsAggregate(question) {
			t.Fatalf("aggregate question was not recognized: %q", question)
		}
	}
	if questionRequestsAggregate("查询骑手手机号") {
		t.Fatal("record lookup was treated as aggregate")
	}
}

func TestPersonCountIntentIsExplicitWithoutCatalogMetricName(t *testing.T) {
	for _, question := range []string{
		"国内公办毕业的80后小微有多少人？",
		"关键人才人数",
		"员工数量是多少",
		"headcount by region",
	} {
		if !questionRequestsPersonCount(question) {
			t.Fatalf("person-count question was not recognized: %q", question)
		}
	}
	if questionRequestsPersonCount("国内公办毕业人员的手机号") {
		t.Fatal("record lookup was treated as a person-count request")
	}
}

func TestDimensionDisambiguationUsesQuestionAndFieldDescriptions(t *testing.T) {
	stub := &whereDesignAIStub{
		content: json.RawMessage(`{
			"selectedDimensionCode":"full_time_education_institution_type",
			"reason":"问题使用“毕业”，对应全日制毕业教育经历",
			"confidence":0.96,
			"needsClarification":false
		}`),
	}
	interpreter := &SemanticInterpreter{ai: stub}
	lookups := interpreter.resolveAmbiguousDimensionLookups(
		context.Background(), "tenant-1", "actor-1", "employee_count",
		"国内公办毕业的80后小微有多少人？",
		[]QueryDimensionValueLookupTrace{
			{
				Term: "国内公办", DimensionCode: "full_time_education_institution_type",
				DimensionName:             "全日制学历办学性质",
				DimensionFieldName:        "full_time_education_institution_type",
				DimensionFieldDescription: "员工全日制学历的办学主体类型编码",
				CandidateMemberKeys:       []string{"国内公办"},
			},
			{
				Term: "国内公办", DimensionCode: "highest_education_institution_type",
				DimensionName:             "最高学历办学性质",
				DimensionFieldName:        "highest_education_institution_type",
				DimensionFieldDescription: "员工最高学历的办学主体类型编码",
				CandidateMemberKeys:       []string{"国内公办"},
			},
			{
				Term: "80后", DimensionCode: "birth_cohort",
				DimensionName:             "出生年代段",
				DimensionFieldName:        "birth_cohort",
				DimensionFieldDescription: "按出生年份划分的员工代际区间",
				CandidateMemberKeys:       []string{"80-85", "85-90"},
			},
		},
	)
	if !lookups[0].Selected || lookups[1].Selected ||
		!lookups[2].Selected ||
		len(lookups[2].SelectedMemberKeys) != 2 {
		t.Fatalf("lookups=%#v", lookups)
	}
	requestText := ""
	for _, message := range stub.invocation.Request.Messages {
		for _, part := range message.Parts {
			requestText += part.Text
		}
	}
	for _, expected := range []string{
		`"question":"国内公办毕业的80后小微有多少人？"`,
		`"fieldDescription":"员工全日制学历的办学主体类型编码"`,
		`"fieldDescription":"员工最高学历的办学主体类型编码"`,
	} {
		if !strings.Contains(requestText, expected) {
			t.Fatalf("dimension disambiguation request lacks %q: %s", expected, requestText)
		}
	}
}

func TestPersistedDecisionMustMatchGovernedSelectedMembers(t *testing.T) {
	lookup := QueryDimensionValueLookupTrace{
		Term: "智家", CanonicalValue: "智家生态圈",
		DimensionCode:      "industry_circle",
		SelectedMemberKeys: []string{"智家生态圈"},
	}
	decision := dimensionDecisionCandidate{
		DecisionID: "decision-1", DimensionCode: "industry_circle",
		CanonicalValue: "智家生态圈", Aliases: []string{"智家"},
		MemberValue: "智家生态圈", SelectedMemberCount: 1,
		WhereCondition:    "industry_circle = '智家生态圈'",
		CompiledCondition: "field-1 = :industry_circle_1",
		Score:             1,
	}
	if !decisionMatchesLookup(decision, lookup) {
		t.Fatal("exact governed persisted decision was not reusable")
	}
	decision.MemberValue = "智慧生活"
	if decisionMatchesLookup(decision, lookup) {
		t.Fatal("decision for a different governed member was reused")
	}
}

func TestMetricCandidateTraceShowsActualCandidateAndSelection(t *testing.T) {
	candidates := []recallCandidate{
		{
			SubjectType: "METRIC", Code: "employee_total_count",
			Label: "员工总人数", Aliases: []string{"人员有多少"},
			Score: 1,
		},
		{
			SubjectType: "METRIC", Code: "active_employee_count",
			Label: "在职人员数", Score: 0.63,
		},
	}
	trace := metricCandidateTraces(
		"小微人员有多少？", candidates,
		[]string{"employee_total_count"}, "EXACT_CATALOG",
	)
	if len(trace) != 2 || !trace[0].Selected ||
		trace[0].MatchedTerm != "人员有多少" ||
		trace[0].MatchMethod != "EXACT_CATALOG" ||
		trace[1].Selected {
		t.Fatalf("trace=%#v", trace)
	}
}

func TestDimensionVectorQueryUsesGovernedDescriptionAndValue(t *testing.T) {
	query := dimensionVectorQuery(QueryDimensionValueLookupTrace{
		DimensionName:             "关键人才",
		DimensionFieldDescription: "标识员工是否被评定为关键人才",
		Term:                      "关键人才",
	})
	if query != "标识员工是否被评定为关键人才:关键人才" {
		t.Fatalf("query=%q", query)
	}
}

func TestWhereDesignerReceivesFieldMetadataAndReturnsValidatedOperator(
	t *testing.T,
) {
	stub := &whereDesignAIStub{
		content: json.RawMessage(`{
			"decisions":[{
				"dimensionFieldName":"key_talent",
				"queryValue":"关键人才",
				"canonicalValue":"关键人才",
				"operator":"CONTAINS",
				"values":["关键人才"],
				"reason":"字段描述表明这是标签筛选",
				"confidence":0.98
			}]
		}`),
	}
	interpreter := &SemanticInterpreter{ai: stub}
	lookups := interpreter.designWherePredicates(
		context.Background(), "tenant-1", "actor-1", "employee_count",
		"关键人才有多少？",
		[]QueryDimensionValueLookupTrace{{
			Term:                      "关键人才",
			DimensionFieldName:        "key_talent",
			DimensionFieldDescription: "标识员工是否被评定为关键人才",
			MatchMethod:               "SEMANTIC_TAG",
			SelectedMemberKeys:        []string{"关键人才,管理干部类-人"},
			Selected:                  true,
		}},
	)
	if len(lookups) != 1 ||
		lookups[0].WhereDesignStatus != "SUCCEEDED" ||
		lookups[0].WhereDesignOperator != "CONTAINS" ||
		lookups[0].WhereDesignModel != "where-design-test-model" {
		t.Fatalf("lookups=%#v", lookups)
	}
	requestText := ""
	for _, message := range stub.invocation.Request.Messages {
		for _, part := range message.Parts {
			requestText += part.Text
		}
	}
	for _, expected := range []string{
		`"dimensionFieldName":"key_talent"`,
		`"dimensionFieldDescription":"标识员工是否被评定为关键人才"`,
		`"queryValue":"关键人才"`,
	} {
		if !strings.Contains(requestText, expected) {
			t.Fatalf("LLM request does not contain %q: %s", expected, requestText)
		}
	}
}

func TestWhereDesignerRejectsInventedValue(t *testing.T) {
	stub := &whereDesignAIStub{
		content: json.RawMessage(`{
			"decisions":[{
				"dimensionFieldName":"employee_status",
				"queryValue":"在职",
				"canonicalValue":"在岗",
				"operator":"EQUALS",
				"values":["正式员工"],
				"reason":"错误地发明了一个值",
				"confidence":0.8
			}]
		}`),
	}
	interpreter := &SemanticInterpreter{ai: stub}
	lookups := interpreter.designWherePredicates(
		context.Background(), "tenant-1", "actor-1", "employee_count",
		"在职员工有多少？",
		[]QueryDimensionValueLookupTrace{{
			Term:                      "在职",
			DimensionFieldName:        "employee_status",
			DimensionFieldDescription: "员工在组织内的当前状态",
			MatchMethod:               "SEMANTIC_MAPPING",
			SelectedMemberKeys:        []string{"在岗"},
			Selected:                  true,
		}},
	)
	if lookups[0].WhereDesignStatus != "FAILED_VALIDATION" ||
		lookups[0].WhereDesignOperator != "" {
		t.Fatalf("invented value was accepted: %#v", lookups[0])
	}
}

func TestSemanticLookupDedupMergesSynonymsAndDropsEmptyValues(t *testing.T) {
	items := deduplicateSemanticLookups([]QueryDimensionValueLookupTrace{
		{
			Term: "智家", CanonicalValue: "智家生态圈",
			MetricCode: "employee_count", DimensionCode: "industry_circle",
			DimensionFieldDescription: "智家产业生态圈归属",
			SelectedMemberKeys:        []string{"智家生态圈"},
			AliasValues:               []string{"智家"},
		},
		{
			Term: "智家生态圈", CanonicalValue: "智家生态圈",
			MetricCode: "employee_count", DimensionCode: "industry_circle",
			DimensionFieldDescription: "智家产业生态圈归属",
			SelectedMemberKeys:        []string{"智家生态圈"},
			AliasValues:               []string{"智家生态圈"},
		},
		{
			Term: "", CanonicalValue: "",
			MetricCode: "employee_count", DimensionCode: "industry_circle",
		},
	})
	if len(items) != 1 || items[0].CanonicalValue != "智家生态圈" ||
		len(items[0].AliasValues) != 2 ||
		items[0].AliasValues[0] != "智家" ||
		items[0].AliasValues[1] != "智家生态圈" {
		t.Fatalf("items=%#v", items)
	}
	if query := dimensionVectorQuery(items[0]); query !=
		"智家产业生态圈归属:智家生态圈" {
		t.Fatalf("query=%q", query)
	}
}
