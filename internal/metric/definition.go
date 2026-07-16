package metric

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/google/uuid"
)

var (
	identifierPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)
	decimalPattern    = regexp.MustCompile(`^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?$`)
)

// Prepare 对指标定义执行严格解码、规范化、白名单校验和确定性哈希。
func Prepare(raw []byte) (Prepared, error) {
	if len(raw) == 0 || len(raw) > 1<<20 {
		return Prepared{}, invalid("definition", "METRIC_DEFINITION_SIZE_INVALID", "指标定义不能为空且不能超过 1 MiB")
	}
	if err := rejectDuplicateKeys(raw); err != nil {
		return Prepared{}, invalid("definition", "METRIC_DEFINITION_JSON_INVALID", "指标定义包含重复键或无效 JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	var definition Definition
	if err := decoder.Decode(&definition); err != nil {
		return Prepared{}, invalid("definition", "METRIC_DEFINITION_JSON_INVALID", "指标定义包含未知字段或无效 JSON")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Prepared{}, invalid("definition", "METRIC_DEFINITION_JSON_INVALID", "指标定义只能包含一个 JSON 文档")
	}
	normalize(&definition)
	issues, dimensions, dependencies := validate(definition)
	if len(issues) > 0 {
		return Prepared{}, &ValidationError{Issues: issues}
	}
	canonical, err := json.Marshal(definition)
	if err != nil {
		return Prepared{}, ErrInvalidDefinition
	}
	sum := sha256.Sum256(canonical)
	return Prepared{
		Definition: definition, DefinitionJSON: canonical, DefinitionHash: hex.EncodeToString(sum[:]),
		DimensionFieldIDs: dimensions, DependencyVersionIDs: dependencies,
	}, nil
}

func normalize(definition *Definition) {
	definition.SchemaVersion = strings.TrimSpace(definition.SchemaVersion)
	definition.Metric.Code = strings.TrimSpace(definition.Metric.Code)
	definition.Metric.Name = strings.TrimSpace(definition.Metric.Name)
	definition.Metric.Description = strings.TrimSpace(definition.Metric.Description)
	definition.Metric.Type = strings.ToUpper(strings.TrimSpace(definition.Metric.Type))
	definition.DatasetID = strings.TrimSpace(definition.DatasetID)
	definition.DatasetVersionID = strings.TrimSpace(definition.DatasetVersionID)
	definition.Aggregation = strings.ToUpper(strings.TrimSpace(definition.Aggregation))
	definition.Unit = strings.TrimSpace(definition.Unit)
	definition.NumberFormat = strings.TrimSpace(definition.NumberFormat)
	definition.TimeFieldID = strings.TrimSpace(definition.TimeFieldID)
	definition.TimeGrain = strings.ToUpper(strings.TrimSpace(definition.TimeGrain))
	definition.Additivity = strings.ToUpper(strings.TrimSpace(definition.Additivity))
	definition.RoundingMode = strings.ToUpper(strings.TrimSpace(definition.RoundingMode))
	definition.NullHandling = strings.ToUpper(strings.TrimSpace(definition.NullHandling))
	definition.DivisionByZero = strings.ToUpper(strings.TrimSpace(definition.DivisionByZero))
	if definition.AllowedDimensions == nil {
		definition.AllowedDimensions = []Dimension{}
	}
	if definition.NonAdditiveDimensionFieldIDs == nil {
		definition.NonAdditiveDimensionFieldIDs = []string{}
	}
	for index := range definition.NonAdditiveDimensionFieldIDs {
		definition.NonAdditiveDimensionFieldIDs[index] = strings.TrimSpace(definition.NonAdditiveDimensionFieldIDs[index])
	}
	for index := range definition.AllowedDimensions {
		dimension := &definition.AllowedDimensions[index]
		dimension.FieldID = strings.TrimSpace(dimension.FieldID)
		dimension.Name = strings.TrimSpace(dimension.Name)
		dimension.SortDirection = strings.ToUpper(strings.TrimSpace(dimension.SortDirection))
		dimension.NullLabel = strings.TrimSpace(dimension.NullLabel)
		if dimension.HierarchyFieldIDs == nil {
			dimension.HierarchyFieldIDs = []string{}
		}
		for hierarchyIndex := range dimension.HierarchyFieldIDs {
			dimension.HierarchyFieldIDs[hierarchyIndex] = strings.TrimSpace(dimension.HierarchyFieldIDs[hierarchyIndex])
		}
	}
	normalizeExpression(&definition.Expression)
}

func normalizeExpression(expression *Expression) {
	expression.Type = strings.ToUpper(strings.TrimSpace(expression.Type))
	expression.FieldID = strings.TrimSpace(expression.FieldID)
	expression.MetricVersionID = strings.TrimSpace(expression.MetricVersionID)
	if number, ok := expression.Value.(json.Number); ok {
		// 十进制常量规范化为字符串，避免 JSON number 在浏览器或网关中丢失精度。
		expression.Value = number.String()
	}
	if expression.Arguments == nil {
		expression.Arguments = []Expression{}
	}
	for index := range expression.Arguments {
		normalizeExpression(&expression.Arguments[index])
	}
}

func validate(definition Definition) ([]ValidationIssue, []string, []string) {
	issues := []ValidationIssue{}
	add := func(path, code, reason string) {
		issues = append(issues, ValidationIssue{Path: path, Code: code, Reason: reason})
	}
	if definition.SchemaVersion != DefinitionVersion {
		add("schemaVersion", "METRIC_SCHEMA_VERSION_UNSUPPORTED", "仅支持指标定义版本 1.0")
	}
	if !identifierPattern.MatchString(definition.Metric.Code) {
		add("metric.code", "METRIC_CODE_INVALID", "编码必须以英文字母开头，且只能包含字母、数字和下划线")
	}
	validateText(&issues, "metric.name", "METRIC_NAME_INVALID", definition.Metric.Name, 1, 200)
	validateText(&issues, "metric.description", "METRIC_DESCRIPTION_INVALID", definition.Metric.Description, 0, 2000)
	if !oneOf(definition.Metric.Type, "ATOMIC", "DERIVED", "RATIO") {
		add("metric.type", "METRIC_TYPE_UNSUPPORTED", "指标类型必须为 ATOMIC、DERIVED 或 RATIO")
	}
	if !canonicalUUID(definition.DatasetID) {
		add("datasetId", "METRIC_DATASET_INVALID", "必须引用规范的数据集 UUID")
	}
	if !canonicalUUID(definition.DatasetVersionID) {
		add("datasetVersionId", "METRIC_DATASET_VERSION_INVALID", "必须引用规范的数据集版本 UUID")
	}
	if !oneOf(definition.Aggregation, "NONE", "SUM", "AVG", "MIN", "MAX", "COUNT", "COUNT_DISTINCT") {
		add("aggregation", "METRIC_AGGREGATION_UNSUPPORTED", "聚合方式不受支持")
	}
	validateText(&issues, "unit", "METRIC_UNIT_INVALID", definition.Unit, 0, 32)
	validateText(&issues, "numberFormat", "METRIC_NUMBER_FORMAT_INVALID", definition.NumberFormat, 1, 64)
	if !oneOf(definition.TimeGrain, "NONE", "DAY", "WEEK", "MONTH", "QUARTER", "YEAR") {
		add("timeGrain", "METRIC_TIME_GRAIN_UNSUPPORTED", "时间粒度不受支持")
	}
	if definition.TimeFieldID == "" && definition.TimeGrain != "NONE" || definition.TimeFieldID != "" && definition.TimeGrain == "NONE" {
		add("timeGrain", "METRIC_TIME_FIELD_MISMATCH", "时间字段和时间粒度必须同时设置或同时为空")
	}
	if !oneOf(definition.Additivity, "ADDITIVE", "SEMI_ADDITIVE", "NON_ADDITIVE") {
		add("additivity", "METRIC_ADDITIVITY_UNSUPPORTED", "可加性声明不受支持")
	}
	if definition.DecimalScale < 0 || definition.DecimalScale > 12 {
		add("decimalScale", "METRIC_DECIMAL_SCALE_INVALID", "十进制小数位必须在 0 到 12 之间")
	}
	if definition.RoundingMode != "HALF_UP" {
		add("roundingMode", "METRIC_ROUNDING_MODE_UNSUPPORTED", "首阶段仅支持 HALF_UP 舍入声明")
	}
	if definition.NullHandling != "IGNORE" {
		add("nullHandling", "METRIC_NULL_HANDLING_UNSUPPORTED", "首阶段仅支持聚合时忽略 NULL")
	}
	if definition.DivisionByZero != "NULL" {
		add("divisionByZero", "METRIC_DIVISION_BY_ZERO_UNSUPPORTED", "首阶段除零结果必须声明为 NULL")
	}

	dimensionSet := map[string]bool{}
	dimensions := make([]string, 0, len(definition.AllowedDimensions))
	for index, dimension := range definition.AllowedDimensions {
		path := fmt.Sprintf("allowedDimensions[%d]", index)
		if dimension.FieldID == "" {
			add(path+".fieldId", "METRIC_DIMENSION_FIELD_REQUIRED", "维度必须引用数据集字段")
		} else if dimensionSet[dimension.FieldID] {
			add(path+".fieldId", "METRIC_DIMENSION_DUPLICATE", "维度字段不能重复")
		}
		dimensionSet[dimension.FieldID] = true
		dimensions = append(dimensions, dimension.FieldID)
		validateText(&issues, path+".name", "METRIC_DIMENSION_NAME_INVALID", dimension.Name, 1, 200)
		if !oneOf(dimension.SortDirection, "ASC", "DESC") {
			add(path+".sortDirection", "METRIC_DIMENSION_SORT_INVALID", "维度排序必须为 ASC 或 DESC")
		}
		validateText(&issues, path+".nullLabel", "METRIC_DIMENSION_NULL_LABEL_INVALID", dimension.NullLabel, 0, 100)
		hierarchySet := map[string]bool{}
		for hierarchyIndex, fieldID := range dimension.HierarchyFieldIDs {
			hierarchyPath := fmt.Sprintf("%s.hierarchyFieldIds[%d]", path, hierarchyIndex)
			if fieldID == "" || hierarchySet[fieldID] {
				add(hierarchyPath, "METRIC_DIMENSION_HIERARCHY_INVALID", "层级字段不能为空或重复")
			}
			hierarchySet[fieldID] = true
		}
	}
	if definition.TimeFieldID != "" && !dimensionSet[definition.TimeFieldID] {
		add("timeFieldId", "METRIC_TIME_DIMENSION_REQUIRED", "时间字段必须同时登记为可用维度")
	}
	nonAdditiveSet := map[string]bool{}
	for index, fieldID := range definition.NonAdditiveDimensionFieldIDs {
		path := fmt.Sprintf("nonAdditiveDimensionFieldIds[%d]", index)
		if !dimensionSet[fieldID] {
			add(path, "METRIC_NON_ADDITIVE_DIMENSION_INVALID", "不可加维度必须来自可用维度")
		}
		if nonAdditiveSet[fieldID] {
			add(path, "METRIC_NON_ADDITIVE_DIMENSION_DUPLICATE", "不可加维度不能重复")
		}
		nonAdditiveSet[fieldID] = true
	}
	if definition.Additivity == "SEMI_ADDITIVE" && len(definition.NonAdditiveDimensionFieldIDs) == 0 {
		add("nonAdditiveDimensionFieldIds", "METRIC_SEMI_ADDITIVE_DIMENSION_REQUIRED", "半可加指标必须声明至少一个不可加维度")
	}
	if definition.Additivity != "SEMI_ADDITIVE" && len(definition.NonAdditiveDimensionFieldIDs) > 0 {
		add("nonAdditiveDimensionFieldIds", "METRIC_NON_ADDITIVE_DIMENSION_UNEXPECTED", "只有半可加指标可以单独声明不可加维度")
	}

	dependencies := map[string]bool{}
	expressionStats := expressionValidationStats{}
	validateMetricExpression(&issues, "expression", definition.Expression, 1, dependencies, &expressionStats)
	if expressionStats.fieldRefs == 0 && expressionStats.metricRefs == 0 {
		add("expression", "METRIC_VALUE_REFERENCE_REQUIRED", "指标表达式必须引用至少一个数据集字段或精确指标版本")
	}
	if definition.Metric.Type == "ATOMIC" && expressionStats.metricRefs > 0 {
		add("expression", "METRIC_ATOMIC_REFERENCE_FORBIDDEN", "原子指标不能引用其他指标版本")
	}
	if definition.Metric.Type != "ATOMIC" && expressionStats.metricRefs == 0 {
		add("expression", "METRIC_DERIVED_REFERENCE_REQUIRED", "派生或比率指标必须引用至少一个精确指标版本")
	}
	if expressionStats.metricRefs > 0 && expressionStats.fieldRefs > 0 {
		add("expression", "METRIC_REFERENCE_FIELD_MIXED", "同一指标不能混用已聚合指标引用和明细字段")
	}
	if expressionStats.metricRefs > 0 && definition.Aggregation != "NONE" {
		add("aggregation", "METRIC_NESTED_AGGREGATION_FORBIDDEN", "引用指标版本时不能再次聚合")
	}
	if expressionStats.fieldRefs > 0 && definition.Aggregation == "NONE" {
		add("aggregation", "METRIC_FIELD_AGGREGATION_REQUIRED", "直接引用数据集字段时必须声明聚合")
	}
	if definition.Metric.Type == "RATIO" && !expressionStats.hasDivision {
		add("expression", "METRIC_RATIO_DIVISION_REQUIRED", "比率指标表达式必须包含除法")
	}
	if (definition.Aggregation == "AVG" || definition.Aggregation == "COUNT_DISTINCT" || definition.Metric.Type == "RATIO" || expressionStats.metricRefs > 0) && definition.Additivity == "ADDITIVE" {
		add("additivity", "METRIC_ADDITIVITY_CONFLICT", "平均值、去重计数、比率或派生指标不能声明为完全可加")
	}
	dependencyIDs := make([]string, 0, len(dependencies))
	for id := range dependencies {
		dependencyIDs = append(dependencyIDs, id)
	}
	sort.Strings(dependencyIDs)
	return issues, dimensions, dependencyIDs
}

type expressionValidationStats struct {
	nodes, fieldRefs, metricRefs int
	hasDivision                  bool
}

func validateMetricExpression(issues *[]ValidationIssue, path string, expression Expression, depth int, dependencies map[string]bool, stats *expressionValidationStats) {
	add := func(suffix, code, reason string) {
		*issues = append(*issues, ValidationIssue{Path: path + suffix, Code: code, Reason: reason})
	}
	stats.nodes++
	if depth > 16 || stats.nodes > 128 {
		add("", "METRIC_EXPRESSION_COMPLEXITY_EXCEEDED", "表达式深度不能超过 16 且节点不能超过 128 个")
		return
	}
	switch expression.Type {
	case "FIELD_REF":
		stats.fieldRefs++
		if expression.FieldID == "" {
			add(".fieldId", "METRIC_FIELD_REQUIRED", "字段引用不能为空")
		}
		if expression.MetricVersionID != "" || expression.Value != nil || len(expression.Arguments) > 0 {
			add("", "METRIC_EXPRESSION_SHAPE_INVALID", "字段引用包含无关属性")
		}
	case "METRIC_REF":
		stats.metricRefs++
		if !canonicalUUID(expression.MetricVersionID) {
			add(".metricVersionId", "METRIC_REFERENCE_INVALID", "必须引用规范的指标版本 UUID")
		} else {
			dependencies[expression.MetricVersionID] = true
		}
		if expression.FieldID != "" || expression.Value != nil || len(expression.Arguments) > 0 {
			add("", "METRIC_EXPRESSION_SHAPE_INVALID", "指标引用包含无关属性")
		}
	case "LITERAL":
		value, ok := expression.Value.(string)
		if !ok || !validDecimal(value) {
			add(".value", "METRIC_LITERAL_INVALID", "十进制常量必须使用不超过 38 位的规范字符串")
		}
		if expression.FieldID != "" || expression.MetricVersionID != "" || len(expression.Arguments) > 0 {
			add("", "METRIC_EXPRESSION_SHAPE_INVALID", "常量包含无关属性")
		}
	case "ADD", "SUBTRACT", "MULTIPLY", "DIVIDE":
		if expression.Type == "DIVIDE" {
			stats.hasDivision = true
		}
		if len(expression.Arguments) != 2 {
			add(".arguments", "METRIC_ARITY_INVALID", "四则运算必须且只能包含两个参数")
		}
		if expression.FieldID != "" || expression.MetricVersionID != "" || expression.Value != nil {
			add("", "METRIC_EXPRESSION_SHAPE_INVALID", "运算表达式包含无关属性")
		}
		for index := range expression.Arguments {
			validateMetricExpression(issues, fmt.Sprintf("%s.arguments[%d]", path, index), expression.Arguments[index], depth+1, dependencies, stats)
		}
	default:
		add(".type", "METRIC_EXPRESSION_TYPE_UNSUPPORTED", "表达式类型不受支持")
	}
}

func validateText(issues *[]ValidationIssue, path, code, value string, minLength, maxLength int) {
	length := len([]rune(value))
	if length < minLength || length > maxLength || containsControl(value) {
		*issues = append(*issues, ValidationIssue{Path: path, Code: code, Reason: fmt.Sprintf("长度必须在 %d 到 %d 个字符之间且不能包含控制字符", minLength, maxLength)})
	}
}

func validDecimal(value string) bool {
	if !decimalPattern.MatchString(value) {
		return false
	}
	digits := 0
	for _, char := range value {
		if char >= '0' && char <= '9' {
			digits++
		}
	}
	return digits <= 38
}

func containsControl(value string) bool {
	for _, char := range value {
		if unicode.IsControl(char) {
			return true
		}
	}
	return false
}

func canonicalUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}

func oneOf(value string, allowed ...string) bool {
	for _, item := range allowed {
		if value == item {
			return true
		}
	}
	return false
}

func invalid(path, code, reason string) error {
	return &ValidationError{Issues: []ValidationIssue{{Path: path, Code: code, Reason: reason}}}
}

// rejectDuplicateKeys 在进入结构体解码前拒绝重复键，避免不同 JSON 解析器采用不同值。
func rejectDuplicateKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := walkJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON token")
	}
	return nil
}

func walkJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok || seen[key] {
				return errors.New("duplicate JSON key")
			}
			seen[key] = true
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return errors.New("invalid JSON object")
		}
	case '[':
		for decoder.More() {
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return errors.New("invalid JSON array")
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	return nil
}
