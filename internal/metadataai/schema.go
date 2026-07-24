package metadataai

import "sort"

// outputSchema 按本次输入固定表 ID、字段 ID 集合和字段数量；
// 无论上游是否声明遵守 Schema，ValidateOutput 仍是最终可信边界。
func outputSchema(input CompletionInput) map[string]any {
	columnIDs := make([]string, 0, len(input.Columns))
	for _, column := range input.Columns {
		columnIDs = append(columnIDs, column.ID)
	}
	column := valueSchema(true)
	if isFileSourceFormat(input.SourceFormat) {
		properties := column["properties"].(map[string]any)
		// deepseek-v3 的严格 Schema 方言对 pattern 的兼容性不稳定；Schema 负责向模型说明合同，
		// 最终格式强制由 ValidateOutput 的 Go 正则和中文字符检查完成。
		properties["businessName"].(map[string]any)["description"] = "文件字段映射名称：小写英文 snake_case，多个单词使用下划线分隔"
		properties["businessDescription"].(map[string]any)["description"] = "文件字段中文业务描述，可包含 ID、SKU 等英文缩写"
	}
	// 仅表头变化时 columns 必须是空数组；空 enum 不符合 JSON Schema，因此无需再约束不可出现的 item。
	if len(columnIDs) > 0 {
		column["properties"].(map[string]any)["targetId"] = map[string]any{"type": "string", "enum": columnIDs}
	}

	required := []string{"schemaVersion", "columns"}
	properties := map[string]any{
		"schemaVersion": map[string]any{"type": "string", "const": SchemaVersion},
		"columns": map[string]any{
			"type": "array", "minItems": len(columnIDs), "maxItems": len(columnIDs),
			"items": column,
		},
	}
	if input.TargetTable {
		table := valueSchema(false)
		table["properties"].(map[string]any)["targetId"] = map[string]any{"type": "string", "const": input.Table.ID}
		if isFileSourceFormat(input.SourceFormat) {
			table["properties"].(map[string]any)["businessName"].(map[string]any)["description"] = "文件表中文业务名称"
		}
		required = append(required, "table")
		properties["table"] = table
	}
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             required,
		"properties":           properties,
	}
}

// valueSchema 构建表或字段建议的严格 JSON Schema 片段。
func valueSchema(column bool) map[string]any {
	required := []string{"targetId", "businessName", "businessDescription", "tags", "sensitivityLevel", "confidence"}
	properties := map[string]any{
		"targetId":            map[string]any{"type": "string", "minLength": 1},
		"businessName":        map[string]any{"type": "string", "minLength": 1, "maxLength": 120},
		"businessDescription": map[string]any{"type": "string", "minLength": 1, "maxLength": 1000},
		"tags": map[string]any{
			// 不设置人为数量上限；受控词表本身、输出 Token 预算和本地去重共同
			// 提供有界保护。deepseek-v3 不支持的 uniqueItems 由 Go 校验兜底。
			"type":  "array",
			"items": map[string]any{"type": "string", "enum": mapKeys(allowedTags)},
		},
		"sensitivityLevel": map[string]any{"type": "string", "enum": mapKeys(allowedSensitivity)},
		"confidence":       map[string]any{"type": "number", "exclusiveMinimum": 0, "maximum": 1},
	}
	if column {
		required = append(required, "semanticType")
		properties["semanticType"] = map[string]any{"type": "string", "enum": mapKeys(allowedSemanticTypes)}
	}
	return map[string]any{"type": "object", "additionalProperties": false, "required": required, "properties": properties}
}

// mapKeys 提取并排序枚举键，保证生成的 Schema 稳定。
func mapKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
