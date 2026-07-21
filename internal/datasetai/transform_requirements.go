package datasetai

import "strings"

type transformRequirementRule struct {
	componentType string
	reason        string
	match         func(string) bool
}

// deriveCreateTransformRequirements turns explicit, user-facing transformation language into
// trusted planner constraints. The matcher deliberately avoids broad business words such as
// “统计” or “按月汇总”: those describe GROUP semantics and must not manufacture a DATE_FORMAT.
func deriveCreateTransformRequirements(instruction string) []TransformRequirement {
	text := strings.ToLower(strings.TrimSpace(instruction))
	if text == "" {
		return []TransformRequirement{}
	}
	contains := func(values ...string) bool {
		for _, value := range values {
			if strings.Contains(text, strings.ToLower(value)) {
				return true
			}
		}
		return false
	}
	dateConversion := func() bool {
		if contains("日期转换", "日期格式", "格式化日期", "时间格式化", "yyyy-mm", "yyyymm", "yyyy年", "年月字段", "年季字段", "季度字段") {
			return true
		}
		return contains("转为", "转成", "转换成", "转换为", "提取") && contains("年份", "年月", "年季", "季度", "年月日")
	}
	conditionMapping := func() bool {
		if contains("条件映射", "case when", "case_when") {
			return true
		}
		return contains("映射为", "标记为", "分类为", "否则输出", "否则为") || contains("如果") && contains("则", "否则")
	}
	rules := []transformRequirementRule{
		{componentType: "TEXT_UPPER", reason: "用户要求文本大写转换", match: func(string) bool {
			return contains("大写转换", "转为大写", "转换成大写", "统一大写", "upper(", "uppercase")
		}},
		{componentType: "TEXT_TRIM", reason: "用户要求清理文本空格", match: func(string) bool {
			return contains("空格清理", "首尾空格", "去空格", "trim(")
		}},
		{componentType: "TEXT_REPLACE", reason: "用户要求替换文本内容", match: func(string) bool {
			return contains("文本替换", "字符串替换", "替换指定文本", "替换为", "替换成", "replace(")
		}},
		{componentType: "TEXT_LOWER", reason: "用户要求文本小写转换", match: func(string) bool {
			return contains("小写转换", "转为小写", "转换成小写", "统一小写", "lower(", "lowercase")
		}},
		{componentType: "TEXT_SUBSTRING", reason: "用户要求截取字段内容", match: func(string) bool {
			return contains("字段截取", "文本截取", "字符串截取", "截取", "substring(", "substr(")
		}},
		{componentType: "TEXT_CONCAT", reason: "用户要求拼接字段内容", match: func(string) bool {
			return contains("字段拼接", "文本拼接", "字符串拼接", "拼接", "concat(")
		}},
		{componentType: "NUMBER_ABSOLUTE", reason: "用户要求计算绝对值", match: func(string) bool { return contains("取绝对值", "绝对值", "abs(") }},
		{componentType: "NUMBER_ROUNDING", reason: "用户要求数值取整", match: func(string) bool {
			return contains("四舍五入", "向上取整", "向下取整", "数值取整", "round(", "floor(", "ceil(")
		}},
		{componentType: "NUMBER_ARITHMETIC", reason: "用户要求字段数值运算", match: func(string) bool {
			return contains("数值运算", "字段相加", "字段相减", "字段相乘", "字段相除", "相加", "相减", "相乘", "相除", "加减乘除")
		}},
		{componentType: "DATE_FORMAT", reason: "用户要求转换日期粒度或格式", match: func(string) bool { return dateConversion() }},
		{componentType: "NULL", reason: "用户要求填充空值", match: func(string) bool {
			return contains("空值填充", "空值处理", "缺失值填充", "缺失值处理", "null填充", "null 填充", "为空时填充", "coalesce(")
		}},
		{componentType: "CAST", reason: "用户要求转换字段类型", match: func(string) bool {
			return contains("类型转换", "数据类型转换", "cast(", "转为字符串", "转成字符串", "转为数值", "转成数值", "转为日期", "转成日期")
		}},
		{componentType: "CONDITION", reason: "用户要求按条件映射输出", match: func(string) bool { return conditionMapping() }},
	}
	result := make([]TransformRequirement, 0, len(rules))
	for _, rule := range rules {
		if rule.match(text) {
			result = append(result, TransformRequirement{ComponentType: rule.componentType, Reason: rule.reason})
		}
	}
	return result
}

func validateTransformRequirements(plan GraphPlan, requirements []TransformRequirement) error {
	if len(requirements) == 0 {
		return nil
	}
	consumedKeys := transformConsumedKeys(plan)
	available := make(map[string]bool, len(plan.Transforms))
	for _, transform := range plan.Transforms {
		for _, rule := range transform.Rules {
			if consumedKeys[fieldKey(transform.ID, rule.Output.ID)] {
				available[transform.ComponentType] = true
				break
			}
		}
	}
	missing := make([]string, 0, len(requirements))
	for _, requirement := range requirements {
		if !available[requirement.ComponentType] {
			missing = append(missing, requirement.ComponentType)
		}
	}
	if len(missing) > 0 {
		return invalidOutputWithReason(InvalidOutputReasonTransform, "plan is missing required transform component types or their outputs are unused: "+strings.Join(missing, ", "))
	}
	return nil
}

func transformConsumedKeys(plan GraphPlan) map[string]bool {
	result := map[string]bool{}
	for _, output := range plan.End.Outputs {
		result[output.Key] = true
	}
	for _, group := range plan.Groups {
		for _, dimension := range group.Dimensions {
			result[fieldKey(dimension.NodeID, dimension.Column)] = true
		}
		for _, metric := range group.Metrics {
			result[fieldKey(metric.NodeID, metric.Column)] = true
		}
	}
	for _, join := range plan.Joins {
		for _, condition := range join.Conditions {
			result[fieldKey(condition.LeftNodeID, condition.LeftColumn)] = true
			result[fieldKey(condition.RightNodeID, condition.RightColumn)] = true
		}
	}
	for _, transform := range plan.Transforms {
		for _, rule := range transform.Rules {
			for _, key := range rule.InputKeys {
				result[key] = true
			}
			if rule.ReplaceSourceKey != "" {
				result[rule.ReplaceSourceKey] = true
			}
			for _, value := range rule.ConditionValues {
				if value.Mode == "FIELD" {
					result[value.Value] = true
				}
			}
		}
	}
	return result
}
