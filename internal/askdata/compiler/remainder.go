package compiler

import (
	"errors"
	"fmt"
	"strings"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/ir"
	"intelligent-report-generation-system/internal/askdata/registry"
)

var ErrInvalidRemainderPlan = errors.New("semantic remainder plan is invalid")

type RemainderStrategy string

const (
	RemainderTotalMinusTop RemainderStrategy = "TOTAL_MINUS_TOP"
	RemainderRecompute     RemainderStrategy = "RECOMPUTE"
)

type RemainderMetric struct {
	Contract         MetricContract
	OutputColumn     string
	RecomputedColumn string
}

type MetricRemainderPlan struct {
	MetricVersionID askdata.ID        `json:"metricVersionId"`
	Strategy        RemainderStrategy `json:"strategy"`
}

type OtherCompileRequest struct {
	Query                       ir.SemanticIR
	Limit                       CompiledLimit
	GroupColumns                []string
	Metrics                     []RemainderMetric
	RecomputedRemainderRelation string
}

type CompiledOther struct {
	SQL         string
	MetricPlans []MetricRemainderPlan
}

// CompileOther appends one governed remainder row. Fully additive metrics use
// total minus displayed TopN; semi/non-additive metrics can only read a
// recomputation relation built with the AggregationPlanner over remainder_rows.
func CompileOther(request OtherCompileRequest) (CompiledOther, error) {
	query := ir.Normalize(request.Query)
	if query.OtherPolicy == ir.OtherNone {
		if request.Limit.SQL == "" {
			return CompiledOther{}, ErrInvalidRemainderPlan
		}
		return CompiledOther{SQL: request.Limit.SQL, MetricPlans: []MetricRemainderPlan{}}, nil
	}
	if query.OtherPolicy != ir.OtherAggregateRemainder || request.Limit.CTE == "" ||
		request.Limit.Limit < 1 || request.Limit.Limit > ir.MaxTopN ||
		len(request.GroupColumns) != len(query.GroupBy) || len(request.Metrics) != len(query.Metrics) {
		return CompiledOther{}, ErrInvalidRemainderPlan
	}
	groups, err := validateRemainderColumns(request.GroupColumns)
	if err != nil {
		return CompiledOther{}, err
	}
	outputSet := make(map[string]struct{}, len(request.Limit.OutputColumns))
	for _, column := range request.Limit.OutputColumns {
		outputSet[column] = struct{}{}
	}
	for _, group := range groups {
		if _, selected := outputSet[group]; !selected {
			return CompiledOther{}, ErrInvalidRemainderPlan
		}
	}
	metricByID := make(map[askdata.ID]RemainderMetric, len(request.Metrics))
	needsRecompute := false
	for _, metric := range request.Metrics {
		if metric.Contract.MetricVersionID.Validate() != nil || !validOrderIdentifier(metric.OutputColumn) {
			return CompiledOther{}, ErrInvalidRemainderPlan
		}
		if _, selected := outputSet[metric.OutputColumn]; !selected {
			return CompiledOther{}, ErrInvalidRemainderPlan
		}
		if _, duplicate := metricByID[metric.Contract.MetricVersionID]; duplicate {
			return CompiledOther{}, ErrInvalidRemainderPlan
		}
		plan, planErr := (AggregationPlanner{}).PlanMetric(metric.Contract, query)
		if planErr != nil {
			return CompiledOther{}, planErr
		}
		if plan.Additivity != registry.FullyAdditive {
			needsRecompute = true
			if !validOrderIdentifier(metric.RecomputedColumn) {
				return CompiledOther{}, ErrInvalidRemainderPlan
			}
		}
		metricByID[metric.Contract.MetricVersionID] = metric
	}
	if needsRecompute && !validOrderIdentifier(request.RecomputedRemainderRelation) {
		return CompiledOther{}, ErrInvalidRemainderPlan
	}

	metricColumns := make([]string, 0, len(query.Metrics))
	otherExpressions := make([]string, 0, len(query.Metrics))
	plans := make([]MetricRemainderPlan, 0, len(query.Metrics))
	for _, selected := range query.Metrics {
		metric, exists := metricByID[selected.MetricVersionID]
		if !exists || selected.Alias != metric.OutputColumn {
			return CompiledOther{}, ErrInvalidRemainderPlan
		}
		metricColumns = append(metricColumns, metric.OutputColumn)
		plan, _ := (AggregationPlanner{}).PlanMetric(metric.Contract, query)
		strategy := RemainderTotalMinusTop
		expression := "COALESCE((SELECT SUM(" + qualifiedOrderColumn("all_rows", metric.OutputColumn) +
			") FROM " + quoteOrderIdentifier("ranked_rows") + " AS " + quoteOrderIdentifier("all_rows") + "), 0) - " +
			"COALESCE((SELECT SUM(" + qualifiedOrderColumn("top_rows", metric.OutputColumn) + ") FROM " +
			quoteOrderIdentifier("limited_rows") + " AS " + quoteOrderIdentifier("top_rows") + "), 0)"
		if plan.Additivity != registry.FullyAdditive {
			strategy = RemainderRecompute
			expression = "(SELECT " + qualifiedOrderColumn("recomputed", metric.RecomputedColumn) + " FROM " +
				quoteOrderIdentifier(request.RecomputedRemainderRelation) + " AS " + quoteOrderIdentifier("recomputed") + ")"
		}
		otherExpressions = append(otherExpressions, expression+" AS "+quoteOrderIdentifier(metric.OutputColumn))
		plans = append(plans, MetricRemainderPlan{MetricVersionID: selected.MetricVersionID, Strategy: strategy})
	}

	otherColumns := make([]string, 0, len(groups)+len(otherExpressions)+2)
	for _, group := range groups {
		// Selecting from an always-empty typed relation preserves DATE, numeric
		// and other group key types across UNION ALL; a bare NULL would become
		// text inside the CTE and fail for non-text dimensions.
		otherColumns = append(otherColumns, "(SELECT "+qualifiedOrderColumn("typed_group", group)+
			" FROM "+quoteOrderIdentifier("ranked_rows")+" AS "+quoteOrderIdentifier("typed_group")+
			" WHERE FALSE) AS "+quoteOrderIdentifier(group))
	}
	otherColumns = append(otherColumns, otherExpressions...)
	otherColumns = append(otherColumns,
		"TRUE AS "+quoteOrderIdentifier("is_remainder"),
		"(SELECT COUNT(*) FROM "+quoteOrderIdentifier("remainder_rows")+") AS "+quoteOrderIdentifier("remainder_member_count"),
	)
	selectedColumns := append(append([]string(nil), groups...), metricColumns...)
	topColumns := qualifiedOrderColumns("limited_rows", selectedColumns)
	topColumns = append(topColumns,
		"FALSE AS "+quoteOrderIdentifier("is_remainder"),
		"0::bigint AS "+quoteOrderIdentifier("remainder_member_count"),
	)
	orderParts := []string{quoteOrderIdentifier("is_remainder") + " ASC"}
	for _, group := range groups {
		orderParts = append(orderParts, quoteOrderIdentifier(group)+" ASC NULLS LAST")
	}
	sql := "WITH " + request.Limit.CTE + ",\n" + quoteOrderIdentifier("remainder_rows") + " AS (\n" +
		"  SELECT * FROM " + quoteOrderIdentifier("ranked_rows") + " WHERE " +
		quoteOrderIdentifier("__row_rank") + " > " + fmt.Sprint(request.Limit.Limit) + "\n),\n" +
		quoteOrderIdentifier("other_row") + " AS (\n  SELECT\n    " + strings.Join(otherColumns, ",\n    ") +
		"\n  WHERE EXISTS (SELECT 1 FROM " + quoteOrderIdentifier("remainder_rows") + ")\n)\n" +
		"SELECT " + strings.Join(topColumns, ", ") + " FROM " + quoteOrderIdentifier("limited_rows") + "\n" +
		"UNION ALL\nSELECT " + strings.Join(qualifiedOrderColumns("other_row", append(selectedColumns,
		"is_remainder", "remainder_member_count")), ", ") + " FROM " + quoteOrderIdentifier("other_row") +
		"\nORDER BY " + strings.Join(orderParts, ", ")
	return CompiledOther{SQL: sql, MetricPlans: plans}, nil
}

func validateRemainderColumns(values []string) ([]string, error) {
	result := append([]string(nil), values...)
	seen := map[string]struct{}{}
	for _, value := range result {
		if !validOrderIdentifier(value) {
			return nil, ErrInvalidRemainderPlan
		}
		if _, duplicate := seen[value]; duplicate {
			return nil, ErrInvalidRemainderPlan
		}
		seen[value] = struct{}{}
	}
	return result, nil
}
