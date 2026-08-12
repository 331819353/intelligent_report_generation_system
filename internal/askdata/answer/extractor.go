package answer

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/shared"
)

type ElementKind string

const (
	ElementNumber    ElementKind = "NUMBER"
	ElementTime      ElementKind = "TIME"
	ElementUnit      ElementKind = "UNIT"
	ElementObject    ElementKind = "OBJECT"
	ElementAssertion ElementKind = "ASSERTION"
)

type NumericKind string

const (
	NumericScalar          NumericKind = "SCALAR"
	NumericPercent         NumericKind = "PERCENT"
	NumericPercentagePoint NumericKind = "PERCENTAGE_POINT"
)

type ExtractedElement struct {
	Kind        ElementKind
	Text        string
	Span        shared.TextSpan
	NumericKind NumericKind
	Value       string
	Unit        string
	Object      *ObjectMatch
	Policy      *PolicyMatch
}

type ObjectKind string

const (
	ObjectMetric    ObjectKind = "METRIC"
	ObjectDimension ObjectKind = "DIMENSION"
	ObjectMember    ObjectKind = "MEMBER"
)

type ObjectEvidence struct {
	ObjectID askdata.ID `json:"objectId"`
	Kind     ObjectKind `json:"kind"`
	Bound    bool       `json:"bound"`
	Names    []string   `json:"names"`
}

const BindingEvidenceVersion = "answer-binding-evidence-v2"

// BindingSource names where a narrative's governed object identities come from.
//
// The verifier checks that every business object named in prose is a bound
// object from a known catalog, which is what makes hallucinated metric names
// detectable. A semantic release is one such catalog; a published dataset
// version is another. The source is explicit so the two are never confused, and
// so a reader can tell which grade of guarantee a verified narrative carries:
// a certified semantic metric brings confirmed additivity, unit, currency and
// ownership, while a dataset field brings identity and unit only.
type BindingSource string

const (
	BindingSourceSemanticRelease BindingSource = "SEMANTIC_RELEASE"
	BindingSourceDatasetVersion  BindingSource = "DATASET_VERSION"
)

type BindingEvidence struct {
	Version string        `json:"version"`
	Source  BindingSource `json:"source"`
	// Exactly one identity field is set, selected by Source.
	SemanticReleaseID askdata.ID       `json:"semanticReleaseId,omitempty"`
	DatasetVersionID  askdata.ID       `json:"datasetVersionId,omitempty"`
	Objects           []ObjectEvidence `json:"objects"`
}

// CatalogID is the identity of the catalog this evidence was built from,
// whichever source it came from.
func (binding BindingEvidence) CatalogID() askdata.ID {
	if binding.Source == BindingSourceDatasetVersion {
		return binding.DatasetVersionID
	}
	return binding.SemanticReleaseID
}

func (binding BindingEvidence) Normalize() BindingEvidence {
	result := binding
	result.Objects = append([]ObjectEvidence(nil), binding.Objects...)
	for index := range result.Objects {
		result.Objects[index].Names = append([]string(nil), result.Objects[index].Names...)
		sort.Strings(result.Objects[index].Names)
	}
	sort.SliceStable(result.Objects, func(left, right int) bool {
		return result.Objects[left].ObjectID < result.Objects[right].ObjectID
	})
	if result.Objects == nil {
		result.Objects = []ObjectEvidence{}
	}
	return result
}

type ObjectMatch struct {
	Kind      ObjectKind
	VersionID askdata.ID
	Bound     bool
	Name      string
}

var (
	absoluteTimePattern   = regexp.MustCompile(`(?:(?:19|20)[0-9]{2}(?:年|[-/.])(?:0?[1-9]|1[0-2])(?:月|[-/.](?:0?[1-9]|[12][0-9]|3[01])日?)?|(?:19|20)[0-9]{2}年)`)
	relativeTimePattern   = regexp.MustCompile(`本财年|上财年|本财季|上财季|本财月|上财月|本季度|上季度|本年度|上年度|本年|上年同期|去年同期|上年|去年|本月|上月|本周|上周|今日|今天|昨日|昨天|当期|本期|同期|对比期`)
	arabicNumberPattern   = regexp.MustCompile(`[+-]?(?:[0-9]{1,3}(?:,[0-9]{3})+|[0-9]+)(?:\.[0-9]+)?(?:[[:space:]]*(?:个百分点|个点|pp|PP|%|％|万|亿))?`)
	chinesePercentPattern = regexp.MustCompile(`百分之[零〇一二两三四五六七八九十百千万亿点]+`)
	chineseNumberPattern  = regexp.MustCompile(`[零〇一二两三四五六七八九十百千万亿点]+(?:个百分点|个点)?`)
	unitPattern           = regexp.MustCompile(`万元|亿元|人民币|美元|CNY|RMB|USD|¥|￥|元|件|人|次|吨|台|户`)
)

func extractElements(text string, binding BindingEvidence, policy policyWordlist, contributionMode bool) []ExtractedElement {
	runeOffsets := byteToRuneOffsets(text)
	times := extractRegexElements(text, absoluteTimePattern, ElementTime, runeOffsets)
	times = append(times, extractRegexElements(text, relativeTimePattern, ElementTime, runeOffsets)...)
	timeSpans := make([]shared.TextSpan, len(times))
	for index := range times {
		timeSpans[index] = times[index].Span
	}

	elements := append([]ExtractedElement(nil), times...)
	numberSpans := make([]shared.TextSpan, 0)
	for _, pattern := range []*regexp.Regexp{chinesePercentPattern, arabicNumberPattern, chineseNumberPattern} {
		for _, location := range pattern.FindAllStringIndex(text, -1) {
			span := byteSpanToRuneSpan(location, runeOffsets)
			if overlapsAny(span, timeSpans) || overlapsAny(span, numberSpans) {
				continue
			}
			raw := text[location[0]:location[1]]
			value, numericKind, unit, ok := normalizeNumericText(raw)
			if !ok || (pattern == chineseNumberPattern && !meaningfulChineseNumber(raw)) {
				continue
			}
			elements = append(elements, ExtractedElement{
				Kind: ElementNumber, Text: raw, Span: span,
				NumericKind: numericKind, Value: value, Unit: unit,
			})
			numberSpans = append(numberSpans, span)
		}
	}

	for _, location := range unitPattern.FindAllStringIndex(text, -1) {
		span := byteSpanToRuneSpan(location, runeOffsets)
		if overlapsAny(span, timeSpans) || unitCoveredByNumber(span, elements) {
			continue
		}
		elements = append(elements, ExtractedElement{Kind: ElementUnit, Text: text[location[0]:location[1]], Span: span, Unit: text[location[0]:location[1]]})
	}
	elements = append(elements, extractObjects(text, binding)...)
	for _, match := range policy.matches(text, contributionMode) {
		copy := match
		elements = append(elements, ExtractedElement{
			Kind: ElementAssertion, Text: match.Text, Span: match.Span, Policy: &copy,
		})
	}
	sort.SliceStable(elements, func(left, right int) bool {
		if elements[left].Span.Start != elements[right].Span.Start {
			return elements[left].Span.Start < elements[right].Span.Start
		}
		if elements[left].Span.End != elements[right].Span.End {
			return elements[left].Span.End < elements[right].Span.End
		}
		return elements[left].Kind < elements[right].Kind
	})
	return elements
}

func extractRegexElements(text string, pattern *regexp.Regexp, kind ElementKind, offsets []int) []ExtractedElement {
	locations := pattern.FindAllStringIndex(text, -1)
	result := make([]ExtractedElement, 0, len(locations))
	for _, location := range locations {
		result = append(result, ExtractedElement{
			Kind: kind, Text: text[location[0]:location[1]], Span: byteSpanToRuneSpan(location, offsets),
		})
	}
	return result
}

func extractObjects(text string, binding BindingEvidence) []ExtractedElement {
	type candidate struct {
		object ObjectEvidence
		name   string
	}
	candidates := make([]candidate, 0)
	appendCandidates := func(objects []ObjectEvidence) {
		for _, object := range objects {
			for _, name := range object.Names {
				if strings.TrimSpace(name) != "" {
					candidates = append(candidates, candidate{object: object, name: name})
				}
			}
		}
	}
	appendCandidates(binding.Objects)
	sort.SliceStable(candidates, func(left, right int) bool {
		return utf8.RuneCountInString(candidates[left].name) > utf8.RuneCountInString(candidates[right].name)
	})
	runes := []rune(text)
	occupied := make([]bool, len(runes))
	result := make([]ExtractedElement, 0)
	for _, item := range candidates {
		needle := []rune(item.name)
		for start := 0; start+len(needle) <= len(runes); start++ {
			if spanOccupied(occupied, start, start+len(needle)) || string(runes[start:start+len(needle)]) != item.name {
				continue
			}
			markSpan(occupied, start, start+len(needle))
			match := ObjectMatch{
				Kind: item.object.Kind, VersionID: item.object.ObjectID,
				Bound: item.object.Bound, Name: item.name,
			}
			result = append(result, ExtractedElement{
				Kind: ElementObject, Text: item.name,
				Span: shared.TextSpan{Start: start, End: start + len(needle)}, Object: &match,
			})
		}
	}
	return result
}

func byteToRuneOffsets(text string) []int {
	offsets := make([]int, len(text)+1)
	runeIndex := 0
	for byteIndex := range text {
		offsets[byteIndex] = runeIndex
		if byteIndex == 0 || !utf8.RuneStart(text[byteIndex]) {
			continue
		}
		runeIndex++
	}
	// Recompute without relying on range's first-index special case.
	runeIndex = 0
	for byteIndex := 0; byteIndex < len(text); {
		_, width := utf8.DecodeRuneInString(text[byteIndex:])
		for offset := 0; offset < width; offset++ {
			offsets[byteIndex+offset] = runeIndex
		}
		byteIndex += width
		runeIndex++
	}
	offsets[len(text)] = runeIndex
	return offsets
}

func byteSpanToRuneSpan(location []int, offsets []int) shared.TextSpan {
	return shared.TextSpan{Start: offsets[location[0]], End: offsets[location[1]]}
}

func overlapsAny(span shared.TextSpan, others []shared.TextSpan) bool {
	for _, other := range others {
		if span.Start < other.End && other.Start < span.End {
			return true
		}
	}
	return false
}

func unitCoveredByNumber(span shared.TextSpan, elements []ExtractedElement) bool {
	for _, element := range elements {
		if element.Kind == ElementNumber && element.Unit != "" && element.Span.Start <= span.Start && element.Span.End >= span.End {
			return true
		}
	}
	return false
}

func meaningfulChineseNumber(raw string) bool {
	if strings.HasSuffix(raw, "个百分点") || strings.HasSuffix(raw, "个点") {
		return true
	}
	for _, character := range raw {
		if strings.ContainsRune("十百千万亿点", character) {
			return true
		}
	}
	return utf8.RuneCountInString(raw) > 1 && !allChineseNumeralDigits(raw)
}

func allChineseNumeralDigits(raw string) bool {
	for _, character := range raw {
		if !strings.ContainsRune("零〇一二两三四五六七八九", character) && !unicode.IsSpace(character) {
			return false
		}
	}
	return true
}
