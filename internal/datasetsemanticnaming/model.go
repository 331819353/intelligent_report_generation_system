// Package datasetsemanticnaming performs governed save-time semantic naming for
// DWD/DWS/ADS drafts. It never reads business rows and never chooses physical
// schemas, tables, views, or SQL.
package datasetsemanticnaming

import (
	"context"
	"encoding/json"

	aiplatform "intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/dataset"
)

const (
	PromptVersion     = "dataset-semantic-naming-v3"
	MaxFields         = 1024
	MaxUpstreams      = 32
	MaxUpstreamFields = 4096
	MaxTaxonomyTags   = 1024
	MaxSuggestions    = 16
	MaxInputBytes     = 256 << 10
)

type Invoker interface {
	Configured() bool
	Invoke(context.Context, aiplatform.Invocation) (aiplatform.InvocationResult, error)
}

type Catalog interface {
	Load(context.Context, string, dataset.Document) (Context, error)
}

type FieldContext struct {
	ID            string          `json:"id"`
	Code          string          `json:"code"`
	Name          string          `json:"name"`
	Description   string          `json:"description"`
	Role          string          `json:"role"`
	CanonicalType string          `json:"canonicalType"`
	SemanticType  string          `json:"semanticType"`
	Aggregation   string          `json:"aggregation"`
	Expression    json.RawMessage `json:"expression"`
}

type DatasetContext struct {
	Code        string `json:"code"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Domain      string `json:"domain"`
	Subject     string `json:"subject"`
	Type        string `json:"type"`
	Layer       string `json:"layer"`
	OutputGrain string `json:"outputGrain"`
}

type NodeContext struct {
	ID               string   `json:"id"`
	Type             string   `json:"type"`
	Alias            string   `json:"alias"`
	DatasetVersionID string   `json:"datasetVersionId"`
	Projection       []string `json:"projection"`
}

type UpstreamContext struct {
	DatasetID   string         `json:"datasetId"`
	VersionID   string         `json:"versionId"`
	Code        string         `json:"code"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Domain      string         `json:"domain"`
	Subject     string         `json:"subject"`
	Layer       string         `json:"layer"`
	OutputGrain string         `json:"outputGrain"`
	Fields      []FieldContext `json:"fields"`
	Tags        []string       `json:"approvedTags"`
}

type TaxonomyTag struct {
	ID          string   `json:"id"`
	Code        string   `json:"code"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Category    string   `json:"category"`
	Aliases     []string `json:"aliases"`
}

type Context struct {
	Upstreams []UpstreamContext `json:"upstreams"`
	Taxonomy  []TaxonomyTag     `json:"controlledTaxonomy"`
}

type Input struct {
	Dataset DatasetContext `json:"dataset"`
	Fields  []FieldContext `json:"outputFields"`
	Nodes   []NodeContext  `json:"nodes"`
	GroupBy []string       `json:"groupByFieldIds"`
	Context Context        `json:"governedContext"`
}

type providerOutput struct {
	Dataset DatasetNaming          `json:"dataset"`
	Fields  map[string]FieldNaming `json:"fields"`
	Tags    []providerTagChoice    `json:"tags"`
}

type DatasetNaming struct {
	Code        string `json:"code"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type FieldNaming struct {
	Code        string `json:"code"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type providerTagChoice struct {
	TagID      string  `json:"tagId"`
	Confidence float64 `json:"confidence"`
	Rationale  string  `json:"rationale"`
}
