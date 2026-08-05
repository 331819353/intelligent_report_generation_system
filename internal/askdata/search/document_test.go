package search

import (
	"strings"
	"testing"

	"intelligent-report-generation-system/internal/askdata/registry"
)

func TestMemberDocumentIncludesDimensionContextAndHasStableHash(t *testing.T) {
	base := MemberDocumentInput{
		ObjectVersionID: "member-east-v1", DimensionVersionID: "dimension-sales-region-v1",
		DimensionName: "销售区域", DimensionDescription: "订单归属的销售大区",
		CanonicalValue: "华东", Aliases: []string{"华东区", " East China ", "华东区"},
		Sensitivity: registry.SensitivityInternal, MemberIndexPolicy: registry.MemberIndexFull,
	}
	first, err := BuildMemberDocument(base)
	if err != nil {
		t.Fatal(err)
	}
	base.Aliases = []string{"华东区", "East China"}
	second, err := BuildMemberDocument(base)
	if err != nil {
		t.Fatal(err)
	}
	if first.InputHash != second.InputHash || first.Text != second.Text {
		t.Fatalf("normalized documents differ: %#v / %#v", first, second)
	}
	for _, wanted := range []string{"dimension=销售区域", "dimension_definition=订单归属的销售大区", "canonical_value=华东"} {
		if !strings.Contains(first.Text, wanted) {
			t.Fatalf("document missing %q: %s", wanted, first.Text)
		}
	}
	if first.IndexPolicy != IndexHybrid || first.DocumentVersion != memberDocumentVersion {
		t.Fatalf("member document = %#v", first)
	}
}

func TestMemberDocumentRejectsNonFullHighCardinalityAndSensitiveValues(t *testing.T) {
	base := MemberDocumentInput{
		ObjectVersionID: "member-1", DimensionVersionID: "dimension-1",
		DimensionName: "客户", DimensionDescription: "客户名称", CanonicalValue: "示例客户",
		Sensitivity: registry.SensitivityInternal, MemberIndexPolicy: registry.MemberIndexFull,
	}
	for _, mutate := range []func(*MemberDocumentInput){
		func(input *MemberDocumentInput) { input.MemberIndexPolicy = registry.MemberIndexExactOnly },
		func(input *MemberDocumentInput) { input.HighCardinality = true },
		func(input *MemberDocumentInput) { input.Sensitivity = registry.SensitivityConfidential },
	} {
		input := base
		mutate(&input)
		if _, err := BuildMemberDocument(input); err == nil {
			t.Fatalf("unsafe member was accepted: %#v", input)
		}
	}
}

func TestClassifiedDocumentsAreVersionedAndRejectSecretsOrPhysicalQueries(t *testing.T) {
	credential := "sk-" + "1234567890abcdefghijklmnop"
	metric, err := BuildMetricDocument(MetricDocumentInput{
		ObjectVersionID: "metric-sales-v1", Name: "销售额", Definition: "支付成功订单的含税金额",
		Aliases: []string{"GMV", "成交额"}, PositiveQuestions: []string{"本月销售额"},
		NegativeExamples: []string{"销售数量"}, Sensitivity: registry.SensitivityInternal,
	})
	if err != nil {
		t.Fatal(err)
	}
	if metric.ObjectType != ObjectMetric || metric.ViewType != ViewDefinitionQuestion || metric.DocumentVersion != metricDocumentVersion {
		t.Fatalf("metric document = %#v", metric)
	}
	if _, err := BuildTermDocument(TermDocumentInput{
		ObjectVersionID: "term-unsafe-v1", Name: "unsafe", Definition: "api_key=" + credential,
		Sensitivity: registry.SensitivityInternal,
	}); err == nil {
		t.Fatal("credential-shaped term was accepted")
	}
	if _, err := BuildDimensionDocument(DimensionDocumentInput{
		ObjectVersionID: "dimension-unsafe-v1", Name: "unsafe",
		Description: "SELECT customer_name FROM customers", Sensitivity: registry.SensitivityInternal,
	}); err == nil {
		t.Fatal("physical SQL was accepted")
	}
	if err := metric.InputHash.Validate(); err != nil {
		t.Fatalf("input hash: %v", err)
	}
}
