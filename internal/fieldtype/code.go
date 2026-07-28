package fieldtype

import (
	"regexp"
	"strings"
	"unicode"
)

var englishCodeToken = regexp.MustCompile(
	`(^|[_\s-])(id|code|no|number|identifier)($|[_\s-])`,
)

var chineseCodeMarkers = []string{
	"编码", "编号", "代码", "代号", "标识",
	"证件号", "手机号", "电话号码", "电话号",
	"账号", "卡号", "单号",
}

// IsCodeLike reports whether any supplied field label describes a business
// identifier rather than a numeric measure. Code-like values must remain text:
// leading zeros and late alphanumeric values are both semantically meaningful.
func IsCodeLike(labels ...string) bool {
	for _, label := range labels {
		label = strings.TrimSpace(label)
		if label == "" {
			continue
		}
		for _, marker := range chineseCodeMarkers {
			if strings.Contains(label, marker) {
				return true
			}
		}
		if englishCodeToken.MatchString(normalizeEnglishLabel(label)) {
			return true
		}
	}
	return false
}

func normalizeEnglishLabel(value string) string {
	runes := []rune(value)
	var builder strings.Builder
	builder.Grow(len(value) + 4)
	for index, current := range runes {
		if index > 0 && unicode.IsUpper(current) &&
			(unicode.IsLower(runes[index-1]) || unicode.IsDigit(runes[index-1])) {
			builder.WriteByte('_')
		}
		builder.WriteRune(unicode.ToLower(current))
	}
	return builder.String()
}
