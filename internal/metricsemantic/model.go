// Package metricsemantic owns metric semantic vectors and hybrid retrieval.
package metricsemantic

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var ErrInvalidRequest = errors.New("metric semantic request is invalid")

type EmbeddingClaim struct {
	ID       string
	TenantID string
	Document string
}

type SearchResult struct {
	SubjectType       string          `json:"subjectType"`
	CandidateID       string          `json:"candidateId,omitempty"`
	MetricID          string          `json:"metricId,omitempty"`
	MetricVersionID   string          `json:"metricVersionId,omitempty"`
	DatasetID         string          `json:"datasetId"`
	DatasetVersionID  string          `json:"datasetVersionId"`
	Name              string          `json:"name"`
	Description       string          `json:"description"`
	Caliber           string          `json:"caliber"`
	Dimensions        []string        `json:"dimensions"`
	Period            string          `json:"period"`
	PeriodDescription string          `json:"periodDescription"`
	Lineage           json.RawMessage `json:"lineage"`
	LineageSummary    string          `json:"lineageSummary"`
	Tags              []string        `json:"tags"`
	SemanticScore     float64         `json:"semanticScore"`
	KeywordScore      float64         `json:"keywordScore"`
	Score             float64         `json:"score"`
	BindingAllowed    bool            `json:"bindingAllowed"`
	EmbeddingReady    bool            `json:"embeddingReady"`
}

type SearchResponse struct {
	Items      []SearchResult `json:"items"`
	Degraded   bool           `json:"degraded"`
	Model      string         `json:"model,omitempty"`
	Dimensions int            `json:"dimensions,omitempty"`
}

type Store interface {
	ListPendingTenantIDs(context.Context) ([]string, error)
	Claim(context.Context, string, string, time.Duration) (*EmbeddingClaim, error)
	Complete(context.Context, EmbeddingClaim, string, string, []float32) error
	Fail(context.Context, EmbeddingClaim, string, string) error
	Search(context.Context, string, string, []float32, int, bool) ([]SearchResult, error)
}
