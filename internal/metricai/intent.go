package metricai

import (
	"regexp"
	"strings"
)

const metricHintMarker = "【AI 参考条件】"

var parenthesizedCodePattern = regexp.MustCompile(`[（(]([^（）()]{1,200})[）)]`)

// analyzeMetricIntent runs before any dataset lookup. UI-selected hints are treated as explicit
// constraints; the free-form requirement supplies the goal and safe ranking inferences.
func analyzeMetricIntent(request AuthoringRequest) MetricIntent {
	requirement := strings.TrimSpace(request.Requirement)
	base, hintBlock := requirement, ""
	if index := strings.Index(requirement, metricHintMarker); index >= 0 {
		base = strings.TrimSpace(requirement[:index])
		hintBlock = requirement[index+len(metricHintMarker):]
	}
	intent := MetricIntent{
		BusinessGoal:               base,
		PreferredDatasetReferences: []string{},
		StatisticalObjects:         []string{},
		Aggregation:                inferIntentAggregation(requirement),
		DateReferences:             []string{},
		Dimensions:                 []string{},
		TimeGrain:                  inferIntentTimeGrain(requirement),
		SearchTerms:                []string{},
	}
	for _, rawLine := range strings.Split(hintBlock, "\n") {
		line := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(rawLine), "-"))
		switch {
		case strings.HasPrefix(line, "优先参考的已发布数据集："):
			intent.PreferredDatasetReferences = splitIntentValues(strings.TrimPrefix(line, "优先参考的已发布数据集："))
		case strings.HasPrefix(line, "统计字段"):
			if separator := strings.Index(line, "："); separator >= 0 {
				intent.StatisticalObjects = splitIntentValues(line[separator+len("："):])
			}
		case strings.HasPrefix(line, "统计日期字段："):
			intent.DateReferences = splitIntentValues(strings.TrimPrefix(line, "统计日期字段："))
		case strings.HasPrefix(line, "分析维度："):
			intent.Dimensions = splitIntentValues(strings.TrimPrefix(line, "分析维度："))
		case strings.HasPrefix(line, "统计口径与聚合："):
			if explicit := inferIntentAggregation(strings.TrimPrefix(line, "统计口径与聚合：")); explicit != "" {
				intent.Aggregation = explicit
			}
		}
	}
	intent.NeedsGrouping = len(intent.Dimensions) > 0 || intent.TimeGrain != "" ||
		containsAny(base, "按月", "按周", "按日", "按年", "按季", "分组", "汇总", "分别统计", "各客户", "每个客户")
	intent.NeedsJoin = len(intent.PreferredDatasetReferences) > 1 ||
		containsAny(base, "关联", "联结", "结合", "跨表", "同时使用", "匹配客户", "匹配订单")

	intent.SearchTerms = nonEmptyUniqueIntent(append(
		[]string{intent.BusinessGoal},
		append(
			append(
				append([]string{}, intent.PreferredDatasetReferences...),
				intent.StatisticalObjects...,
			),
			append(intent.DateReferences, intent.Dimensions...)...,
		)...,
	), 64)
	return intent
}

func inferIntentAggregation(value string) string {
	upper := strings.ToUpper(value)
	switch {
	case strings.Contains(upper, "COUNT_DISTINCT") || strings.Contains(upper, "COUNT DISTINCT") ||
		containsAny(value, "去重计数", "去重统计", "不同客户", "唯一客户"):
		return "COUNT_DISTINCT"
	case strings.Contains(upper, "AVG") || containsAny(value, "平均值", "平均数", "均值", "客单价"):
		return "AVG"
	case strings.Contains(upper, "MIN") || strings.Contains(value, "最小值"):
		return "MIN"
	case strings.Contains(upper, "MAX") || strings.Contains(value, "最大值"):
		return "MAX"
	case strings.Contains(upper, "COUNT") || containsAny(value, "计数", "笔数", "次数", "订单量", "订单数", "数量"):
		return "COUNT"
	case strings.Contains(upper, "SUM") || containsAny(value, "求和", "总额", "合计", "销售额", "支付金额", "收入"):
		return "SUM"
	default:
		return ""
	}
}

func inferIntentTimeGrain(value string) string {
	upper := strings.ToUpper(value)
	switch {
	case strings.Contains(upper, "QUARTER") || containsAny(value, "按季", "季度", "季报"):
		return "QUARTER"
	case strings.Contains(upper, "MONTH") || containsAny(value, "按月", "月度", "月份", "每月"):
		return "MONTH"
	case strings.Contains(upper, "WEEK") || containsAny(value, "按周", "周度", "每周"):
		return "WEEK"
	case strings.Contains(upper, "DAY") || containsAny(value, "按日", "每日", "天级"):
		return "DAY"
	case strings.Contains(upper, "YEAR") || containsAny(value, "按年", "年度", "每年"):
		return "YEAR"
	default:
		return ""
	}
}

func splitIntentValues(value string) []string {
	parts := strings.FieldsFunc(value, func(char rune) bool {
		return char == '、' || char == '，' || char == ',' || char == ';' || char == '；'
	})
	result := make([]string, 0, len(parts)*2)
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		result = append(result, part)
		for _, match := range parenthesizedCodePattern.FindAllStringSubmatch(part, 4) {
			if len(match) == 2 {
				result = append(result, strings.TrimSpace(match[1]))
			}
		}
	}
	return nonEmptyUniqueIntent(result, 32)
}

func nonEmptyUniqueIntent(values []string, limit int) []string {
	result := []string{}
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
		if len(result) >= limit {
			break
		}
	}
	return result
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

func intentSearchText(request AuthoringRequest, intent MetricIntent) string {
	terms := append([]string{request.Requirement}, intent.SearchTerms...)
	if intent.Aggregation != "" {
		terms = append(terms, intent.Aggregation)
	}
	if intent.TimeGrain != "" {
		terms = append(terms, intent.TimeGrain)
	}
	return strings.Join(nonEmptyUniqueIntent(terms, 80), " ")
}
