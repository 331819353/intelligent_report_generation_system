package metadataai

import "sort"

// OutputSchema 提供给支持严格结构化输出的模型，同时由 ValidateOutput 在本地复核；
// 无论上游是否声明遵守 Schema，本地域校验始终是最终可信边界。
var OutputSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"required":             []string{"schemaVersion", "table", "columns"},
	"properties": map[string]any{
		"schemaVersion": map[string]any{"type": "string", "const": SchemaVersion},
		"table":         valueSchema(false),
		"columns": map[string]any{
			"type":  "array",
			"items": valueSchema(true),
		},
	},
}

// valueSchema 构建表或字段建议的严格 JSON Schema 片段。
func valueSchema(column bool) map[string]any {
	required := []string{"targetId", "businessName", "businessDescription", "tags", "sensitivityLevel", "confidence"}
	properties := map[string]any{
		"targetId":            map[string]any{"type": "string", "minLength": 1},
		"businessName":        map[string]any{"type": "string", "minLength": 1, "maxLength": 120},
		"businessDescription": map[string]any{"type": "string", "minLength": 1, "maxLength": 1000},
		"tags": map[string]any{
			"type": "array", "maxItems": 12, "uniqueItems": true,
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
