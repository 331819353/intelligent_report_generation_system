package compiler

import (
	"errors"
	"fmt"
	"strings"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/ir"
)

const PlanInvalidSortTargetCode = "PLAN_INVALID_SORT_TARGET"

var (
	ErrInvalidSortPlan  = errors.New("semantic sort plan is invalid")
	ErrInvalidLimitPlan = errors.New("semantic TopN plan is invalid")
)

// SortPlanError contains only governed semantic identities. Physical column
// names stay out of user-facing failures.
type SortPlanError struct {
	Code            string            `json:"code"`
	TargetType      ir.SortTargetType `json:"targetType"`
	TargetVersionID askdata.ID        `json:"targetVersionId"`
}

func (failure *SortPlanError) Error() string {
	return fmt.Sprintf("%s: %s/%s", failure.Code, failure.TargetType, failure.TargetVersionID)
}

func (failure *SortPlanError) Unwrap() error { return ErrInvalidSortPlan }

// SortColumnBinding is produced from release-pinned result projections. The
// optional comparison columns are compiler-owned aliases, never user text.
type SortColumnBinding struct {
	TargetType      ir.SortTargetType
	TargetVersionID askdata.ID
	CurrentColumn   string
	DeltaColumn     string
	RatioColumn     string
}

type SortCompileRequest struct {
	Query              ir.SemanticIR
	Columns            []SortColumnBinding
	StableGroupColumns []string
}

type CompiledSort struct {
	RankOrderBy        string
	RowOrderBy         string
	StableGroupColumns []string
	TieBreaking        ir.TieBreaking
}

// CompileSort binds every semantic sort target to an already-selected result
// column. INCLUDE_ALL deliberately excludes stable group keys from the RANK
// window so equal boundary values remain tied. DETERMINISTIC_CUT adds those
// keys only to ROW_NUMBER's ordering.
func CompileSort(request SortCompileRequest) (CompiledSort, error) {
	query := ir.Normalize(request.Query)
	if len(query.Sort) == 0 || len(query.Sort) > ir.MaxSorts ||
		(query.TieBreaking != ir.TieIncludeAll && query.TieBreaking != ir.TieDeterministicCut) {
		return CompiledSort{}, ErrInvalidSortPlan
	}
	bindings := make(map[string]SortColumnBinding, len(request.Columns))
	for _, binding := range request.Columns {
		key := sortBindingKey(binding.TargetType, binding.TargetVersionID)
		if binding.TargetVersionID.Validate() != nil ||
			(binding.TargetType != ir.SortTargetMetric && binding.TargetType != ir.SortTargetDimension) ||
			!validOrderIdentifier(binding.CurrentColumn) {
			return CompiledSort{}, ErrInvalidSortPlan
		}
		if binding.DeltaColumn != "" && !validOrderIdentifier(binding.DeltaColumn) {
			return CompiledSort{}, ErrInvalidSortPlan
		}
		if binding.RatioColumn != "" && !validOrderIdentifier(binding.RatioColumn) {
			return CompiledSort{}, ErrInvalidSortPlan
		}
		if _, duplicate := bindings[key]; duplicate {
			return CompiledSort{}, ErrInvalidSortPlan
		}
		bindings[key] = binding
	}

	semanticParts := make([]string, 0, len(query.Sort))
	seenTargets := map[string]struct{}{}
	for _, value := range query.Sort {
		if !selectedSortTarget(query, value) {
			return CompiledSort{}, &SortPlanError{
				Code: PlanInvalidSortTargetCode, TargetType: value.TargetType,
				TargetVersionID: value.TargetVersionID,
			}
		}
		key := sortBindingKey(value.TargetType, value.TargetVersionID)
		if _, duplicate := seenTargets[key]; duplicate {
			return CompiledSort{}, ErrInvalidSortPlan
		}
		seenTargets[key] = struct{}{}
		binding, exists := bindings[key]
		if !exists || (value.Direction != ir.SortAscending && value.Direction != ir.SortDescending) ||
			(value.Nulls != ir.NullsFirst && value.Nulls != ir.NullsLast) {
			return CompiledSort{}, ErrInvalidSortPlan
		}
		column := binding.CurrentColumn
		switch value.RankBy {
		case ir.RankByCurrentValue:
		case ir.RankByDelta:
			column = binding.DeltaColumn
		case ir.RankByRatio:
			column = binding.RatioColumn
		default:
			return CompiledSort{}, ErrInvalidSortPlan
		}
		if value.TargetType == ir.SortTargetDimension && value.RankBy != ir.RankByCurrentValue ||
			!validOrderIdentifier(column) {
			return CompiledSort{}, ErrInvalidSortPlan
		}
		semanticParts = append(semanticParts, qualifiedOrderColumn("rank_source", column)+" "+
			string(value.Direction)+" NULLS "+string(value.Nulls))
	}

	stableColumns := make([]string, 0, len(request.StableGroupColumns))
	seenStable := map[string]struct{}{}
	for _, column := range request.StableGroupColumns {
		if !validOrderIdentifier(column) {
			return CompiledSort{}, ErrInvalidSortPlan
		}
		if _, duplicate := seenStable[column]; duplicate {
			return CompiledSort{}, ErrInvalidSortPlan
		}
		seenStable[column] = struct{}{}
		stableColumns = append(stableColumns, column)
	}
	if query.TieBreaking == ir.TieDeterministicCut && len(query.GroupBy) != len(stableColumns) {
		return CompiledSort{}, ErrInvalidSortPlan
	}
	rowParts := append([]string(nil), semanticParts...)
	if query.TieBreaking == ir.TieDeterministicCut {
		for _, column := range stableColumns {
			rowParts = append(rowParts, qualifiedOrderColumn("rank_source", column)+" ASC NULLS LAST")
		}
	}
	return CompiledSort{
		RankOrderBy: strings.Join(semanticParts, ", "), RowOrderBy: strings.Join(rowParts, ", "),
		StableGroupColumns: stableColumns, TieBreaking: query.TieBreaking,
	}, nil
}

type LimitCompileRequest struct {
	SourceRelation string
	OutputColumns  []string
	Sort           CompiledSort
	Limit          *int
}

// CompiledLimit exposes the CTE body separately so CompileOther and future
// comparison composition can reuse the exact same ranking relation.
type CompiledLimit struct {
	SQL            string
	MetadataSQL    string
	CTE            string
	SourceRelation string
	OutputColumns  []string
	Limit          int
	TieBreaking    ir.TieBreaking
}

// TopNMetadata is returned by CompiledLimit.MetadataSQL. ActualRowCount is
// measured after tie handling, not assumed to equal the requested N.
type TopNMetadata struct {
	TiesIncluded   bool `json:"tiesIncluded"`
	TiesCut        bool `json:"tiesCut"`
	ActualRowCount int  `json:"actualRowCount"`
}

func CompileLimit(request LimitCompileRequest) (CompiledLimit, error) {
	limit := ir.DefaultTopN
	if request.Limit != nil {
		limit = *request.Limit
	}
	if limit < 1 || limit > ir.MaxTopN || !validOrderIdentifier(request.SourceRelation) ||
		request.Sort.RankOrderBy == "" || request.Sort.RowOrderBy == "" ||
		(request.Sort.TieBreaking != ir.TieIncludeAll && request.Sort.TieBreaking != ir.TieDeterministicCut) {
		return CompiledLimit{}, ErrInvalidLimitPlan
	}
	outputs, err := validateOrderColumns(request.OutputColumns)
	if err != nil {
		return CompiledLimit{}, err
	}
	rankFunction := "RANK"
	if request.Sort.TieBreaking == ir.TieDeterministicCut {
		rankFunction = "ROW_NUMBER"
	}
	cte := quoteOrderIdentifier("ranked_rows") + " AS (\n" +
		"  SELECT " + quoteOrderIdentifier("rank_source") + ".*,\n" +
		"    RANK() OVER (ORDER BY " + request.Sort.RankOrderBy + ") AS " + quoteOrderIdentifier("__tie_rank") + ",\n" +
		"    " + rankFunction + "() OVER (ORDER BY " + request.Sort.RowOrderBy + ") AS " + quoteOrderIdentifier("__row_rank") + "\n" +
		"  FROM " + quoteOrderIdentifier(request.SourceRelation) + " AS " + quoteOrderIdentifier("rank_source") + "\n" +
		"),\n" + quoteOrderIdentifier("limited_rows") + " AS (\n" +
		"  SELECT * FROM " + quoteOrderIdentifier("ranked_rows") +
		" WHERE " + quoteOrderIdentifier("__row_rank") + " <= " + fmt.Sprint(limit) + "\n)"
	selectColumns := qualifiedOrderColumns("limited_rows", outputs)
	sql := "WITH " + cte + "\nSELECT " + strings.Join(selectColumns, ", ") +
		"\nFROM " + quoteOrderIdentifier("limited_rows") +
		"\nORDER BY " + qualifiedOrderColumn("limited_rows", "__row_rank")
	metadataSQL := "WITH " + cte + "\nSELECT\n" +
		"  ((SELECT COUNT(*) FROM " + quoteOrderIdentifier("limited_rows") + ") > " + fmt.Sprint(limit) +
		") AS " + quoteOrderIdentifier("ties_included") + ",\n" +
		"  EXISTS (SELECT 1 FROM " + quoteOrderIdentifier("ranked_rows") + " AS " + quoteOrderIdentifier("excluded") +
		" WHERE " + qualifiedOrderColumn("excluded", "__row_rank") + " > " + fmt.Sprint(limit) +
		" AND " + qualifiedOrderColumn("excluded", "__tie_rank") + " = (SELECT " +
		qualifiedOrderColumn("boundary", "__tie_rank") + " FROM " + quoteOrderIdentifier("ranked_rows") + " AS " +
		quoteOrderIdentifier("boundary") + " WHERE " + qualifiedOrderColumn("boundary", "__row_rank") + " = " +
		fmt.Sprint(limit) + " LIMIT 1)) AS " + quoteOrderIdentifier("ties_cut") + ",\n" +
		"  (SELECT COUNT(*) FROM " + quoteOrderIdentifier("limited_rows") + ") AS " + quoteOrderIdentifier("actual_row_count")
	return CompiledLimit{
		SQL: sql, MetadataSQL: metadataSQL, CTE: cte, SourceRelation: request.SourceRelation,
		OutputColumns: outputs, Limit: limit, TieBreaking: request.Sort.TieBreaking,
	}, nil
}

func selectedSortTarget(query ir.SemanticIR, value ir.Sort) bool {
	switch value.TargetType {
	case ir.SortTargetMetric:
		for _, metric := range query.Metrics {
			if metric.MetricVersionID == value.TargetVersionID {
				return true
			}
		}
	case ir.SortTargetDimension:
		for _, group := range query.GroupBy {
			if group.DimensionVersionID == value.TargetVersionID {
				return true
			}
		}
	}
	return false
}

func sortBindingKey(kind ir.SortTargetType, id askdata.ID) string {
	return string(kind) + "\x00" + string(id)
}

func validateOrderColumns(values []string) ([]string, error) {
	if len(values) == 0 || len(values) > ir.MaxGroupBy+ir.MaxMetrics*3 {
		return nil, ErrInvalidLimitPlan
	}
	result := append([]string(nil), values...)
	seen := map[string]struct{}{}
	for _, value := range result {
		if !validOrderIdentifier(value) || value == "__tie_rank" || value == "__row_rank" ||
			value == "is_remainder" || value == "remainder_member_count" {
			return nil, ErrInvalidLimitPlan
		}
		if _, duplicate := seen[value]; duplicate {
			return nil, ErrInvalidLimitPlan
		}
		seen[value] = struct{}{}
	}
	return result, nil
}

func qualifiedOrderColumns(alias string, columns []string) []string {
	result := make([]string, len(columns))
	for index, column := range columns {
		result[index] = qualifiedOrderColumn(alias, column)
	}
	return result
}

func qualifiedOrderColumn(alias, column string) string {
	return quoteOrderIdentifier(alias) + "." + quoteOrderIdentifier(column)
}

func quoteOrderIdentifier(value string) string { return `"` + value + `"` }

func validOrderIdentifier(value string) bool { return validJoinIdentifier(value) }
