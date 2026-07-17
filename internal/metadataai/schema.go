package metadataai

import "sort"

// outputSchema 按本次输入固定表 ID、字段 ID 集合和字段数量；
// 无论上游是否声明遵守 Schema，ValidateOutput 仍是最终可信边界。
func outputSchema(input CompletionInput) map[string]any {
	table := valueSchema(false)
	table["properties"].(map[string]any)["targetId"] = map[string]any{"type": "string", "const": input.Table.ID}

	columnIDs := make([]string, 0, len(input.Columns))
	for _, column := range input.Columns {
		columnIDs = append(columnIDs, column.ID)
	}
	column := valueSchema(true)
	if len(columnIDs) > 0 {
		column["properties"].(map[string]any)["targetId"] = map[string]any{"type": "string", "enum": columnIDs}
	}

	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"schemaVersion", "table", "columns"},
		"properties": map[string]any{
			"schemaVersion": map[string]any{"type": "string", "const": SchemaVersion},
			"table":         table,
			"columns": map[string]any{
				"type": "array", "minItems": len(columnIDs), "maxItems": len(columnIDs),
				"items": column,
			},
		},
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
			// deepseek-v3 的严格 Schema 语法不支持 uniqueItems；重复标签仍由 ValidateOutput 拒绝。
			"type": "array", "maxItems": 12,
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
