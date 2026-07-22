package assetembedding

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

type tableFacts struct {
	SourceType          string
	CatalogName         string
	SchemaName          string
	TableName           string
	BusinessName        string
	BusinessDescription string
	Tags                []string
	Columns             []columnFacts
}

type columnFacts struct {
	ID                  string
	ColumnName          string
	BusinessName        string
	BusinessDescription string
	Tags                []string
	SemanticType        string
	CanonicalType       string
	OrdinalPosition     int
}

func tableDocument(value tableFacts) string {
	lines := []string{
		"文档版本：" + DocumentVersion,
		"资产类型：TABLE",
		"数据源类型：" + clean(value.SourceType),
		"Catalog：" + clean(value.CatalogName),
		"Schema：" + clean(value.SchemaName),
		"物理表名：" + clean(value.TableName),
		"业务表名：" + clean(value.BusinessName),
		"业务描述：" + clean(value.BusinessDescription),
		"表标签：" + strings.Join(normalizeTags(value.Tags), "、"),
		"活动字段：",
	}
	columns := append([]columnFacts(nil), value.Columns...)
	sort.SliceStable(columns, func(i, j int) bool { return columns[i].OrdinalPosition < columns[j].OrdinalPosition })
	for _, column := range columns {
		lines = append(lines, fmt.Sprintf("- %s｜%s｜%s｜%s｜%s｜%s",
			clean(column.ColumnName), clean(column.BusinessName), clean(column.BusinessDescription),
			clean(column.SemanticType), clean(column.CanonicalType), strings.Join(normalizeTags(column.Tags), "、")))
	}
	return strings.Join(lines, "\n")
}

func columnDocument(table tableFacts, column columnFacts) string {
	return strings.Join([]string{
		"文档版本：" + DocumentVersion,
		"资产类型：COLUMN",
		"数据源类型：" + clean(table.SourceType),
		"所属表物理名：" + clean(table.TableName),
		"所属表业务名：" + clean(table.BusinessName),
		"字段物理名：" + clean(column.ColumnName),
		"字段业务名：" + clean(column.BusinessName),
		"字段描述：" + clean(column.BusinessDescription),
		"字段标签：" + strings.Join(normalizeTags(column.Tags), "、"),
		"语义类型：" + clean(column.SemanticType),
		"规范类型：" + clean(column.CanonicalType),
	}, "\n")
}

func inputHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func normalizeTags(values []string) []string {
	result := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = clean(value)
		key := strings.ToLower(value)
		if value == "" || seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func clean(value string) string {
	value = strings.TrimSpace(strings.ToValidUTF8(value, "�"))
	return strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return ' '
		}
		return character
	}, value)
}
