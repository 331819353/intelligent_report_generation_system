// Package ircontract defines the dependency-neutral, version-pinned semantic
// intermediate representation accepted by the deterministic query compiler.
package ircontract

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"intelligent-report-generation-system/internal/askdata"
)

const (
	Version             = "1.0"
	MaxMetrics          = 16
	MaxGroupBy          = 32
	MaxFilters          = 32
	MaxMembersPerFilter = 100
	MaxSorts            = 8
	DefaultTopN         = 10
	MaxTopN             = 1_000
	MaxResultRows       = 10_000
	// MaxLimit remains the execution/result safety ceiling consumed by the
	// tool host and validator. SemanticIR.Limit is a TopN contract and uses
	// MaxTopN instead.
	MaxLimit = MaxResultRows
)

var outputAliasPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

type Metric struct {
	MetricVersionID askdata.ID `json:"metricVersionId"`
	Alias           string     `json:"alias"`
}

type TimeGrain string

const (
	TimeGrainDay     TimeGrain = "DAY"
	TimeGrainWeek    TimeGrain = "WEEK"
	TimeGrainMonth   TimeGrain = "MONTH"
	TimeGrainQuarter TimeGrain = "QUARTER"
	TimeGrainYear    TimeGrain = "YEAR"
)

type GroupBy struct {
	DimensionVersionID askdata.ID `json:"dimensionVersionId"`
	Grain              *TimeGrain `json:"grain"`
}

type FilterOperator string

const (
	FilterEquals    FilterOperator = "EQUALS"
	FilterNotEquals FilterOperator = "NOT_EQUALS"
	FilterIn        FilterOperator = "IN"
	FilterNotIn     FilterOperator = "NOT_IN"
	FilterIsNull    FilterOperator = "IS_NULL"
	FilterIsNotNull FilterOperator = "IS_NOT_NULL"
)

type Filter struct {
	DimensionVersionID askdata.ID     `json:"dimensionVersionId"`
	Operator           FilterOperator `json:"operator"`
	MemberVersionIDs   []askdata.ID   `json:"memberVersionIds"`
}

// TimeRange is half-open [start, endExclusive) in the named IANA timezone.
// Dates are calendar dates in YYYY-MM-DD format and are never raw SQL values.
type TimeRange struct {
	DimensionVersionID askdata.ID `json:"dimensionVersionId"`
	Start              string     `json:"start"`
	EndExclusive       string     `json:"endExclusive"`
	Timezone           string     `json:"timezone"`
	RequestedPeriod    string     `json:"requestedPeriod,omitempty"`
	Grain              TimeGrain  `json:"grain,omitempty"`
}

type ComparisonType string

const (
	ComparisonYearOverYear     ComparisonType = "YEAR_OVER_YEAR"
	ComparisonMonthOverMonth   ComparisonType = "MONTH_OVER_MONTH"
	ComparisonPeriodOverPeriod ComparisonType = "PERIOD_OVER_PERIOD"
)

type Comparison struct {
	Type    ComparisonType `json:"type"`
	Periods int            `json:"periods"`
}

type SortTargetType string

const (
	SortTargetMetric    SortTargetType = "METRIC"
	SortTargetDimension SortTargetType = "DIMENSION"
)

type SortDirection string

const (
	SortAscending  SortDirection = "ASC"
	SortDescending SortDirection = "DESC"
)

type NullOrdering string

const (
	NullsFirst NullOrdering = "FIRST"
	NullsLast  NullOrdering = "LAST"
)

type RankBy string

const (
	RankByCurrentValue RankBy = "CURRENT_VALUE"
	RankByDelta        RankBy = "DELTA"
	RankByRatio        RankBy = "RATIO"
)

type OtherPolicy string

const (
	OtherNone               OtherPolicy = "NONE"
	OtherAggregateRemainder OtherPolicy = "AGGREGATE_REMAINDER"
)

type TieBreaking string

const (
	TieIncludeAll       TieBreaking = "INCLUDE_ALL"
	TieDeterministicCut TieBreaking = "DETERMINISTIC_CUT"
)

type Sort struct {
	TargetType      SortTargetType `json:"targetType"`
	TargetVersionID askdata.ID     `json:"targetVersionId"`
	Direction       SortDirection  `json:"direction"`
	Nulls           NullOrdering   `json:"nulls"`
	RankBy          RankBy         `json:"rankBy"`
}

// SemanticIR contains only stable semantic IDs and bounded logical operators.
// It deliberately cannot represent a physical identifier, SQL fragment or an
// unbound user-provided dimension value.
type SemanticIR struct {
	IRVersion           string              `json:"irVersion"`
	SemanticReleaseID   askdata.ID          `json:"semanticReleaseId"`
	SemanticContentHash askdata.ContentHash `json:"semanticContentHash"`
	DomainID            askdata.ID          `json:"domainId"`
	ModelVersionID      askdata.ID          `json:"modelVersionId"`
	Metrics             []Metric            `json:"metrics"`
	GroupBy             []GroupBy           `json:"groupBy"`
	Filters             []Filter            `json:"filters"`
	TimeRange           *TimeRange          `json:"timeRange"`
	Comparison          *Comparison         `json:"comparison"`
	Sort                []Sort              `json:"sort"`
	Limit               int                 `json:"limit"`
	OtherPolicy         OtherPolicy         `json:"otherPolicy"`
	TieBreaking         TieBreaking         `json:"tieBreaking"`
}

// Decode rejects unknown fields and returns the normalized form that must be
// passed to hashing, authorization and compilation.
func Decode(raw []byte) (SemanticIR, error) {
	var value SemanticIR
	if err := askdata.DecodeStrictJSON(raw, &value); err != nil {
		return SemanticIR{}, err
	}
	normalized := Normalize(value)
	if err := normalized.Validate(); err != nil {
		return SemanticIR{}, err
	}
	return normalized, nil
}

func (value SemanticIR) Validate() error {
	if value.IRVersion != Version {
		return fmt.Errorf("irVersion must be %q", Version)
	}
	if err := value.SemanticReleaseID.Validate(); err != nil {
		return fmt.Errorf("semanticReleaseId: %w", err)
	}
	if err := value.SemanticContentHash.Validate(); err != nil {
		return fmt.Errorf("semanticContentHash: %w", err)
	}
	if err := value.DomainID.Validate(); err != nil {
		return fmt.Errorf("domainId: %w", err)
	}
	if err := value.ModelVersionID.Validate(); err != nil {
		return fmt.Errorf("modelVersionId: %w", err)
	}
	if len(value.Metrics) == 0 || len(value.Metrics) > MaxMetrics {
		return fmt.Errorf("metrics count must be between 1 and %d", MaxMetrics)
	}
	metricIDs := make(map[askdata.ID]struct{}, len(value.Metrics))
	aliases := make(map[string]struct{}, len(value.Metrics))
	for index, metric := range value.Metrics {
		if err := metric.MetricVersionID.Validate(); err != nil {
			return fmt.Errorf("metrics[%d].metricVersionId: %w", index, err)
		}
		if _, exists := metricIDs[metric.MetricVersionID]; exists {
			return fmt.Errorf("metrics[%d] duplicates metricVersionId %q", index, metric.MetricVersionID)
		}
		metricIDs[metric.MetricVersionID] = struct{}{}
		if !outputAliasPattern.MatchString(metric.Alias) {
			return fmt.Errorf("metrics[%d].alias is invalid", index)
		}
		if _, exists := aliases[metric.Alias]; exists {
			return fmt.Errorf("metrics[%d] duplicates alias %q", index, metric.Alias)
		}
		aliases[metric.Alias] = struct{}{}
	}
	if len(value.GroupBy) > MaxGroupBy {
		return fmt.Errorf("groupBy exceeds %d items", MaxGroupBy)
	}
	dimensionIDs := make(map[askdata.ID]struct{}, len(value.GroupBy))
	for index, group := range value.GroupBy {
		if err := group.DimensionVersionID.Validate(); err != nil {
			return fmt.Errorf("groupBy[%d].dimensionVersionId: %w", index, err)
		}
		if _, exists := dimensionIDs[group.DimensionVersionID]; exists {
			return fmt.Errorf("groupBy[%d] duplicates dimensionVersionId %q", index, group.DimensionVersionID)
		}
		dimensionIDs[group.DimensionVersionID] = struct{}{}
		if group.Grain != nil && !validTimeGrain(*group.Grain) {
			return fmt.Errorf("groupBy[%d].grain is invalid", index)
		}
	}
	if len(value.Filters) > MaxFilters {
		return fmt.Errorf("filters exceeds %d items", MaxFilters)
	}
	filterKeys := map[string]struct{}{}
	for index, filter := range value.Filters {
		if err := filter.DimensionVersionID.Validate(); err != nil {
			return fmt.Errorf("filters[%d].dimensionVersionId: %w", index, err)
		}
		if !validFilterOperator(filter.Operator) {
			return fmt.Errorf("filters[%d].operator is invalid", index)
		}
		key := string(filter.DimensionVersionID) + "\x00" + string(filter.Operator)
		if _, exists := filterKeys[key]; exists {
			return fmt.Errorf("filters[%d] duplicates dimension/operator", index)
		}
		filterKeys[key] = struct{}{}
		if len(filter.MemberVersionIDs) > MaxMembersPerFilter {
			return fmt.Errorf("filters[%d].memberVersionIds exceeds %d items", index, MaxMembersPerFilter)
		}
		requiresMembers := filter.Operator != FilterIsNull && filter.Operator != FilterIsNotNull
		if requiresMembers && len(filter.MemberVersionIDs) == 0 {
			return fmt.Errorf("filters[%d].memberVersionIds is required for %s", index, filter.Operator)
		}
		if !requiresMembers && len(filter.MemberVersionIDs) != 0 {
			return fmt.Errorf("filters[%d].memberVersionIds must be empty for %s", index, filter.Operator)
		}
		seenMembers := map[askdata.ID]struct{}{}
		for memberIndex, memberID := range filter.MemberVersionIDs {
			if err := memberID.Validate(); err != nil {
				return fmt.Errorf("filters[%d].memberVersionIds[%d]: %w", index, memberIndex, err)
			}
			if _, exists := seenMembers[memberID]; exists {
				return fmt.Errorf("filters[%d].memberVersionIds[%d] is duplicated", index, memberIndex)
			}
			seenMembers[memberID] = struct{}{}
		}
	}
	if value.TimeRange != nil {
		if err := value.TimeRange.Validate(); err != nil {
			return fmt.Errorf("timeRange: %w", err)
		}
	}
	if value.Comparison != nil {
		if !validComparisonType(value.Comparison.Type) {
			return errors.New("comparison.type is invalid")
		}
		if value.Comparison.Periods < 1 || value.Comparison.Periods > 120 {
			return errors.New("comparison.periods must be between 1 and 120")
		}
		if value.TimeRange == nil {
			return errors.New("comparison requires timeRange")
		}
	}
	if len(value.Sort) > MaxSorts {
		return fmt.Errorf("sort exceeds %d items", MaxSorts)
	}
	seenSorts := make(map[string]struct{}, len(value.Sort))
	for index, sortValue := range value.Sort {
		if err := sortValue.TargetVersionID.Validate(); err != nil {
			return fmt.Errorf("sort[%d].targetVersionId: %w", index, err)
		}
		switch sortValue.TargetType {
		case SortTargetMetric:
			if _, exists := metricIDs[sortValue.TargetVersionID]; !exists {
				return fmt.Errorf("sort[%d] references a metric not present in metrics", index)
			}
		case SortTargetDimension:
			if _, exists := dimensionIDs[sortValue.TargetVersionID]; !exists {
				return fmt.Errorf("sort[%d] references a dimension not present in groupBy", index)
			}
		default:
			return fmt.Errorf("sort[%d].targetType is invalid", index)
		}
		if sortValue.Direction != SortAscending && sortValue.Direction != SortDescending {
			return fmt.Errorf("sort[%d].direction is invalid", index)
		}
		if sortValue.Nulls != NullsFirst && sortValue.Nulls != NullsLast {
			return fmt.Errorf("sort[%d].nulls is invalid", index)
		}
		if sortValue.RankBy != RankByCurrentValue && sortValue.RankBy != RankByDelta &&
			sortValue.RankBy != RankByRatio {
			return fmt.Errorf("sort[%d].rankBy is invalid", index)
		}
		if value.Comparison == nil && sortValue.RankBy != RankByCurrentValue {
			return fmt.Errorf("sort[%d].rankBy requires comparison", index)
		}
		if sortValue.TargetType == SortTargetDimension && sortValue.RankBy != RankByCurrentValue {
			return fmt.Errorf("sort[%d].dimension target only supports CURRENT_VALUE", index)
		}
		key := string(sortValue.TargetType) + "\x00" + string(sortValue.TargetVersionID)
		if _, duplicate := seenSorts[key]; duplicate {
			return fmt.Errorf("sort[%d] duplicates a sort target", index)
		}
		seenSorts[key] = struct{}{}
	}
	if value.Limit < 1 || value.Limit > MaxTopN {
		return fmt.Errorf("limit must be between 1 and %d", MaxTopN)
	}
	if value.OtherPolicy != OtherNone && value.OtherPolicy != OtherAggregateRemainder {
		return errors.New("otherPolicy is invalid")
	}
	if value.TieBreaking != TieIncludeAll && value.TieBreaking != TieDeterministicCut {
		return errors.New("tieBreaking is invalid")
	}
	if value.OtherPolicy == OtherAggregateRemainder && (len(value.Sort) == 0 || len(value.GroupBy) == 0) {
		return errors.New("AGGREGATE_REMAINDER requires sort and groupBy")
	}
	return nil
}

func (value TimeRange) Validate() error {
	if err := value.DimensionVersionID.Validate(); err != nil {
		return fmt.Errorf("dimensionVersionId: %w", err)
	}
	start, err := time.Parse("2006-01-02", value.Start)
	if err != nil {
		return errors.New("start must use YYYY-MM-DD")
	}
	end, err := time.Parse("2006-01-02", value.EndExclusive)
	if err != nil {
		return errors.New("endExclusive must use YYYY-MM-DD")
	}
	if !end.After(start) {
		return errors.New("endExclusive must be after start")
	}
	if strings.TrimSpace(value.Timezone) == "" {
		return errors.New("timezone is required")
	}
	if _, err := time.LoadLocation(value.Timezone); err != nil {
		return errors.New("timezone must be a known IANA timezone")
	}
	if (value.RequestedPeriod == "") != (value.Grain == "") {
		return errors.New("requestedPeriod and grain must be supplied together")
	}
	if value.RequestedPeriod != "" {
		if !regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`).MatchString(value.RequestedPeriod) {
			return errors.New("requestedPeriod is invalid")
		}
		if !validTimeGrain(value.Grain) {
			return errors.New("grain is invalid")
		}
	}
	return nil
}

func validTimeGrain(value TimeGrain) bool {
	switch value {
	case TimeGrainDay, TimeGrainWeek, TimeGrainMonth, TimeGrainQuarter, TimeGrainYear:
		return true
	default:
		return false
	}
}

func validFilterOperator(value FilterOperator) bool {
	switch value {
	case FilterEquals, FilterNotEquals, FilterIn, FilterNotIn, FilterIsNull, FilterIsNotNull:
		return true
	default:
		return false
	}
}

func validComparisonType(value ComparisonType) bool {
	switch value {
	case ComparisonYearOverYear, ComparisonMonthOverMonth, ComparisonPeriodOverPeriod:
		return true
	default:
		return false
	}
}
