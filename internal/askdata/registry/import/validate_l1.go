package registryimport

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"intelligent-report-generation-system/internal/askdata"
)

var importCodePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,127}$`)

var requiredImportValues = map[AssetType][]string{
	AssetModel:            {"code", "name", "datasetVersionId", "entityCode", "grainDescription", "grainKeyFields", "ownerEmail"},
	AssetMeasure:          {"modelCode", "code", "name", "logicalFieldId", "defaultAggregation", "additivity", "nullPolicy"},
	AssetMetric:           {"code", "name", "modelCode", "formula", "additivity", "timeGrain", "displayPrecision", "zeroDenominatorPolicy", "ownerEmail"},
	AssetMetricDimension:  {"metricCode", "dimensionCode", "compatible", "role"},
	AssetDimension:        {"modelCode", "code", "name", "kind", "logicalFieldId", "sensitivity", "memberIndexPolicy", "groupable", "filterable", "sortable", "ownerEmail"},
	AssetMember:           {"dimensionCode", "canonicalValue", "displayLabel", "validFrom", "sensitivity"},
	AssetHierarchy:        {"code", "name", "levelOrder", "dimensionCode"},
	AssetRelationship:     {"leftModelCode", "rightModelCode", "joinAst", "joinType", "cardinality", "fanoutPolicy", "validFrom"},
	AssetTerm:             {"term", "termType", "targetCode", "matchMode", "priority", "validFrom", "source"},
	AssetCertifiedExample: {"question", "expectedMetricCodes"},
	AssetKPIBundle:        {"code", "name", "metricCodes", "defaultTimeExpression"},
	AssetEvalCase:         {"question", "actorRole", "expectedOutcome", "setType", "shardId"},
	AssetKnowledge:        {"code", "name", "knowledgeKind", "authority", "body"},
}

var listImportColumns = map[string]struct{}{
	"grainKeyFields": {}, "nonAdditiveDimensionCodes": {}, "positiveExamples": {},
	"negativeExamples": {}, "aliases": {}, "hierarchyPath": {}, "negativeContexts": {},
	"expectedMetricCodes": {}, "expectedDimensionCodes": {}, "expectedMemberValues": {},
	"applicableRoles": {}, "metricCodes": {}, "defaultDimensionCodes": {},
	"defaultChartTypes": {}, "applicableQuestionTypes": {}, "synonyms": {},
}

var integerImportColumns = map[string]struct{}{
	"displayPrecision": {}, "priority": {}, "levelOrder": {}, "shardId": {},
}

var dateImportColumns = map[string]struct{}{"validFrom": {}, "validTo": {}}

var jsonObjectImportColumns = map[string]struct{}{
	"formula": {}, "defaultFilter": {}, "joinAst": {}, "roleMapping": {}, "expectedResultHint": {},
}

func parseAndValidateL1(assetType AssetType, input RawImportRow) parsedImportRow {
	result := parsedImportRow{
		RowNo: input.RowNo, Type: assetType,
		Raw:    append(json.RawMessage(nil), input.Raw...),
		Values: map[string]string{},
	}
	definition, err := TemplateDefinitionFor(assetType)
	if err != nil {
		addIssue(&result, "row", ImportTypeInvalid, "资产类型没有导入合同", "12 类受支持的 assetType", string(assetType))
		return result
	}
	var rawValues map[string]any
	if err := askdata.DecodeStrictJSON(input.Raw, &rawValues); err != nil || rawValues == nil {
		addIssue(&result, "row", ImportTypeInvalid, "导入行不是合法 JSON 对象", "UTF-8 JSON object", string(input.Raw))
		return result
	}
	for _, column := range definition.Columns {
		value, exists := rawValues[column.Name]
		if !exists {
			addIssue(&result, column.Name, ImportRequiredMissing, "模板列缺失", "保留模板列 "+column.Name, "")
			continue
		}
		text, ok := value.(string)
		if !ok {
			addIssue(&result, column.Name, ImportTypeInvalid, "单元格必须是文本值", "string", fmt.Sprintf("%T", value))
			continue
		}
		text = strings.TrimSpace(text)
		result.Values[column.Name] = text
		validateL1Cell(&result, column, text)
	}
	for _, column := range requiredImportValues[assetType] {
		if strings.TrimSpace(result.Values[column]) == "" {
			addIssue(&result, column, ImportRequiredMissing, "必填值为空", "非空值", "")
		}
	}
	if assetType == AssetTerm && result.Values["targetCode"] != "" {
		if result.Values["termType"] == "MEMBER" {
			if _, _, err := parseMemberTargetCode(result.Values["targetCode"]); err != nil {
				addIssue(&result, "targetCode", ImportTypeInvalid,
					"成员词典目标缺少维度限定或成员值非法",
					"dimensionCode::canonicalValue", result.Values["targetCode"])
			}
		} else if !importCodePattern.MatchString(result.Values["targetCode"]) {
			addIssue(&result, "targetCode", ImportTypeInvalid, "语义 code 格式非法",
				"字母开头，仅含字母、数字、下划线，最长 128", result.Values["targetCode"])
		}
	}
	if assetType == AssetKnowledge {
		// 正文与 business_term_versions.definition 同宽（<=4000），提前在 L1
		// 拦下而不是等到提交事务里由约束拒绝。
		if len(result.Values["body"]) > 4000 {
			addIssue(&result, "body", ImportTypeInvalid, "知识正文超过治理长度上限",
				"不超过 4000 字节的 UTF-8 文本", result.Values["body"][:64]+"…")
		}
		if target := result.Values["targetCode"]; target != "" {
			if result.Values["targetType"] == "MEMBER" {
				if _, _, err := parseMemberTargetCode(target); err != nil {
					addIssue(&result, "targetCode", ImportTypeInvalid,
						"成员目标缺少维度限定或成员值非法",
						"dimensionCode::canonicalValue", target)
				}
			} else if !importCodePattern.MatchString(target) {
				addIssue(&result, "targetCode", ImportTypeInvalid, "语义 code 格式非法",
					"字母开头，仅含字母、数字、下划线，最长 128", target)
			}
		}
	}
	if len(result.Issues) == 0 {
		result.Layer = 1
	}
	return result
}

func validateL1Cell(row *parsedImportRow, column TemplateColumn, value string) {
	if value == "" {
		return
	}
	if len(value) > 32_768 || !utf8.ValidString(value) || strings.IndexFunc(value, func(r rune) bool {
		return r < 0x20 && r != '\t'
	}) >= 0 {
		addIssue(row, column.Name, ImportTypeInvalid, "值包含控制字符或超过长度上限", "不超过 32768 字节的 UTF-8 文本", value)
		return
	}
	if len(column.EnumValues) > 0 && !containsExact(column.EnumValues, value) {
		addIssue(row, column.Name, ImportEnumInvalid, "值不在受控枚举中", strings.Join(column.EnumValues, " | "), value)
	}
	if _, list := listImportColumns[column.Name]; list {
		if err := validateListCell(value); err != nil {
			addIssue(row, column.Name, ImportTypeInvalid, "多值列格式错误", "使用 | 分隔且每项非空；不得使用逗号或分号", value)
		}
	}
	if _, integer := integerImportColumns[column.Name]; integer {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 || parsed > 1_000_000 {
			addIssue(row, column.Name, ImportTypeInvalid, "整数格式或范围非法", "0 到 1000000 的十进制整数", value)
		}
	}
	if _, date := dateImportColumns[column.Name]; date {
		if !validImportDate(value) {
			addIssue(row, column.Name, ImportTypeInvalid, "日期时间格式非法", "YYYY-MM-DD 或 RFC3339", value)
		}
	}
	if column.Name == "datasetVersionId" && !canonicalUUID(value) {
		addIssue(row, column.Name, ImportTypeInvalid, "数据集版本 ID 非法", "canonical UUID", value)
	}
	if _, document := jsonObjectImportColumns[column.Name]; document {
		if !validImportJSONObject(value) {
			code := ImportTypeInvalid
			if column.Name == "formula" {
				code = ImportFormulaInvalid
			}
			addIssue(row, column.Name, code, "受控表达式不是合法安全 JSON 对象", "无 sql/query/statement 键且深度受限的 JSON object", value)
		}
	}
	// TERM.targetCode is context-sensitive: MEMBER targets use the qualified
	// dimensionCode::canonicalValue contract and are validated after termType
	// has been parsed. Every other code column uses the ordinary code grammar.
	if isCodeColumn(column.Name) && column.Name != "targetCode" && !importCodePattern.MatchString(value) {
		addIssue(row, column.Name, ImportTypeInvalid, "语义 code 格式非法", "字母开头，仅含字母、数字、下划线，最长 128", value)
	}
}

func containsExact(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func validateListCell(value string) error {
	if strings.ContainsAny(value, ",；;") {
		return ErrInvalidImportRow
	}
	parts := splitList(value)
	if len(parts) == 0 || len(parts) > 256 {
		return ErrInvalidImportRow
	}
	seen := map[string]struct{}{}
	for _, part := range parts {
		key := canonicalLookup(part)
		if key == "" || len(part) > 2048 {
			return ErrInvalidImportRow
		}
		if _, duplicate := seen[key]; duplicate {
			return ErrInvalidImportRow
		}
		seen[key] = struct{}{}
	}
	return nil
}

func splitList(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, "|")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		result = append(result, strings.TrimSpace(part))
	}
	return result
}

func validImportDate(value string) bool {
	if _, err := time.Parse("2006-01-02", value); err == nil {
		return true
	}
	_, err := time.Parse(time.RFC3339, value)
	return err == nil
}

func validImportJSONObject(value string) bool {
	if len(value) > 131_072 || !utf8.ValidString(value) {
		return false
	}
	var document map[string]any
	if err := askdata.DecodeStrictJSON([]byte(value), &document); err != nil || document == nil {
		return false
	}
	nodes := 0
	return validateImportJSONValue(document, 0, &nodes) == nil
}

func validateImportJSONValue(value any, depth int, nodes *int) error {
	if depth > 32 {
		return ErrInvalidImportRow
	}
	*nodes++
	if *nodes > 4096 {
		return ErrInvalidImportRow
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			normalized := strings.ReplaceAll(strings.ReplaceAll(strings.ToLower(key), "_", ""), "-", "")
			if normalized == "sql" || normalized == "rawsql" || normalized == "query" || normalized == "statement" {
				return ErrInvalidImportRow
			}
			if err := validateImportJSONValue(child, depth+1, nodes); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range typed {
			if err := validateImportJSONValue(child, depth+1, nodes); err != nil {
				return err
			}
		}
	case string:
		if len(typed) > 8192 || !utf8.ValidString(typed) {
			return ErrInvalidImportRow
		}
	case nil, bool, json.Number, float64:
	default:
		return ErrInvalidImportRow
	}
	return nil
}

func isCodeColumn(name string) bool {
	if name == "code" || name == "entityCode" || name == "timeContractCode" || name == "bridgeModelCode" {
		return true
	}
	if strings.HasSuffix(name, "Code") {
		return true
	}
	return false
}

func compactJSON(value string) string {
	buffer := bytes.Buffer{}
	if json.Compact(&buffer, []byte(value)) != nil {
		return value
	}
	return buffer.String()
}
