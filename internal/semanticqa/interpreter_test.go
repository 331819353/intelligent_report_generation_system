package semanticqa

import "testing"

func TestDeterministicSlotsExtractsOnlyExactRecalledObjects(t *testing.T) {
	candidates := []recallCandidate{
		{SubjectType: "METRIC", Code: "revenue", Label: "销售额"},
		{
			SubjectType: "MEMBER", DimensionCode: "region",
			Label: "华东", MemberValue: "华东",
		},
		{SubjectType: "DIMENSION", Code: "region", Label: "区域"},
	}
	slots := deterministicSlots("查看华东销售额趋势", candidates)
	if slots.Intent != "TREND" || slots.MetricCode != "revenue" ||
		slots.DimensionCode != "region" || slots.MemberValue != "华东" {
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
	if len(external) != 2 {
		t.Fatalf("external candidates=%#v", external)
	}
	for _, candidate := range external {
		if candidate.SubjectType == "MEMBER" ||
			candidate.MemberValue != "" || candidate.Label == "华东" {
			t.Fatalf("member value escaped tenant-local recall: %#v", candidate)
		}
	}
}
