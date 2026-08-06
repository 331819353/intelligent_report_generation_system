// Package search builds and retrieves version-pinned semantic search evidence.
package search

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/registry"
	"intelligent-report-generation-system/internal/askdata/security"
)

type ObjectType string
type ViewType string
type IndexPolicy string

const (
	ObjectMetric           ObjectType = "METRIC"
	ObjectDimension        ObjectType = "DIMENSION"
	ObjectMember           ObjectType = "MEMBER"
	ObjectBusinessTerm     ObjectType = "BUSINESS_TERM"
	ObjectCertifiedExample ObjectType = "CERTIFIED_EXAMPLE"

	ViewNameAlias          ViewType = "NAME_ALIAS"
	ViewDefinitionQuestion ViewType = "DEFINITION_QUESTION"
	ViewDimensionValue     ViewType = "DIMENSION_VALUE"
	ViewExampleIntent      ViewType = "EXAMPLE_INTENT"

	IndexLexical IndexPolicy = "LEXICAL"
	IndexVector  IndexPolicy = "VECTOR"
	IndexHybrid  IndexPolicy = "HYBRID"

	metricDocumentVersion    = "metric-search-v1"
	dimensionDocumentVersion = "dimension-search-v1"
	memberDocumentVersion    = "member-search-v1"
	termDocumentVersion      = "term-search-v1"
	exampleDocumentVersion   = "certified-example-search-v1"

	maxDocumentRunes = 32_768
)

var physicalQueryPattern = regexp.MustCompile(`(?is)\b(select\s+.+\s+from|insert\s+into|update\s+.+\s+set|delete\s+from|match\s*\(.+\)\s*(return|where))\b`)

type Document struct {
	ObjectType      ObjectType           `json:"objectType"`
	ObjectVersionID askdata.ID           `json:"objectVersionId"`
	ViewType        ViewType             `json:"viewType"`
	Sensitivity     registry.Sensitivity `json:"sensitivity"`
	IndexPolicy     IndexPolicy          `json:"indexPolicy"`
	DocumentVersion string               `json:"documentVersion"`
	Text            string               `json:"text"`
	Metadata        json.RawMessage      `json:"metadata"`
	InputHash       askdata.ContentHash  `json:"inputHash"`
}

type MetricDocumentInput struct {
	ObjectVersionID                              askdata.ID
	Name, Definition, Unit                       string
	Aliases, PositiveQuestions, NegativeExamples []string
	Sensitivity                                  registry.Sensitivity
}

type DimensionDocumentInput struct {
	ObjectVersionID         askdata.ID
	Name, Description, Kind string
	Aliases                 []string
	Sensitivity             registry.Sensitivity
}

type MemberDocumentInput struct {
	ObjectVersionID, DimensionVersionID askdata.ID
	DimensionName, DimensionDescription string
	CanonicalValue                      string
	Aliases                             []string
	Sensitivity                         registry.Sensitivity
	MemberIndexPolicy                   registry.MemberIndexPolicy
	HighCardinality                     bool
}

type TermDocumentInput struct {
	ObjectVersionID  askdata.ID
	Name, Definition string
	Aliases          []string
	Sensitivity      registry.Sensitivity
}

type CertifiedExampleDocumentInput struct {
	ObjectVersionID                 askdata.ID
	QuestionTemplate, IntentSummary string
	MetricNames, DimensionNames     []string
	Sensitivity                     registry.Sensitivity
}

func BuildMetricDocument(input MetricDocumentInput) (Document, error) {
	name, err := normalizeText(input.Name, 512)
	if err != nil {
		return Document{}, fmt.Errorf("name: %w", err)
	}
	aliases, err := normalizeList(input.Aliases, 64)
	if err != nil {
		return Document{}, fmt.Errorf("aliases: %w", err)
	}
	positive, err := normalizeList(input.PositiveQuestions, 32)
	if err != nil {
		return Document{}, fmt.Errorf("positiveQuestions: %w", err)
	}
	negative, err := normalizeList(input.NegativeExamples, 32)
	if err != nil {
		return Document{}, fmt.Errorf("negativeExamples: %w", err)
	}
	metadata := struct {
		Name              string   `json:"name"`
		Aliases           []string `json:"aliases"`
		PositiveQuestions []string `json:"positiveQuestions"`
		NegativeExamples  []string `json:"negativeExamples"`
	}{name, aliases, positive, negative}
	return buildDocument(
		ObjectMetric, input.ObjectVersionID, ViewDefinitionQuestion, input.Sensitivity,
		metricDocumentVersion, []field{
			{"name", name}, {"aliases", strings.Join(aliases, " / ")},
			{"definition", input.Definition}, {"unit", input.Unit},
			{"questions", strings.Join(positive, " / ")},
			{"hard_negatives", strings.Join(negative, " / ")},
		}, metadata,
	)
}

func BuildDimensionDocument(input DimensionDocumentInput) (Document, error) {
	name, err := normalizeText(input.Name, 512)
	if err != nil {
		return Document{}, fmt.Errorf("name: %w", err)
	}
	aliases, err := normalizeList(input.Aliases, 64)
	if err != nil {
		return Document{}, fmt.Errorf("aliases: %w", err)
	}
	metadata := struct {
		Name    string   `json:"name"`
		Aliases []string `json:"aliases"`
	}{name, aliases}
	return buildDocument(
		ObjectDimension, input.ObjectVersionID, ViewNameAlias, input.Sensitivity,
		dimensionDocumentVersion, []field{
			{"name", name}, {"aliases", strings.Join(aliases, " / ")},
			{"description", input.Description}, {"kind", input.Kind},
		}, metadata,
	)
}

func BuildMemberDocument(input MemberDocumentInput) (Document, error) {
	exposure, err := security.DecideMemberExposure(
		input.Sensitivity, input.MemberIndexPolicy, input.HighCardinality,
	)
	if err != nil || !exposure.Embedding {
		return Document{}, errors.New("member policy forbids label-bearing search documents")
	}
	if err := input.DimensionVersionID.Validate(); err != nil {
		return Document{}, fmt.Errorf("dimensionVersionId: %w", err)
	}
	dimensionName, err := normalizeText(input.DimensionName, 512)
	if err != nil {
		return Document{}, fmt.Errorf("dimensionName: %w", err)
	}
	canonicalValue, err := normalizeText(input.CanonicalValue, 512)
	if err != nil {
		return Document{}, fmt.Errorf("canonicalValue: %w", err)
	}
	aliases, err := normalizeList(input.Aliases, 64)
	if err != nil {
		return Document{}, fmt.Errorf("aliases: %w", err)
	}
	metadata := struct {
		DimensionVersionID askdata.ID `json:"dimensionVersionId"`
		DimensionName      string     `json:"dimensionName"`
		CanonicalValue     string     `json:"canonicalValue"`
		Aliases            []string   `json:"aliases"`
	}{input.DimensionVersionID, dimensionName, canonicalValue, aliases}
	// Dimension name and description are intentionally repeated beside the
	// canonical member value. This prevents identical values such as “华东” or
	// “A级” from competing without their dimension context.
	return buildDocument(
		ObjectMember, input.ObjectVersionID, ViewDimensionValue, input.Sensitivity,
		memberDocumentVersion, []field{
			{"dimension", dimensionName}, {"dimension_definition", input.DimensionDescription},
			{"canonical_value", canonicalValue}, {"aliases", strings.Join(aliases, " / ")},
		}, metadata,
	)
}

func BuildTermDocument(input TermDocumentInput) (Document, error) {
	name, err := normalizeText(input.Name, 512)
	if err != nil {
		return Document{}, fmt.Errorf("name: %w", err)
	}
	aliases, err := normalizeList(input.Aliases, 64)
	if err != nil {
		return Document{}, fmt.Errorf("aliases: %w", err)
	}
	metadata := struct {
		Name    string   `json:"name"`
		Aliases []string `json:"aliases"`
	}{name, aliases}
	return buildDocument(
		ObjectBusinessTerm, input.ObjectVersionID, ViewNameAlias, input.Sensitivity,
		termDocumentVersion, []field{
			{"name", name}, {"aliases", strings.Join(aliases, " / ")}, {"definition", input.Definition},
		}, metadata,
	)
}

func BuildCertifiedExampleDocument(input CertifiedExampleDocumentInput) (Document, error) {
	metrics, err := normalizeList(input.MetricNames, 32)
	if err != nil {
		return Document{}, fmt.Errorf("metricNames: %w", err)
	}
	dimensions, err := normalizeList(input.DimensionNames, 32)
	if err != nil {
		return Document{}, fmt.Errorf("dimensionNames: %w", err)
	}
	metadata := struct {
		QuestionTemplate string   `json:"questionTemplate"`
		Metrics          []string `json:"metrics"`
		Dimensions       []string `json:"dimensions"`
	}{input.QuestionTemplate, metrics, dimensions}
	return buildDocument(
		ObjectCertifiedExample, input.ObjectVersionID, ViewExampleIntent, input.Sensitivity,
		exampleDocumentVersion, []field{
			{"question_template", input.QuestionTemplate}, {"intent", input.IntentSummary},
			{"metrics", strings.Join(metrics, " / ")}, {"dimensions", strings.Join(dimensions, " / ")},
		}, metadata,
	)
}

type field struct{ name, value string }

func buildDocument(
	objectType ObjectType, objectVersionID askdata.ID, viewType ViewType,
	sensitivity registry.Sensitivity, version string, fields []field, metadata any,
) (Document, error) {
	if err := objectVersionID.Validate(); err != nil {
		return Document{}, fmt.Errorf("objectVersionId: %w", err)
	}
	if sensitivity == registry.SensitivityRestricted ||
		(sensitivity != registry.SensitivityPublic && sensitivity != registry.SensitivityInternal && sensitivity != registry.SensitivityConfidential) {
		return Document{}, errors.New("sensitivity is unsupported for search documents")
	}
	parts := make([]string, 0, len(fields)+1)
	parts = append(parts, "type="+strings.ToLower(string(objectType)))
	for _, item := range fields {
		value, err := normalizeText(item.value, maxDocumentRunes)
		if err != nil {
			return Document{}, fmt.Errorf("%s: %w", item.name, err)
		}
		if value != "" {
			parts = append(parts, item.name+"="+value)
		}
	}
	text := strings.Join(parts, " | ")
	if utf8.RuneCountInString(text) > maxDocumentRunes {
		return Document{}, errors.New("search document exceeds maximum length")
	}
	if ai.ContainsSensitiveText(text) {
		return Document{}, errors.New("search document contains credential-shaped text")
	}
	if physicalQueryPattern.MatchString(text) {
		return Document{}, errors.New("search document contains a physical query")
	}
	metadataRaw, err := json.Marshal(metadata)
	if err != nil {
		return Document{}, err
	}
	indexPolicy := IndexHybrid
	if sensitivity == registry.SensitivityConfidential {
		indexPolicy = IndexLexical
	}
	document := Document{
		ObjectType: objectType, ObjectVersionID: objectVersionID, ViewType: viewType,
		Sensitivity: sensitivity, IndexPolicy: indexPolicy,
		DocumentVersion: version, Text: text, Metadata: metadataRaw,
	}
	hashPayload, err := json.Marshal(struct {
		ObjectType      ObjectType           `json:"objectType"`
		ObjectVersionID askdata.ID           `json:"objectVersionId"`
		ViewType        ViewType             `json:"viewType"`
		Sensitivity     registry.Sensitivity `json:"sensitivity"`
		IndexPolicy     IndexPolicy          `json:"indexPolicy"`
		DocumentVersion string               `json:"documentVersion"`
		Text            string               `json:"text"`
		Metadata        json.RawMessage      `json:"metadata"`
	}{document.ObjectType, document.ObjectVersionID, document.ViewType, document.Sensitivity, document.IndexPolicy, document.DocumentVersion, document.Text, document.Metadata})
	if err != nil {
		return Document{}, err
	}
	document.InputHash = askdata.HashBytes(hashPayload)
	return document, nil
}

func normalizeList(values []string, maximum int) ([]string, error) {
	if len(values) > maximum {
		return nil, fmt.Errorf("exceeds %d items", maximum)
	}
	result := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for index, value := range values {
		normalized, err := normalizeText(value, 512)
		if err != nil {
			return nil, fmt.Errorf("item %d: %w", index, err)
		}
		if normalized == "" {
			continue
		}
		key := strings.ToLower(normalized)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, normalized)
	}
	sort.Slice(result, func(i, j int) bool { return strings.ToLower(result[i]) < strings.ToLower(result[j]) })
	return result, nil
}

func normalizeText(value string, maximum int) (string, error) {
	if !utf8.ValidString(value) {
		return "", errors.New("text is not valid UTF-8")
	}
	value = norm.NFKC.String(value)
	value = strings.Join(strings.FieldsFunc(value, unicode.IsSpace), " ")
	for _, character := range value {
		if unicode.IsControl(character) {
			return "", errors.New("text contains control characters")
		}
	}
	if utf8.RuneCountInString(value) > maximum {
		return "", fmt.Errorf("text exceeds %d characters", maximum)
	}
	return value, nil
}
