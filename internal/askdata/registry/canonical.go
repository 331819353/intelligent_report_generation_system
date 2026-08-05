package registry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"intelligent-report-generation-system/internal/askdata"
)

// CanonicalJSON produces the registry's stable JSON representation. Object
// keys are sorted, insignificant whitespace is removed, and mathematically
// equivalent JSON decimal spellings such as 1, 1.0 and 1e0 are normalized.
func CanonicalJSON(raw []byte) ([]byte, error) {
	var strict any
	if err := askdata.DecodeStrictJSON(raw, &strict); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("decode canonical JSON: %w", err)
	}
	if err := ensureCanonicalEOF(decoder); err != nil {
		return nil, err
	}
	var output bytes.Buffer
	if err := writeCanonicalJSON(&output, value, 0); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func CanonicalValue(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal canonical value: %w", err)
	}
	return CanonicalJSON(raw)
}

func CanonicalContentHash(value any) (askdata.ContentHash, []byte, error) {
	canonical, err := CanonicalValue(value)
	if err != nil {
		return "", nil, err
	}
	return askdata.HashBytes(canonical), canonical, nil
}

func writeCanonicalJSON(output *bytes.Buffer, value any, depth int) error {
	if depth > 128 {
		return errors.New("canonical JSON exceeds maximum nesting")
	}
	switch typed := value.(type) {
	case nil:
		output.WriteString("null")
	case bool:
		if typed {
			output.WriteString("true")
		} else {
			output.WriteString("false")
		}
	case string:
		raw, _ := json.Marshal(typed)
		output.Write(raw)
	case json.Number:
		normalized, err := canonicalNumber(string(typed))
		if err != nil {
			return err
		}
		output.WriteString(normalized)
	case []any:
		output.WriteByte('[')
		for index, child := range typed {
			if index > 0 {
				output.WriteByte(',')
			}
			if err := writeCanonicalJSON(output, child, depth+1); err != nil {
				return err
			}
		}
		output.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		output.WriteByte('{')
		for index, key := range keys {
			if index > 0 {
				output.WriteByte(',')
			}
			rawKey, _ := json.Marshal(key)
			output.Write(rawKey)
			output.WriteByte(':')
			if err := writeCanonicalJSON(output, typed[key], depth+1); err != nil {
				return err
			}
		}
		output.WriteByte('}')
	default:
		return fmt.Errorf("unsupported canonical JSON value %T", value)
	}
	return nil
}

func canonicalNumber(input string) (string, error) {
	if len(input) == 0 || len(input) > 128 {
		return "", errors.New("canonical JSON number has invalid length")
	}
	negative := strings.HasPrefix(input, "-")
	if negative {
		input = input[1:]
	}
	exponent := 0
	if position := strings.IndexAny(input, "eE"); position >= 0 {
		parsed, err := strconv.Atoi(input[position+1:])
		if err != nil || parsed < -1024 || parsed > 1024 {
			return "", errors.New("canonical JSON exponent is out of bounds")
		}
		exponent = parsed
		input = input[:position]
	}
	integer, fraction := input, ""
	if position := strings.IndexByte(input, '.'); position >= 0 {
		integer, fraction = input[:position], input[position+1:]
	}
	digits := integer + fraction
	leading := len(digits) - len(strings.TrimLeft(digits, "0"))
	digits = strings.TrimLeft(digits, "0")
	if digits == "" {
		return "0", nil
	}
	decimalPosition := len(integer) + exponent - leading
	var normalized string
	switch {
	case decimalPosition <= 0:
		normalized = "0." + strings.Repeat("0", -decimalPosition) + digits
	case decimalPosition >= len(digits):
		normalized = digits + strings.Repeat("0", decimalPosition-len(digits))
	default:
		normalized = digits[:decimalPosition] + "." + digits[decimalPosition:]
	}
	if strings.Contains(normalized, ".") {
		normalized = strings.TrimRight(normalized, "0")
		normalized = strings.TrimRight(normalized, ".")
	}
	if negative {
		normalized = "-" + normalized
	}
	return normalized, nil
}

func ensureCanonicalEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("canonical JSON must contain exactly one value")
		}
		return err
	}
	return nil
}
