package insight

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/shared"
	"intelligent-report-generation-system/internal/report"
)

// Evidence must be computed from a query result the server itself executed.
//
// The Evidence Bundle is what makes a published conclusion checkable: its facts
// carry cell references into the result they came from, and the narrative
// verifier refuses any claim those facts do not support. Accepting a bundle
// from a caller would let anyone with edit rights assert "revenue = 999" as
// verified evidence and pass the publication fact gate with it. So the only
// producer is this file, fed by a real execution.

// ResultTable is the executed component result, in the shape the report runtime
// returns it. It is declared here rather than imported so this package stays
// free of runtime dependencies.
type ResultTable struct {
	Columns []string
	Rows    [][]any
}

// MethodRoles names which bound column plays which part in an analysis method.
// The report's own data binding supplies them, so evidence can never be derived
// over a column the component does not actually display.
type MethodRoles struct {
	// Dimension is the categorical or ordering column. Empty for a single-value
	// method such as CURRENT_VALUE over a one-row result.
	Dimension string
	// Value is the measure column the facts are computed from.
	Value string
	// Previous and Target are optional comparison columns.
	Previous string
	Target   string
	// Group splits values for GROUP_DIFFERENCE.
	Group string
}

// BindingRoles derives the analysis roles from a component's own data binding.
// It only ever names fields the component already binds.
func BindingRoles(binding *report.DataBinding) (MethodRoles, error) {
	if binding == nil || binding.BindingMode != report.BindingDatasetField {
		return MethodRoles{}, errors.New("evidence derivation requires a dataset field binding")
	}
	roles := MethodRoles{}
	for _, dimension := range binding.Dimensions {
		switch dimension.Role {
		case report.RoleSeries, report.RoleColor:
			if roles.Group == "" {
				roles.Group = dimension.Field
			}
		default:
			if roles.Dimension == "" {
				roles.Dimension = dimension.Field
			}
		}
	}
	for index, measure := range binding.Measures {
		switch {
		case index == 0:
			roles.Value = measure.Field
		case roles.Previous == "":
			roles.Previous = measure.Field
		case roles.Target == "":
			roles.Target = measure.Field
		}
	}
	if roles.Value == "" {
		return MethodRoles{}, errors.New("evidence derivation requires a bound measure")
	}
	return roles, nil
}

// BuildMethodInput projects an executed result into the deterministic analysis
// input. Every value keeps a CellRef back to the row and column it came from,
// which is what lets the verifier check a narrative claim against real data.
func BuildMethodInput(result ResultTable, roles MethodRoles, topN int) (MethodInput, error) {
	index := make(map[string]int, len(result.Columns))
	for position, column := range result.Columns {
		index[column] = position
	}
	valueIndex, exists := index[roles.Value]
	if !exists {
		return MethodInput{}, fmt.Errorf("result has no measure column %q", roles.Value)
	}
	optional := func(name string) (int, bool) {
		if name == "" {
			return 0, false
		}
		position, ok := index[name]
		return position, ok
	}
	dimensionIndex, hasDimension := optional(roles.Dimension)
	previousIndex, hasPrevious := optional(roles.Previous)
	targetIndex, hasTarget := optional(roles.Target)
	groupIndex, hasGroup := optional(roles.Group)

	// The measure column is the citation's column coordinate, so it has to be a
	// valid governed identifier before any fact can reference it.
	if askdata.ID(roles.Value).Validate() != nil {
		return MethodInput{}, fmt.Errorf("measure column %q is not a valid citation coordinate", roles.Value)
	}
	values := make([]NumericValue, 0, len(result.Rows))
	for rowNumber, row := range result.Rows {
		if valueIndex >= len(row) {
			return MethodInput{}, errors.New("result row is shorter than its column list")
		}
		key := strconv.Itoa(rowNumber + 1)
		dimensionKey := "row"
		if hasDimension && dimensionIndex < len(row) {
			key = cellText(row[dimensionIndex])
			dimensionKey = roles.Dimension
		}
		// Row coordinates use the shared percent-encoded group-by form, the same
		// one Ask Data citations use, so a report fact and an answer citation
		// point at a cell the same way.
		rowKey, err := shared.FormatRowKey([]shared.RowKeyPart{{Key: dimensionKey, Value: key}})
		if err != nil {
			return MethodInput{}, fmt.Errorf("row %d coordinate: %w", rowNumber+1, err)
		}
		numeric, ok := numberOf(row[valueIndex])
		item := NumericValue{
			Key: key, Value: numeric, Missing: !ok,
			// Keying the row by its dimension value keeps a fact traceable even
			// if row order changes between executions.
			CellRef: shared.CellRef{RowKey: rowKey, ColumnKey: roles.Value},
		}
		if hasGroup && groupIndex < len(row) {
			item.Group = cellText(row[groupIndex])
		}
		if hasPrevious && previousIndex < len(row) {
			if previous, previousOK := numberOf(row[previousIndex]); previousOK {
				item.Previous = &previous
			}
		}
		if hasTarget && targetIndex < len(row) {
			if target, targetOK := numberOf(row[targetIndex]); targetOK {
				item.Target = &target
			}
		}
		values = append(values, item)
	}
	if len(values) == 0 {
		return MethodInput{}, errors.New("result contains no rows to derive evidence from")
	}
	return MethodInput{Values: values, TopN: topN}, nil
}

func cellText(value any) string {
	if value == nil {
		return "—"
	}
	text := strings.TrimSpace(fmt.Sprintf("%v", value))
	if text == "" {
		return "—"
	}
	if len(text) > 256 {
		return text[:256]
	}
	return text
}

func numberOf(value any) (float64, bool) {
	switch typed := value.(type) {
	case nil:
		return 0, false
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return parsed, err == nil
	default:
		// json.Number and anything else with a usable decimal text form.
		parsed, err := strconv.ParseFloat(strings.TrimSpace(fmt.Sprintf("%v", typed)), 64)
		return parsed, err == nil
	}
}
