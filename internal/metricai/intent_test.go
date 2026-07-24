package metricai

import "testing"

func TestAnalyzeMetricIntentCombinesRequirementAndExplicitHintsBeforeRetrieval(t *testing.T) {
	request := AuthoringRequest{Requirement: `统计每月不同客户的支付总额

【AI 参考条件】
以下条件由用户选择；未指定的内容请由 AI 基于授权资产补全：
- 优先参考的已发布数据集：支付明细（数据源：销售库；源表：payments）、客户资料
- 统计字段（SUM 数值聚合对象）：支付明细 / 实付金额（paid_amount）
- 统计日期字段：支付明细 / 支付时间（paid_at）
- 分析维度：客户资料 / 客户名称（customer_name）
- 统计口径与聚合：求和（SUM）`}

	intent := analyzeMetricIntent(request)
	if intent.BusinessGoal != "统计每月不同客户的支付总额" ||
		intent.Aggregation != "SUM" || intent.TimeGrain != "MONTH" ||
		!intent.NeedsGrouping || !intent.NeedsJoin {
		t.Fatalf("intent=%#v", intent)
	}
	if len(intent.PreferredDatasetReferences) < 2 ||
		len(intent.StatisticalObjects) < 2 ||
		len(intent.DateReferences) < 2 ||
		len(intent.Dimensions) < 2 {
		t.Fatalf("explicit hints were not retained: %#v", intent)
	}
	search := intentSearchText(request, intent)
	for _, expected := range []string{"paid_amount", "paid_at", "customer_name", "支付明细"} {
		if !containsAny(search, expected) {
			t.Fatalf("search text %q does not contain %q", search, expected)
		}
	}
}

func TestAnalyzeMetricIntentInfersCountDistinctAndGroupingFromNaturalLanguage(t *testing.T) {
	intent := analyzeMetricIntent(AuthoringRequest{
		Requirement: "按地区统计每月不同客户的支付客户数",
	})
	if intent.Aggregation != "COUNT_DISTINCT" || intent.TimeGrain != "MONTH" ||
		!intent.NeedsGrouping {
		t.Fatalf("intent=%#v", intent)
	}
}
