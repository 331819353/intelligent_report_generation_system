package runtime

import (
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/queryruntime"
	"intelligent-report-generation-system/internal/report"
)

// A DATASET_FIELD component binds a set of dimensions and measures, but the
// dataset version it reads has its own output grain. When the bound dimensions
// do not cover that grain, projecting the version's columns returns several
// rows per bound dimension value: a bar chart bound to (channel, revenue) would
// plot one bar per underlying row instead of one bar per channel.
//
// Rolling those rows up is only meaningful when the measure's own semantics
// survive it. This file decides that question and, when the answer is yes,
// performs the roll-up. When the answer is no it fails closed with a specific
// code, because a plausible-looking wrong total is worse than a visible error.

const (
	// CodeRollupUndeclared is returned when a measure has to be rolled up but no
	// governed contract declares how to aggregate it.
	CodeRollupUndeclared = "REPORT_ROLLUP_MEASURE_UNDECLARED"
	// CodeRollupNonAdditive is returned for measures that cannot be re-aggregated
	// across the dropped dimensions (averages, ratios, distinct counts,
	// semi-additive balances).
	CodeRollupNonAdditive = "REPORT_ROLLUP_MEASURE_NON_ADDITIVE"
	// CodeRollupTruncated is returned when the source rows hit the query row
	// ceiling: an aggregate over a truncated scan is not a partial total, it is a
	// wrong one.
	CodeRollupTruncated = "REPORT_ROLLUP_SOURCE_TRUNCATED"
)

// RollupError carries the same Code() contract as runtime.Error so ExecuteBatch
// surfaces the specific reason to the component instead of a generic failure.
type RollupError struct {
	code    string
	field   string
	message string
}

func newRollupError(code, field, message string) *RollupError {
	return &RollupError{code: code, field: field, message: message}
}

func (err *RollupError) Code() string  { return err.code }
func (err *RollupError) Field() string { return err.field }

func (err *RollupError) Error() string {
	if err.field == "" {
		return err.code + ": " + err.message
	}
	return fmt.Sprintf("%s: measure %q: %s", err.code, err.field, err.message)
}

// rollupOperator is the operation applied when combining rows that share the
// same bound dimension values.
type rollupOperator string

const (
	rollupSum rollupOperator = "SUM"
	rollupMin rollupOperator = "MIN"
	rollupMax rollupOperator = "MAX"
)

// resolveRollupOperator maps a measure's declared aggregation to the operator
// that combines already-aggregated partials correctly. COUNT is included
// because counts of disjoint groups add; AVG and COUNT_DISTINCT are not,
// because partials cannot be recombined without their inputs.
func resolveRollupOperator(measure queryruntime.VersionMeasure) (rollupOperator, error) {
	if !measure.Declared && measure.Aggregation == "" {
		return "", newRollupError(CodeRollupUndeclared, measure.Field,
			"dataset version does not declare an aggregation for this measure")
	}
	switch strings.ToUpper(measure.Additivity) {
	case "", "ADDITIVE", "FULLY_ADDITIVE":
	default:
		return "", newRollupError(CodeRollupNonAdditive, measure.Field,
			fmt.Sprintf("measure is %s and cannot be summed across the dropped dimensions", measure.Additivity))
	}
	switch strings.ToUpper(measure.Aggregation) {
	case "SUM", "COUNT":
		return rollupSum, nil
	case "MIN":
		return rollupMin, nil
	case "MAX":
		return rollupMax, nil
	case "AVG", "COUNT_DISTINCT":
		return "", newRollupError(CodeRollupNonAdditive, measure.Field,
			fmt.Sprintf("%s partials cannot be recombined without their inputs", measure.Aggregation))
	default:
		return "", newRollupError(CodeRollupUndeclared, measure.Field,
			fmt.Sprintf("aggregation %q is not a supported roll-up", measure.Aggregation))
	}
}

// NeedsRollup reports whether the bound dimensions leave the dataset version's
// grain incomplete. When every grain key field is still bound, each result row
// is already unique per bound dimension tuple and must be passed through
// untouched.
func NeedsRollup(dimensions []report.FieldBinding, grainKeyFields []string) bool {
	if len(grainKeyFields) == 0 {
		return false
	}
	bound := make(map[string]struct{}, len(dimensions))
	for _, dimension := range dimensions {
		bound[strings.TrimSpace(dimension.Field)] = struct{}{}
	}
	for _, key := range grainKeyFields {
		if _, exists := bound[strings.TrimSpace(key)]; !exists {
			return true
		}
	}
	return false
}

// RollUp groups result rows by the bound dimension columns and combines each
// measure with its governed operator. Columns keep their requested order:
// dimensions first, then measures, exactly as the binding declared them.
func RollUp(
	result QueryResult,
	dimensions, measures []report.FieldBinding,
	contract queryruntime.VersionRollupContract,
) (QueryResult, error) {
	if result.Partial {
		return QueryResult{}, newRollupError(CodeRollupTruncated, "",
			"source rows hit the query row limit, so an aggregate over them would be wrong")
	}
	index := make(map[string]int, len(result.Columns))
	for position, column := range result.Columns {
		index[column] = position
	}
	groupIndexes := make([]int, 0, len(dimensions))
	for _, dimension := range dimensions {
		position, exists := index[dimension.Field]
		if !exists {
			return QueryResult{}, fmt.Errorf("dimension %q is missing from the query result", dimension.Field)
		}
		groupIndexes = append(groupIndexes, position)
	}
	type measurePlan struct {
		position int
		operator rollupOperator
	}
	plans := make([]measurePlan, 0, len(measures))
	for _, measure := range measures {
		position, exists := index[measure.Field]
		if !exists {
			return QueryResult{}, fmt.Errorf("measure %q is missing from the query result", measure.Field)
		}
		operator, err := resolveRollupOperator(contract.Measures[measure.Field])
		if err != nil {
			return QueryResult{}, err
		}
		plans = append(plans, measurePlan{position: position, operator: operator})
	}

	type group struct {
		key    []any
		values []*big.Float
		seen   []bool
	}
	order := make([]string, 0, len(result.Rows))
	groups := make(map[string]*group, len(result.Rows))
	for _, row := range result.Rows {
		identity, key := groupIdentity(row, groupIndexes)
		current, exists := groups[identity]
		if !exists {
			current = &group{key: key, values: make([]*big.Float, len(plans)), seen: make([]bool, len(plans))}
			groups[identity] = current
			order = append(order, identity)
		}
		for position, plan := range plans {
			value, ok := decimalOf(row[plan.position])
			if !ok {
				// NULL and non-numeric cells are skipped rather than coerced to
				// zero: a missing input must not look like a measured zero.
				continue
			}
			if !current.seen[position] {
				current.values[position] = value
				current.seen[position] = true
				continue
			}
			current.values[position] = combine(plan.operator, current.values[position], value)
		}
	}

	// Deterministic output order keeps result hashes stable across executions.
	sort.Strings(order)
	rows := make([][]any, 0, len(order))
	for _, identity := range order {
		current := groups[identity]
		row := make([]any, 0, len(groupIndexes)+len(plans))
		row = append(row, current.key...)
		for position := range plans {
			if !current.seen[position] {
				row = append(row, nil)
				continue
			}
			row = append(row, json.Number(current.values[position].Text('f', -1)))
		}
		rows = append(rows, row)
	}

	columns := make([]string, 0, len(dimensions)+len(measures))
	for _, dimension := range dimensions {
		columns = append(columns, dimension.Field)
	}
	for _, measure := range measures {
		columns = append(columns, measure.Field)
	}
	hash, err := hashResult(columns, rows)
	if err != nil {
		return QueryResult{}, err
	}
	return QueryResult{Columns: columns, Rows: rows, Hash: hash}, nil
}

func hashResult(columns []string, rows [][]any) (askdata.ContentHash, error) {
	payload, err := json.Marshal(struct {
		Columns []string `json:"columns"`
		Rows    [][]any  `json:"rows"`
	}{columns, rows})
	if err != nil {
		return "", err
	}
	return askdata.HashBytes(payload), nil
}

func combine(operator rollupOperator, left, right *big.Float) *big.Float {
	switch operator {
	case rollupMin:
		if right.Cmp(left) < 0 {
			return right
		}
		return left
	case rollupMax:
		if right.Cmp(left) > 0 {
			return right
		}
		return left
	default:
		return new(big.Float).Add(left, right)
	}
}

func groupIdentity(row []any, indexes []int) (string, []any) {
	key := make([]any, 0, len(indexes))
	parts := make([]string, 0, len(indexes))
	for _, position := range indexes {
		var value any
		if position < len(row) {
			value = row[position]
		}
		key = append(key, value)
		parts = append(parts, fmt.Sprintf("%v", value))
	}
	return strings.Join(parts, "\x00"), key
}

// decimalOf parses a cell without float64 coercion so currency and quantity
// totals stay exact.
func decimalOf(value any) (*big.Float, bool) {
	switch typed := value.(type) {
	case nil:
		return nil, false
	case json.Number:
		return parseDecimal(typed.String())
	case string:
		return parseDecimal(typed)
	case int:
		return new(big.Float).SetInt64(int64(typed)), true
	case int32:
		return new(big.Float).SetInt64(int64(typed)), true
	case int64:
		return new(big.Float).SetInt64(typed), true
	case float32:
		return big.NewFloat(float64(typed)), true
	case float64:
		return big.NewFloat(typed), true
	case pgtype.Numeric:
		// PostgreSQL NUMERIC columns are decoded by pgx as pgtype.Numeric. Keep
		// their exact decimal representation instead of coercing through
		// float64; otherwise every warehouse-backed measure is mistaken for a
		// non-numeric cell and a valid aggregate is rendered as NULL.
		driverValue, err := typed.Value()
		if err != nil || driverValue == nil {
			return nil, false
		}
		raw, ok := driverValue.(string)
		if !ok {
			return nil, false
		}
		return parseDecimal(raw)
	default:
		return nil, false
	}
}

func parseDecimal(raw string) (*big.Float, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, false
	}
	value, _, err := big.ParseFloat(trimmed, 10, 200, big.ToNearestEven)
	if err != nil {
		return nil, false
	}
	return value, true
}
