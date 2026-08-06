package understanding

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"time"
)

const QuestionTimezone = "Asia/Shanghai"

type TimeExpression string

const (
	TimeExpressionToday              TimeExpression = "TODAY"
	TimeExpressionYesterday          TimeExpression = "YESTERDAY"
	TimeExpressionCurrentWeek        TimeExpression = "CURRENT_WEEK"
	TimeExpressionPreviousWeek       TimeExpression = "PREVIOUS_WEEK"
	TimeExpressionCurrentMonth       TimeExpression = "CURRENT_MONTH"
	TimeExpressionPreviousMonth      TimeExpression = "PREVIOUS_MONTH"
	TimeExpressionCurrentYear        TimeExpression = "CURRENT_YEAR"
	TimeExpressionPreviousYear       TimeExpression = "PREVIOUS_YEAR"
	TimeExpressionCurrentFiscalYear  TimeExpression = "CURRENT_FISCAL_YEAR"
	TimeExpressionPreviousFiscalYear TimeExpression = "PREVIOUS_FISCAL_YEAR"
	TimeExpressionExplicitDay        TimeExpression = "EXPLICIT_DAY"
	TimeExpressionExplicitMonth      TimeExpression = "EXPLICIT_MONTH"
	TimeExpressionExplicitYear       TimeExpression = "EXPLICIT_YEAR"
	TimeExpressionExplicitRange      TimeExpression = "EXPLICIT_RANGE"
)

const (
	ReasonAmbiguousDate         = "TIME_AMBIGUOUS_DATE"
	ReasonInvalidDate           = "TIME_INVALID_DATE"
	ReasonMultipleTimes         = "TIME_MULTIPLE_EXPRESSIONS"
	ReasonFiscalStartRequired   = "TIME_FISCAL_START_REQUIRED"
	ReasonAmbiguousFiscalYear   = "TIME_AMBIGUOUS_FISCAL_YEAR"
	ReasonAmbiguousNaturalWeek  = "TIME_AMBIGUOUS_NATURAL_WEEK"
	ReasonIncompatibleDateRange = "TIME_RANGE_GRAIN_CONFLICT"
	ReasonInvalidDateRange      = "TIME_INVALID_RANGE"
)

// ResolvedTime is a deterministic, calendar-date range in Asia/Shanghai.
// Start is inclusive and EndExclusive is exclusive.
type ResolvedTime struct {
	Text         string         `json:"text"`
	Span         Span           `json:"span"`
	Expression   TimeExpression `json:"expression"`
	Start        string         `json:"start"`
	EndExclusive string         `json:"endExclusive"`
	Grain        TimeGrain      `json:"grain"`
	Timezone     string         `json:"timezone"`
}

func (resolved ResolvedTime) Validate(question string) error {
	if err := validateMention(question, resolved.Text, resolved.Span); err != nil {
		return err
	}
	if !validTimeExpression(resolved.Expression) {
		return errors.New("time expression is invalid")
	}
	if !validTimeGrain(resolved.Grain) {
		return errors.New("time grain is invalid")
	}
	if resolved.Timezone != QuestionTimezone {
		return fmt.Errorf("time timezone must be %q", QuestionTimezone)
	}
	start, err := time.Parse("2006-01-02", resolved.Start)
	if err != nil {
		return errors.New("time start must use YYYY-MM-DD")
	}
	end, err := time.Parse("2006-01-02", resolved.EndExclusive)
	if err != nil {
		return errors.New("time endExclusive must use YYYY-MM-DD")
	}
	if !end.After(start) {
		return errors.New("time endExclusive must be after start")
	}
	return nil
}

func (resolved ResolvedTime) Understanding() TimeUnderstanding {
	return TimeUnderstanding{
		Text: resolved.Text, Span: resolved.Span, Grain: resolved.Grain, Timezone: resolved.Timezone,
	}
}

func validTimeExpression(expression TimeExpression) bool {
	switch expression {
	case TimeExpressionToday, TimeExpressionYesterday,
		TimeExpressionCurrentWeek, TimeExpressionPreviousWeek,
		TimeExpressionCurrentMonth, TimeExpressionPreviousMonth,
		TimeExpressionCurrentYear, TimeExpressionPreviousYear,
		TimeExpressionCurrentFiscalYear, TimeExpressionPreviousFiscalYear,
		TimeExpressionExplicitDay, TimeExpressionExplicitMonth,
		TimeExpressionExplicitYear, TimeExpressionExplicitRange:
		return true
	default:
		return false
	}
}

type relativeTimeRule struct {
	phrases    []string
	expression TimeExpression
	grain      TimeGrain
	resolve    func(*RuleParser) (time.Time, time.Time, string)
}

var relativeTimeRules = []relativeTimeRule{
	{[]string{"今天", "今日"}, TimeExpressionToday, TimeGrainDay, resolveToday},
	{[]string{"昨天", "昨日"}, TimeExpressionYesterday, TimeGrainDay, resolveYesterday},
	{[]string{"本自然周", "这个自然周", "本周", "这周", "本星期", "这星期"}, TimeExpressionCurrentWeek, TimeGrainWeek, resolveCurrentWeek},
	{[]string{"上个自然周", "上一自然周", "上周", "上星期"}, TimeExpressionPreviousWeek, TimeGrainWeek, resolvePreviousWeek},
	{[]string{"本月", "这个月", "当月"}, TimeExpressionCurrentMonth, TimeGrainMonth, resolveCurrentMonth},
	{[]string{"上个月", "上月"}, TimeExpressionPreviousMonth, TimeGrainMonth, resolvePreviousMonth},
	{[]string{"今年", "本年"}, TimeExpressionCurrentYear, TimeGrainYear, resolveCurrentYear},
	{[]string{"去年", "上一年"}, TimeExpressionPreviousYear, TimeGrainYear, resolvePreviousYear},
	{[]string{"本财年", "当前财年", "这个财年"}, TimeExpressionCurrentFiscalYear, TimeGrainYear, resolveCurrentFiscalYear},
	{[]string{"上财年", "上一财年"}, TimeExpressionPreviousFiscalYear, TimeGrainYear, resolvePreviousFiscalYear},
}

type timeCandidate struct {
	span         Span
	expression   TimeExpression
	grain        TimeGrain
	start        time.Time
	endExclusive time.Time
	reason       string
}

func parseTimeRules(parser *RuleParser, question NormalizedQuestion) (*ResolvedTime, []UnresolvedSpan, error) {
	candidates := relativeTimeCandidates(parser, question)
	explicitCandidates, covered := explicitTimeCandidates(parser, question)
	candidates = append(candidates, explicitCandidates...)
	candidates = append(candidates, ambiguousTimeCandidates(question, append(candidateSpans(candidates), covered...))...)
	candidates = preferNonOverlappingTimeCandidates(candidates)
	if len(candidates) == 0 {
		return nil, []UnresolvedSpan{}, nil
	}
	if len(candidates) > 1 {
		span := candidateUnion(candidates)
		unresolved, err := unresolvedFromNormalized(question, span, ReasonMultipleTimes, NeededTimeResolution)
		if err != nil {
			return nil, nil, err
		}
		return nil, []UnresolvedSpan{unresolved}, nil
	}
	candidate := candidates[0]
	if candidate.reason != "" {
		unresolved, err := unresolvedFromNormalized(question, candidate.span, candidate.reason, NeededTimeResolution)
		if err != nil {
			return nil, nil, err
		}
		return nil, []UnresolvedSpan{unresolved}, nil
	}
	originalSpan, err := question.OriginalSpan(candidate.span)
	if err != nil {
		return nil, nil, err
	}
	originalText, err := question.OriginalText(candidate.span)
	if err != nil {
		return nil, nil, err
	}
	resolved := &ResolvedTime{
		Text: originalText, Span: originalSpan, Expression: candidate.expression,
		Start: formatCalendarDate(candidate.start), EndExclusive: formatCalendarDate(candidate.endExclusive),
		Grain: candidate.grain, Timezone: QuestionTimezone,
	}
	if err := resolved.Validate(question.Original); err != nil {
		return nil, nil, err
	}
	return resolved, []UnresolvedSpan{}, nil
}

func relativeTimeCandidates(parser *RuleParser, question NormalizedQuestion) []timeCandidate {
	var candidates []timeCandidate
	for _, rule := range relativeTimeRules {
		for _, phrase := range rule.phrases {
			for _, span := range findLiteralSpans(question.Normalized, phrase) {
				start, end, reason := rule.resolve(parser)
				candidates = append(candidates, timeCandidate{
					span: span, expression: rule.expression, grain: rule.grain,
					start: start, endExclusive: end, reason: reason,
				})
			}
		}
	}
	return preferNonOverlappingTimeCandidates(candidates)
}

type explicitTokenKind uint8

const (
	explicitDay explicitTokenKind = iota + 1
	explicitMonth
	explicitYear
)

type explicitTimeToken struct {
	span         Span
	kind         explicitTokenKind
	start        time.Time
	endExclusive time.Time
	reason       string
}

var explicitTimePattern = regexp.MustCompile(
	`[0-9]{4}年[0-9]{1,2}月[0-9]{1,2}日?|[0-9]{4}[-/][0-9]{1,2}[-/][0-9]{1,2}|` +
		`[0-9]{4}年[0-9]{1,2}月|[0-9]{4}[-/][0-9]{1,2}|[0-9]{4}年`,
)

var (
	explicitChineseDayPattern     = regexp.MustCompile(`^([0-9]{4})年([0-9]{1,2})月([0-9]{1,2})日?$`)
	explicitSeparatedDayPattern   = regexp.MustCompile(`^([0-9]{4})[-/]([0-9]{1,2})[-/]([0-9]{1,2})$`)
	explicitChineseMonthPattern   = regexp.MustCompile(`^([0-9]{4})年([0-9]{1,2})月$`)
	explicitSeparatedMonthPattern = regexp.MustCompile(`^([0-9]{4})[-/]([0-9]{1,2})$`)
	explicitYearPattern           = regexp.MustCompile(`^([0-9]{4})年$`)
	rangeConnectorPattern         = regexp.MustCompile(`^\s*(?:到|至|~)\s*$`)
)

func explicitTimeCandidates(parser *RuleParser, question NormalizedQuestion) ([]timeCandidate, []Span) {
	matches := regexpSpans(question.Normalized, explicitTimePattern)
	tokens := make([]explicitTimeToken, 0, len(matches))
	for _, span := range matches {
		if !numericBoundary(question.Normalized, span) {
			continue
		}
		text := string([]rune(question.Normalized)[span.Start:span.End])
		tokens = append(tokens, parseExplicitTimeToken(parser.location, text, span))
	}
	sort.Slice(tokens, func(i, j int) bool { return tokens[i].span.Start < tokens[j].span.Start })

	var candidates []timeCandidate
	covered := make([]Span, 0, len(tokens))
	for index := 0; index < len(tokens); {
		if index+1 < len(tokens) && isRangeConnector(question.Normalized, tokens[index].span.End, tokens[index+1].span.Start) {
			left, right := tokens[index], tokens[index+1]
			span := Span{Start: left.span.Start, End: right.span.End}
			candidate := timeCandidate{span: span, expression: TimeExpressionExplicitRange, grain: tokenGrain(left.kind)}
			switch {
			case left.reason != "" || right.reason != "":
				candidate.reason = ReasonInvalidDate
			case left.kind != right.kind:
				candidate.reason = ReasonIncompatibleDateRange
			default:
				candidate.start = left.start
				candidate.endExclusive = right.endExclusive
				if !candidate.endExclusive.After(candidate.start) {
					candidate.reason = ReasonInvalidDateRange
				}
			}
			candidates = append(candidates, candidate)
			covered = append(covered, span)
			index += 2
			continue
		}
		token := tokens[index]
		candidates = append(candidates, timeCandidate{
			span: token.span, expression: tokenExpression(token.kind), grain: tokenGrain(token.kind),
			start: token.start, endExclusive: token.endExclusive, reason: token.reason,
		})
		covered = append(covered, token.span)
		index++
	}
	return candidates, covered
}

func parseExplicitTimeToken(location *time.Location, text string, span Span) explicitTimeToken {
	token := explicitTimeToken{span: span}
	var parts []string
	switch {
	case explicitChineseDayPattern.MatchString(text):
		parts = explicitChineseDayPattern.FindStringSubmatch(text)[1:]
		token.kind = explicitDay
	case explicitSeparatedDayPattern.MatchString(text):
		parts = explicitSeparatedDayPattern.FindStringSubmatch(text)[1:]
		token.kind = explicitDay
	case explicitChineseMonthPattern.MatchString(text):
		parts = explicitChineseMonthPattern.FindStringSubmatch(text)[1:]
		token.kind = explicitMonth
	case explicitSeparatedMonthPattern.MatchString(text):
		parts = explicitSeparatedMonthPattern.FindStringSubmatch(text)[1:]
		token.kind = explicitMonth
	case explicitYearPattern.MatchString(text):
		parts = explicitYearPattern.FindStringSubmatch(text)[1:]
		token.kind = explicitYear
	default:
		token.reason = ReasonInvalidDate
		return token
	}
	values := make([]int, len(parts))
	for index, part := range parts {
		value, err := strconv.Atoi(part)
		if err != nil {
			token.reason = ReasonInvalidDate
			return token
		}
		values[index] = value
	}
	year := values[0]
	month, day := 1, 1
	if len(values) > 1 {
		month = values[1]
	}
	if len(values) > 2 {
		day = values[2]
	}
	start, ok := strictCalendarDate(location, year, month, day)
	if !ok {
		token.reason = ReasonInvalidDate
		return token
	}
	token.start = start
	switch token.kind {
	case explicitDay:
		token.endExclusive = start.AddDate(0, 0, 1)
	case explicitMonth:
		token.endExclusive = start.AddDate(0, 1, 0)
	case explicitYear:
		token.endExclusive = start.AddDate(1, 0, 0)
	}
	return token
}

var (
	ambiguousNumericDatePattern = regexp.MustCompile(`[0-9]{1,2}[-/][0-9]{1,2}(?:[-/][0-9]{2,4})?`)
	ambiguousChineseDatePattern = regexp.MustCompile(`[0-9]{1,2}月(?:[0-9]{1,2}日)?`)
)

func ambiguousTimeCandidates(question NormalizedQuestion, covered []Span) []timeCandidate {
	var candidates []timeCandidate
	for _, pattern := range []*regexp.Regexp{ambiguousNumericDatePattern, ambiguousChineseDatePattern} {
		for _, span := range regexpSpans(question.Normalized, pattern) {
			if overlapsAny(span, covered) || !numericBoundary(question.Normalized, span) {
				continue
			}
			candidates = append(candidates, timeCandidate{span: span, reason: ReasonAmbiguousDate})
		}
	}
	occupied := append(append([]Span(nil), covered...), candidateSpans(candidates)...)
	for _, phrase := range []struct {
		text, reason string
	}{{"财年", ReasonAmbiguousFiscalYear}, {"自然周", ReasonAmbiguousNaturalWeek}} {
		for _, span := range findLiteralSpans(question.Normalized, phrase.text) {
			if overlapsAny(span, occupied) {
				continue
			}
			candidates = append(candidates, timeCandidate{span: span, reason: phrase.reason})
		}
	}
	return candidates
}

func resolveToday(parser *RuleParser) (time.Time, time.Time, string) {
	start := parser.referenceDate
	return start, start.AddDate(0, 0, 1), ""
}

func resolveYesterday(parser *RuleParser) (time.Time, time.Time, string) {
	end := parser.referenceDate
	return end.AddDate(0, 0, -1), end, ""
}

func resolveCurrentWeek(parser *RuleParser) (time.Time, time.Time, string) {
	weekday := (int(parser.referenceDate.Weekday()) + 6) % 7
	start := parser.referenceDate.AddDate(0, 0, -weekday)
	return start, start.AddDate(0, 0, 7), ""
}

func resolvePreviousWeek(parser *RuleParser) (time.Time, time.Time, string) {
	start, _, _ := resolveCurrentWeek(parser)
	return start.AddDate(0, 0, -7), start, ""
}

func resolveCurrentMonth(parser *RuleParser) (time.Time, time.Time, string) {
	start := time.Date(parser.referenceDate.Year(), parser.referenceDate.Month(), 1, 0, 0, 0, 0, parser.location)
	return start, start.AddDate(0, 1, 0), ""
}

func resolvePreviousMonth(parser *RuleParser) (time.Time, time.Time, string) {
	start, _, _ := resolveCurrentMonth(parser)
	return start.AddDate(0, -1, 0), start, ""
}

func resolveCurrentYear(parser *RuleParser) (time.Time, time.Time, string) {
	start := time.Date(parser.referenceDate.Year(), time.January, 1, 0, 0, 0, 0, parser.location)
	return start, start.AddDate(1, 0, 0), ""
}

func resolvePreviousYear(parser *RuleParser) (time.Time, time.Time, string) {
	start, _, _ := resolveCurrentYear(parser)
	return start.AddDate(-1, 0, 0), start, ""
}

func resolveCurrentFiscalYear(parser *RuleParser) (time.Time, time.Time, string) {
	if parser.fiscalYearStartMonth == 0 {
		return time.Time{}, time.Time{}, ReasonFiscalStartRequired
	}
	year := parser.referenceDate.Year()
	if parser.referenceDate.Month() < parser.fiscalYearStartMonth {
		year--
	}
	start := time.Date(year, parser.fiscalYearStartMonth, 1, 0, 0, 0, 0, parser.location)
	return start, start.AddDate(1, 0, 0), ""
}

func resolvePreviousFiscalYear(parser *RuleParser) (time.Time, time.Time, string) {
	start, _, reason := resolveCurrentFiscalYear(parser)
	if reason != "" {
		return time.Time{}, time.Time{}, reason
	}
	return start.AddDate(-1, 0, 0), start, ""
}

func strictCalendarDate(location *time.Location, year, month, day int) (time.Time, bool) {
	if year < 1 || year > 9999 || month < 1 || month > 12 || day < 1 || day > 31 {
		return time.Time{}, false
	}
	value := time.Date(year, time.Month(month), day, 0, 0, 0, 0, location)
	return value, value.Year() == year && int(value.Month()) == month && value.Day() == day
}

func tokenExpression(kind explicitTokenKind) TimeExpression {
	switch kind {
	case explicitDay:
		return TimeExpressionExplicitDay
	case explicitMonth:
		return TimeExpressionExplicitMonth
	default:
		return TimeExpressionExplicitYear
	}
}

func tokenGrain(kind explicitTokenKind) TimeGrain {
	switch kind {
	case explicitDay:
		return TimeGrainDay
	case explicitMonth:
		return TimeGrainMonth
	default:
		return TimeGrainYear
	}
}

func isRangeConnector(text string, leftEnd, rightStart int) bool {
	runes := []rune(text)
	if leftEnd > rightStart || leftEnd < 0 || rightStart > len(runes) {
		return false
	}
	return rangeConnectorPattern.MatchString(string(runes[leftEnd:rightStart]))
}

func numericBoundary(text string, span Span) bool {
	runes := []rune(text)
	if span.Start > 0 && runes[span.Start-1] >= '0' && runes[span.Start-1] <= '9' {
		return false
	}
	if span.End < len(runes) && runes[span.End] >= '0' && runes[span.End] <= '9' {
		return false
	}
	return true
}

func preferNonOverlappingTimeCandidates(values []timeCandidate) []timeCandidate {
	sort.Slice(values, func(i, j int) bool {
		if values[i].span.Start != values[j].span.Start {
			return values[i].span.Start < values[j].span.Start
		}
		return values[i].span.End-values[i].span.Start > values[j].span.End-values[j].span.Start
	})
	result := make([]timeCandidate, 0, len(values))
	for _, value := range values {
		if overlapsAny(value.span, candidateSpans(result)) {
			continue
		}
		result = append(result, value)
	}
	return result
}

func candidateSpans(values []timeCandidate) []Span {
	result := make([]Span, len(values))
	for index, value := range values {
		result[index] = value.span
	}
	return result
}

func candidateUnion(values []timeCandidate) Span {
	result := values[0].span
	for _, value := range values[1:] {
		if value.span.Start < result.Start {
			result.Start = value.span.Start
		}
		if value.span.End > result.End {
			result.End = value.span.End
		}
	}
	// UnresolvedSpan shares the mention contract's 512-rune text ceiling. For
	// widely separated matches the first exact expression is enough to anchor
	// the stable multiple-expression reason without manufacturing a huge span.
	if result.End-result.Start > 512 {
		return values[0].span
	}
	return result
}

func formatCalendarDate(value time.Time) string {
	return value.Format("2006-01-02")
}

func unresolvedFromNormalized(
	question NormalizedQuestion,
	normalized Span,
	reason string,
	needed ...NeededEvidence,
) (UnresolvedSpan, error) {
	original, err := question.OriginalSpan(normalized)
	if err != nil {
		return UnresolvedSpan{}, err
	}
	text, err := question.OriginalText(normalized)
	if err != nil {
		return UnresolvedSpan{}, err
	}
	return UnresolvedSpan{Text: text, Span: original, Reason: reason, NeededEvidence: needed}, nil
}

func overlapsAny(target Span, values []Span) bool {
	for _, value := range values {
		if target.Start < value.End && value.Start < target.End {
			return true
		}
	}
	return false
}
