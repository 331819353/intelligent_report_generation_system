package datasource

import (
	"encoding/json"
	"strings"
	"time"
	"unicode"
)

// maskMetadataSampleRows 保留字段、空值和粗粒度格式，但不保留任何业务值。
// 它是发送给 LLM 前的最后一道本地转换，不依赖模型侧“请勿泄露”提示。
func maskMetadataSampleRows(rows []map[string]any) []map[string]any {
	masked := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		item := make(map[string]any, len(row))
		for key, value := range row {
			item[key] = maskMetadataSampleValue(value)
		}
		masked = append(masked, item)
	}
	return masked
}

func maskMetadataSampleValue(value any) any {
	switch typed := value.(type) {
	case nil:
		return nil
	case string:
		return maskMetadataSampleString(typed)
	case []byte:
		return "<bytes>"
	case bool:
		return false
	case int:
		return int(0)
	case int8:
		return int8(0)
	case int16:
		return int16(0)
	case int32:
		return int32(0)
	case int64:
		return int64(0)
	case uint:
		return uint(0)
	case uint8:
		return uint8(0)
	case uint16:
		return uint16(0)
	case uint32:
		return uint32(0)
	case uint64:
		return uint64(0)
	case float32:
		return float32(0)
	case float64:
		return float64(0)
	case json.Number:
		if strings.ContainsAny(string(typed), ".eE") {
			return json.Number("0.0")
		}
		return json.Number("0")
	case time.Time:
		return time.Date(2000, 1, 1, 0, 0, 0, 0, typed.Location())
	default:
		// 不调用 String/Stringer，避免未知驱动类型在格式化时把原值带出进程。
		return "<value>"
	}
}

func maskMetadataSampleString(value string) string {
	return strings.Map(func(char rune) rune {
		switch {
		case unicode.IsLetter(char):
			return 'X'
		case unicode.IsDigit(char):
			return '0'
		case unicode.IsSpace(char), unicode.IsPunct(char), unicode.IsSymbol(char):
			return char
		default:
			return 'X'
		}
	}, value)
}
