// Package ir defines the version-pinned semantic intermediate representation
// accepted by the deterministic query compiler.
package ir

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
	MaxLimit            = 10_000
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

type Sort struct {
	TargetType      SortTargetType `json:"targetType"`
	TargetVersionID askdata.ID     `json:"targetVersionId"`
	Direction       SortDirection  `json:"direction"`
	Nulls           NullOrdering   `json:"nulls"`
}

// SemanticIR contains only stable semantic IDs and bounded logical operators.
// It deliberately cannot represent a physical identifier, SQL fragment or an
// unbound user-provided dimension value.
type SemanticIR struct {
	IRVersion           string              `json:"irVersion"`
	SemanticReleaseID   askdata.ID          `json:"semanticReleaseId"`
	SemanticContentHash askdata.ContentHash `json:"semanticContentHash"`
	ModelVersionID      askdata.ID          `json:"modelVersionId"`
	Metrics             []Metric            `json:"metrics"`
	GroupBy             []GroupBy           `json:"groupBy"`
	Filters             []Filter            `json:"filters"`
	TimeRange           *TimeRange          `json:"timeRange"`
	Comparison          *Comparison         `json:"comparison"`
	Sort                []Sort              `json:"sort"`
	Limit               int                 `json:"limit"`
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
	}
	if value.Limit < 1 || value.Limit > MaxLimit {
		return fmt.Errorf("limit must be between 1 and %d", MaxLimit)
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
