// Package shared contains contracts that must remain identical across the
// Ask Data and Report pipelines.
package shared

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"intelligent-report-generation-system/internal/askdata"
)

const (
	MaxRowKeyParts = 32
	MaxCitations   = 256
)

// TextSpan uses zero-based Unicode code-point offsets. End is exclusive.
type TextSpan struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

func (span TextSpan) MarshalJSON() ([]byte, error) {
	return json.Marshal([2]int{span.Start, span.End})
}

func (span *TextSpan) UnmarshalJSON(raw []byte) error {
	if span == nil {
		return errors.New("textSpan destination is nil")
	}
	var values []int
	if err := json.Unmarshal(raw, &values); err != nil {
		return errors.New("textSpan must be a two-integer array")
	}
	if len(values) != 2 {
		return errors.New("textSpan must contain exactly two integers")
	}
	span.Start, span.End = values[0], values[1]
	return nil
}

// RowKeyPart is one grouping coordinate. Parts are serialized in the exact
// order of the governed query's group-by keys, never map or lexical order.
type RowKeyPart struct {
	Key   string
	Value string
}

// CellRef is the single result-cell coordinate shared by Answer citations and
// report Evidence facts.
type CellRef struct {
	RowKey    string `json:"rowKey"`
	ColumnKey string `json:"columnKey"`
}

type CitationKind string

const (
	CitationResultCell CitationKind = "RESULT_CELL"
	CitationContract   CitationKind = "CONTRACT"
	CitationTimeSpec   CitationKind = "TIME_SPEC"
)

// Citation is a closed tagged union. Exactly one coordinate shape is valid
// for each Kind; inapplicable fields are omitted from JSON.
type Citation struct {
	TextSpan   TextSpan     `json:"textSpan"`
	Kind       CitationKind `json:"kind"`
	RowKey     *string      `json:"rowKey,omitempty"`
	ColumnKey  *string      `json:"columnKey,omitempty"`
	ContractID *askdata.ID  `json:"contractId,omitempty"`
}

func NewResultCellCitation(span TextSpan, ref CellRef) Citation {
	rowKey, columnKey := ref.RowKey, ref.ColumnKey
	return Citation{TextSpan: span, Kind: CitationResultCell, RowKey: &rowKey, ColumnKey: &columnKey}
}

func NewContractCitation(span TextSpan, contractID askdata.ID) Citation {
	return Citation{TextSpan: span, Kind: CitationContract, ContractID: &contractID}
}

func NewTimeSpecCitation(span TextSpan) Citation {
	return Citation{TextSpan: span, Kind: CitationTimeSpec}
}

func (citation Citation) CellRef() (CellRef, bool) {
	if citation.Kind != CitationResultCell || citation.RowKey == nil || citation.ColumnKey == nil {
		return CellRef{}, false
	}
	ref := CellRef{RowKey: *citation.RowKey, ColumnKey: *citation.ColumnKey}
	return ref, ref.Validate() == nil
}

func (citation Citation) Validate() error {
	if citation.TextSpan.Start < 0 || citation.TextSpan.End <= citation.TextSpan.Start {
		return errors.New("textSpan must be a non-empty Unicode code-point interval")
	}
	switch citation.Kind {
	case CitationResultCell:
		if citation.RowKey == nil || citation.ColumnKey == nil || citation.ContractID != nil {
			return errors.New("RESULT_CELL citation requires only rowKey and columnKey")
		}
		return (CellRef{RowKey: *citation.RowKey, ColumnKey: *citation.ColumnKey}).Validate()
	case CitationContract:
		if citation.ContractID == nil || citation.RowKey != nil || citation.ColumnKey != nil {
			return errors.New("CONTRACT citation requires only contractId")
		}
		return citation.ContractID.Validate()
	case CitationTimeSpec:
		if citation.RowKey != nil || citation.ColumnKey != nil || citation.ContractID != nil {
			return errors.New("TIME_SPEC citation has no external coordinate fields")
		}
		return nil
	default:
		return fmt.Errorf("unsupported citation kind %q", citation.Kind)
	}
}

func (ref CellRef) Validate() error {
	if _, err := ParseRowKey(ref.RowKey); err != nil {
		return fmt.Errorf("rowKey: %w", err)
	}
	if err := askdata.ID(ref.ColumnKey).Validate(); err != nil {
		return fmt.Errorf("columnKey: %w", err)
	}
	return nil
}

// FormatRowKey encodes grouping values using an unambiguous percent-encoded
// form: key=value|key=value. The caller supplies the governed group-by order.
func FormatRowKey(parts []RowKeyPart) (string, error) {
	if len(parts) == 0 || len(parts) > MaxRowKeyParts {
		return "", fmt.Errorf("row key parts must contain between 1 and %d items", MaxRowKeyParts)
	}
	seen := make(map[string]struct{}, len(parts))
	encoded := make([]string, len(parts))
	for index, part := range parts {
		if err := validateCoordinateText(part.Key); err != nil {
			return "", fmt.Errorf("parts[%d].key: %w", index, err)
		}
		if err := validateCoordinateText(part.Value); err != nil {
			return "", fmt.Errorf("parts[%d].value: %w", index, err)
		}
		if _, exists := seen[part.Key]; exists {
			return "", fmt.Errorf("parts[%d] duplicates key %q", index, part.Key)
		}
		seen[part.Key] = struct{}{}
		encoded[index] = percentEncode(part.Key) + "=" + percentEncode(part.Value)
	}
	return strings.Join(encoded, "|"), nil
}

func ParseRowKey(value string) ([]RowKeyPart, error) {
	if value == "" || len(value) > 8192 || !utf8.ValidString(value) {
		return nil, errors.New("row key is empty, too large or invalid UTF-8")
	}
	segments := strings.Split(value, "|")
	if len(segments) == 0 || len(segments) > MaxRowKeyParts {
		return nil, fmt.Errorf("row key must contain between 1 and %d parts", MaxRowKeyParts)
	}
	parts := make([]RowKeyPart, len(segments))
	seen := make(map[string]struct{}, len(segments))
	for index, segment := range segments {
		if strings.Count(segment, "=") != 1 {
			return nil, fmt.Errorf("part %d must contain one coordinate separator", index)
		}
		rawKey, rawValue, _ := strings.Cut(segment, "=")
		key, err := percentDecode(rawKey)
		if err != nil {
			return nil, fmt.Errorf("part %d key: %w", index, err)
		}
		coordinateValue, err := percentDecode(rawValue)
		if err != nil {
			return nil, fmt.Errorf("part %d value: %w", index, err)
		}
		if err := validateCoordinateText(key); err != nil {
			return nil, fmt.Errorf("part %d key: %w", index, err)
		}
		if err := validateCoordinateText(coordinateValue); err != nil {
			return nil, fmt.Errorf("part %d value: %w", index, err)
		}
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("part %d duplicates key %q", index, key)
		}
		seen[key] = struct{}{}
		parts[index] = RowKeyPart{Key: key, Value: coordinateValue}
	}
	canonical, err := FormatRowKey(parts)
	if err != nil || canonical != value {
		return nil, errors.New("row key is not in canonical coordinate form")
	}
	return parts, nil
}

func NormalizeCitations(citations []Citation) []Citation {
	result := append([]Citation(nil), citations...)
	sort.SliceStable(result, func(left, right int) bool {
		if result[left].TextSpan.Start != result[right].TextSpan.Start {
			return result[left].TextSpan.Start < result[right].TextSpan.Start
		}
		if result[left].TextSpan.End != result[right].TextSpan.End {
			return result[left].TextSpan.End < result[right].TextSpan.End
		}
		return result[left].Kind < result[right].Kind
	})
	if result == nil {
		return []Citation{}
	}
	return result
}

func ValidateCitations(text string, citations []Citation) error {
	if !utf8.ValidString(text) {
		return errors.New("citation text is invalid UTF-8")
	}
	if len(citations) > MaxCitations {
		return fmt.Errorf("citations exceeds %d items", MaxCitations)
	}
	length := utf8.RuneCountInString(text)
	normalized := NormalizeCitations(citations)
	for index, citation := range normalized {
		if err := citation.Validate(); err != nil {
			return fmt.Errorf("citations[%d]: %w", index, err)
		}
		if citation.TextSpan.End > length {
			return fmt.Errorf("citations[%d].textSpan is outside the narrative text", index)
		}
		if index > 0 && citation.TextSpan.Start < normalized[index-1].TextSpan.End {
			return fmt.Errorf("citations[%d].textSpan overlaps the previous citation", index)
		}
	}
	return nil
}

func validateCoordinateText(value string) error {
	if strings.TrimSpace(value) == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > 256 || strings.ContainsAny(value, "\x00\r\n") {
		return errors.New("coordinate must be non-empty valid UTF-8 with at most 256 code points and no controls")
	}
	return nil
}

func percentEncode(value string) string {
	const hexadecimal = "0123456789ABCDEF"
	var builder strings.Builder
	for _, character := range []byte(value) {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || strings.ContainsRune("-._~", rune(character)) {
			builder.WriteByte(character)
			continue
		}
		builder.WriteByte('%')
		builder.WriteByte(hexadecimal[character>>4])
		builder.WriteByte(hexadecimal[character&15])
	}
	return builder.String()
}

func percentDecode(value string) (string, error) {
	decoded := make([]byte, 0, len(value))
	for index := 0; index < len(value); index++ {
		character := value[index]
		if character == '%' {
			if index+2 >= len(value) || !upperHex(value[index+1]) || !upperHex(value[index+2]) {
				return "", errors.New("percent escape must use two uppercase hexadecimal digits")
			}
			decoded = append(decoded, hexValue(value[index+1])<<4|hexValue(value[index+2]))
			index += 2
			continue
		}
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || strings.ContainsRune("-._~", rune(character))) {
			return "", errors.New("reserved or non-ASCII bytes must be percent encoded")
		}
		decoded = append(decoded, character)
	}
	if !utf8.Valid(decoded) {
		return "", errors.New("decoded coordinate is invalid UTF-8")
	}
	return string(decoded), nil
}

func upperHex(value byte) bool {
	return value >= '0' && value <= '9' || value >= 'A' && value <= 'F'
}

func hexValue(value byte) byte {
	if value <= '9' {
		return value - '0'
	}
	return value - 'A' + 10
}
