package insight

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/shared"
)

// The model never writes a number or an object name.
//
// A narrative is only trustworthy if every quoted figure came from the evidence,
// and asking a language model to emit exact citation offsets over its own prose
// is unreliable in a way that fails open — a slightly wrong span silently
// unbinds a claim from its evidence. So the model writes prose containing
// markers, and this file substitutes each marker with verified text while
// computing the citation span deterministically. A hallucinated figure is not
// detected after the fact; it cannot be expressed.

var markerPattern = regexp.MustCompile(`\{\{(fact|field):([^{}]{1,256})\}\}`)

// MarkerObject is a governed object the narrative may name, with the display
// name that will be substituted for it.
type MarkerObject struct {
	ObjectID askdata.ID
	Name     string
	// Measure distinguishes a metric from a dimension for the binding catalog.
	Measure bool
}

// MarkerSources are the only values a narrative can contain.
type MarkerSources struct {
	Facts   map[askdata.ID]Fact
	Objects map[string]MarkerObject
}

// RenderMarkedContent replaces every marker with verified text and returns the
// rendered content together with citations whose spans address the canonical
// text exactly as InsightContent.CanonicalText joins it.
func RenderMarkedContent(
	content InsightContent, sources MarkerSources,
) (InsightContent, []shared.Citation, error) {
	// CanonicalText joins these parts with "\n" in this order; spans must be
	// computed against that same sequence or every citation would be offset.
	parts := append([]string{content.Summary}, content.Findings...)
	parts = append(parts, content.Risks...)
	parts = append(parts, content.Actions...)

	rendered := make([]string, len(parts))
	citations := make([]shared.Citation, 0, len(parts))
	offset := 0
	for index, part := range parts {
		text, local, err := renderPart(part, sources)
		if err != nil {
			return InsightContent{}, nil, fmt.Errorf("part %d: %w", index, err)
		}
		for _, citation := range local {
			citation.TextSpan.Start += offset
			citation.TextSpan.End += offset
			citations = append(citations, citation)
		}
		rendered[index] = text
		// +1 for the "\n" that CanonicalText inserts between parts.
		offset += utf8.RuneCountInString(text) + 1
	}

	result := InsightContent{
		Summary:  rendered[0],
		Findings: rendered[1 : 1+len(content.Findings)],
		Risks:    rendered[1+len(content.Findings) : 1+len(content.Findings)+len(content.Risks)],
		Actions:  rendered[1+len(content.Findings)+len(content.Risks):],
	}
	return result, citations, nil
}

func renderPart(part string, sources MarkerSources) (string, []shared.Citation, error) {
	matches := markerPattern.FindAllStringSubmatchIndex(part, -1)
	if len(matches) == 0 {
		if strings.Contains(part, "{{") || strings.Contains(part, "}}") {
			return "", nil, errors.New("text contains a malformed marker")
		}
		return part, nil, nil
	}
	var builder strings.Builder
	citations := make([]shared.Citation, 0, len(matches))
	cursor := 0
	for _, match := range matches {
		start, end := match[0], match[1]
		kind := part[match[2]:match[3]]
		key := part[match[4]:match[5]]
		builder.WriteString(part[cursor:start])
		// Spans are Unicode code points, not bytes.
		spanStart := utf8.RuneCountInString(builder.String())

		text, citation, err := substitute(kind, key, sources)
		if err != nil {
			return "", nil, err
		}
		builder.WriteString(text)
		citation.TextSpan = shared.TextSpan{Start: spanStart, End: spanStart + utf8.RuneCountInString(text)}
		citations = append(citations, citation)
		cursor = end
	}
	builder.WriteString(part[cursor:])
	remainder := builder.String()
	if strings.Contains(remainder, "{{") || strings.Contains(remainder, "}}") {
		return "", nil, errors.New("text contains a malformed marker")
	}
	return remainder, citations, nil
}

func substitute(kind, key string, sources MarkerSources) (string, shared.Citation, error) {
	switch kind {
	case "fact":
		fact, exists := sources.Facts[askdata.ID(key)]
		if !exists {
			return "", shared.Citation{}, fmt.Errorf("marker references unknown fact %q", key)
		}
		if len(fact.CellRefs) == 0 {
			return "", shared.Citation{}, fmt.Errorf("fact %q has no source cell", key)
		}
		// Value and unit are one citation: the verifier accepts a citation whose
		// span contains the element it is checking, and the cell carries the unit.
		text := fact.CurrentValue
		if fact.Unit != "" {
			text += " " + fact.Unit
		}
		return text, shared.NewResultCellCitation(shared.TextSpan{}, fact.CellRefs[0]), nil
	case "field":
		object, exists := sources.Objects[key]
		if !exists {
			return "", shared.Citation{}, fmt.Errorf("marker references unknown field %q", key)
		}
		objectID := object.ObjectID
		return object.Name, shared.Citation{Kind: shared.CitationContract, ContractID: &objectID}, nil
	default:
		return "", shared.Citation{}, fmt.Errorf("unsupported marker kind %q", kind)
	}
}

// MarkerSourcesFor builds the substitution table a narrative over this evidence
// may draw on: its own facts, and the governed objects in the binding catalog.
func MarkerSourcesFor(bundle EvidenceBundle, objects []MarkerObject) MarkerSources {
	facts := make(map[askdata.ID]Fact, len(bundle.Facts))
	for _, fact := range bundle.Facts {
		facts[fact.ID] = fact
	}
	catalog := make(map[string]MarkerObject, len(objects))
	for _, object := range objects {
		catalog[string(object.ObjectID)] = object
	}
	return MarkerSources{Facts: facts, Objects: catalog}
}
