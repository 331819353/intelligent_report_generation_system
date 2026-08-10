// Package evaluation contains deterministic, storage-independent evaluation
// primitives. It compares semantic intent and typed results, never SQL text.
package evaluation

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/ir"
)

const (
	EquivalenceVersion       = "result-equivalence-v1"
	MaxResultColumns         = 256
	MaxResultRows            = ir.MaxLimit
	MaxResultCells           = 1_000_000
	MaxCellRunes             = 32_768
	MaxNormalizedResultBytes = 64 << 20
	MaxDifferences           = 64
)

var (
	ErrInvalidResult      = errors.New("evaluation result is invalid")
	ErrDuplicateResultKey = errors.New("evaluation result contains a duplicate key")
	columnNamePattern     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.:@/-]{0,127}$`)
	decimalTextPattern    = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)
)

type ScalarType string

const (
	ScalarString   ScalarType = "STRING"
	ScalarInteger  ScalarType = "INTEGER"
	ScalarDecimal  ScalarType = "DECIMAL"
	ScalarFloat    ScalarType = "FLOAT"
	ScalarBoolean  ScalarType = "BOOLEAN"
	ScalarDate     ScalarType = "DATE"
	ScalarDateTime ScalarType = "DATETIME"
)

// Column declares the trusted comparison type for one logical result column.
// Timezone is mandatory for DATETIME so zone-less warehouse values cannot be
// interpreted using a machine-local default. FLOAT columns cannot be keys
// because approximate equality is not a safe identity relation.
type Column struct {
	Name     string     `json:"name"`
	Type     ScalarType `json:"type"`
	Key      bool       `json:"key"`
	Timezone string     `json:"timezone"`
}

type ResultSchema struct {
	Columns []Column `json:"columns"`
}

// ResultSet is the small, bounded logical output accepted from fixtures or a
// query runtime. Durations, warnings and physical metadata are intentionally
// absent because they do not determine semantic result equivalence.
type ResultSet struct {
	Columns []string `json:"columns"`
	Rows    [][]any  `json:"rows"`
}

// NormalizedValue keeps NULL distinct from an empty string and stores every
// non-null scalar in a deterministic textual representation. DECIMAL uses an
// exact reduced rational representation; FLOAT keeps its exact float64 value
// and applies tolerance only during comparison.
type NormalizedValue struct {
	Type  ScalarType `json:"type"`
	Null  bool       `json:"null"`
	Value string     `json:"value"`
}

type NormalizedResult struct {
	SchemaVersion string              `json:"schemaVersion"`
	Columns       []Column            `json:"columns"`
	Rows          [][]NormalizedValue `json:"rows"`
	RowCount      int                 `json:"rowCount"`
	ContentHash   askdata.ContentHash `json:"contentHash"`
}

type Artifact struct {
	IR                 ir.SemanticIR        `json:"ir"`
	Result             ResultSet            `json:"result"`
	DeclaredResultHash *askdata.ContentHash `json:"declaredResultHash"`
}

type FloatTolerance struct {
	Absolute float64 `json:"absolute"`
	Relative float64 `json:"relative"`
}

func DefaultFloatTolerance() FloatTolerance {
	return FloatTolerance{Absolute: 1e-9, Relative: 1e-9}
}

type ComparisonRequest struct {
	Schema         ResultSchema   `json:"schema"`
	Expected       Artifact       `json:"expected"`
	Actual         Artifact       `json:"actual"`
	FloatTolerance FloatTolerance `json:"floatTolerance"`
}

type Difference struct {
	Code string `json:"code"`
	Path string `json:"path"`
}

// EquivalenceReport distinguishes semantic equality from byte-exact result
// equality. Near FLOAT values may be ResultEquivalent while ExactResultHashMatch
// is false; both normalized hashes remain validated and are always reported.
type EquivalenceReport struct {
	SchemaVersion        string              `json:"schemaVersion"`
	Equivalent           bool                `json:"equivalent"`
	IREquivalent         bool                `json:"irEquivalent"`
	ResultEquivalent     bool                `json:"resultEquivalent"`
	IRHashMatch          bool                `json:"irHashMatch"`
	ExactResultHashMatch bool                `json:"exactResultHashMatch"`
	ExpectedIRHash       askdata.ContentHash `json:"expectedIrHash"`
	ActualIRHash         askdata.ContentHash `json:"actualIrHash"`
	ExpectedResultHash   askdata.ContentHash `json:"expectedResultHash"`
	ActualResultHash     askdata.ContentHash `json:"actualResultHash"`
	ExpectedRowCount     int                 `json:"expectedRowCount"`
	ActualRowCount       int                 `json:"actualRowCount"`
	Differences          []Difference        `json:"differences"`
}

func (column Column) Validate() error {
	if !columnNamePattern.MatchString(column.Name) || !validScalarType(column.Type) {
		return errors.New("column name or type is invalid")
	}
	if column.Type == ScalarDateTime {
		if strings.TrimSpace(column.Timezone) == "" || column.Timezone != strings.TrimSpace(column.Timezone) {
			return errors.New("DATETIME column requires a timezone")
		}
		if _, err := time.LoadLocation(column.Timezone); err != nil {
			return errors.New("DATETIME column timezone must be a known IANA timezone")
		}
	} else if column.Timezone != "" {
		return errors.New("timezone is only allowed for DATETIME columns")
	}
	if column.Key && column.Type == ScalarFloat {
		return errors.New("FLOAT column cannot be a result key")
	}
	return nil
}

func (schema ResultSchema) Validate() error {
	if schema.Columns == nil || len(schema.Columns) < 1 || len(schema.Columns) > MaxResultColumns {
		return fmt.Errorf("result schema columns count must be between 1 and %d", MaxResultColumns)
	}
	seen := make(map[string]struct{}, len(schema.Columns))
	for index, column := range schema.Columns {
		if err := column.Validate(); err != nil {
			return fmt.Errorf("columns[%d]: %w", index, err)
		}
		if _, duplicate := seen[column.Name]; duplicate {
			return fmt.Errorf("columns[%d] duplicates name %q", index, column.Name)
		}
		seen[column.Name] = struct{}{}
	}
	return nil
}

func (tolerance FloatTolerance) Validate() error {
	if math.IsNaN(tolerance.Absolute) || math.IsInf(tolerance.Absolute, 0) || tolerance.Absolute < 0 || tolerance.Absolute > 1e12 {
		return errors.New("absolute float tolerance is invalid")
	}
	if math.IsNaN(tolerance.Relative) || math.IsInf(tolerance.Relative, 0) || tolerance.Relative < 0 || tolerance.Relative > 1 {
		return errors.New("relative float tolerance is invalid")
	}
	return nil
}

// NormalizeResult validates the logical schema, reorders input columns by
// stable name, canonicalizes values, rejects duplicate keys and sorts rows.
func NormalizeResult(schema ResultSchema, input ResultSet) (NormalizedResult, error) {
	if err := schema.Validate(); err != nil {
		return NormalizedResult{}, fmt.Errorf("%w: schema: %v", ErrInvalidResult, err)
	}
	if input.Columns == nil || input.Rows == nil || len(input.Columns) != len(schema.Columns) || len(input.Rows) > MaxResultRows {
		return NormalizedResult{}, ErrInvalidResult
	}
	if len(input.Rows) > 0 && len(input.Rows) > MaxResultCells/len(schema.Columns) {
		return NormalizedResult{}, fmt.Errorf("%w: result exceeds %d cells", ErrInvalidResult, MaxResultCells)
	}
	inputIndexes := make(map[string]int, len(input.Columns))
	for index, name := range input.Columns {
		if !columnNamePattern.MatchString(name) {
			return NormalizedResult{}, fmt.Errorf("%w: columns[%d] is invalid", ErrInvalidResult, index)
		}
		if _, duplicate := inputIndexes[name]; duplicate {
			return NormalizedResult{}, fmt.Errorf("%w: columns[%d] is duplicated", ErrInvalidResult, index)
		}
		inputIndexes[name] = index
	}
	columns := append([]Column(nil), schema.Columns...)
	sort.Slice(columns, func(i, j int) bool { return columns[i].Name < columns[j].Name })
	sourceIndexes := make([]int, len(columns))
	for index, column := range columns {
		sourceIndex, exists := inputIndexes[column.Name]
		if !exists {
			return NormalizedResult{}, fmt.Errorf("%w: input is missing column %q", ErrInvalidResult, column.Name)
		}
		sourceIndexes[index] = sourceIndex
	}

	rows := make([][]NormalizedValue, len(input.Rows))
	for rowIndex, row := range input.Rows {
		if len(row) != len(input.Columns) {
			return NormalizedResult{}, fmt.Errorf("%w: rows[%d] column count mismatch", ErrInvalidResult, rowIndex)
		}
		rows[rowIndex] = make([]NormalizedValue, len(columns))
		for columnIndex, column := range columns {
			value, err := normalizeValue(column, row[sourceIndexes[columnIndex]])
			if err != nil {
				return NormalizedResult{}, fmt.Errorf("%w: rows[%d].%s: %v", ErrInvalidResult, rowIndex, column.Name, err)
			}
			rows[rowIndex][columnIndex] = value
		}
	}
	keyIndexes := resultKeyIndexes(columns)
	if err := rejectDuplicateKeys(rows, keyIndexes); err != nil {
		return NormalizedResult{}, err
	}
	sort.Slice(rows, func(i, j int) bool { return normalizedRowLess(rows[i], rows[j], keyIndexes) })
	result := NormalizedResult{
		SchemaVersion: EquivalenceVersion, Columns: columns, Rows: rows, RowCount: len(rows),
	}
	payload, err := normalizedResultPayload(result)
	if err != nil {
		return NormalizedResult{}, err
	}
	if len(payload) > MaxNormalizedResultBytes {
		return NormalizedResult{}, fmt.Errorf("%w: normalized result exceeds %d bytes", ErrInvalidResult, MaxNormalizedResultBytes)
	}
	result.ContentHash = askdata.HashBytes(payload)
	return result, result.Validate()
}

func (result NormalizedResult) Validate() error {
	if result.SchemaVersion != EquivalenceVersion || result.Columns == nil || result.Rows == nil ||
		len(result.Columns) < 1 || len(result.Columns) > MaxResultColumns || result.RowCount != len(result.Rows) ||
		len(result.Rows) > MaxResultRows {
		return ErrInvalidResult
	}
	previousColumn := ""
	for index, column := range result.Columns {
		if err := column.Validate(); err != nil {
			return fmt.Errorf("%w: columns[%d]: %v", ErrInvalidResult, index, err)
		}
		if previousColumn != "" && column.Name <= previousColumn {
			return fmt.Errorf("%w: normalized columns must be sorted and unique", ErrInvalidResult)
		}
		previousColumn = column.Name
	}
	if len(result.Rows) > 0 && len(result.Rows) > MaxResultCells/len(result.Columns) {
		return fmt.Errorf("%w: result exceeds %d cells", ErrInvalidResult, MaxResultCells)
	}
	for rowIndex, row := range result.Rows {
		if len(row) != len(result.Columns) {
			return fmt.Errorf("%w: rows[%d] column count mismatch", ErrInvalidResult, rowIndex)
		}
		for columnIndex, value := range row {
			if err := validateNormalizedValue(result.Columns[columnIndex], value); err != nil {
				return fmt.Errorf("%w: rows[%d].%s: %v", ErrInvalidResult, rowIndex, result.Columns[columnIndex].Name, err)
			}
		}
	}
	keyIndexes := resultKeyIndexes(result.Columns)
	if err := rejectDuplicateKeys(result.Rows, keyIndexes); err != nil {
		return err
	}
	for index := 1; index < len(result.Rows); index++ {
		if normalizedRowLess(result.Rows[index], result.Rows[index-1], keyIndexes) {
			return fmt.Errorf("%w: normalized rows are not sorted", ErrInvalidResult)
		}
	}
	if err := result.ContentHash.Validate(); err != nil {
		return fmt.Errorf("%w: contentHash: %v", ErrInvalidResult, err)
	}
	payload, err := normalizedResultPayload(result)
	if err != nil {
		return err
	}
	if len(payload) > MaxNormalizedResultBytes || result.ContentHash != askdata.HashBytes(payload) {
		return fmt.Errorf("%w: contentHash does not match normalized result", ErrInvalidResult)
	}
	return nil
}

// Compare canonicalizes both IRs and result sets. IR equality is strict across
// every SemanticIR field; result equality is exact except for FLOAT cells,
// which use the explicitly supplied absolute/relative tolerances.
func Compare(request ComparisonRequest) (EquivalenceReport, error) {
	if err := request.Schema.Validate(); err != nil {
		return EquivalenceReport{}, fmt.Errorf("comparison schema: %w", err)
	}
	if err := request.FloatTolerance.Validate(); err != nil {
		return EquivalenceReport{}, err
	}
	expectedIR, _, expectedIRHash, err := ir.Canonicalize(request.Expected.IR)
	if err != nil {
		return EquivalenceReport{}, fmt.Errorf("expected IR: %w", err)
	}
	actualIR, _, actualIRHash, err := ir.Canonicalize(request.Actual.IR)
	if err != nil {
		return EquivalenceReport{}, fmt.Errorf("actual IR: %w", err)
	}
	expectedResult, err := NormalizeResult(request.Schema, request.Expected.Result)
	if err != nil {
		return EquivalenceReport{}, fmt.Errorf("expected result: %w", err)
	}
	actualResult, err := NormalizeResult(request.Schema, request.Actual.Result)
	if err != nil {
		return EquivalenceReport{}, fmt.Errorf("actual result: %w", err)
	}
	if err := validateDeclaredResultHash("expected", request.Expected.DeclaredResultHash, expectedResult.ContentHash); err != nil {
		return EquivalenceReport{}, err
	}
	if err := validateDeclaredResultHash("actual", request.Actual.DeclaredResultHash, actualResult.ContentHash); err != nil {
		return EquivalenceReport{}, err
	}

	differences := compareIR(expectedIR, actualIR)
	irEquivalent := len(differences) == 0
	resultDifferences := compareResults(expectedResult, actualResult, request.FloatTolerance)
	differences = appendBoundedDifferences(differences, resultDifferences...)
	resultEquivalent := len(resultDifferences) == 0
	return EquivalenceReport{
		SchemaVersion: EquivalenceVersion,
		Equivalent:    irEquivalent && resultEquivalent, IREquivalent: irEquivalent, ResultEquivalent: resultEquivalent,
		IRHashMatch: expectedIRHash == actualIRHash, ExactResultHashMatch: expectedResult.ContentHash == actualResult.ContentHash,
		ExpectedIRHash: expectedIRHash, ActualIRHash: actualIRHash,
		ExpectedResultHash: expectedResult.ContentHash, ActualResultHash: actualResult.ContentHash,
		ExpectedRowCount: expectedResult.RowCount, ActualRowCount: actualResult.RowCount,
		Differences: differences,
	}, nil
}

func normalizeValue(column Column, input any) (NormalizedValue, error) {
	value, err := unwrapDriverValue(input)
	if err != nil {
		return NormalizedValue{}, err
	}
	if value == nil {
		return NormalizedValue{Type: column.Type, Null: true, Value: ""}, nil
	}
	var normalized string
	switch column.Type {
	case ScalarString:
		normalized, err = normalizeString(value)
	case ScalarInteger:
		normalized, err = normalizeInteger(value)
	case ScalarDecimal:
		normalized, err = normalizeDecimal(value)
	case ScalarFloat:
		normalized, err = normalizeFloat(value)
	case ScalarBoolean:
		normalized, err = normalizeBoolean(value)
	case ScalarDate:
		normalized, err = normalizeDate(value)
	case ScalarDateTime:
		normalized, err = normalizeDateTime(value, column.Timezone)
	default:
		err = errors.New("unsupported scalar type")
	}
	if err != nil {
		return NormalizedValue{}, err
	}
	return NormalizedValue{Type: column.Type, Value: normalized}, nil
}

func unwrapDriverValue(value any) (any, error) {
	valuer, ok := value.(driver.Valuer)
	if !ok {
		return value, nil
	}
	unwrapped, err := valuer.Value()
	if err != nil {
		return nil, fmt.Errorf("read driver value: %w", err)
	}
	if _, nested := unwrapped.(driver.Valuer); nested {
		return nil, errors.New("nested driver value is unsupported")
	}
	return unwrapped, nil
}

func normalizeString(value any) (string, error) {
	var text string
	switch typed := value.(type) {
	case string:
		text = typed
	case []byte:
		text = string(typed)
	default:
		return "", errors.New("must be a string")
	}
	if !utf8.ValidString(text) || utf8.RuneCountInString(text) > MaxCellRunes {
		return "", errors.New("string is invalid or too long")
	}
	return text, nil
}

func normalizeInteger(value any) (string, error) {
	text, err := integerText(value)
	if err != nil {
		return "", err
	}
	integer := new(big.Int)
	if len(text) > 256 {
		return "", errors.New("integer is outside the supported evaluation range")
	}
	if _, ok := integer.SetString(text, 10); !ok {
		return "", errors.New("must be an integer")
	}
	if len(strings.TrimLeft(integer.String(), "-")) > 128 {
		return "", errors.New("integer is outside the supported evaluation range")
	}
	return integer.String(), nil
}

func integerText(value any) (string, error) {
	switch typed := value.(type) {
	case int:
		return strconv.FormatInt(int64(typed), 10), nil
	case int8:
		return strconv.FormatInt(int64(typed), 10), nil
	case int16:
		return strconv.FormatInt(int64(typed), 10), nil
	case int32:
		return strconv.FormatInt(int64(typed), 10), nil
	case int64:
		return strconv.FormatInt(typed, 10), nil
	case uint:
		return strconv.FormatUint(uint64(typed), 10), nil
	case uint8:
		return strconv.FormatUint(uint64(typed), 10), nil
	case uint16:
		return strconv.FormatUint(uint64(typed), 10), nil
	case uint32:
		return strconv.FormatUint(uint64(typed), 10), nil
	case uint64:
		return strconv.FormatUint(typed, 10), nil
	case json.Number:
		return strings.TrimSpace(typed.String()), nil
	case string:
		return strings.TrimSpace(typed), nil
	case []byte:
		return strings.TrimSpace(string(typed)), nil
	default:
		return "", errors.New("must be an integer")
	}
}

func normalizeDecimal(value any) (string, error) {
	text, err := exactNumericText(value)
	if err != nil {
		return "", err
	}
	if len(text) > 512 || !decimalTextPattern.MatchString(text) {
		return "", errors.New("must be an exact decimal number")
	}
	rational := new(big.Rat)
	if _, ok := rational.SetString(text); !ok {
		return "", errors.New("must be an exact decimal number")
	}
	return rational.RatString(), nil
}

func exactNumericText(value any) (string, error) {
	switch typed := value.(type) {
	case json.Number:
		return strings.TrimSpace(typed.String()), nil
	case string:
		return strings.TrimSpace(typed), nil
	case []byte:
		return strings.TrimSpace(string(typed)), nil
	case int:
		return strconv.FormatInt(int64(typed), 10), nil
	case int8:
		return strconv.FormatInt(int64(typed), 10), nil
	case int16:
		return strconv.FormatInt(int64(typed), 10), nil
	case int32:
		return strconv.FormatInt(int64(typed), 10), nil
	case int64:
		return strconv.FormatInt(typed, 10), nil
	case uint:
		return strconv.FormatUint(uint64(typed), 10), nil
	case uint8:
		return strconv.FormatUint(uint64(typed), 10), nil
	case uint16:
		return strconv.FormatUint(uint64(typed), 10), nil
	case uint32:
		return strconv.FormatUint(uint64(typed), 10), nil
	case uint64:
		return strconv.FormatUint(typed, 10), nil
	default:
		return "", errors.New("DECIMAL must not pass through a binary float")
	}
}

func normalizeFloat(value any) (string, error) {
	var number float64
	var err error
	switch typed := value.(type) {
	case float64:
		number = typed
	case float32:
		number = float64(typed)
	case int:
		number = float64(typed)
	case int8:
		number = float64(typed)
	case int16:
		number = float64(typed)
	case int32:
		number = float64(typed)
	case int64:
		number = float64(typed)
	case uint:
		number = float64(typed)
	case uint8:
		number = float64(typed)
	case uint16:
		number = float64(typed)
	case uint32:
		number = float64(typed)
	case uint64:
		number = float64(typed)
	case json.Number:
		number, err = strconv.ParseFloat(strings.TrimSpace(typed.String()), 64)
	case string:
		number, err = strconv.ParseFloat(strings.TrimSpace(typed), 64)
	case []byte:
		number, err = strconv.ParseFloat(strings.TrimSpace(string(typed)), 64)
	default:
		return "", errors.New("must be a float")
	}
	if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
		return "", errors.New("must be a finite float")
	}
	if number == 0 {
		number = 0
	}
	return strconv.FormatFloat(number, 'g', -1, 64), nil
}

func normalizeBoolean(value any) (string, error) {
	switch typed := value.(type) {
	case bool:
		return strconv.FormatBool(typed), nil
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(typed))
		if err == nil {
			return strconv.FormatBool(parsed), nil
		}
	case []byte:
		parsed, err := strconv.ParseBool(strings.TrimSpace(string(typed)))
		if err == nil {
			return strconv.FormatBool(parsed), nil
		}
	}
	return "", errors.New("must be a boolean")
}

func normalizeDate(value any) (string, error) {
	if typed, ok := value.(time.Time); ok {
		return typed.Format("2006-01-02"), nil
	}
	text, err := normalizeString(value)
	if err != nil {
		return "", errors.New("must be a calendar date")
	}
	parsed, err := time.Parse("2006-01-02", strings.TrimSpace(text))
	if err != nil {
		return "", errors.New("must use YYYY-MM-DD")
	}
	return parsed.Format("2006-01-02"), nil
}

func normalizeDateTime(value any, timezone string) (string, error) {
	if typed, ok := value.(time.Time); ok {
		return typed.UTC().Format(time.RFC3339Nano), nil
	}
	text, err := normalizeString(value)
	if err != nil {
		return "", errors.New("must be a datetime")
	}
	text = strings.TrimSpace(text)
	if parsed, parseErr := time.Parse(time.RFC3339Nano, text); parseErr == nil {
		return parsed.UTC().Format(time.RFC3339Nano), nil
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return "", errors.New("datetime timezone is invalid")
	}
	for _, layout := range []string{"2006-01-02 15:04:05.999999999", "2006-01-02T15:04:05.999999999"} {
		if parsed, parseErr := time.ParseInLocation(layout, text, location); parseErr == nil {
			return parsed.UTC().Format(time.RFC3339Nano), nil
		}
	}
	return "", errors.New("must be RFC3339 or a zone-less SQL datetime")
}

func validateNormalizedValue(column Column, value NormalizedValue) error {
	if value.Type != column.Type {
		return errors.New("normalized type does not match column")
	}
	if value.Null {
		if value.Value != "" {
			return errors.New("NULL normalized value must be empty")
		}
		return nil
	}
	switch value.Type {
	case ScalarString:
		if !utf8.ValidString(value.Value) || utf8.RuneCountInString(value.Value) > MaxCellRunes {
			return errors.New("normalized string is invalid")
		}
	case ScalarInteger:
		integer := new(big.Int)
		if _, ok := integer.SetString(value.Value, 10); !ok || integer.String() != value.Value {
			return errors.New("normalized integer is not canonical")
		}
	case ScalarDecimal:
		rational := new(big.Rat)
		if _, ok := rational.SetString(value.Value); !ok || rational.RatString() != value.Value {
			return errors.New("normalized decimal is not canonical")
		}
	case ScalarFloat:
		number, err := strconv.ParseFloat(value.Value, 64)
		if err != nil || math.IsNaN(number) || math.IsInf(number, 0) ||
			(number == 0 && value.Value != "0") || strconv.FormatFloat(number, 'g', -1, 64) != value.Value {
			return errors.New("normalized float is not canonical")
		}
	case ScalarBoolean:
		if value.Value != "true" && value.Value != "false" {
			return errors.New("normalized boolean is not canonical")
		}
	case ScalarDate:
		parsed, err := time.Parse("2006-01-02", value.Value)
		if err != nil || parsed.Format("2006-01-02") != value.Value {
			return errors.New("normalized date is not canonical")
		}
	case ScalarDateTime:
		parsed, err := time.Parse(time.RFC3339Nano, value.Value)
		if err != nil || parsed.Location() != time.UTC || parsed.Format(time.RFC3339Nano) != value.Value {
			return errors.New("normalized datetime is not canonical UTC")
		}
	default:
		return errors.New("normalized scalar type is invalid")
	}
	return nil
}

func resultKeyIndexes(columns []Column) []int {
	indexes := []int{}
	for index, column := range columns {
		if column.Key {
			indexes = append(indexes, index)
		}
	}
	return indexes
}

func rejectDuplicateKeys(rows [][]NormalizedValue, keyIndexes []int) error {
	if len(keyIndexes) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(rows))
	for index, row := range rows {
		key, err := normalizedRowKey(row, keyIndexes)
		if err != nil {
			return err
		}
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("%w at normalized row %d", ErrDuplicateResultKey, index)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func normalizedRowKey(row []NormalizedValue, indexes []int) (string, error) {
	values := make([]NormalizedValue, len(indexes))
	for index, columnIndex := range indexes {
		if columnIndex < 0 || columnIndex >= len(row) {
			return "", ErrInvalidResult
		}
		values[index] = row[columnIndex]
	}
	payload, err := json.Marshal(values)
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

func normalizedRowLess(left, right []NormalizedValue, keyIndexes []int) bool {
	for _, index := range keyIndexes {
		if comparison := compareNormalizedValueOrder(left[index], right[index]); comparison != 0 {
			return comparison < 0
		}
	}
	for index := range left {
		if comparison := compareNormalizedValueOrder(left[index], right[index]); comparison != 0 {
			return comparison < 0
		}
	}
	return false
}

func compareNormalizedValueOrder(left, right NormalizedValue) int {
	if left.Null != right.Null {
		if left.Null {
			return -1
		}
		return 1
	}
	if left.Type != right.Type {
		return strings.Compare(string(left.Type), string(right.Type))
	}
	return strings.Compare(left.Value, right.Value)
}

func normalizedResultPayload(result NormalizedResult) ([]byte, error) {
	type resultWithoutHash NormalizedResult
	payload := resultWithoutHash(result)
	payload.ContentHash = ""
	return json.Marshal(payload)
}

func validateDeclaredResultHash(label string, declared *askdata.ContentHash, actual askdata.ContentHash) error {
	if declared == nil {
		return nil
	}
	if err := declared.Validate(); err != nil {
		return fmt.Errorf("%s declared result hash: %w", label, err)
	}
	if *declared != actual {
		return fmt.Errorf("%s declared result hash does not match normalized result", label)
	}
	return nil
}

func compareIR(expected, actual ir.SemanticIR) []Difference {
	differences := []Difference{}
	checks := []struct {
		path     string
		expected any
		actual   any
	}{
		{"ir.irVersion", expected.IRVersion, actual.IRVersion},
		{"ir.semanticReleaseId", expected.SemanticReleaseID, actual.SemanticReleaseID},
		{"ir.semanticContentHash", expected.SemanticContentHash, actual.SemanticContentHash},
		{"ir.domainId", expected.DomainID, actual.DomainID},
		{"ir.modelVersionId", expected.ModelVersionID, actual.ModelVersionID},
		{"ir.metrics", expected.Metrics, actual.Metrics},
		{"ir.groupBy", expected.GroupBy, actual.GroupBy},
		{"ir.filters", expected.Filters, actual.Filters},
		{"ir.timeRange", expected.TimeRange, actual.TimeRange},
		{"ir.comparison", expected.Comparison, actual.Comparison},
		{"ir.sort", expected.Sort, actual.Sort},
		{"ir.limit", expected.Limit, actual.Limit},
	}
	for _, check := range checks {
		if !reflect.DeepEqual(check.expected, check.actual) {
			differences = appendBoundedDifferences(differences, Difference{Code: "IR_MISMATCH", Path: check.path})
		}
	}
	return differences
}

func compareResults(expected, actual NormalizedResult, tolerance FloatTolerance) []Difference {
	if !reflect.DeepEqual(expected.Columns, actual.Columns) {
		return []Difference{{Code: "RESULT_COLUMNS_MISMATCH", Path: "result.columns"}}
	}
	differences := []Difference{}
	if expected.RowCount != actual.RowCount {
		differences = appendBoundedDifferences(differences, Difference{Code: "RESULT_ROW_COUNT_MISMATCH", Path: "result.rows"})
	}
	rowCount := min(expected.RowCount, actual.RowCount)
	keyIndexes := resultKeyIndexes(expected.Columns)
	for rowIndex := 0; rowIndex < rowCount; rowIndex++ {
		if len(keyIndexes) > 0 && !normalizedKeyEqual(expected.Rows[rowIndex], actual.Rows[rowIndex], keyIndexes) {
			differences = appendBoundedDifferences(differences, Difference{Code: "RESULT_KEY_MISMATCH", Path: fmt.Sprintf("result.rows[%d]", rowIndex)})
			continue
		}
		for columnIndex, column := range expected.Columns {
			if !normalizedValuesEquivalent(expected.Rows[rowIndex][columnIndex], actual.Rows[rowIndex][columnIndex], tolerance) {
				differences = appendBoundedDifferences(differences, Difference{
					Code: "RESULT_VALUE_MISMATCH", Path: fmt.Sprintf("result.rows[%d].%s", rowIndex, column.Name),
				})
			}
		}
	}
	return differences
}

func normalizedKeyEqual(left, right []NormalizedValue, indexes []int) bool {
	for _, index := range indexes {
		if !reflect.DeepEqual(left[index], right[index]) {
			return false
		}
	}
	return true
}

func normalizedValuesEquivalent(expected, actual NormalizedValue, tolerance FloatTolerance) bool {
	if expected.Type != actual.Type || expected.Null != actual.Null {
		return false
	}
	if expected.Null || expected.Value == actual.Value {
		return true
	}
	if expected.Type != ScalarFloat {
		return false
	}
	left, leftErr := strconv.ParseFloat(expected.Value, 64)
	right, rightErr := strconv.ParseFloat(actual.Value, 64)
	if leftErr != nil || rightErr != nil {
		return false
	}
	delta := math.Abs(left - right)
	return delta <= tolerance.Absolute || delta <= tolerance.Relative*math.Max(math.Abs(left), math.Abs(right))
}

func appendBoundedDifferences(values []Difference, additions ...Difference) []Difference {
	remaining := MaxDifferences - len(values)
	if remaining <= 0 {
		return values
	}
	if len(additions) > remaining {
		additions = additions[:remaining]
	}
	return append(values, additions...)
}

func validScalarType(value ScalarType) bool {
	switch value {
	case ScalarString, ScalarInteger, ScalarDecimal, ScalarFloat, ScalarBoolean, ScalarDate, ScalarDateTime:
		return true
	default:
		return false
	}
}
