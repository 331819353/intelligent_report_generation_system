// Package understanding contains the storage-independent contract describing
// what a user said before any mention is bound to a semantic object.
package understanding

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"intelligent-report-generation-system/internal/askdata"
)

const (
	SchemaVersion            = "1.0"
	MaxQuestionRunes         = 4096
	MaxDomainHypotheses      = 8
	MaxMetricMentions        = 16
	MaxDimensionMentions     = 32
	MaxValueMentions         = 32
	MaxComparisonMentions    = 8
	MaxOrderingMentions      = 8
	MaxUnresolvedSpans       = 32
	MaxEvidencePerHypothesis = 16
)

// Span uses zero-based Unicode code-point offsets. End is exclusive.
type Span struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

type DomainHypothesis struct {
	DomainID     askdata.ID            `json:"domainId"`
	Score        float64               `json:"score"`
	EvidenceRefs []askdata.EvidenceRef `json:"evidenceRefs"`
}

type AggregationHint string

const (
	AggregationDefault       AggregationHint = "DEFAULT"
	AggregationSum           AggregationHint = "SUM"
	AggregationAverage       AggregationHint = "AVG"
	AggregationMinimum       AggregationHint = "MIN"
	AggregationMaximum       AggregationHint = "MAX"
	AggregationCount         AggregationHint = "COUNT"
	AggregationCountDistinct AggregationHint = "COUNT_DISTINCT"
)

type MetricMention struct {
	Text            string          `json:"text"`
	Span            Span            `json:"span"`
	AggregationHint AggregationHint `json:"aggregationHint"`
}

type DimensionRole string

const (
	DimensionRoleGroupBy DimensionRole = "GROUP_BY"
	DimensionRoleFilter  DimensionRole = "FILTER"
	DimensionRoleTime    DimensionRole = "TIME"
	DimensionRoleSort    DimensionRole = "SORT"
)

type TimeGrain string

const (
	TimeGrainDay     TimeGrain = "DAY"
	TimeGrainWeek    TimeGrain = "WEEK"
	TimeGrainMonth   TimeGrain = "MONTH"
	TimeGrainQuarter TimeGrain = "QUARTER"
	TimeGrainYear    TimeGrain = "YEAR"
)

type DimensionMention struct {
	Text  string        `json:"text"`
	Span  Span          `json:"span"`
	Role  DimensionRole `json:"role"`
	Grain *TimeGrain    `json:"grain"`
}

type ValueOperatorHint string

const (
	ValueOperatorDefault   ValueOperatorHint = "DEFAULT"
	ValueOperatorEquals    ValueOperatorHint = "EQUALS"
	ValueOperatorNotEquals ValueOperatorHint = "NOT_EQUALS"
	ValueOperatorIn        ValueOperatorHint = "IN"
	ValueOperatorNotIn     ValueOperatorHint = "NOT_IN"
)

type ValueMention struct {
	Text          string            `json:"text"`
	Span          Span              `json:"span"`
	DimensionHint *string           `json:"dimensionHint"`
	OperatorHint  ValueOperatorHint `json:"operatorHint"`
}

type TimeUnderstanding struct {
	Text     string    `json:"text"`
	Span     Span      `json:"span"`
	Grain    TimeGrain `json:"grain"`
	Timezone string    `json:"timezone"`
}

type ComparisonType string

const (
	ComparisonYearOverYear     ComparisonType = "YEAR_OVER_YEAR"
	ComparisonMonthOverMonth   ComparisonType = "MONTH_OVER_MONTH"
	ComparisonPeriodOverPeriod ComparisonType = "PERIOD_OVER_PERIOD"
	ComparisonDelta            ComparisonType = "DELTA"
	ComparisonRatio            ComparisonType = "RATIO"
)

type ComparisonMention struct {
	Text       string         `json:"text"`
	Span       Span           `json:"span"`
	Type       ComparisonType `json:"type"`
	TargetText *string        `json:"targetText"`
}

type SortDirection string

const (
	SortAscending  SortDirection = "ASC"
	SortDescending SortDirection = "DESC"
)

type OrderingMention struct {
	Text       string        `json:"text"`
	Span       Span          `json:"span"`
	TargetText string        `json:"targetText"`
	Direction  SortDirection `json:"direction"`
}

type NeededEvidence string

const (
	NeededMetricCandidates    NeededEvidence = "METRIC_CANDIDATES"
	NeededDimensionCandidates NeededEvidence = "DIMENSION_CANDIDATES"
	NeededMemberCandidates    NeededEvidence = "MEMBER_CANDIDATES"
	NeededTimeResolution      NeededEvidence = "TIME_RESOLUTION"
	NeededGraphPlan           NeededEvidence = "GRAPH_PLAN"
	NeededConversationContext NeededEvidence = "CONVERSATION_CONTEXT"
)

type UnresolvedSpan struct {
	Text           string           `json:"text"`
	Span           Span             `json:"span"`
	Reason         string           `json:"reason"`
	NeededEvidence []NeededEvidence `json:"neededEvidence"`
}

// QuestionUnderstanding contains mentions and hypotheses only. It deliberately
// has no SQL, table, column, formula or arbitrary object-definition fields.
type QuestionUnderstanding struct {
	SchemaVersion     string              `json:"schemaVersion"`
	Question          string              `json:"question"`
	DomainHypotheses  []DomainHypothesis  `json:"domainHypotheses"`
	MetricMentions    []MetricMention     `json:"metricMentions"`
	DimensionMentions []DimensionMention  `json:"dimensionMentions"`
	ValueMentions     []ValueMention      `json:"valueMentions"`
	Time              *TimeUnderstanding  `json:"time"`
	Comparisons       []ComparisonMention `json:"comparisons"`
	Ordering          []OrderingMention   `json:"ordering"`
	Limit             *int                `json:"limit"`
	UnresolvedSpans   []UnresolvedSpan    `json:"unresolvedSpans"`
}

// Decode parses the only supported wire format and rejects unknown fields,
// duplicate keys and invalid intent contracts.
func Decode(raw []byte) (QuestionUnderstanding, error) {
	var understanding QuestionUnderstanding
	if err := askdata.DecodeStrictJSON(raw, &understanding); err != nil {
		return QuestionUnderstanding{}, err
	}
	if err := understanding.Validate(); err != nil {
		return QuestionUnderstanding{}, err
	}
	return understanding, nil
}

func (understanding QuestionUnderstanding) Validate() error {
	if understanding.SchemaVersion != SchemaVersion {
		return fmt.Errorf("schemaVersion must be %q", SchemaVersion)
	}
	if strings.TrimSpace(understanding.Question) == "" || !utf8.ValidString(understanding.Question) {
		return errors.New("question must be non-empty valid UTF-8")
	}
	questionLength := utf8.RuneCountInString(understanding.Question)
	if questionLength > MaxQuestionRunes {
		return fmt.Errorf("question exceeds %d Unicode code points", MaxQuestionRunes)
	}
	if len(understanding.DomainHypotheses) > MaxDomainHypotheses {
		return fmt.Errorf("domainHypotheses exceeds %d items", MaxDomainHypotheses)
	}
	seenDomains := map[askdata.ID]struct{}{}
	for index, hypothesis := range understanding.DomainHypotheses {
		if err := hypothesis.DomainID.Validate(); err != nil {
			return fmt.Errorf("domainHypotheses[%d].domainId: %w", index, err)
		}
		if _, exists := seenDomains[hypothesis.DomainID]; exists {
			return fmt.Errorf("domainHypotheses[%d] duplicates domainId %q", index, hypothesis.DomainID)
		}
		seenDomains[hypothesis.DomainID] = struct{}{}
		if math.IsNaN(hypothesis.Score) || math.IsInf(hypothesis.Score, 0) || hypothesis.Score < 0 || hypothesis.Score > 1 {
			return fmt.Errorf("domainHypotheses[%d].score must be between 0 and 1", index)
		}
		if len(hypothesis.EvidenceRefs) == 0 || len(hypothesis.EvidenceRefs) > MaxEvidencePerHypothesis {
			return fmt.Errorf("domainHypotheses[%d].evidenceRefs count is invalid", index)
		}
		for evidenceIndex, evidence := range hypothesis.EvidenceRefs {
			if err := evidence.Validate(); err != nil {
				return fmt.Errorf("domainHypotheses[%d].evidenceRefs[%d]: %w", index, evidenceIndex, err)
			}
		}
	}
	if len(understanding.MetricMentions) > MaxMetricMentions {
		return fmt.Errorf("metricMentions exceeds %d items", MaxMetricMentions)
	}
	for index, mention := range understanding.MetricMentions {
		if err := validateMention(understanding.Question, mention.Text, mention.Span); err != nil {
			return fmt.Errorf("metricMentions[%d]: %w", index, err)
		}
		if !validAggregation(mention.AggregationHint) {
			return fmt.Errorf("metricMentions[%d].aggregationHint is invalid", index)
		}
	}
	if len(understanding.DimensionMentions) > MaxDimensionMentions {
		return fmt.Errorf("dimensionMentions exceeds %d items", MaxDimensionMentions)
	}
	for index, mention := range understanding.DimensionMentions {
		if err := validateMention(understanding.Question, mention.Text, mention.Span); err != nil {
			return fmt.Errorf("dimensionMentions[%d]: %w", index, err)
		}
		if !validDimensionRole(mention.Role) {
			return fmt.Errorf("dimensionMentions[%d].role is invalid", index)
		}
		if mention.Grain != nil && !validTimeGrain(*mention.Grain) {
			return fmt.Errorf("dimensionMentions[%d].grain is invalid", index)
		}
		if mention.Grain != nil && mention.Role != DimensionRoleGroupBy && mention.Role != DimensionRoleTime {
			return fmt.Errorf("dimensionMentions[%d].grain requires GROUP_BY or TIME role", index)
		}
	}
	if len(understanding.ValueMentions) > MaxValueMentions {
		return fmt.Errorf("valueMentions exceeds %d items", MaxValueMentions)
	}
	for index, mention := range understanding.ValueMentions {
		if err := validateMention(understanding.Question, mention.Text, mention.Span); err != nil {
			return fmt.Errorf("valueMentions[%d]: %w", index, err)
		}
		if mention.DimensionHint != nil && (strings.TrimSpace(*mention.DimensionHint) == "" || utf8.RuneCountInString(*mention.DimensionHint) > 128) {
			return fmt.Errorf("valueMentions[%d].dimensionHint is invalid", index)
		}
		if !validValueOperator(mention.OperatorHint) {
			return fmt.Errorf("valueMentions[%d].operatorHint is invalid", index)
		}
	}
	if understanding.Time != nil {
		if err := validateMention(understanding.Question, understanding.Time.Text, understanding.Time.Span); err != nil {
			return fmt.Errorf("time: %w", err)
		}
		if !validTimeGrain(understanding.Time.Grain) {
			return errors.New("time.grain is invalid")
		}
		if understanding.Time.Timezone == "" {
			return errors.New("time.timezone is required")
		}
		if _, err := time.LoadLocation(understanding.Time.Timezone); err != nil {
			return errors.New("time.timezone is not a known IANA timezone")
		}
	}
	if len(understanding.Comparisons) > MaxComparisonMentions {
		return fmt.Errorf("comparisons exceeds %d items", MaxComparisonMentions)
	}
	for index, comparison := range understanding.Comparisons {
		if err := validateMention(understanding.Question, comparison.Text, comparison.Span); err != nil {
			return fmt.Errorf("comparisons[%d]: %w", index, err)
		}
		if !validComparison(comparison.Type) {
			return fmt.Errorf("comparisons[%d].type is invalid", index)
		}
		if comparison.TargetText != nil && (strings.TrimSpace(*comparison.TargetText) == "" || utf8.RuneCountInString(*comparison.TargetText) > 256) {
			return fmt.Errorf("comparisons[%d].targetText is invalid", index)
		}
	}
	if len(understanding.Ordering) > MaxOrderingMentions {
		return fmt.Errorf("ordering exceeds %d items", MaxOrderingMentions)
	}
	for index, ordering := range understanding.Ordering {
		if err := validateMention(understanding.Question, ordering.Text, ordering.Span); err != nil {
			return fmt.Errorf("ordering[%d]: %w", index, err)
		}
		if strings.TrimSpace(ordering.TargetText) == "" || utf8.RuneCountInString(ordering.TargetText) > 256 {
			return fmt.Errorf("ordering[%d].targetText is invalid", index)
		}
		if ordering.Direction != SortAscending && ordering.Direction != SortDescending {
			return fmt.Errorf("ordering[%d].direction is invalid", index)
		}
	}
	if understanding.Limit != nil && (*understanding.Limit < 1 || *understanding.Limit > 10_000) {
		return errors.New("limit must be between 1 and 10000")
	}
	if len(understanding.UnresolvedSpans) > MaxUnresolvedSpans {
		return fmt.Errorf("unresolvedSpans exceeds %d items", MaxUnresolvedSpans)
	}
	for index, unresolved := range understanding.UnresolvedSpans {
		if err := validateMention(understanding.Question, unresolved.Text, unresolved.Span); err != nil {
			return fmt.Errorf("unresolvedSpans[%d]: %w", index, err)
		}
		if strings.TrimSpace(unresolved.Reason) == "" || utf8.RuneCountInString(unresolved.Reason) > 256 {
			return fmt.Errorf("unresolvedSpans[%d].reason is invalid", index)
		}
		if len(unresolved.NeededEvidence) == 0 || len(unresolved.NeededEvidence) > 6 {
			return fmt.Errorf("unresolvedSpans[%d].neededEvidence count is invalid", index)
		}
		seen := map[NeededEvidence]struct{}{}
		for evidenceIndex, needed := range unresolved.NeededEvidence {
			if !validNeededEvidence(needed) {
				return fmt.Errorf("unresolvedSpans[%d].neededEvidence[%d] is invalid", index, evidenceIndex)
			}
			if _, exists := seen[needed]; exists {
				return fmt.Errorf("unresolvedSpans[%d].neededEvidence[%d] is duplicated", index, evidenceIndex)
			}
			seen[needed] = struct{}{}
		}
	}
	return nil
}

func validateMention(question, text string, span Span) error {
	if strings.TrimSpace(text) == "" || !utf8.ValidString(text) || utf8.RuneCountInString(text) > 512 {
		return errors.New("text must be non-empty valid UTF-8 with at most 512 code points")
	}
	runes := []rune(question)
	if span.Start < 0 || span.End <= span.Start || span.End > len(runes) {
		return errors.New("span is outside the question or empty")
	}
	if string(runes[span.Start:span.End]) != text {
		return errors.New("span does not exactly match text")
	}
	return nil
}

func validAggregation(value AggregationHint) bool {
	switch value {
	case AggregationDefault, AggregationSum, AggregationAverage, AggregationMinimum, AggregationMaximum, AggregationCount, AggregationCountDistinct:
		return true
	default:
		return false
	}
}

func validDimensionRole(value DimensionRole) bool {
	switch value {
	case DimensionRoleGroupBy, DimensionRoleFilter, DimensionRoleTime, DimensionRoleSort:
		return true
	default:
		return false
	}
}

func validTimeGrain(value TimeGrain) bool {
	switch value {
	case TimeGrainDay, TimeGrainWeek, TimeGrainMonth, TimeGrainQuarter, TimeGrainYear:
		return true
	default:
		return false
	}
}

func validValueOperator(value ValueOperatorHint) bool {
	switch value {
	case ValueOperatorDefault, ValueOperatorEquals, ValueOperatorNotEquals, ValueOperatorIn, ValueOperatorNotIn:
		return true
	default:
		return false
	}
}

func validComparison(value ComparisonType) bool {
	switch value {
	case ComparisonYearOverYear, ComparisonMonthOverMonth, ComparisonPeriodOverPeriod, ComparisonDelta, ComparisonRatio:
		return true
	default:
		return false
	}
}

func validNeededEvidence(value NeededEvidence) bool {
	switch value {
	case NeededMetricCandidates, NeededDimensionCandidates, NeededMemberCandidates, NeededTimeResolution, NeededGraphPlan, NeededConversationContext:
		return true
	default:
		return false
	}
}
