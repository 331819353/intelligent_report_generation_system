package semanticqa

import "testing"

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
