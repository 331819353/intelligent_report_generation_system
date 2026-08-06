package understanding

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"intelligent-report-generation-system/internal/askdata"
)

const (
	RuleParseVersion = "question-rules-v1"
	MaxRuleLimit     = 10_000
)

const (
	ReasonMultipleComparisons  = "GRAMMAR_MULTIPLE_COMPARISONS"
	ReasonLimitOutOfRange      = "GRAMMAR_LIMIT_OUT_OF_RANGE"
	ReasonUnsupportedRankRatio = "GRAMMAR_UNSUPPORTED_PERCENT_RANKING"
	ReasonMultipleRankings     = "GRAMMAR_MULTIPLE_RANKINGS"
	ReasonConflictingOrder     = "GRAMMAR_CONFLICTING_ORDER"
	ReasonTooManyMatches       = "GRAMMAR_TOO_MANY_MATCHES"
)

type GroupingMarker string

const (
	GroupingBy   GroupingMarker = "BY"
	GroupingEach GroupingMarker = "EACH"
)

type RankingRule struct {
	Text      string        `json:"text"`
	Span      Span          `json:"span"`
	Limit     int           `json:"limit"`
	Direction SortDirection `json:"direction"`
}

type SortRule struct {
	Text      string        `json:"text"`
	Span      Span          `json:"span"`
	Direction SortDirection `json:"direction"`
}

// GroupingRule does not bind a semantic target. Grain is present only when
// the lexical marker itself names a calendar grain.
type GroupingRule struct {
	Text   string         `json:"text"`
	Span   Span           `json:"span"`
	Marker GroupingMarker `json:"marker"`
	Grain  *TimeGrain     `json:"grain"`
}

// RuleParseResult is replay-bound to its question and calendar inputs.
type RuleParseResult struct {
	Version              string              `json:"version"`
	NormalizationVersion string              `json:"normalizationVersion"`
	QuestionHash         askdata.ContentHash `json:"questionHash"`
	ReferenceDate        string              `json:"referenceDate"`
	Timezone             string              `json:"timezone"`
	FiscalYearStartMonth int                 `json:"fiscalYearStartMonth"`
	Time                 *ResolvedTime       `json:"time"`
	Comparisons          []ComparisonMention `json:"comparisons"`
	Ranking              *RankingRule        `json:"ranking"`
	Sorts                []SortRule          `json:"sorts"`
	Groupings            []GroupingRule      `json:"groupings"`
	UnresolvedSpans      []UnresolvedSpan    `json:"unresolvedSpans"`
}

type RuleParser struct {
	location             *time.Location
	referenceDate        time.Time
	fiscalYearStartMonth time.Month
}

// NewRuleParser always interprets calendar dates in Asia/Shanghai. A zero
// fiscal month means fiscal expressions are unresolved instead of guessed.
func NewRuleParser(reference time.Time, fiscalYearStartMonth time.Month) (*RuleParser, error) {
	if reference.IsZero() {
		return nil, errors.New("reference time is required")
	}
	if fiscalYearStartMonth < 0 || fiscalYearStartMonth > 12 {
		return nil, errors.New("fiscal year start month must be between 1 and 12, or zero when unavailable")
	}
	location, err := time.LoadLocation(QuestionTimezone)
	if err != nil {
		return nil, fmt.Errorf("load question timezone: %w", err)
	}
	local := reference.In(location)
	return &RuleParser{
		location:             location,
		referenceDate:        time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location),
		fiscalYearStartMonth: fiscalYearStartMonth,
	}, nil
}

func (parser *RuleParser) Parse(question NormalizedQuestion) (RuleParseResult, error) {
	if parser == nil || parser.location == nil || parser.referenceDate.IsZero() {
		return RuleParseResult{}, errors.New("rule parser is not initialized")
	}
	if err := question.Validate(); err != nil {
		return RuleParseResult{}, fmt.Errorf("normalized question: %w", err)
	}
	result, err := parser.parseUnchecked(question)
	if err != nil {
		return RuleParseResult{}, err
	}
	if err := result.validateFields(question); err != nil {
		return RuleParseResult{}, fmt.Errorf("rule parse result: %w", err)
	}
	return result, nil
}

func (parser *RuleParser) parseUnchecked(question NormalizedQuestion) (RuleParseResult, error) {
	resolvedTime, timeUnresolved, err := parseTimeRules(parser, question)
	if err != nil {
		return RuleParseResult{}, fmt.Errorf("parse time rules: %w", err)
	}
	comparisons, comparisonUnresolved, err := parseComparisonRules(question)
	if err != nil {
		return RuleParseResult{}, fmt.Errorf("parse comparisons: %w", err)
	}
	ranking, rankingUnresolved, err := parseRankingRules(question)
	if err != nil {
		return RuleParseResult{}, fmt.Errorf("parse ranking: %w", err)
	}
	sorts, sortUnresolved, err := parseSortRules(question)
	if err != nil {
		return RuleParseResult{}, fmt.Errorf("parse sorting: %w", err)
	}
	groupings, groupingUnresolved, err := parseGroupingRules(question)
	if err != nil {
		return RuleParseResult{}, fmt.Errorf("parse grouping: %w", err)
	}

	unresolved := make([]UnresolvedSpan, 0, 8)
	for _, values := range [][]UnresolvedSpan{timeUnresolved, comparisonUnresolved, rankingUnresolved, sortUnresolved, groupingUnresolved} {
		unresolved = append(unresolved, values...)
	}
	if ranking != nil && sortDirectionsConflict(ranking.Direction, sorts) {
		normalized, mapErr := question.NormalizedSpan(ranking.Span)
		if mapErr != nil {
			return RuleParseResult{}, mapErr
		}
		conflict, mapErr := unresolvedFromNormalized(question, normalized, ReasonConflictingOrder, NeededConversationContext)
		if mapErr != nil {
			return RuleParseResult{}, mapErr
		}
		unresolved = append(unresolved, conflict)
	}
	sortUnresolvedSpans(unresolved)
	if len(unresolved) > MaxUnresolvedSpans {
		return RuleParseResult{}, fmt.Errorf("unresolved grammar spans exceed %d items", MaxUnresolvedSpans)
	}

	return RuleParseResult{
		Version: RuleParseVersion, NormalizationVersion: question.Version,
		QuestionHash:  askdata.HashBytes([]byte(question.Original)),
		ReferenceDate: formatCalendarDate(parser.referenceDate), Timezone: QuestionTimezone,
		FiscalYearStartMonth: int(parser.fiscalYearStartMonth), Time: resolvedTime,
		Comparisons: comparisons, Ranking: ranking, Sorts: sorts, Groupings: groupings,
		UnresolvedSpans: unresolved,
	}, nil
}

// Validate replays the deterministic parser and therefore rejects modified or
// stale rule results, not just malformed fields.
func (result RuleParseResult) Validate(question NormalizedQuestion) error {
	if err := question.Validate(); err != nil {
		return fmt.Errorf("normalized question: %w", err)
	}
	if err := result.validateFields(question); err != nil {
		return err
	}
	location, err := time.LoadLocation(QuestionTimezone)
	if err != nil {
		return err
	}
	reference, err := time.ParseInLocation("2006-01-02", result.ReferenceDate, location)
	if err != nil {
		return errors.New("referenceDate must use YYYY-MM-DD")
	}
	parser, err := NewRuleParser(reference, time.Month(result.FiscalYearStartMonth))
	if err != nil {
		return err
	}
	expected, err := parser.parseUnchecked(question)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(result, expected) {
		return errors.New("rule parse result does not match its source and parser configuration")
	}
	return nil
}

func (result RuleParseResult) validateFields(question NormalizedQuestion) error {
	if result.Version != RuleParseVersion || result.NormalizationVersion != question.Version {
		return errors.New("rule or normalization version is invalid")
	}
	if err := result.QuestionHash.Validate(); err != nil {
		return fmt.Errorf("questionHash: %w", err)
	}
	if result.QuestionHash != askdata.HashBytes([]byte(question.Original)) {
		return errors.New("questionHash does not match the source question")
	}
	if result.Timezone != QuestionTimezone {
		return fmt.Errorf("timezone must be %q", QuestionTimezone)
	}
	if result.FiscalYearStartMonth < 0 || result.FiscalYearStartMonth > 12 {
		return errors.New("fiscalYearStartMonth is invalid")
	}
	if _, err := time.Parse("2006-01-02", result.ReferenceDate); err != nil {
		return errors.New("referenceDate must use YYYY-MM-DD")
	}
	if result.Time != nil {
		if err := result.Time.Validate(question.Original); err != nil {
			return fmt.Errorf("time: %w", err)
		}
	}
	if result.Comparisons == nil || result.Sorts == nil || result.Groupings == nil || result.UnresolvedSpans == nil {
		return errors.New("result collections must be non-null")
	}
	if len(result.Comparisons) > MaxComparisonMentions || len(result.Sorts) > MaxOrderingMentions || len(result.Groupings) > MaxDimensionMentions || len(result.UnresolvedSpans) > MaxUnresolvedSpans {
		return errors.New("rule result exceeds a collection limit")
	}
	for index, comparison := range result.Comparisons {
		if err := validateMention(question.Original, comparison.Text, comparison.Span); err != nil {
			return fmt.Errorf("comparisons[%d]: %w", index, err)
		}
		if !validComparison(comparison.Type) || comparison.TargetText != nil {
			return fmt.Errorf("comparisons[%d] is invalid", index)
		}
	}
	if result.Ranking != nil {
		if err := validateMention(question.Original, result.Ranking.Text, result.Ranking.Span); err != nil {
			return fmt.Errorf("ranking: %w", err)
		}
		if result.Ranking.Limit < 1 || result.Ranking.Limit > MaxRuleLimit || !validSortDirection(result.Ranking.Direction) {
			return errors.New("ranking is invalid")
		}
	}
	for index, rule := range result.Sorts {
		if err := validateMention(question.Original, rule.Text, rule.Span); err != nil {
			return fmt.Errorf("sorts[%d]: %w", index, err)
		}
		if !validSortDirection(rule.Direction) {
			return fmt.Errorf("sorts[%d].direction is invalid", index)
		}
	}
	for index, grouping := range result.Groupings {
		if err := validateMention(question.Original, grouping.Text, grouping.Span); err != nil {
			return fmt.Errorf("groupings[%d]: %w", index, err)
		}
		if grouping.Marker != GroupingBy && grouping.Marker != GroupingEach {
			return fmt.Errorf("groupings[%d].marker is invalid", index)
		}
		if grouping.Grain != nil && !validTimeGrain(*grouping.Grain) {
			return fmt.Errorf("groupings[%d].grain is invalid", index)
		}
	}
	for index, unresolved := range result.UnresolvedSpans {
		if err := validateMention(question.Original, unresolved.Text, unresolved.Span); err != nil {
			return fmt.Errorf("unresolvedSpans[%d]: %w", index, err)
		}
		if unresolved.Reason == "" || len(unresolved.NeededEvidence) == 0 {
			return fmt.Errorf("unresolvedSpans[%d] is incomplete", index)
		}
	}
	return nil
}

type literalRule[T any] struct {
	text  string
	value T
}

var comparisonRules = []literalRule[ComparisonType]{
	{"与上期相比", ComparisonPeriodOverPeriod}, {"较上期", ComparisonPeriodOverPeriod},
	{"比上期", ComparisonPeriodOverPeriod}, {"同比", ComparisonYearOverYear}, {"环比", ComparisonMonthOverMonth},
}

func parseComparisonRules(question NormalizedQuestion) ([]ComparisonMention, []UnresolvedSpan, error) {
	type candidate struct {
		span  Span
		value ComparisonType
	}
	var candidates []candidate
	for _, rule := range comparisonRules {
		for _, span := range findLiteralSpans(question.Normalized, rule.text) {
			candidates = append(candidates, candidate{span, rule.value})
		}
	}
	candidates = nonOverlapping(candidates, func(value candidate) Span { return value.span })
	if len(candidates) > MaxComparisonMentions {
		value, err := unresolvedFromNormalized(question, candidates[0].span, ReasonTooManyMatches, NeededConversationContext)
		return []ComparisonMention{}, []UnresolvedSpan{value}, err
	}
	result := make([]ComparisonMention, 0, len(candidates))
	types := map[ComparisonType]struct{}{}
	for _, candidate := range candidates {
		span, text, err := originalMatch(question, candidate.span)
		if err != nil {
			return nil, nil, err
		}
		result = append(result, ComparisonMention{Text: text, Span: span, Type: candidate.value})
		types[candidate.value] = struct{}{}
	}
	unresolved := []UnresolvedSpan{}
	if len(types) > 1 {
		value, err := unresolvedFromNormalized(question, candidates[0].span, ReasonMultipleComparisons, NeededConversationContext)
		if err != nil {
			return nil, nil, err
		}
		unresolved = append(unresolved, value)
	}
	return result, unresolved, nil
}

var rankingPattern = regexp.MustCompile(`(?:top|bottom|倒数|前|后|末)\s*[0-9]+(?:名|个)?%?`)
var rankingValuePattern = regexp.MustCompile(`^(top|bottom|倒数|前|后|末)\s*([0-9]+)(?:名|个)?(%?)$`)

func parseRankingRules(question NormalizedQuestion) (*RankingRule, []UnresolvedSpan, error) {
	type candidate struct {
		span      Span
		limit     int
		direction SortDirection
		reason    string
	}
	var candidates []candidate
	for _, span := range regexpSpans(question.Normalized, rankingPattern) {
		text := string([]rune(question.Normalized)[span.Start:span.End])
		parts := rankingValuePattern.FindStringSubmatch(text)
		if len(parts) != 4 {
			continue
		}
		if (parts[1] == "top" || parts[1] == "bottom") && !wordBoundary(question.Normalized, span) {
			continue
		}
		value := candidate{span: span, direction: SortDescending}
		if parts[1] == "bottom" || parts[1] == "倒数" || parts[1] == "后" || parts[1] == "末" {
			value.direction = SortAscending
		}
		limit, err := strconv.ParseUint(parts[2], 10, 64)
		if err != nil || limit < 1 || limit > MaxRuleLimit {
			value.reason = ReasonLimitOutOfRange
		} else {
			value.limit = int(limit)
		}
		if parts[3] == "%" {
			value.reason = ReasonUnsupportedRankRatio
		}
		candidates = append(candidates, value)
	}
	if len(candidates) == 0 {
		return nil, []UnresolvedSpan{}, nil
	}
	if len(candidates) > 1 {
		value, err := unresolvedFromNormalized(question, candidates[0].span, ReasonMultipleRankings, NeededConversationContext)
		return nil, []UnresolvedSpan{value}, err
	}
	value := candidates[0]
	if value.reason != "" {
		unresolved, err := unresolvedFromNormalized(question, value.span, value.reason, NeededConversationContext)
		return nil, []UnresolvedSpan{unresolved}, err
	}
	span, text, err := originalMatch(question, value.span)
	if err != nil {
		return nil, nil, err
	}
	return &RankingRule{Text: text, Span: span, Limit: value.limit, Direction: value.direction}, []UnresolvedSpan{}, nil
}

var sortRules = []literalRule[SortDirection]{
	{"ascending", SortAscending}, {"从低到高", SortAscending}, {"由低到高", SortAscending}, {"升序", SortAscending}, {"asc", SortAscending},
	{"descending", SortDescending}, {"从高到低", SortDescending}, {"由高到低", SortDescending}, {"降序", SortDescending}, {"desc", SortDescending},
}

func parseSortRules(question NormalizedQuestion) ([]SortRule, []UnresolvedSpan, error) {
	type candidate struct {
		span  Span
		value SortDirection
	}
	var candidates []candidate
	for _, rule := range sortRules {
		for _, span := range findLiteralSpans(question.Normalized, rule.text) {
			if isASCIIKeyword(rule.text) && !wordBoundary(question.Normalized, span) {
				continue
			}
			candidates = append(candidates, candidate{span, rule.value})
		}
	}
	candidates = nonOverlapping(candidates, func(value candidate) Span { return value.span })
	if len(candidates) > MaxOrderingMentions {
		value, err := unresolvedFromNormalized(question, candidates[0].span, ReasonTooManyMatches, NeededConversationContext)
		return []SortRule{}, []UnresolvedSpan{value}, err
	}
	result := make([]SortRule, 0, len(candidates))
	directions := map[SortDirection]struct{}{}
	for _, candidate := range candidates {
		span, text, err := originalMatch(question, candidate.span)
		if err != nil {
			return nil, nil, err
		}
		result = append(result, SortRule{Text: text, Span: span, Direction: candidate.value})
		directions[candidate.value] = struct{}{}
	}
	unresolved := []UnresolvedSpan{}
	if len(directions) > 1 {
		value, err := unresolvedFromNormalized(question, candidates[0].span, ReasonConflictingOrder, NeededConversationContext)
		if err != nil {
			return nil, nil, err
		}
		unresolved = append(unresolved, value)
	}
	return result, unresolved, nil
}

type groupingLexeme struct {
	text   string
	marker GroupingMarker
	grain  TimeGrain
}

var groupingLexemes = []groupingLexeme{
	{"按季度", GroupingBy, TimeGrainQuarter}, {"按月", GroupingBy, TimeGrainMonth}, {"按周", GroupingBy, TimeGrainWeek},
	{"按天", GroupingBy, TimeGrainDay}, {"按日", GroupingBy, TimeGrainDay}, {"按年", GroupingBy, TimeGrainYear},
	{"每季度", GroupingEach, TimeGrainQuarter}, {"每月", GroupingEach, TimeGrainMonth}, {"每周", GroupingEach, TimeGrainWeek},
	{"每天", GroupingEach, TimeGrainDay}, {"每日", GroupingEach, TimeGrainDay}, {"每年", GroupingEach, TimeGrainYear},
}

func parseGroupingRules(question NormalizedQuestion) ([]GroupingRule, []UnresolvedSpan, error) {
	type candidate struct {
		span   Span
		marker GroupingMarker
		grain  *TimeGrain
	}
	var candidates []candidate
	var known []Span
	for _, rule := range groupingLexemes {
		for _, span := range findLiteralSpans(question.Normalized, rule.text) {
			grain := rule.grain
			candidates = append(candidates, candidate{span, rule.marker, &grain})
			known = append(known, span)
		}
	}
	for _, span := range findLiteralSpans(question.Normalized, "每个") {
		if !overlapsAny(span, known) {
			candidates = append(candidates, candidate{span: span, marker: GroupingEach})
		}
	}
	for _, phrase := range []string{"按照", "按"} {
		for _, span := range findLiteralSpans(question.Normalized, phrase) {
			if !overlapsAny(span, known) && !falseGroupingBy(question.Normalized, span) {
				candidates = append(candidates, candidate{span: span, marker: GroupingBy})
			}
		}
	}
	candidates = nonOverlapping(candidates, func(value candidate) Span { return value.span })
	if len(candidates) > MaxDimensionMentions {
		value, err := unresolvedFromNormalized(question, candidates[0].span, ReasonTooManyMatches, NeededConversationContext)
		return []GroupingRule{}, []UnresolvedSpan{value}, err
	}
	result := make([]GroupingRule, 0, len(candidates))
	for _, candidate := range candidates {
		span, text, err := originalMatch(question, candidate.span)
		if err != nil {
			return nil, nil, err
		}
		result = append(result, GroupingRule{Text: text, Span: span, Marker: candidate.marker, Grain: candidate.grain})
	}
	return result, []UnresolvedSpan{}, nil
}

func nonOverlapping[T any](values []T, span func(T) Span) []T {
	sort.Slice(values, func(i, j int) bool {
		left, right := span(values[i]), span(values[j])
		if left.Start != right.Start {
			return left.Start < right.Start
		}
		return left.End > right.End
	})
	result := make([]T, 0, len(values))
	var occupied []Span
	for _, value := range values {
		if current := span(value); !overlapsAny(current, occupied) {
			result = append(result, value)
			occupied = append(occupied, current)
		}
	}
	return result
}

func falseGroupingBy(text string, span Span) bool {
	runes := []rune(text)
	if span.End >= len(runes) {
		return false
	}
	remainder := string(runes[span.End:])
	for _, prefix := range []string{"揭", "摩", "钮", "键", "压"} {
		if strings.HasPrefix(remainder, prefix) {
			return true
		}
	}
	return false
}

func sortDirectionsConflict(direction SortDirection, rules []SortRule) bool {
	for _, rule := range rules {
		if rule.Direction != direction {
			return true
		}
	}
	return false
}

func validSortDirection(direction SortDirection) bool {
	return direction == SortAscending || direction == SortDescending
}

func sortUnresolvedSpans(values []UnresolvedSpan) {
	sort.SliceStable(values, func(i, j int) bool {
		if values[i].Span.Start != values[j].Span.Start {
			return values[i].Span.Start < values[j].Span.Start
		}
		if values[i].Span.End != values[j].Span.End {
			return values[i].Span.End < values[j].Span.End
		}
		return values[i].Reason < values[j].Reason
	})
}

func originalMatch(question NormalizedQuestion, normalized Span) (Span, string, error) {
	span, err := question.OriginalSpan(normalized)
	if err != nil {
		return Span{}, "", err
	}
	text, err := question.OriginalText(normalized)
	return span, text, err
}

func findLiteralSpans(text, phrase string) []Span {
	if phrase == "" {
		return nil
	}
	var result []Span
	for offset := 0; offset <= len(text); {
		relative := strings.Index(text[offset:], phrase)
		if relative < 0 {
			break
		}
		start, end := offset+relative, offset+relative+len(phrase)
		result = append(result, Span{utf8.RuneCountInString(text[:start]), utf8.RuneCountInString(text[:end])})
		offset = end
	}
	return result
}

func regexpSpans(text string, pattern *regexp.Regexp) []Span {
	matches := pattern.FindAllStringIndex(text, -1)
	result := make([]Span, 0, len(matches))
	for _, match := range matches {
		result = append(result, Span{utf8.RuneCountInString(text[:match[0]]), utf8.RuneCountInString(text[:match[1]])})
	}
	return result
}

func isASCIIKeyword(value string) bool {
	for _, character := range value {
		if character >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

func wordBoundary(text string, span Span) bool {
	runes := []rune(text)
	isWord := func(value rune) bool {
		return value >= 'a' && value <= 'z' || value >= '0' && value <= '9' || value == '_'
	}
	return (span.Start == 0 || !isWord(runes[span.Start-1])) && (span.End == len(runes) || !isWord(runes[span.End]))
}
