package report

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"intelligent-report-generation-system/internal/askdata"
)

var (
	forbiddenKeyPattern = regexp.MustCompile(`(?i)^(?:.*sql.*|.*connection(?:string|uri).*|.*(?:password|passwd|credential|secret|apikey|accesskey|accesstoken|refreshtoken).*|dsn|scripts?|javascript)$`)
	htmlEventKeyPattern = regexp.MustCompile(`(?i)^on[a-z]+$`)
	sqlValuePattern     = regexp.MustCompile(`(?is)^\s*(?:select\s+.+|with\s+.+|insert\s+into\b|update\s+.+\s+set\b|delete\s+from\b|drop\s+(?:table|view|database)\b|alter\s+table\b|create\s+(?:table|view|database|user)\b|grant\s+.+\s+on\b|revoke\s+.+\s+on\b)`)
	connectionPattern   = regexp.MustCompile(`(?i)(?:postgres(?:ql)?|mysql|sqlserver|mongodb(?:\+srv)?|redis|jdbc):(?:/|\\)|(?:server|host)\s*=.+;.+(?:password|pwd|user id)\s*=`)
	scriptPattern       = regexp.MustCompile(`(?is)<\s*script\b|\bon[a-z]+\s*=`)
)

// Decode applies byte, depth and content guards before reusing the platform's
// strict decoder. It returns only a fully validated Report Definition V1.
func Decode(raw []byte) (ReportDefinition, error) {
	if err := inspectJSONDocument(raw); err != nil {
		return ReportDefinition{}, err
	}
	var definition ReportDefinition
	if err := askdata.DecodeStrictJSON(raw, &definition); err != nil {
		return ReportDefinition{}, err
	}
	for index := range definition.Components {
		definition.Components[index].Options.RichText = SanitizeRichText(
			definition.Components[index].Options.RichText,
		)
	}
	if err := definition.Validate(); err != nil {
		return ReportDefinition{}, err
	}
	return definition, nil
}

func validateEncodedDefinition(definition ReportDefinition) error {
	raw, err := json.Marshal(definition)
	if err != nil {
		return fmt.Errorf("encode report definition: %w", err)
	}
	return inspectJSONDocument(raw)
}

func inspectJSONDocument(raw []byte) error {
	if len(raw) > MaxDefinitionBytes {
		return fmt.Errorf("report definition exceeds %d bytes", MaxDefinitionBytes)
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return errors.New("report definition is required")
	}
	if !utf8.Valid(raw) {
		return errors.New("report definition contains invalid UTF-8")
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("inspect report definition: %w", err)
	}
	if _, ok := value.(map[string]any); !ok {
		return errors.New("report definition must be a JSON object")
	}
	return inspectValue(value, "$", 1, "")
}

func inspectValue(value any, path string, depth int, key string) error {
	if depth > MaxJSONDepth {
		return fmt.Errorf("report definition exceeds maximum JSON depth %d at %s", MaxJSONDepth, path)
	}
	switch typed := value.(type) {
	case map[string]any:
		for childKey, child := range typed {
			if utf8.RuneCountInString(childKey) > MaxStringLength {
				return fmt.Errorf("object key exceeds %d characters at %s", MaxStringLength, path)
			}
			if forbiddenKeyPattern.MatchString(childKey) || htmlEventKeyPattern.MatchString(childKey) {
				return fmt.Errorf("forbidden field %q at %s", childKey, path)
			}
			if err := inspectValue(child, path+"."+childKey, depth+1, childKey); err != nil {
				return err
			}
		}
	case []any:
		for index, child := range typed {
			if err := inspectValue(child, fmt.Sprintf("%s[%d]", path, index), depth+1, key); err != nil {
				return err
			}
		}
	case string:
		if key != "richText" && utf8.RuneCountInString(typed) > MaxStringLength {
			return fmt.Errorf("string at %s exceeds %d characters", path, MaxStringLength)
		}
		if key == "richText" && len(typed) > MaxRichTextBytes {
			return fmt.Errorf("rich text at %s exceeds %d bytes", path, MaxRichTextBytes)
		}
		if mayContainSQL(typed) && sqlValuePattern.MatchString(typed) {
			return fmt.Errorf("SQL content is forbidden at %s", path)
		}
		if strings.ContainsAny(typed, ":=") && connectionPattern.MatchString(typed) {
			return fmt.Errorf("connection string is forbidden at %s", path)
		}
		if key != "richText" && strings.ContainsAny(typed, "<=") && scriptPattern.MatchString(typed) {
			return fmt.Errorf("script or HTML event content is forbidden at %s", path)
		}
	}
	return nil
}

func mayContainSQL(value string) bool {
	value = strings.TrimLeft(value, " \t\n\f\r")
	if value == "" {
		return false
	}
	switch value[0] {
	case 's', 'S', 'w', 'W', 'i', 'I', 'u', 'U', 'd', 'D',
		'a', 'A', 'c', 'C', 'g', 'G', 'r', 'R':
		return true
	default:
		return false
	}
}
