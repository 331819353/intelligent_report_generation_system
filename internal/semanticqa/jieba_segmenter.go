package semanticqa

import (
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/go-ego/gse"
	gsepos "github.com/go-ego/gse/hmm/pos"
)

type jiebaWord struct {
	Text         string
	PartOfSpeech string
}

var defaultJiebaPOS struct {
	once      sync.Once
	segmenter *gsepos.Segmenter
	err       error
}

func segmentWithJieba(text string) ([]jiebaWord, error) {
	defaultJiebaPOS.once.Do(func() {
		base := gse.Segmenter{
			AlphaNum: true,
			SkipLog:  true,
		}
		if err := base.LoadDictEmbed("zh"); err != nil {
			defaultJiebaPOS.err = err
			return
		}
		segmenter := &gsepos.Segmenter{}
		segmenter.WithGse(base)
		defaultJiebaPOS.segmenter = segmenter
	})
	if defaultJiebaPOS.err != nil {
		return nil, defaultJiebaPOS.err
	}
	if defaultJiebaPOS.segmenter == nil || text == "" {
		return nil, nil
	}
	segments := defaultJiebaPOS.segmenter.Cut(text, true)
	result := make([]jiebaWord, 0, len(segments))
	for _, segment := range segments {
		if segment.Text == "" {
			continue
		}
		result = append(result, jiebaWord{
			Text: segment.Text, PartOfSpeech: segment.Pos,
		})
	}
	return coalesceJiebaEntityWords(result), nil
}

func coalesceJiebaEntityWords(words []jiebaWord) []jiebaWord {
	namedEntities := make([]jiebaWord, 0, len(words))
	for _, word := range words {
		if len(namedEntities) == 0 {
			namedEntities = append(namedEntities, word)
			continue
		}
		last := &namedEntities[len(namedEntities)-1]
		lastFamily := jiebaNamedEntityFamily(last.PartOfSpeech)
		currentFamily := jiebaNamedEntityFamily(word.PartOfSpeech)
		if lastFamily != "" && lastFamily == currentFamily {
			last.Text += word.Text
			last.PartOfSpeech = lastFamily
			continue
		}
		namedEntities = append(namedEntities, word)
	}
	result := make([]jiebaWord, 0, len(namedEntities))
	for index := 0; index < len(namedEntities); index++ {
		word := namedEntities[index]
		isSingleNoun := isJiebaCommonNoun(word.PartOfSpeech) &&
			utf8.RuneCountInString(word.Text) == 1
		if isSingleNoun && index+1 < len(namedEntities) &&
			isJiebaCommonNoun(namedEntities[index+1].PartOfSpeech) {
			word.Text += namedEntities[index+1].Text
			word.PartOfSpeech = "n"
			result = append(result, word)
			index++
			continue
		}
		if isSingleNoun && len(result) > 0 &&
			isJiebaCommonNoun(result[len(result)-1].PartOfSpeech) {
			result[len(result)-1].Text += word.Text
			result[len(result)-1].PartOfSpeech = "n"
			continue
		}
		result = append(result, word)
	}
	return result
}

func jiebaNamedEntityFamily(partOfSpeech string) string {
	partOfSpeech = strings.ToLower(strings.TrimSpace(partOfSpeech))
	switch {
	case strings.HasPrefix(partOfSpeech, "nr"):
		return "nr"
	case strings.HasPrefix(partOfSpeech, "ns"):
		return "ns"
	case strings.HasPrefix(partOfSpeech, "nt"):
		return "nt"
	default:
		return ""
	}
}

func isJiebaCommonNoun(partOfSpeech string) bool {
	partOfSpeech = strings.ToLower(strings.TrimSpace(partOfSpeech))
	return partOfSpeech == "n" ||
		partOfSpeech == "ng" ||
		partOfSpeech == "vn"
}

func appendJiebaQueryTokens(
	tokens []QueryToken,
	question []rune,
	start, end int,
) []QueryToken {
	if start >= end {
		return tokens
	}
	words, err := segmentWithJieba(string(question[start:end]))
	if err != nil || len(words) == 0 {
		return appendCharacterClassQueryTokens(tokens, question, start, end)
	}
	cursor := start
	for _, word := range words {
		if strings.TrimSpace(word.Text) == "" {
			cursor = min(end, cursor+utf8.RuneCountInString(word.Text))
			continue
		}
		remaining := string(question[cursor:end])
		byteIndex := strings.Index(remaining, word.Text)
		if byteIndex < 0 {
			continue
		}
		tokenStart := cursor + utf8.RuneCountInString(remaining[:byteIndex])
		if cursor < tokenStart {
			tokens = appendCharacterClassQueryTokens(
				tokens, question, cursor, tokenStart,
			)
		}
		tokenEnd := tokenStart + utf8.RuneCountInString(word.Text)
		if tokenEnd > end {
			break
		}
		entityType, entityName, entityCode, confidence :=
			jiebaEntityMetadata(word.Text, word.PartOfSpeech)
		tokens = append(tokens, QueryToken{
			Text: word.Text, Normalized: strings.ToLower(word.Text),
			PartOfSpeech: word.PartOfSpeech,
			EntityType:   entityType, EntityName: entityName,
			EntityCode: entityCode, Start: tokenStart, End: tokenEnd,
			Source: "JIEBA_HMM_POS", Confidence: confidence,
		})
		cursor = tokenEnd
	}
	if cursor < end {
		tokens = appendCharacterClassQueryTokens(tokens, question, cursor, end)
	}
	return tokens
}

func jiebaEntityMetadata(
	text, partOfSpeech string,
) (entityType, entityName, entityCode string, confidence float64) {
	partOfSpeech = strings.ToLower(strings.TrimSpace(partOfSpeech))
	switch {
	case allPunctuationOrSymbols(text):
		return "PUNCTUATION", "", "", 1
	case strings.HasPrefix(partOfSpeech, "nr"):
		return "PERSON", "人名", "JIEBA_POS_NR", 0.82
	case strings.HasPrefix(partOfSpeech, "ns"):
		return "LOCATION", "地名", "JIEBA_POS_NS", 0.82
	case strings.HasPrefix(partOfSpeech, "nt"):
		return "ORGANIZATION", "机构名", "JIEBA_POS_NT", 0.8
	case strings.HasPrefix(partOfSpeech, "nz"):
		return "PROPER_NOUN", "专有名词", "JIEBA_POS_NZ", 0.72
	case partOfSpeech == "t" || partOfSpeech == "tg":
		return "TIME", "时间词", "JIEBA_POS_T", 0.76
	case strings.HasPrefix(partOfSpeech, "m"):
		return "NUMBER", "数值", "JIEBA_POS_M", 0.8
	case (strings.HasPrefix(partOfSpeech, "n") ||
		partOfSpeech == "vn") &&
		utf8.RuneCountInString(strings.TrimSpace(text)) > 1:
		return "NOUN_CANDIDATE", "名词候选", "JIEBA_POS_N", 0.62
	default:
		return "TEXT", "", "", 0.6
	}
}

func allPunctuationOrSymbols(text string) bool {
	found := false
	for _, value := range text {
		if unicode.IsSpace(value) {
			continue
		}
		found = true
		if !unicode.IsPunct(value) && !unicode.IsSymbol(value) {
			return false
		}
	}
	return found
}

func appendCharacterClassQueryTokens(
	tokens []QueryToken,
	question []rune,
	start, end int,
) []QueryToken {
	for cursor := start; cursor < end; {
		if unicode.IsSpace(question[cursor]) {
			cursor++
			continue
		}
		tokenStart := cursor
		entityType := "TEXT"
		switch {
		case unicode.IsPunct(question[cursor]) || unicode.IsSymbol(question[cursor]):
			entityType = "PUNCTUATION"
			cursor++
		case unicode.IsLetter(question[cursor]) || unicode.IsNumber(question[cursor]):
			isASCII := question[cursor] <= unicode.MaxASCII
			cursor++
			for cursor < end &&
				(unicode.IsLetter(question[cursor]) ||
					unicode.IsNumber(question[cursor]) ||
					question[cursor] == '_') &&
				(question[cursor] <= unicode.MaxASCII) == isASCII {
				cursor++
			}
		default:
			cursor++
		}
		text := string(question[tokenStart:cursor])
		source := "FALLBACK"
		confidence := 0.0
		if entityType == "PUNCTUATION" {
			source, confidence = "RULE", 1
		}
		tokens = append(tokens, QueryToken{
			Text: text, Normalized: strings.ToLower(text),
			EntityType: entityType, Start: tokenStart, End: cursor,
			Source: source, Confidence: confidence,
		})
	}
	return tokens
}
