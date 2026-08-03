package semanticqa

import "testing"

func testSemanticParsingRules() semanticParsingRules {
	rules := semanticParsingRules{
		metricNameSuffixes: []semanticParsingRule{
			{Pattern: "订单数量合计", MinimumLength: 2, Priority: 100},
			{Pattern: "商品数量合计", MinimumLength: 2, Priority: 100},
			{Pattern: "数量合计", MinimumLength: 2, Priority: 100},
			{Pattern: "总量", MinimumLength: 2, Priority: 100},
			{Pattern: "数量", MinimumLength: 2, Priority: 100},
			{Pattern: "总数", MinimumLength: 2, Priority: 100},
			{Pattern: "金额", MinimumLength: 2, Priority: 100},
			{Pattern: "数", MinimumLength: 2, Priority: 100},
		},
		adminRegionSuffixes: []semanticParsingRule{
			{Pattern: "特别行政区", OutputName: "城市", OutputCode: "city", MinimumLength: 2, MaximumLength: 12, Priority: 160},
			{Pattern: "自治区", OutputName: "省份", OutputCode: "province", MinimumLength: 2, MaximumLength: 12, Priority: 150},
			{Pattern: "省", OutputName: "省份", OutputCode: "province", MinimumLength: 2, MaximumLength: 12, Priority: 120},
			{Pattern: "市", OutputName: "城市", OutputCode: "city", MinimumLength: 2, MaximumLength: 12, Priority: 120},
			{Pattern: "区", OutputName: "行政区", OutputCode: "district", MinimumLength: 2, MaximumLength: 12, Priority: 110},
			{Pattern: "县", OutputName: "行政区", OutputCode: "district", MinimumLength: 2, MaximumLength: 12, Priority: 110},
		},
		queryResidualTerms: map[string]bool{},
		broadMetricPhrases: []semanticParsingRule{
			{Pattern: "经营情况"}, {Pattern: "业务情况"},
			{Pattern: "整体情况"}, {Pattern: "总体情况"},
			{Pattern: "经营怎么样"}, {Pattern: "业务怎么样"},
			{Pattern: "表现怎么样"}, {Pattern: "数据怎么样"},
			{Pattern: "经营如何"}, {Pattern: "业务如何"},
			{Pattern: "整体如何"},
		},
	}
	for _, term := range []string{
		"总量", "总数", "数量", "金额", "总额", "合计", "平均",
		"均值", "比例", "占比", "分别", "是什么", "怎么样",
		"什么", "多少", "几笔", "几条", "帮我", "请问", "查询",
		"统计", "查看", "告诉我", "一下", "经营情况", "经营",
		"情况", "怎么", "呢", "按", "按照", "根据", "基于", "依据",
		"每个", "各",
	} {
		rules.queryResidualTerms[normalizeParsingRuleText(term)] = true
	}
	return rules
}

func TestSemanticParsingRulesDriveDeterministicInterpretation(t *testing.T) {
	rules := testSemanticParsingRules()
	terms := rules.metricTerms(recallCandidate{
		SubjectType: "METRIC", Code: "order_count", Label: "订单数量合计",
	})
	foundStem := false
	for _, term := range terms {
		if term == "订单" {
			foundStem = true
		}
	}
	if !foundStem {
		t.Fatalf("metric stem was not derived from configured suffixes: %#v", terms)
	}
	value, name, code, found := rules.administrativeLocation("北京市")
	if !found || value != "北京" || name != "城市" || code != "city" {
		t.Fatalf("unexpected administrative mapping: %q %q %q %v", value, name, code, found)
	}
	if !rules.isDeterministicResidual("总量") ||
		!rules.isDeterministicResidual("按照") ||
		!rules.requestsBroadMetricSelection("北京市经营情况怎么样？") ||
		rules.requestsBroadMetricSelection("北京市投诉总量是多少？") {
		t.Fatal("configured residual or broad-question rules were not applied")
	}
}
