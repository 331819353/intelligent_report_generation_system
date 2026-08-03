package semanticqa

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const questionNormalizerVersion = "NFKC_CASEFOLD_UTF8_ALIGN_V1"

var questionCaseFolder = cases.Fold()

type QuestionAlignment struct {
	NormalizedStart int `json:"normalizedStart"`
	NormalizedEnd   int `json:"normalizedEnd"`
	OriginalStart   int `json:"originalStart"`
	OriginalEnd     int `json:"originalEnd"`
}

type QuestionMentionCandidate struct {
	ObjectType    string `json:"objectType"`
	ObjectID      string `json:"objectId"`
	ObjectVersion string `json:"objectVersion"`
	Code          string `json:"code"`
	Label         string `json:"label"`
	VID           string `json:"vid"`
}

type QuestionMention struct {
	Type           string                     `json:"type"`
	StartByte      int                        `json:"startByte"`
	EndByte        int                        `json:"endByte"`
	MentionText    string                     `json:"mentionText"`
	NormalizedText string                     `json:"normalizedText"`
	Detector       string                     `json:"detector"`
	EvidenceID     string                     `json:"evidenceId"`
	Candidates     []QuestionMentionCandidate `json:"candidates"`
	Relation       string                     `json:"relation,omitempty"`
}

type QuestionUnderstanding struct {
	NormalizerVersion string                     `json:"normalizerVersion"`
	NormalizedText    string                     `json:"normalizedText"`
	AlignmentMap      []QuestionAlignment        `json:"alignmentMap"`
	Mentions          []QuestionMention          `json:"mentions"`
	CertifiedExamples []QuestionCertifiedExample `json:"certifiedExamples"`
}

type QuestionCertifiedExample struct {
	ObjectID            string   `json:"objectId"`
	ObjectVersion       string   `json:"objectVersion"`
	Score               float64  `json:"score"`
	ReferencedObjectIDs []string `json:"referencedObjectIds"`
	EvidenceID          string   `json:"evidenceId"`
}

var (
	questionTimeMentionPattern = regexp.MustCompile(`(?:今|昨|明|上|下|本)?(?:天|日|周|月|季|季度|年)|(?:20[0-9]{2})[-年/](?:0?[1-9]|1[0-2])(?:[-月/](?:0?[1-9]|[12][0-9]|3[01])日?)?`)
	questionComparePattern     = regexp.MustCompile(`同比|环比|较上期|对比|相比`)
	questionLimitPattern       = regexp.MustCompile(`(?:前|top\s*)([1-9][0-9]{0,2})`)
)

func understandQuestion(original string, snapshot *QuestionSemanticSnapshot) QuestionUnderstanding {
	result := normalizeQuestion(original)
	result.Mentions = append(result.Mentions, detectRuleMentions(original, result)...)
	if snapshot != nil {
		result.Mentions = append(result.Mentions, detectGovernedMentions(original, result, *snapshot)...)
		result.CertifiedExamples = recallCertifiedExamples(result.NormalizedText, *snapshot)
	}
	result.Mentions = mergeQuestionMentions(result.Mentions)
	return result
}

// governedSimpleQuestionHints recognizes the deliberately narrow fast path:
// one uniquely linked governed metric plus optional deterministic time or
// comparison language. Any unaccounted business text falls back to the full
// bounded interpreter instead of being silently dropped.
func governedSimpleQuestionHints(
	original string,
	understanding QuestionUnderstanding,
) ([]string, QuerySemanticHints, bool) {
	metricCodes := []string{}
	covered := make([]bool, len(original))
	intent := "METRIC"
	for _, mention := range understanding.Mentions {
		if mention.StartByte < 0 || mention.EndByte > len(original) || mention.StartByte >= mention.EndByte {
			return nil, QuerySemanticHints{}, false
		}
		switch mention.Type {
		case "METRIC":
			if mention.Detector != "GOVERNED_EXACT_ALIAS" || len(mention.Candidates) != 1 {
				return nil, QuerySemanticHints{}, false
			}
			metricCodes = append(metricCodes, mention.Candidates[0].Code)
		case "TIME_RANGE":
		case "COMPARE":
			intent = "COMPARISON"
		case "LIMIT", "DIMENSION", "DIMENSION_VALUE", "ENTITY":
			return nil, QuerySemanticHints{}, false
		default:
			return nil, QuerySemanticHints{}, false
		}
		for index := mention.StartByte; index < mention.EndByte; index++ {
			covered[index] = true
		}
	}
	metricCodes = uniqueStrings(metricCodes, 8)
	if len(metricCodes) != 1 {
		return nil, QuerySemanticHints{}, false
	}
	remaining := make([]byte, 0, len(original))
	for index := 0; index < len(original); index++ {
		if !covered[index] {
			remaining = append(remaining, original[index])
		}
	}
	residual := normalizeQuestion(string(remaining)).NormalizedText
	for _, filler := range []string{
		"帮我", "请问", "给我", "告诉我", "查一下", "查询", "看一下",
		"是多少", "是什么", "有多少", "多少", "的", "是", "为", "呢", "吗",
	} {
		residual = strings.ReplaceAll(residual, filler, "")
	}
	residual = strings.Map(func(character rune) rune {
		if unicode.IsSpace(character) || unicode.IsPunct(character) {
			return -1
		}
		return character
	}, residual)
	if residual != "" {
		return nil, QuerySemanticHints{}, false
	}
	return metricCodes, QuerySemanticHints{
		Intent: intent, MetricNames: append([]string(nil), metricCodes...),
	}, true
}

func normalizeQuestion(original string) QuestionUnderstanding {
	var normalized strings.Builder
	alignments := make([]QuestionAlignment, 0, utf8.RuneCountInString(original))
	lastWasSpace := false
	for originalStart, character := range original {
		originalEnd := originalStart + utf8.RuneLen(character)
		for _, normalizedCharacter := range questionCaseFolder.String(
			norm.NFKC.String(string(character)),
		) {
			if unicode.IsSpace(normalizedCharacter) {
				if lastWasSpace || normalized.Len() == 0 {
					continue
				}
				normalizedCharacter = ' '
				lastWasSpace = true
			} else {
				lastWasSpace = false
			}
			normalizedStart := normalized.Len()
			normalized.WriteRune(normalizedCharacter)
			alignments = append(alignments, QuestionAlignment{
				NormalizedStart: normalizedStart, NormalizedEnd: normalized.Len(),
				OriginalStart: originalStart, OriginalEnd: originalEnd,
			})
		}
	}
	normalizedText := strings.TrimSpace(normalized.String())
	for len(alignments) > 0 && alignments[len(alignments)-1].NormalizedStart >= len(normalizedText) {
		alignments = alignments[:len(alignments)-1]
	}
	return QuestionUnderstanding{
		NormalizerVersion: questionNormalizerVersion,
		NormalizedText:    normalizedText, AlignmentMap: alignments,
		Mentions: []QuestionMention{}, CertifiedExamples: []QuestionCertifiedExample{},
	}
}

func recallCertifiedExamples(
	normalizedQuestion string,
	snapshot QuestionSemanticSnapshot,
) []QuestionCertifiedExample {
	result := []QuestionCertifiedExample{}
	for _, object := range snapshot.Objects {
		if object.ObjectType != "CERTIFIED_EXAMPLE" {
			continue
		}
		example := normalizeQuestion(semanticString(object.Contract["question"])).NormalizedText
		score := questionPhraseSimilarity(normalizedQuestion, example)
		if score < 0.25 {
			continue
		}
		hash := sha256.Sum256([]byte(
			object.ObjectID + "\x00" + object.ObjectVersion + "\x00" + normalizedQuestion,
		))
		result = append(result, QuestionCertifiedExample{
			ObjectID: object.ObjectID, ObjectVersion: object.ObjectVersion, Score: score,
			ReferencedObjectIDs: semanticStringSlice(object.Contract["objectIds"]),
			EvidenceID:          "example:" + hex.EncodeToString(hash[:]),
		})
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].Score != result[right].Score {
			return result[left].Score > result[right].Score
		}
		return result[left].ObjectID < result[right].ObjectID
	})
	if len(result) > 5 {
		result = result[:5]
	}
	return result
}

func questionPhraseSimilarity(left, right string) float64 {
	if left == "" || right == "" {
		return 0
	}
	if left == right {
		return 1
	}
	if strings.Contains(left, right) || strings.Contains(right, left) {
		return 0.9
	}
	grams := func(value string) map[string]bool {
		characters := []rune(strings.ReplaceAll(value, " ", ""))
		result := map[string]bool{}
		if len(characters) == 1 {
			result[string(characters)] = true
		}
		for index := 0; index+1 < len(characters); index++ {
			result[string(characters[index:index+2])] = true
		}
		return result
	}
	leftGrams, rightGrams := grams(left), grams(right)
	intersection, union := 0, len(leftGrams)
	for gram := range rightGrams {
		if leftGrams[gram] {
			intersection++
		} else {
			union++
		}
	}
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

func detectRuleMentions(original string, understanding QuestionUnderstanding) []QuestionMention {
	result := []QuestionMention{}
	appendMatches := func(pattern *regexp.Regexp, mentionType, relation string) {
		for _, match := range pattern.FindAllStringIndex(understanding.NormalizedText, -1) {
			start, end, ok := originalRangeForNormalized(understanding.AlignmentMap, match[0], match[1])
			if !ok || start < 0 || end > len(original) || start >= end {
				continue
			}
			normalizedText := understanding.NormalizedText[match[0]:match[1]]
			result = append(result, QuestionMention{
				Type: mentionType, StartByte: start, EndByte: end,
				MentionText: original[start:end], NormalizedText: normalizedText,
				Detector: "DETERMINISTIC_RULE", Relation: relation,
				EvidenceID: questionMentionEvidenceID(mentionType, start, end, normalizedText, nil),
				Candidates: []QuestionMentionCandidate{},
			})
		}
	}
	appendMatches(questionTimeMentionPattern, "TIME_RANGE", "FILTER")
	appendMatches(questionComparePattern, "COMPARE", "COMPARE")
	appendMatches(questionLimitPattern, "LIMIT", "SORT_LIMIT")
	return result
}

func detectGovernedMentions(
	original string,
	understanding QuestionUnderstanding,
	snapshot QuestionSemanticSnapshot,
) []QuestionMention {
	type aliasTarget struct {
		alias     string
		typeName  string
		candidate QuestionMentionCandidate
	}
	targets := []aliasTarget{}
	for _, object := range snapshot.Objects {
		mentionType := mentionTypeForSemanticObject(object.ObjectType)
		if mentionType == "" {
			continue
		}
		candidate := QuestionMentionCandidate{
			ObjectType: object.ObjectType, ObjectID: object.ObjectID,
			ObjectVersion: object.ObjectVersion, Code: object.Code(),
			Label: object.Label(), VID: object.VID(snapshot.TenantID),
		}
		for _, alias := range object.Aliases() {
			normalizedAlias := normalizeQuestion(alias).NormalizedText
			if normalizedAlias != "" {
				targets = append(targets, aliasTarget{alias: normalizedAlias, typeName: mentionType, candidate: candidate})
			}
		}
	}
	byRange := map[string]*QuestionMention{}
	for _, target := range targets {
		searchFrom := 0
		for searchFrom <= len(understanding.NormalizedText)-len(target.alias) {
			relative := strings.Index(understanding.NormalizedText[searchFrom:], target.alias)
			if relative < 0 {
				break
			}
			normalizedStart := searchFrom + relative
			normalizedEnd := normalizedStart + len(target.alias)
			searchFrom = normalizedStart + len(target.alias)
			if !questionAliasBoundary(understanding.NormalizedText, normalizedStart, normalizedEnd) {
				continue
			}
			start, end, ok := originalRangeForNormalized(understanding.AlignmentMap, normalizedStart, normalizedEnd)
			if !ok || start < 0 || end > len(original) || start >= end {
				continue
			}
			key := target.typeName + ":" + itoa(start) + ":" + itoa(end)
			mention := byRange[key]
			if mention == nil {
				mention = &QuestionMention{
					Type: target.typeName, StartByte: start, EndByte: end,
					MentionText:    original[start:end],
					NormalizedText: understanding.NormalizedText[normalizedStart:normalizedEnd],
					Detector:       "GOVERNED_EXACT_ALIAS", Candidates: []QuestionMentionCandidate{},
				}
				byRange[key] = mention
			}
			mention.Candidates = appendMentionCandidate(mention.Candidates, target.candidate)
		}
	}
	result := make([]QuestionMention, 0, len(byRange))
	for _, mention := range byRange {
		sort.Slice(mention.Candidates, func(left, right int) bool {
			if mention.Candidates[left].Code != mention.Candidates[right].Code {
				return mention.Candidates[left].Code < mention.Candidates[right].Code
			}
			return mention.Candidates[left].ObjectID < mention.Candidates[right].ObjectID
		})
		mention.EvidenceID = questionMentionEvidenceID(
			mention.Type, mention.StartByte, mention.EndByte, mention.NormalizedText, mention.Candidates,
		)
		result = append(result, *mention)
	}
	return result
}

func originalRangeForNormalized(
	alignments []QuestionAlignment, start, end int,
) (int, int, bool) {
	originalStart, originalEnd := -1, -1
	for _, alignment := range alignments {
		if alignment.NormalizedEnd <= start || alignment.NormalizedStart >= end {
			continue
		}
		if originalStart < 0 || alignment.OriginalStart < originalStart {
			originalStart = alignment.OriginalStart
		}
		if alignment.OriginalEnd > originalEnd {
			originalEnd = alignment.OriginalEnd
		}
	}
	return originalStart, originalEnd, originalStart >= 0 && originalEnd > originalStart
}

func questionAliasBoundary(text string, start, end int) bool {
	alias := text[start:end]
	if alias == "" || !asciiWord(alias) {
		return true
	}
	if start > 0 {
		character, _ := utf8.DecodeLastRuneInString(text[:start])
		if unicode.IsLetter(character) || unicode.IsDigit(character) || character == '_' {
			return false
		}
	}
	if end < len(text) {
		character, _ := utf8.DecodeRuneInString(text[end:])
		if unicode.IsLetter(character) || unicode.IsDigit(character) || character == '_' {
			return false
		}
	}
	return true
}

func asciiWord(value string) bool {
	for _, character := range value {
		if character > unicode.MaxASCII || !(unicode.IsLetter(character) || unicode.IsDigit(character) || character == '_') {
			return false
		}
	}
	return true
}

func mentionTypeForSemanticObject(objectType string) string {
	switch objectType {
	case "METRIC", "MEASURE":
		return "METRIC"
	case "DIMENSION", "TIME", "COHORT":
		return "DIMENSION"
	case "DIMENSION_VALUE":
		return "DIMENSION_VALUE"
	case "ENTITY":
		return "ENTITY"
	default:
		return ""
	}
}

func appendMentionCandidate(
	items []QuestionMentionCandidate,
	candidate QuestionMentionCandidate,
) []QuestionMentionCandidate {
	for _, item := range items {
		if item.ObjectType == candidate.ObjectType && item.ObjectID == candidate.ObjectID &&
			item.ObjectVersion == candidate.ObjectVersion {
			return items
		}
	}
	return append(items, candidate)
}

func mergeQuestionMentions(items []QuestionMention) []QuestionMention {
	result := make([]QuestionMention, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		key := item.Type + ":" + itoa(item.StartByte) + ":" + itoa(item.EndByte) + ":" + item.Detector
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, item)
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].StartByte != result[right].StartByte {
			return result[left].StartByte < result[right].StartByte
		}
		if result[left].EndByte != result[right].EndByte {
			return result[left].EndByte > result[right].EndByte
		}
		return result[left].Type < result[right].Type
	})
	return result
}

func questionMentionEvidenceID(
	mentionType string,
	start, end int,
	normalized string,
	candidates []QuestionMentionCandidate,
) string {
	parts := []string{mentionType, normalized}
	for _, candidate := range candidates {
		parts = append(parts, candidate.ObjectType, candidate.ObjectID, candidate.ObjectVersion)
	}
	hash := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "span:" + hex.EncodeToString(hash[:]) + ":" + itoa(start) + ":" + itoa(end)
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := [32]byte{}
	position := len(digits)
	for value > 0 {
		position--
		digits[position] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[position:])
}
