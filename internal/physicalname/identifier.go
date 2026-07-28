// Package physicalname defines the physical column-name contract shared by the
// dataset DSL, query compiler, and warehouse staging path.
package physicalname

import "regexp"

// columnIdentifier allows quoted database identifiers commonly produced by
// spreadsheets. Parentheses are intentionally supported for headers such as
// "单价值(分析)"; SQL delimiters, whitespace, qualification dots, operators,
// and statement separators remain outside the whitelist.
var columnIdentifier = regexp.MustCompile(`^[\p{L}][\p{L}\p{N}_$#()（）]{0,127}$`)

// ValidColumn reports whether value can cross every physical-column boundary.
// Callers still apply dialect-specific byte limits where necessary.
func ValidColumn(value string) bool {
	return columnIdentifier.MatchString(value)
}
