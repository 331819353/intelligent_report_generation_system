// Package ir builds replay-validated semantic IR artifacts and re-exports the
// frozen dependency-neutral IR contract used by the cognition and compiler
// boundaries.
package ir

import (
	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/ircontract"
)

const (
	Version             = ircontract.Version
	MaxMetrics          = ircontract.MaxMetrics
	MaxGroupBy          = ircontract.MaxGroupBy
	MaxFilters          = ircontract.MaxFilters
	MaxMembersPerFilter = ircontract.MaxMembersPerFilter
	MaxSorts            = ircontract.MaxSorts
	MaxLimit            = ircontract.MaxLimit
)

type Metric = ircontract.Metric
type TimeGrain = ircontract.TimeGrain

const (
	TimeGrainDay     = ircontract.TimeGrainDay
	TimeGrainWeek    = ircontract.TimeGrainWeek
	TimeGrainMonth   = ircontract.TimeGrainMonth
	TimeGrainQuarter = ircontract.TimeGrainQuarter
	TimeGrainYear    = ircontract.TimeGrainYear
)

type GroupBy = ircontract.GroupBy
type FilterOperator = ircontract.FilterOperator

const (
	FilterEquals    = ircontract.FilterEquals
	FilterNotEquals = ircontract.FilterNotEquals
	FilterIn        = ircontract.FilterIn
	FilterNotIn     = ircontract.FilterNotIn
	FilterIsNull    = ircontract.FilterIsNull
	FilterIsNotNull = ircontract.FilterIsNotNull
)

type Filter = ircontract.Filter
type TimeRange = ircontract.TimeRange
type ComparisonType = ircontract.ComparisonType

const (
	ComparisonYearOverYear     = ircontract.ComparisonYearOverYear
	ComparisonMonthOverMonth   = ircontract.ComparisonMonthOverMonth
	ComparisonPeriodOverPeriod = ircontract.ComparisonPeriodOverPeriod
)

type Comparison = ircontract.Comparison
type SortTargetType = ircontract.SortTargetType

const (
	SortTargetMetric    = ircontract.SortTargetMetric
	SortTargetDimension = ircontract.SortTargetDimension
)

type SortDirection = ircontract.SortDirection

const (
	SortAscending  = ircontract.SortAscending
	SortDescending = ircontract.SortDescending
)

type NullOrdering = ircontract.NullOrdering

const (
	NullsFirst = ircontract.NullsFirst
	NullsLast  = ircontract.NullsLast
)

type Sort = ircontract.Sort
type SemanticIR = ircontract.SemanticIR

func Decode(raw []byte) (SemanticIR, error) { return ircontract.Decode(raw) }

func Normalize(value SemanticIR) SemanticIR { return ircontract.Normalize(value) }

func Canonicalize(value SemanticIR) (SemanticIR, []byte, askdata.ContentHash, error) {
	return ircontract.Canonicalize(value)
}
