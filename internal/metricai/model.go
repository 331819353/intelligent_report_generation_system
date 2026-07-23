package metricai

import (
	"context"
	"errors"

	aiplatform "intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/metric"
)

const (
	SchemaVersion = "1.0"
	PromptVersion = "metric-authoring-v7"
	Purpose       = aiplatform.PurposeMetricAuthoring
)

const (
	StrategyReuseMetric        = "REUSE_METRIC"
	StrategyCreateOnDataset    = "CREATE_ON_DATASET"
	StrategyCreateDataset      = "CREATE_DATASET"
	StrategyModifyDataset      = "MODIFY_DATASET"
	StrategyDataGap            = "DATA_GAP"
	StrategyNeedsClarification = "NEEDS_CLARIFICATION"
)

var (
	ErrInvalidRequest          = errors.New("metric AI request is invalid")
	ErrInvalidRetrievalContext = errors.New("metric AI retrieval context is invalid")
	ErrProviderUnavailable     = errors.New("metric AI provider is not configured")
	ErrInvalidOutput           = errors.New("metric AI output is invalid")
)

// AuthoringRequest is AI-first: the current API accepts one natural-language requirement.
// The legacy fields remain decode-only compatibility inputs and are normalized into Requirement
// before retrieval or prompting; new clients must not send them.
type AuthoringRequest struct {
	Requirement      string `json:"requirement,omitempty"`
	Name             string `json:"name,omitempty"`
	DefinitionIntent string `json:"definitionIntent,omitempty"`
	TimeIntent       string `json:"timeIntent,omitempty"`
}

// AuthorizedDataset is one exact logical snapshot supplied by the host after permission checks.
// Its retrieval collection and Status distinguish published snapshots from modifiable drafts.
// Aggregated datasets may be included as evidence, but are ineligible as action targets in V1.
// Mapped identifies the system-maintained one-table projection backed by origin_table_id. Mapped
// snapshots are isolated into RetrievalContext.MappedDatasets and are immutable source evidence
// for CREATE_DATASET; they are never direct metric or in-place modification targets.
type AuthorizedDataset struct {
	ID          string `json:"id"`
	VersionID   string `json:"versionId"`
	VersionNo   int    `json:"versionNo"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Status      string `json:"status"`
	DSLHash     string `json:"dslHash"`
	Aggregated  bool   `json:"aggregated"`
	Mapped      bool   `json:"mapped"`
	Manageable  bool   `json:"manageable"`
}

// AuthorizedField is a logical field from one exact dataset version. Physical SQL, credentials,
// and sample values never enter the metric authoring envelope.
type AuthorizedField struct {
	DatasetID        string `json:"datasetId"`
	DatasetVersionID string `json:"datasetVersionId"`
	ID               string `json:"id"`
	Code             string `json:"code"`
	Name             string `json:"name"`
	Description      string `json:"description"`
	CanonicalType    string `json:"canonicalType"`
	Role             string `json:"role"`
	SemanticType     string `json:"semanticType"`
}

// AuthorizedMetric is a published metric version the caller may read. It can only drive
// REUSE_METRIC in this atomic-only MVP; candidate definitions may not reference it.
type AuthorizedMetric struct {
	ID               string            `json:"id"`
	VersionID        string            `json:"versionId"`
	Code             string            `json:"code"`
	Name             string            `json:"name"`
	Description      string            `json:"description"`
	Status           string            `json:"status"`
	DatasetID        string            `json:"datasetId"`
	DatasetVersionID string            `json:"datasetVersionId"`
	DefinitionHash   string            `json:"definitionHash"`
	Definition       metric.Definition `json:"definition"`
}

// AuthorizedAtomicFact is an internal, non-bindable measurement building block extracted from
// a published dataset DAG. It helps authoring choose fields and aggregations but is never a
// reusable metric, action target, or user-visible metric-center asset.
type AuthorizedAtomicFact struct {
	DatasetID        string   `json:"datasetId"`
	DatasetVersionID string   `json:"datasetVersionId"`
	SourceFieldIDs   []string `json:"sourceFieldIds"`
	Name             string   `json:"name"`
	Description      string   `json:"description"`
	Caliber          string   `json:"caliber"`
	Aggregation      string   `json:"aggregation"`
	Dimensions       []string `json:"dimensions"`
	Period           string   `json:"period"`
	Tags             []string `json:"tags"`
	Confidence       float64  `json:"confidence"`
}

// RetrievalContext is produced by a trusted, permission-aware host implementation. Datasets and
// Fields contain ordinary published snapshots that may support direct metric creation.
// ModifiableDraft* contain ordinary, separately authorized drafts that may only support dataset
// modification. Mapped* are read-only fallback sources for creating a new ordinary dataset.
// Manageable is a separately evaluated DATASET/MANAGE capability, never inferred from READ.
type RetrievalContext struct {
	Datasets                []AuthorizedDataset    `json:"datasets"`
	Fields                  []AuthorizedField      `json:"fields"`
	ModifiableDraftDatasets []AuthorizedDataset    `json:"modifiableDraftDatasets"`
	ModifiableDraftFields   []AuthorizedField      `json:"modifiableDraftFields"`
	MappedDatasets          []AuthorizedDataset    `json:"mappedDatasets"`
	MappedFields            []AuthorizedField      `json:"mappedFields"`
	AtomicFacts             []AuthorizedAtomicFact `json:"atomicFacts"`
	ExistingMetrics         []AuthorizedMetric     `json:"existingMetrics"`
}

// Retriever is the only discovery boundary. The model never receives a database connection or
// an open-ended tool and therefore cannot expand its own authorization scope.
type Retriever interface {
	Retrieve(context.Context, string, string, AuthoringRequest) (RetrievalContext, error)
}

// RetrievalEvidence makes every cited dataset, field, or metric version independently
// checkable against the authorized retrieval snapshot.
type RetrievalEvidence struct {
	SourceType       string `json:"sourceType"`
	SourceID         string `json:"sourceId"`
	DatasetID        string `json:"datasetId"`
	DatasetVersionID string `json:"datasetVersionId"`
	Reason           string `json:"reason"`
}

// MetricAuthoringProposal is review-only state. A nil candidate is required for strategies whose
// exact published dataset version is not yet known.
type MetricAuthoringProposal struct {
	SchemaVersion             string              `json:"schemaVersion"`
	Strategy                  string              `json:"strategy"`
	Summary                   string              `json:"summary"`
	TargetDatasetID           string              `json:"targetDatasetId"`
	TargetDatasetVersionID    string              `json:"targetDatasetVersionId"`
	ReuseMetricVersionID      string              `json:"reuseMetricVersionId"`
	RetrievalEvidence         []RetrievalEvidence `json:"retrievalEvidence"`
	CandidateMetricDefinition *metric.Definition  `json:"candidateMetricDefinition"`
	DatasetInstruction        string              `json:"datasetInstruction"`
	ClarificationQuestions    []string            `json:"clarificationQuestions"`
	Assumptions               []string            `json:"assumptions"`
	Warnings                  []string            `json:"warnings"`
}

type ProposalResult struct {
	RequestID            string                  `json:"requestId"`
	RetrievalContextHash string                  `json:"retrievalContextHash"`
	Proposal             MetricAuthoringProposal `json:"proposal"`
}
