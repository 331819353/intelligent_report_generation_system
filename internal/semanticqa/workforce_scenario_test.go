package semanticqa

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestWorkforceQuestionContractResolvesThreeIndependentDimensions(t *testing.T) {
	raw, err := os.ReadFile(
		"../../testdata/semantic-qa/workforce-answer-contract.json",
	)
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		Question                         string `json:"question"`
		FollowUp                         string `json:"followUpQuestion"`
		MetricCode                       string `json:"metricCode"`
		ExpectedValue                    int    `json:"expectedSampleValue"`
		ExpectedFollowUpValue            int    `json:"expectedFollowUpValue"`
		ExpectedExpandedKeyTalentMembers int    `json:"expectedExpandedKeyTalentMembers"`
		ExpectedFilter                   []struct {
			DimensionCode  string   `json:"dimensionCode"`
			DimensionName  string   `json:"dimensionName"`
			MemberKeys     []string `json:"memberKeys"`
			CanonicalLabel string   `json:"canonicalLabel"`
			Aliases        []string `json:"aliases"`
		} `json:"expectedFilters"`
		SampleRows []struct {
			BusinessTrack      string `json:"business_track"`
			EmployeeStatus     string `json:"employee_status"`
			KeyTalent          string `json:"key_talent"`
			EmployeeTotalCount int    `json:"employee_total_count"`
		} `json:"sampleMaterializedRows"`
	}
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatal(err)
	}
	if contract.MetricCode !=
		"metric_dws_employee_profile_regenerated_20260727_em_904c04ae2441" ||
		len(contract.ExpectedFilter) != 2 {
		t.Fatalf("contract=%#v", contract)
	}
	tokens := memberLookupTokens(contract.Question, "在职人员数")
	for _, expected := range []string{"80后", "小微"} {
		if !slices.Contains(tokens, expected) {
			t.Fatalf("token %q missing from %#v", expected, tokens)
		}
	}
	semanticRaw, err := os.ReadFile(
		"../../testdata/semantic-qa/semantic-assets-workforce.json",
	)
	if err != nil {
		t.Fatal(err)
	}
	var semanticAssets struct {
		Items []struct {
			CommonTerm    string `json:"commonTerm"`
			MappingValue  string `json:"mappingValue"`
			KnowledgeType string `json:"knowledgeType"`
		} `json:"items"`
	}
	if err := json.Unmarshal(semanticRaw, &semanticAssets); err != nil {
		t.Fatal(err)
	}
	hasEmploymentStatusMapping := false
	hasKeyTalentTagMapping := false
	for _, item := range semanticAssets.Items {
		if item.CommonTerm == "在职" && item.MappingValue == "在岗" &&
			item.KnowledgeType == "人员状态" {
			hasEmploymentStatusMapping = true
		}
		if item.CommonTerm == "关键人才" &&
			item.MappingValue == "TAG:关键人才" &&
			item.KnowledgeType == "关键人才" {
			hasKeyTalentTagMapping = true
		}
	}
	if !hasEmploymentStatusMapping || !hasKeyTalentTagMapping {
		t.Fatal("workforce semantic mappings are incomplete")
	}
	matches := []scopedMemberMatch{}
	for _, filter := range contract.ExpectedFilter {
		for _, memberKey := range filter.MemberKeys {
			matches = append(matches, scopedMemberMatch{
				MemberValue:   memberKey,
				DimensionID:   filter.DimensionCode + "-id",
				DimensionCode: filter.DimensionCode,
				DimensionName: filter.DimensionName,
				MatchedValue:  filter.CanonicalLabel,
				SetMapped:     len(filter.MemberKeys) > 1,
			})
		}
	}
	selected, ambiguous := selectMetricScopedMemberMatches(
		matches, contract.Question,
	)
	if ambiguous || len(selected) != 3 {
		t.Fatalf("selected=%#v ambiguous=%v", selected, ambiguous)
	}
	actual := 0
	followUpActual := 0
	for _, row := range contract.SampleRows {
		if slices.Contains([]string{"80-85", "85-90"}, row.BusinessTrack) &&
			row.EmployeeStatus == "在岗" {
			actual += row.EmployeeTotalCount
			if slices.Contains(
				strings.FieldsFunc(
					row.KeyTalent,
					func(value rune) bool { return value == ',' || value == '，' },
				),
				"关键人才",
			) {
				followUpActual += row.EmployeeTotalCount
			}
		}
	}
	if actual != contract.ExpectedValue {
		t.Fatalf("sample value=%d expected=%d", actual, contract.ExpectedValue)
	}
	if followUpActual != contract.ExpectedFollowUpValue {
		t.Fatalf(
			"follow-up value=%d expected=%d",
			followUpActual, contract.ExpectedFollowUpValue,
		)
	}
	tagMatches := make([]scopedMemberMatch, 0, contract.ExpectedExpandedKeyTalentMembers)
	for index := 0; index < contract.ExpectedExpandedKeyTalentMembers; index++ {
		tagMatches = append(tagMatches, scopedMemberMatch{
			MemberValue: "关键人才组合-" + string(rune('一'+index)),
			DimensionID: "key-talent-id", DimensionCode: "key_talent",
			DimensionName: "关键人才", MatchedValue: "关键人才",
			SetMapped: true,
		})
	}
	selectedTags, tagAmbiguous := selectMetricScopedMemberMatches(
		tagMatches, contract.FollowUp,
	)
	if tagAmbiguous || len(selectedTags) !=
		contract.ExpectedExpandedKeyTalentMembers {
		t.Fatalf(
			"selectedTags=%d ambiguous=%v",
			len(selectedTags), tagAmbiguous,
		)
	}
}

func TestWorkforceQuestionMemberTokens(t *testing.T) {
	tokens := memberLookupTokens("80后小微在职人员有多少人", "员工总人数")
	for _, expected := range []string{"80后", "在职"} {
		if !slices.Contains(tokens, expected) {
			t.Fatalf("member token %q missing from %#v", expected, tokens)
		}
	}
}

func TestSemanticSetMappingSelectsEveryGovernedMember(t *testing.T) {
	matches := []scopedMemberMatch{
		{
			MemberValue: "80-85", DimensionID: "birth",
			DimensionCode: "birth_cohort", DimensionName: "出生年代段",
			MatchedValue: "80后", SetMapped: true,
		},
		{
			MemberValue: "85-90", DimensionID: "birth",
			DimensionCode: "birth_cohort", DimensionName: "出生年代段",
			MatchedValue: "80后", SetMapped: true,
		},
		{
			MemberValue: "在岗", DimensionID: "status",
			DimensionCode: "employee_status", DimensionName: "人员状态",
			MatchedValue: "在职",
		},
	}
	selected, ambiguous := selectMetricScopedMemberMatches(
		matches, "80后小微在职人员有多少人",
	)
	if ambiguous || len(selected) != 3 {
		t.Fatalf("selected=%#v ambiguous=%v", selected, ambiguous)
	}
}

func TestDimensionValueTraceUsesActualCandidatesAndMasksSensitiveValues(
	t *testing.T,
) {
	matches := []scopedMemberMatch{
		{
			MemberValue: "80-85", DimensionID: "birth",
			DimensionCode: "birth_cohort", DimensionName: "出生年代段",
			MatchedValue: "80后", MatchMethod: "SEMANTIC_SET",
			SetMapped: true,
		},
		{
			MemberValue: "85-90", DimensionID: "birth",
			DimensionCode: "birth_cohort", DimensionName: "出生年代段",
			MatchedValue: "80后", MatchMethod: "SEMANTIC_SET",
			SetMapped: true,
		},
		{
			MemberValue: "机密成员", DimensionID: "sensitive",
			DimensionCode: "restricted_group", DimensionName: "受限人群",
			MatchedValue: "受限", MatchMethod: "MEMBER_ALIAS",
			Sensitive: true,
		},
	}
	trace := buildDimensionValueLookupTrace(matches, matches)
	if len(trace) != 2 {
		t.Fatalf("trace=%#v", trace)
	}
	var birth, sensitive QueryDimensionValueLookupTrace
	for _, item := range trace {
		switch item.DimensionCode {
		case "birth_cohort":
			birth = item
		case "restricted_group":
			sensitive = item
		}
	}
	if !birth.Selected || birth.CandidateCount != 2 ||
		!slices.Equal(
			birth.SelectedMemberKeys, []string{"80-85", "85-90"},
		) ||
		birth.MatchMethod != "SEMANTIC_SET" {
		t.Fatalf("birth trace=%#v", birth)
	}
	if !sensitive.Selected || sensitive.CandidateCount != 1 ||
		len(sensitive.CandidateMemberKeys) != 0 ||
		len(sensitive.SelectedMemberKeys) != 0 {
		t.Fatalf("sensitive trace leaked values: %#v", sensitive)
	}
}

func TestThreeTurnStandaloneQuestionUsesFinalGovernedSelection(t *testing.T) {
	keyTalent := make([]string, 54)
	for index := range keyTalent {
		keyTalent[index] = "关键人才组合"
	}
	selections := []QueryFinalSelectionTrace{{
		MetricCode: "employee_total_count", MetricName: "员工总人数",
		Dimensions: []QueryFinalDimensionTrace{
			{
				DimensionCode: "birth_cohort",
				DimensionName: "出生年代段",
				MemberKeys:    []string{"80-85", "85-90"},
			},
			{
				DimensionCode: "employee_status",
				DimensionName: "人员状态", MemberKeys: []string{"在岗"},
			},
			{
				DimensionCode: "key_talent",
				DimensionName: "关键人才", MemberKeys: keyTalent,
			},
		},
	}}
	lookups := []QueryDimensionValueLookupTrace{
		{
			Term: "80后", MetricCode: "employee_total_count",
			DimensionCode: "birth_cohort", Selected: true,
		},
		{
			Term: "在职", MetricCode: "employee_total_count",
			DimensionCode: "employee_status", Selected: true,
		},
		{
			Term: "关键人才", MetricCode: "employee_total_count",
			DimensionCode: "key_talent", Selected: true,
		},
	}
	actual := buildStandaloneQuestion(
		[]string{
			"80后小微在职人员有多少人？",
			"其中关键人才有多少？",
			"再核对一次这些人有多少？",
		},
		selections, lookups,
	)
	for _, expected := range []string{
		"在小微人员范围内",
		"出生年代段=80后（映射：80-85、85-90）",
		"人员状态=在职（映射：在岗）",
		"关键人才=关键人才（映射：54 个已治理标准值）",
		"员工总人数",
	} {
		if !strings.Contains(actual, expected) {
			t.Fatalf("standalone question %q missing %q", actual, expected)
		}
	}
}

func TestKeyTalentTraceShowsBusinessLikeAndSafeCompiledPredicate(
	t *testing.T,
) {
	memberKeys := []string{
		"关键人才,技术专家", "关键人才,后备干部",
	}
	plan := QueryPlan{
		MetricFieldID: "field_employee_total_count",
		Conditions: QueryConditionDocument{
			MetricCode: "employee_total_count",
			Dimensions: []QueryDimensionClause{{
				DimensionCode: "key_talent",
				MemberKeys:    memberKeys,
			}},
		},
		PlanningTrace: []QueryDimensionValueLookupTrace{{
			Term: "关键人才", MetricCode: "employee_total_count",
			DimensionCode: "key_talent", DimensionName: "关键人才",
			DimensionFieldID: "field_key_talent",
			MatchMethod:      "SEMANTIC_TAG", CandidateCount: 2,
			CandidateMemberKeys: memberKeys, SelectedMemberKeys: memberKeys,
			Selected: true, Source: "CURRENT_TURN",
			VectorQuery:          "关键人才:关键人才",
			VectorSearchStatus:   "SUCCEEDED",
			VectorCandidateCount: 2,
		}},
	}
	reconcilePlanningTrace(&plan)
	if len(plan.PlanningTrace) != 1 {
		t.Fatalf("trace=%#v", plan.PlanningTrace)
	}
	trace := plan.PlanningTrace[0]
	if trace.MetricFieldID != "field_employee_total_count" ||
		trace.WhereCondition != "key_talent LIKE '%关键人才%'" ||
		trace.CompiledCondition !=
			"field_key_talent IN (:key_talent_1 … :key_talent_2)" ||
		trace.CandidateFilter.AcceptedCount != 2 ||
		trace.CandidateFilter.RejectedCount != 0 {
		t.Fatalf("trace=%#v", trace)
	}
}
