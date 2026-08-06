package understanding

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const (
	NormalizationVersion       = "question-normalization-v1"
	MaxNormalizedQuestionRunes = MaxQuestionRunes * 4
)

var (
	ErrInvalidQuestion   = errors.New("invalid question for normalization")
	ErrInvalidOffsetSpan = errors.New("invalid normalization offset span")
	ErrRemovedOffsetSpan = errors.New("original span was removed during normalization")
)

// NormalizedQuestion keeps the source question immutable while providing a
// deterministic text projection for lexical matching and rule parsing.
// Offsets in both directions are zero-based Unicode code-point offsets.
type NormalizedQuestion struct {
	Version    string `json:"version"`
	Original   string `json:"original"`
	Normalized string `json:"normalized"`

	origins []Span
}

// NormalizeQuestion applies compatibility/case/punctuation normalization,
// collapses whitespace, removes only anchored harmless discourse wrappers and
// closes safe number-unit whitespace. It never rewrites numeric values,
// administrative suffixes or text in the semantic core.
func NormalizeQuestion(question string) (NormalizedQuestion, error) {
	return buildNormalizedQuestion(question)
}

func buildNormalizedQuestion(question string) (NormalizedQuestion, error) {
	if err := validateNormalizationInput(question); err != nil {
		return NormalizedQuestion{}, err
	}

	mapped, err := normalizeUnicode(question)
	if err != nil {
		return NormalizedQuestion{}, err
	}
	mapped = collapseSpaces(mapped)
	mapped = trimSpaces(mapped)
	mapped = stripDiscourseWrappers(mapped)
	mapped = trimSpaces(mapped)
	mapped = closeNumberUnitSpaces(mapped)
	mapped = collapseSpaces(mapped)
	mapped = trimSpaces(mapped)
	if len(mapped) == 0 || !hasSemanticContent(mapped) {
		return NormalizedQuestion{}, fmt.Errorf("%w: question has no semantic content", ErrInvalidQuestion)
	}
	if len(mapped) > MaxNormalizedQuestionRunes {
		return NormalizedQuestion{}, fmt.Errorf(
			"%w: normalized question exceeds %d Unicode code points",
			ErrInvalidQuestion, MaxNormalizedQuestionRunes,
		)
	}

	result := NormalizedQuestion{
		Version: NormalizationVersion, Original: question,
		Normalized: mappedText(mapped), origins: mappedOrigins(mapped),
	}
	return result, nil
}

// Validate verifies the normalization contract and its offset projection.
func (question NormalizedQuestion) Validate() error {
	if question.Version != NormalizationVersion {
		return fmt.Errorf("normalization version must be %q", NormalizationVersion)
	}
	expected, err := buildNormalizedQuestion(question.Original)
	if err != nil {
		return err
	}
	if !utf8.ValidString(question.Normalized) || question.Normalized == "" {
		return errors.New("normalized question must be non-empty valid UTF-8")
	}
	normalizedLength := utf8.RuneCountInString(question.Normalized)
	if normalizedLength > MaxNormalizedQuestionRunes {
		return fmt.Errorf("normalized question exceeds %d Unicode code points", MaxNormalizedQuestionRunes)
	}
	if normalizedLength != len(question.origins) {
		return errors.New("normalized question offset map length mismatch")
	}
	originalLength := utf8.RuneCountInString(question.Original)
	previousStart := -1
	for index, origin := range question.origins {
		if origin.Start < 0 || origin.End <= origin.Start || origin.End > originalLength {
			return fmt.Errorf("normalized offset %d is outside the original question", index)
		}
		if origin.Start < previousStart {
			return errors.New("normalized offset map is not monotonic")
		}
		previousStart = origin.Start
	}
	if !hasSemanticRunes([]rune(question.Normalized)) {
		return errors.New("normalized question has no semantic content")
	}
	if question.Normalized != expected.Normalized || !equalSpans(question.origins, expected.origins) {
		return errors.New("normalized question does not match its source and normalization version")
	}
	return nil
}

// OriginalSpan maps a non-empty normalized span back to the smallest original
// rune range that produced it. Compatibility expansions conservatively map
// every expanded rune to the complete original normalization segment.
func (question NormalizedQuestion) OriginalSpan(normalized Span) (Span, error) {
	if err := question.Validate(); err != nil {
		return Span{}, err
	}
	if normalized.Start < 0 || normalized.End <= normalized.Start || normalized.End > len(question.origins) {
		return Span{}, ErrInvalidOffsetSpan
	}
	result := Span{
		Start: question.origins[normalized.Start].Start,
		End:   question.origins[normalized.End-1].End,
	}
	for _, origin := range question.origins[normalized.Start:normalized.End] {
		if origin.Start < result.Start {
			result.Start = origin.Start
		}
		if origin.End > result.End {
			result.End = origin.End
		}
	}
	return result, nil
}

// OriginalText returns the exact source text for a normalized span.
func (question NormalizedQuestion) OriginalText(normalized Span) (string, error) {
	original, err := question.OriginalSpan(normalized)
	if err != nil {
		return "", err
	}
	return string([]rune(question.Original)[original.Start:original.End]), nil
}

// NormalizedSpan projects an original rune range into normalized offsets. A
// range made entirely of removed discourse wrappers has no normalized span.
func (question NormalizedQuestion) NormalizedSpan(original Span) (Span, error) {
	if err := question.Validate(); err != nil {
		return Span{}, err
	}
	originalLength := utf8.RuneCountInString(question.Original)
	if original.Start < 0 || original.End <= original.Start || original.End > originalLength {
		return Span{}, ErrInvalidOffsetSpan
	}
	start, end := -1, -1
	for index, origin := range question.origins {
		if origin.End <= original.Start || origin.Start >= original.End {
			continue
		}
		if start == -1 {
			start = index
		}
		end = index + 1
	}
	if start == -1 {
		return Span{}, ErrRemovedOffsetSpan
	}
	return Span{Start: start, End: end}, nil
}

type mappedRune struct {
	value  rune
	origin Span
}

var questionCaseFold = cases.Fold()

func validateNormalizationInput(question string) error {
	if !utf8.ValidString(question) || strings.TrimSpace(question) == "" {
		return fmt.Errorf("%w: question must be non-empty valid UTF-8", ErrInvalidQuestion)
	}
	if length := utf8.RuneCountInString(question); length > MaxQuestionRunes {
		return fmt.Errorf("%w: question exceeds %d Unicode code points", ErrInvalidQuestion, MaxQuestionRunes)
	}
	for _, character := range question {
		if unicode.IsControl(character) && !unicode.IsSpace(character) {
			return fmt.Errorf("%w: question contains a control character", ErrInvalidQuestion)
		}
		if isUnsafeFormatCharacter(character) {
			return fmt.Errorf("%w: question contains an unsafe invisible or directional character", ErrInvalidQuestion)
		}
	}
	return nil
}

func isUnsafeFormatCharacter(character rune) bool {
	switch character {
	case '\u061c', '\u200b', '\u200e', '\u200f', '\u202a', '\u202b', '\u202c', '\u202d', '\u202e',
		'\u2060', '\u2066', '\u2067', '\u2068', '\u2069', '\ufeff':
		return true
	default:
		return false
	}
}

func normalizeUnicode(question string) ([]mappedRune, error) {
	result := make([]mappedRune, 0, utf8.RuneCountInString(question))
	byteOffset, runeOffset := 0, 0
	for byteOffset < len(question) {
		segmentLength := norm.NFKC.NextBoundaryInString(question[byteOffset:], true)
		if segmentLength <= 0 {
			return nil, fmt.Errorf("%w: Unicode normalization boundary is invalid", ErrInvalidQuestion)
		}
		segment := question[byteOffset : byteOffset+segmentLength]
		segmentRunes := utf8.RuneCountInString(segment)
		origin := Span{Start: runeOffset, End: runeOffset + segmentRunes}
		transformed := norm.NFKC.String(segment)
		transformed = questionCaseFold.String(transformed)
		transformed = norm.NFKC.String(transformed)
		for _, character := range transformed {
			if unicode.IsSpace(character) {
				result = append(result, mappedRune{value: ' ', origin: origin})
				continue
			}
			if unicode.IsControl(character) {
				return nil, fmt.Errorf("%w: normalized question contains a control character", ErrInvalidQuestion)
			}
			for _, canonical := range canonicalPunctuation(character) {
				result = append(result, mappedRune{value: canonical, origin: origin})
			}
		}
		byteOffset += segmentLength
		runeOffset += segmentRunes
	}
	return result, nil
}

func canonicalPunctuation(character rune) []rune {
	switch character {
	case '。', '｡':
		return []rune{'.'}
	case '、', '﹑':
		return []rune{','}
	case '“', '”', '„', '‟', '«', '»', '「', '」', '『', '』':
		return []rune{'"'}
	case '‘', '’', '‚', '‛':
		return []rune{'\''}
	case '‐', '‑', '‒', '–', '—', '―', '−':
		return []rune{'-'}
	case '…', '⋯':
		return []rune{'.', '.', '.'}
	default:
		return []rune{character}
	}
}

func collapseSpaces(input []mappedRune) []mappedRune {
	if len(input) == 0 {
		return nil
	}
	result := make([]mappedRune, 0, len(input))
	for _, character := range input {
		if character.value != ' ' || len(result) == 0 || result[len(result)-1].value != ' ' {
			result = append(result, character)
			continue
		}
		last := &result[len(result)-1]
		if character.origin.Start < last.origin.Start {
			last.origin.Start = character.origin.Start
		}
		if character.origin.End > last.origin.End {
			last.origin.End = character.origin.End
		}
	}
	return result
}

func trimSpaces(input []mappedRune) []mappedRune {
	start, end := 0, len(input)
	for start < end && input[start].value == ' ' {
		start++
	}
	for end > start && input[end-1].value == ' ' {
		end--
	}
	return append([]mappedRune(nil), input[start:end]...)
}

var discoursePrefixes = sortedPhrases([]string{
	"麻烦帮我", "可以帮我", "能否帮我", "请帮我", "能帮我",
	"请问一下", "我想知道", "我想了解", "我想看看", "我想看",
	"请问", "麻烦", "劳驾", "帮我",
	"could you", "would you", "can you", "please", "show me", "tell me",
})

var discourseSuffixes = sortedPhrases([]string{
	"可以吗", "行吗", "好吗", "一下", "please", "呢", "吗", "吧", "呀", "啊", "哈", "呗",
})

func sortedPhrases(values []string) [][]rune {
	result := make([][]rune, 0, len(values))
	for _, value := range values {
		result = append(result, []rune(value))
	}
	sort.Slice(result, func(i, j int) bool { return len(result[i]) > len(result[j]) })
	return result
}

func stripDiscourseWrappers(input []mappedRune) []mappedRune {
	result := append([]mappedRune(nil), input...)
	for attempts := 0; attempts < 4; attempts++ {
		matched := false
		for _, prefix := range discoursePrefixes {
			if !hasPrefix(result, prefix) || !phraseBoundaryAfter(result, len(prefix)) {
				continue
			}
			result = result[len(prefix):]
			result = trimPrefixSeparators(result)
			matched = true
			break
		}
		if !matched {
			break
		}
	}

	for attempts := 0; attempts < 2; attempts++ {
		terminalStart := len(result)
		for terminalStart > 0 && isTerminalPunctuation(result[terminalStart-1].value) {
			terminalStart--
		}
		contentEnd := terminalStart
		for contentEnd > 0 && result[contentEnd-1].value == ' ' {
			contentEnd--
		}
		matched := false
		for _, suffix := range discourseSuffixes {
			start := contentEnd - len(suffix)
			if start < 0 || !matchesAt(result, start, suffix) || !phraseBoundaryBefore(result, start) {
				continue
			}
			for start > 0 && result[start-1].value == ' ' {
				start--
			}
			result = append(append([]mappedRune(nil), result[:start]...), result[terminalStart:]...)
			matched = true
			break
		}
		if !matched {
			break
		}
	}
	return result
}

func trimPrefixSeparators(input []mappedRune) []mappedRune {
	index := 0
	for index < len(input) {
		switch input[index].value {
		case ' ', ',', ':', ';':
			index++
		default:
			return append([]mappedRune(nil), input[index:]...)
		}
	}
	return nil
}

func hasPrefix(input []mappedRune, prefix []rune) bool {
	return matchesAt(input, 0, prefix)
}

func matchesAt(input []mappedRune, start int, phrase []rune) bool {
	if start < 0 || start+len(phrase) > len(input) {
		return false
	}
	for index, character := range phrase {
		if input[start+index].value != character {
			return false
		}
	}
	return true
}

func phraseBoundaryAfter(input []mappedRune, index int) bool {
	if index >= len(input) {
		return true
	}
	previous := input[index-1].value
	if !isASCIIWord(previous) {
		return true
	}
	return !isASCIIWord(input[index].value)
}

func phraseBoundaryBefore(input []mappedRune, index int) bool {
	if index <= 0 {
		return true
	}
	first := input[index].value
	if !isASCIIWord(first) {
		return true
	}
	return !isASCIIWord(input[index-1].value)
}

func isASCIIWord(character rune) bool {
	return character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '_'
}

func isTerminalPunctuation(character rune) bool {
	switch character {
	case '?', '!', '.':
		return true
	default:
		return false
	}
}

func closeNumberUnitSpaces(input []mappedRune) []mappedRune {
	if len(input) < 3 {
		return input
	}
	result := make([]mappedRune, 0, len(input))
	for index, character := range input {
		if character.value == ' ' && len(result) > 0 && unicode.IsDigit(result[len(result)-1].value) &&
			isUnitStart(input, index+1) {
			continue
		}
		result = append(result, character)
	}
	return result
}

var chineseNumberUnits = sortedPhrases([]string{
	"个百分点", "万亿元", "百万元", "亿元", "万元", "千元", "百元",
	"平方米", "立方米", "公斤", "千克", "毫克", "公里", "千米", "厘米", "毫米",
	"毫秒", "分钟", "小时", "季度", "个月", "年", "月", "周", "日", "天",
	"元", "个", "件", "次", "人", "家", "户", "笔", "单", "倍",
})

var asciiNumberUnits = map[string]struct{}{
	"b": {}, "bps": {}, "cny": {}, "cm": {}, "d": {}, "gb": {}, "g": {}, "h": {},
	"k": {}, "kb": {}, "kg": {}, "km": {}, "m": {}, "mb": {}, "mg": {}, "min": {},
	"mm": {}, "ms": {}, "m2": {}, "m3": {}, "rmb": {}, "s": {}, "tb": {}, "usd": {}, "w": {},
}

func isUnitStart(input []mappedRune, start int) bool {
	if start >= len(input) {
		return false
	}
	switch input[start].value {
	case '%', '‰', '‱':
		return true
	case '°':
		return start+1 < len(input) && (input[start+1].value == 'c' || input[start+1].value == 'f')
	}
	for _, unit := range chineseNumberUnits {
		if matchesAt(input, start, unit) {
			return true
		}
	}
	end := start
	for end < len(input) && ((input[end].value >= 'a' && input[end].value <= 'z') || unicode.IsDigit(input[end].value)) {
		end++
	}
	if end == start {
		return false
	}
	_, exists := asciiNumberUnits[mappedText(input[start:end])]
	return exists
}

func hasSemanticContent(input []mappedRune) bool {
	runes := make([]rune, len(input))
	for index, character := range input {
		runes[index] = character.value
	}
	return hasSemanticRunes(runes)
}

func hasSemanticRunes(input []rune) bool {
	for _, character := range input {
		if unicode.IsLetter(character) || unicode.IsNumber(character) || unicode.IsSymbol(character) {
			return true
		}
	}
	return false
}

func mappedText(input []mappedRune) string {
	var builder strings.Builder
	for _, character := range input {
		builder.WriteRune(character.value)
	}
	return builder.String()
}

func mappedOrigins(input []mappedRune) []Span {
	result := make([]Span, len(input))
	for index, character := range input {
		result[index] = character.origin
	}
	return result
}

func equalSpans(left, right []Span) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
