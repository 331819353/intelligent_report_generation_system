package compiler

import (
	"fmt"
	"sort"

	"intelligent-report-generation-system/internal/dataset"
)

// BuildRecomputedTotalPlan derives the governed, ungrouped validation query
// used to recompute totals for metrics that cannot be summed from displayed
// rows. It reuses the live parameter bindings of the original plan and never
// reads mutable registry state.
func BuildRecomputedTotalPlan(plan QueryPlan, metricColumns []string) (QueryPlan, error) {
	if err := plan.validate(); err != nil || plan.compiled == nil || plan.parameterValues == nil {
		return QueryPlan{}, fmt.Errorf("%w: total plan source is not executable", ErrInvalidQueryPlan)
	}
	requested := make(map[string]struct{}, len(metricColumns))
	for _, name := range metricColumns {
		if !trustedOutputNamePattern.MatchString(name) {
			return QueryPlan{}, fmt.Errorf("%w: invalid total metric column", ErrInvalidQueryPlan)
		}
		if _, duplicate := requested[name]; duplicate {
			return QueryPlan{}, fmt.Errorf("%w: duplicate total metric column", ErrInvalidQueryPlan)
		}
		requested[name] = struct{}{}
	}
	if len(requested) == 0 {
		return QueryPlan{}, fmt.Errorf("%w: total metric columns are required", ErrInvalidQueryPlan)
	}

	document := plan.Document
	document.Fields = make([]dataset.Field, 0, len(requested))
	for _, field := range plan.Document.Fields {
		if _, selected := requested[field.Code]; !selected {
			continue
		}
		if field.Role != "MEASURE" {
			return QueryPlan{}, fmt.Errorf("%w: total column is not a metric", ErrInvalidQueryPlan)
		}
		document.Fields = append(document.Fields, field)
		delete(requested, field.Code)
	}
	if len(requested) != 0 {
		missing := make([]string, 0, len(requested))
		for name := range requested {
			missing = append(missing, name)
		}
		sort.Strings(missing)
		return QueryPlan{}, fmt.Errorf("%w: total metric columns unavailable: %v", ErrInvalidQueryPlan, missing)
	}
	document.GroupBy = []string{}
	document.Having = []dataset.Filter{}
	document.Sorts = []dataset.Sort{}
	document.OutputGrain = dataset.OutputGrain{
		Description: "one row of recomputed governed metric totals",
		KeyFields:   []string{document.Fields[0].Code},
	}
	document.ExecutionPolicy.PreviewLimit = 1
	document.ExecutionPolicy.ResultLimit = 1

	return compileQueryPlan(
		plan.Role,
		document,
		plan.Source,
		plan.ParameterShapes,
		plan.parameterValues,
		1,
	)
}
