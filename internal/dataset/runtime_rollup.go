package dataset

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrRuntimeRollupUnsupported reports that a document cannot be rolled up by
// rewriting it: it already aggregates, is DISTINCT, or a requested field carries
// a window expression. Callers fall back to reading rows at the version's grain.
var ErrRuntimeRollupUnsupported = errors.New("dataset document cannot be rolled up at execution time")

// RuntimeRollupMeasure names one output measure and the aggregate function to
// apply over the version's rows when the projection drops part of its grain.
type RuntimeRollupMeasure struct {
	Field    string
	Function string
}

var runtimeRollupFunctions = map[string]bool{
	"SUM": true, "AVG": true, "MIN": true, "MAX": true, "COUNT": true, "COUNT_DISTINCT": true,
}

// BuildRuntimeDistinct derives a private one-field grouped query used by
// report filter option discovery. Grouping by the governed logical expression
// makes the database return unique values without reading thousands of detail
// rows into the API process. The stored dataset version is never modified.
func BuildRuntimeDistinct(document Document, fieldCode string) (Document, error) {
	fieldCode = strings.TrimSpace(fieldCode)
	var selected *Field
	for index := range document.Fields {
		if document.Fields[index].Code == fieldCode {
			copy := document.Fields[index]
			selected = &copy
			break
		}
	}
	if selected == nil || expressionHasWindow(selected.Expression) || expressionContainsAggregateExpression(selected.Expression) {
		return Document{}, fmt.Errorf("%w: field %q cannot be grouped for filter options", ErrRuntimeRollupUnsupported, fieldCode)
	}

	rewritten := document
	rewritten.Fields = []Field{*selected}
	rewritten.Distinct = false
	rewritten.GroupBy = []string{selected.ID}
	rewritten.GroupByMode = ""
	rewritten.GroupingSets = nil
	rewritten.Having = nil
	rewritten.FactContract = nil
	rewritten.AnalysisContract = nil
	rewritten.Sorts = []Sort{{FieldID: selected.ID, Direction: "ASC", Nulls: "LAST"}}
	rewritten.OutputGrain = OutputGrain{Description: "运行时筛选候选值去重", KeyFields: []string{fieldCode}}
	return AsRuntimeRollupExecution(rewritten), nil
}

// BuildRuntimeRollup derives a private, server-side execution document from a
// PUBLISHED detail-grain version: it projects only the requested dimensions,
// wraps every requested measure in its governed aggregate function and groups
// by the dimensions, so the database returns one row per bound dimension tuple
// instead of every source row.
//
// The rewritten document keeps the version's nodes, joins, transforms, filters,
// parameters, policies and execution policy untouched — only the output shape
// changes. It is marked as a runtime execution (see AsRuntimeRollupExecution)
// so layer contracts that forbid aggregation on stored ODS/DIM/DWD documents do
// not apply; the marker is not serialized, therefore persisted documents cannot
// obtain the exception.
func BuildRuntimeRollup(document Document, dimensions []string, measures []RuntimeRollupMeasure) (Document, error) {
	if len(measures) == 0 {
		return Document{}, fmt.Errorf("%w: no measures requested", ErrRuntimeRollupUnsupported)
	}
	if document.Distinct || documentHasGroupingOrAggregation(document) {
		return Document{}, fmt.Errorf("%w: version already changes its source grain", ErrRuntimeRollupUnsupported)
	}
	byCode := make(map[string]Field, len(document.Fields))
	for _, field := range document.Fields {
		byCode[field.Code] = field
	}
	requested := map[string]bool{}
	fields := make([]Field, 0, len(dimensions)+len(measures))
	groupBy := make([]string, 0, len(dimensions))
	for _, code := range dimensions {
		code = strings.TrimSpace(code)
		field, exists := byCode[code]
		if !exists || requested[code] {
			return Document{}, fmt.Errorf("%w: dimension %q is not an output field", ErrRuntimeRollupUnsupported, code)
		}
		if expressionHasWindow(field.Expression) {
			return Document{}, fmt.Errorf("%w: dimension %q uses a window expression", ErrRuntimeRollupUnsupported, code)
		}
		requested[code] = true
		fields = append(fields, field)
		groupBy = append(groupBy, field.ID)
	}
	for _, measure := range measures {
		code := strings.TrimSpace(measure.Field)
		field, exists := byCode[code]
		function := strings.ToUpper(strings.TrimSpace(measure.Function))
		if !exists || requested[code] || !runtimeRollupFunctions[function] {
			return Document{}, fmt.Errorf("%w: measure %q cannot be aggregated with %q", ErrRuntimeRollupUnsupported, code, function)
		}
		if expressionHasWindow(field.Expression) || expressionContainsAggregateExpression(field.Expression) {
			return Document{}, fmt.Errorf("%w: measure %q already aggregates", ErrRuntimeRollupUnsupported, code)
		}
		requested[code] = true
		argument := field.Expression
		field.Expression = Expression{Type: "AGGREGATE", Function: function, Argument: &argument}
		field.Aggregation = function
		fields = append(fields, field)
	}

	rewritten := document
	rewritten.Fields = fields
	rewritten.GroupBy = groupBy
	rewritten.GroupByMode = ""
	rewritten.GroupingSets = nil
	rewritten.Having = nil
	rewritten.FactContract = nil
	rewritten.AnalysisContract = nil
	// Grouped rows come back in the database's arbitrary order; charts plot
	// categories in row order, so sort by the dimensions (dates ascend, categories
	// stable) exactly like the in-memory roll-up does.
	rewritten.Sorts = make([]Sort, 0, len(groupBy))
	for _, fieldID := range groupBy {
		rewritten.Sorts = append(rewritten.Sorts, Sort{FieldID: fieldID, Direction: "ASC", Nulls: "LAST"})
	}
	rewritten.OutputGrain = OutputGrain{Description: "运行时按绑定维度汇总", KeyFields: append([]string(nil), dimensions...)}
	if document.OutputGrain.TimeField != "" && requested[document.OutputGrain.TimeField] {
		rewritten.OutputGrain.TimeField = document.OutputGrain.TimeField
		rewritten.OutputGrain.DefaultTimeGrain = document.OutputGrain.DefaultTimeGrain
	}
	return AsRuntimeRollupExecution(rewritten), nil
}

// AsRuntimeRollupExecution marks a private, server-derived execution document
// produced by BuildRuntimeRollup. Layer contracts that keep stored ODS/DIM/DWD
// documents at source grain do not apply to it: the stored version is unchanged,
// only this one execution reads it grouped. The marker is not serialized.
func AsRuntimeRollupExecution(document Document) Document {
	document.runtimeRollup = true
	return document
}

// IsRuntimeRollupExecution reports whether the document carries the private
// runtime roll-up marker; resolvers that derive a new execution document from it
// must re-apply the marker.
func IsRuntimeRollupExecution(document Document) bool {
	return document.runtimeRollup
}

// PrepareDocument validates and plans an already-decoded document without a
// JSON round trip, so private execution markers survive. Stored documents keep
// using Prepare(raw), which is the only path that persists a DSL.
func PrepareDocument(document Document) (Prepared, error) {
	document = normalize(document)
	if err := Validate(document); err != nil {
		return Prepared{}, err
	}
	dslJSON, err := document.MarshalJSON()
	if err != nil {
		return Prepared{}, err
	}
	plan := BuildLogicalPlan(document)
	planJSON, err := json.Marshal(plan)
	if err != nil {
		return Prepared{}, err
	}
	return Prepared{
		Document: document, DSLJSON: dslJSON, DSLHash: hashJSON(dslJSON),
		LogicalPlan: plan, LogicalPlanJSON: planJSON, PlanHash: hashJSON(planJSON),
	}, nil
}

func expressionContainsAggregateExpression(expression Expression) bool {
	found := false
	visitDatasetExpression(expression, func(value Expression) {
		found = found || value.Type == "AGGREGATE"
	})
	return found
}
