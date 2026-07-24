package semanticcatalog

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

type documentFacts struct {
	Subject Subject
	Lines   []documentLine
	Tags    []string
}

type documentLine struct {
	Label string
	Value string
}

func buildDocument(facts documentFacts) (string, string, error) {
	lines := make([]string, 0, len(facts.Lines)+2)
	lines = append(lines, "对象类型："+safeText(facts.Subject.Type))
	for _, item := range facts.Lines {
		label := safeText(item.Label)
		value := safeText(item.Value)
		if label == "" || value == "" {
			continue
		}
		lines = append(lines, label+"："+value)
	}
	baseLines := append([]string(nil), lines...)
	tags := normalizedTerms(facts.Tags)
	if len(tags) > 0 {
		lines = append(lines, "检索标签："+strings.Join(tags, "、"))
	}
	document := strings.Join(lines, "\n")
	if document == "" {
		return "", "", ErrInvalidRequest
	}
	if len(document) > maxDocumentBytes {
		document = boundedDocument(baseLines, tags, document)
	}
	if document == "" || len(document) > maxDocumentBytes {
		return "", "", ErrInvalidRequest
	}
	sum := sha256.Sum256([]byte(DocumentVersion + "\n" + document))
	return document, hex.EncodeToString(sum[:]), nil
}

// boundedDocument keeps catalog cardinality unbounded while the embedding input
// remains bounded. The digest of the complete canonical facts is included, so a
// change to an omitted tail tag still changes input_hash and forces a new vector.
func boundedDocument(baseLines, tags []string, complete string) string {
	completeHash := sha256.Sum256([]byte(complete))
	itemCount := len(baseLines) + len(tags)
	trailer := fmt.Sprintf(
		"内容截断：完整事实项%d；完整事实摘要%s",
		itemCount, hex.EncodeToString(completeHash[:]),
	)
	budget := maxDocumentBytes - len(trailer) - 1
	if budget <= 0 {
		return ""
	}
	items := make([]string, 0, itemCount)
	items = append(items, baseLines...)
	for _, tag := range tags {
		items = append(items, "检索标签："+tag)
	}
	selected := make([]string, 0, len(items)+1)
	used := 0
	for _, item := range items {
		added := len(item)
		if len(selected) > 0 {
			added++
		}
		if used+added > budget {
			break
		}
		selected = append(selected, item)
		used += added
	}
	if len(selected) == 0 {
		return ""
	}
	selected = append(selected, trailer)
	return strings.Join(selected, "\n")
}

func safeText(value string) string {
	value = strings.ToValidUTF8(value, "�")
	var builder strings.Builder
	builder.Grow(len(value))
	space := false
	for _, character := range value {
		if unicode.IsControl(character) {
			if !space {
				builder.WriteByte(' ')
				space = true
			}
			continue
		}
		if unicode.IsSpace(character) {
			if !space {
				builder.WriteByte(' ')
				space = true
			}
			continue
		}
		builder.WriteRune(character)
		space = false
	}
	return strings.TrimSpace(builder.String())
}

func normalizedTerms(values []string) []string {
	seen := map[string]string{}
	for _, value := range values {
		value = safeText(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; !exists {
			seen[key] = value
		}
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, seen[key])
	}
	return result
}

func boolText(value bool) string {
	if value {
		return "是"
	}
	return "否"
}

func integerText(value int64) string {
	return strconv.FormatInt(value, 10)
}

func fieldSummary(code, name, canonicalType, semanticType, role, aggregation string, nullable bool) string {
	parts := []string{
		safeText(code), safeText(name), safeText(canonicalType), safeText(semanticType),
		safeText(role), safeText(aggregation),
	}
	result := make([]string, 0, len(parts)+1)
	for _, part := range parts {
		if part != "" {
			result = append(result, part)
		}
	}
	result = append(result, fmt.Sprintf("可空=%s", boolText(nullable)))
	return strings.Join(result, "|")
}
