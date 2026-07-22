// Package semanticquality contains deterministic compatibility rules shared by metadata
// persistence and semantic indexing. The rules are deliberately provider-independent.
package semanticquality

import "strings"

// Compatible rejects semantic roles whose physical representation cannot safely support them.
// Unknown and business-only semantic roles remain compatible; downstream domains still apply
// their own stricter rules when a field is used as a measure, time field or predicate.
func Compatible(canonicalType, semanticType string) bool {
	canonicalType = normalize(canonicalType)
	semanticType = normalize(semanticType)
	if semanticType == "" {
		return true
	}
	switch semanticType {
	case "AMOUNT", "PERCENTAGE", "QUANTITY":
		return numeric(canonicalType)
	case "DATE":
		return canonicalType == "DATE" || canonicalType == "DATETIME" || canonicalType == "TIMESTAMP"
	case "TIME":
		return canonicalType == "TIME" || canonicalType == "DATETIME" || canonicalType == "TIMESTAMP"
	case "DATETIME":
		return canonicalType == "DATETIME" || canonicalType == "TIMESTAMP"
	case "BOOLEAN":
		return canonicalType == "BOOLEAN" || canonicalType == "BOOL"
	default:
		return true
	}
}

func numeric(value string) bool {
	switch value {
	case "INTEGER", "INT", "BIGINT", "SMALLINT", "TINYINT", "DECIMAL", "NUMERIC", "NUMBER", "FLOAT", "DOUBLE", "REAL":
		return true
	default:
		return false
	}
}

func normalize(value string) string {
	return strings.ToUpper(strings.TrimSpace(value))
}
