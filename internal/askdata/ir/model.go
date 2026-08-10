// Package ir builds replay-validated semantic IR artifacts and re-exports the
// frozen dependency-neutral IR contract used by the cognition and compiler
// boundaries.
package ir

import (
	"time"

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
	DefaultTopN         = ircontract.DefaultTopN
	MaxTopN             = ircontract.MaxTopN
	MaxResultRows       = ircontract.MaxResultRows
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

// ResolvedTimeSpec is the deterministic, replay-safe result of applying a
// certified Time Contract to the relative request retained in Semantic IR.
// All intervals are half-open and carry the business timezone offset.
type ResolvedTimeSpec struct {
	RequestedPeriod             string              `json:"requestedPeriod"`
	Grain                       string              `json:"grain"`
	PolicyApplied               string              `json:"policyApplied"`
	PolicySource                string              `json:"policySource"`
	ResolvedStart               time.Time           `json:"resolvedStart"`
	ResolvedEndExclusive        time.Time           `json:"resolvedEndExclusive"`
	DataAvailableThrough        time.Time           `json:"dataAvailableThrough"`
	TruncatedByDataAvailability bool                `json:"truncatedByDataAvailability"`
	PeriodFallbackApplied       bool                `json:"periodFallbackApplied"`
	Timezone                    string              `json:"timezone"`
	CalendarVersionID           string              `json:"calendarVersionId,omitempty"`
	Comparison                  *ResolvedComparison `json:"comparison,omitempty"`
}

type ResolvedComparison struct {
	Type                 string    `json:"type"`
	Periods              int       `json:"periods"`
	Alignment            string    `json:"alignment"`
	ResolvedStart        time.Time `json:"resolvedStart"`
	ResolvedEndExclusive time.Time `json:"resolvedEndExclusive"`
	OverflowApplied      bool      `json:"overflowApplied"`
}
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

type RankBy = ircontract.RankBy

const (
	RankByCurrentValue = ircontract.RankByCurrentValue
	RankByDelta        = ircontract.RankByDelta
	RankByRatio        = ircontract.RankByRatio
)

type OtherPolicy = ircontract.OtherPolicy

const (
	OtherNone               = ircontract.OtherNone
	OtherAggregateRemainder = ircontract.OtherAggregateRemainder
)

type TieBreaking = ircontract.TieBreaking

const (
	TieIncludeAll       = ircontract.TieIncludeAll
	TieDeterministicCut = ircontract.TieDeterministicCut
)

type Sort = ircontract.Sort
type SemanticIR = ircontract.SemanticIR

func Decode(raw []byte) (SemanticIR, error) { return ircontract.Decode(raw) }

func Normalize(value SemanticIR) SemanticIR { return ircontract.Normalize(value) }

func Canonicalize(value SemanticIR) (SemanticIR, []byte, askdata.ContentHash, error) {
	return ircontract.Canonicalize(value)
}
