package semanticqa

import (
	"encoding/json"
	"errors"

	"intelligent-report-generation-system/internal/dataset"
)

var (
	ErrInvalidRequest  = errors.New("semantic QA request is invalid")
	ErrNotFound        = errors.New("semantic QA object was not found")
	ErrConflict        = errors.New("semantic QA object changed concurrently")
	ErrDisabled        = errors.New("semantic QA capability is disabled")
	ErrGraphNotReady   = errors.New("semantic graph is not ready")
	ErrUnprovenPath    = errors.New("semantic query path is not proven")
	ErrUnsafeChange    = errors.New("DAG change set is unsafe")
	ErrInvalidState    = errors.New("semantic QA state transition is invalid")
	ErrProjectionLease = errors.New("semantic graph projection lease was lost")
)

type Settings struct {
	Enabled                bool    `json:"enabled"`
	GraphProjectionEnabled bool    `json:"graphProjectionEnabled"`
	QuestionChangeEnabled  bool    `json:"questionChangeEnabled"`
	MinimumPathConfidence  float64 `json:"minimumPathConfidence"`
	MaximumPathHops        int     `json:"maximumPathHops"`
	UpdatedAt              string  `json:"updatedAt"`
}

type ChangeOperation struct {
	Operation string          `json:"operation"`
	Path      string          `json:"path"`
	Value     json.RawMessage `json:"value,omitempty"`
}

type CreateChangeSetInput struct {
	TargetDatasetID        string            `json:"targetDatasetId,omitempty"`
	TriggerType            string            `json:"triggerType"`
	ChangeKind             string            `json:"changeKind"`
	TargetLayer            string            `json:"targetLayer"`
	Title                  string            `json:"title"`
	Question               string            `json:"question,omitempty"`
	BaselineDatasetVersion *int64            `json:"baselineDatasetVersion,omitempty"`
	BaselineDSLHash        string            `json:"baselineDslHash,omitempty"`
	RequestKey             string            `json:"requestKey"`
	Operations             []ChangeOperation `json:"operations"`
}

// CreateChangeSetFromCandidateInput is the compatibility boundary for the
// existing question-driven DAG designer. The full candidate is validated and
// converted into bounded component patches in memory; it is never persisted as
// an opaque replacement document.
type CreateChangeSetFromCandidateInput struct {
	TargetDatasetID string          `json:"targetDatasetId,omitempty"`
	TriggerType     string          `json:"triggerType"`
	Title           string          `json:"title"`
	Question        string          `json:"question,omitempty"`
	RequestKey      string          `json:"requestKey"`
	CandidateDSL    json.RawMessage `json:"candidateDsl"`
}

type ChangeValidation struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Path     string `json:"path"`
	Message  string `json:"message"`
}

type ChangeSet struct {
	ID                     string             `json:"id"`
	TargetDatasetID        string             `json:"targetDatasetId,omitempty"`
	TriggerType            string             `json:"triggerType"`
	ChangeKind             string             `json:"changeKind"`
	TargetLayer            string             `json:"targetLayer"`
	Title                  string             `json:"title"`
	QuestionHash           string             `json:"questionHash,omitempty"`
	BaselineDatasetVersion *int64             `json:"baselineDatasetVersion,omitempty"`
	BaselineDSLHash        string             `json:"baselineDslHash,omitempty"`
	RequestKey             string             `json:"requestKey"`
	Status                 string             `json:"status"`
	ErrorCode              string             `json:"errorCode,omitempty"`
	RecordVersion          int64              `json:"recordVersion"`
	Operations             []ChangeOperation  `json:"operations"`
	Validations            []ChangeValidation `json:"validations"`
	CreatedAt              string             `json:"createdAt"`
	UpdatedAt              string             `json:"updatedAt"`
}

type ValidateChangeSetInput struct {
	ExpectedRecordVersion int64 `json:"expectedRecordVersion"`
}

type ApplyChangeSetInput struct {
	ExpectedRecordVersion int64 `json:"expectedRecordVersion"`
}

type RejectChangeSetInput struct {
	ExpectedRecordVersion int64  `json:"expectedRecordVersion"`
	ReasonCode            string `json:"reasonCode"`
}

type ApplyChangeSetResult struct {
	ChangeSet ChangeSet `json:"changeSet"`
	DatasetID string    `json:"datasetId"`
	DSLHash   string    `json:"dslHash"`
}

type ConsumerContractInput struct {
	DatasetID        string `json:"datasetId"`
	DatasetVersionID string `json:"datasetVersionId"`
	Required         bool   `json:"required"`
}

type CreateConsumerContractInput struct {
	Code         string                  `json:"code"`
	Name         string                  `json:"name"`
	Purpose      string                  `json:"purpose"`
	OutputGrain  json.RawMessage         `json:"outputGrain"`
	ServiceLevel json.RawMessage         `json:"serviceLevel"`
	Inputs       []ConsumerContractInput `json:"inputs"`
}

type ConsumerContract struct {
	ID           string                  `json:"id"`
	Code         string                  `json:"code"`
	Name         string                  `json:"name"`
	Purpose      string                  `json:"purpose"`
	OutputGrain  json.RawMessage         `json:"outputGrain"`
	ServiceLevel json.RawMessage         `json:"serviceLevel"`
	Status       string                  `json:"status"`
	Version      int64                   `json:"version"`
	Inputs       []ConsumerContractInput `json:"inputs"`
	CreatedAt    string                  `json:"createdAt"`
	UpdatedAt    string                  `json:"updatedAt"`
}

type PublishConsumerContractInput struct {
	ExpectedVersion int64 `json:"expectedVersion"`
}

type WarehouseDAGNode struct {
	DatasetVersionID string `json:"datasetVersionId"`
	DatasetID        string `json:"datasetId"`
	Code             string `json:"code"`
	Name             string `json:"name"`
	Layer            string `json:"layer"`
	Status           string `json:"status"`
}

type WarehouseDAGEdge struct {
	FromDatasetVersionID string `json:"fromDatasetVersionId"`
	ToDatasetVersionID   string `json:"toDatasetVersionId"`
	SourceType           string `json:"sourceType"`
}

type WarehouseBuildDAG struct {
	RootDatasetVersionID string             `json:"rootDatasetVersionId"`
	Nodes                []WarehouseDAGNode `json:"nodes"`
	Edges                []WarehouseDAGEdge `json:"edges"`
	TopologicalOrder     []string           `json:"topologicalOrder"`
}

type QueryPlanInput struct {
	Question           string                   `json:"question"`
	Intent             string                   `json:"intent"`
	MemberValue        string                   `json:"memberValue,omitempty"`
	MemberFilters      []QueryMemberFilterInput `json:"memberFilters,omitempty"`
	DimensionCode      string                   `json:"dimensionCode,omitempty"`
	MetricCode         string                   `json:"metricCode"`
	ContextQueryPlanID string                   `json:"contextQueryPlanId,omitempty"`
	TimeRange          *QueryTimeRange          `json:"timeRange,omitempty"`
	TimePreset         string                   `json:"timePreset,omitempty"`
	Timezone           string                   `json:"timezone,omitempty"`
	ComparisonMode     string                   `json:"comparisonMode,omitempty"`
	ComparisonRange    *QueryTimeRange          `json:"comparisonRange,omitempty"`
	TopN               int                      `json:"topN,omitempty"`
	SortDirection      string                   `json:"sortDirection,omitempty"`
	MaximumPathHops    int                      `json:"maximumPathHops,omitempty"`
	// The following fields are server-owned resolution metadata. They are
	// populated by the governed catalog interpreter and must never be accepted
	// from API callers.
	MetricCandidateCount int    `json:"-"`
	MetricMatchMethod    string `json:"-"`
	Domain               string `json:"-"`
}

type QueryMemberFilterInput struct {
	DimensionCode string `json:"dimensionCode"`
	MemberValue   string `json:"memberValue"`
}

// QueryTimeRange is a half-open, caller-controlled time boundary. Values must
// be RFC3339 instants or ISO dates; the runtime never derives SQL from them.
type QueryTimeRange struct {
	Start        string `json:"start"`
	EndExclusive string `json:"endExclusive"`
}

// QueryExecutionBinding is reloaded from governed metadata immediately before
// execution so a persisted plan cannot smuggle fields or filter expressions.
type QueryExecutionBinding struct {
	DimensionFieldID string
	MemberKey        string
	MemberFilters    []QueryMemberFilterBinding
	TimeFieldID      string
	TimeFieldType    string
	TimeRange        *QueryTimeRange
	ComparisonMode   string
	ComparisonRange  *QueryTimeRange
	TopN             int
	SortDirection    string
}

type QueryMemberFilterBinding struct {
	DimensionID string `json:"dimensionId"`
	FieldID     string `json:"fieldId"`
	MemberKey   string `json:"memberKey"`
}

type QueryEvidence struct {
	Index        int     `json:"index"`
	NodeKey      string  `json:"nodeKey"`
	RelationType string  `json:"relationType,omitempty"`
	SubjectType  string  `json:"subjectType"`
	SubjectRef   string  `json:"subjectRef"`
	Label        string  `json:"label"`
	Authority    string  `json:"authority"`
	Confidence   float64 `json:"confidence"`
	EvidenceHash string  `json:"evidenceHash"`
}

type QueryResolutionStep struct {
	Stage          string `json:"stage"`
	Status         string `json:"status"`
	CandidateCount int    `json:"candidateCount,omitempty"`
	SelectedCode   string `json:"selectedCode,omitempty"`
	Decision       string `json:"decision,omitempty"`
}

// QueryConditionDocument is the canonical leaf produced by the dynamic
// decision DAG. It contains governed identifiers and opaque member keys, never
// executable SQL or untrusted expressions.
type QueryConditionDocument struct {
	Domain           string                 `json:"domain"`
	MetricCode       string                 `json:"metricCode"`
	MetricVersionID  string                 `json:"metricVersionId"`
	DatasetVersionID string                 `json:"datasetVersionId"`
	Dimensions       []QueryDimensionClause `json:"dimensions"`
	TimeRange        *QueryTimeRange        `json:"timeRange,omitempty"`
}

type QueryDimensionClause struct {
	DimensionCode string `json:"dimensionCode"`
	DimensionID   string `json:"dimensionId"`
	MemberKey     string `json:"memberKey"`
}

type QueryPlan struct {
	ID                        string                 `json:"id"`
	GraphGenerationID         string                 `json:"graphGenerationId"`
	GraphGeneration           int64                  `json:"graphGeneration"`
	QuestionHash              string                 `json:"questionHash"`
	Intent                    string                 `json:"intent"`
	Status                    string                 `json:"status"`
	Confidence                float64                `json:"confidence"`
	SelectedMetricID          string                 `json:"selectedMetricId,omitempty"`
	SelectedMetricVersionID   string                 `json:"selectedMetricVersionId,omitempty"`
	SelectedDimensionID       string                 `json:"selectedDimensionId,omitempty"`
	SelectedDatasetVersionID  string                 `json:"selectedDatasetVersionId,omitempty"`
	SelectedMaterializationID string                 `json:"selectedMaterializationId,omitempty"`
	PathHash                  string                 `json:"pathHash,omitempty"`
	FailureCode               string                 `json:"failureCode,omitempty"`
	Evidence                  []QueryEvidence        `json:"evidence"`
	Resolution                []QueryResolutionStep  `json:"resolution"`
	Conditions                QueryConditionDocument `json:"conditions"`
	ExecutedQueryID           string                 `json:"executedQueryId,omitempty"`
	ExecutionErrorCode        string                 `json:"executionErrorCode,omitempty"`
	ExecutionDurationMS       *int64                 `json:"executionDurationMs,omitempty"`
	ExecutionRowCount         *int                   `json:"executionRowCount,omitempty"`
	CreatedAt                 string                 `json:"createdAt"`
}

type ExecuteQueryPlanInput struct {
	ExpectedGraphGenerationID string         `json:"expectedGraphGenerationId"`
	ExpectedPathHash          string         `json:"expectedPathHash"`
	QueryID                   string         `json:"queryId"`
	Parameters                map[string]any `json:"parameters"`
	MaxRows                   int            `json:"maxRows,omitempty"`
}

type AnswerEvidence struct {
	GraphGenerationID     string          `json:"graphGenerationId"`
	GraphGeneration       int64           `json:"graphGeneration"`
	PathHash              string          `json:"pathHash"`
	MetricID              string          `json:"metricId"`
	MetricVersionID       string          `json:"metricVersionId"`
	DimensionID           string          `json:"dimensionId,omitempty"`
	DatasetVersionID      string          `json:"datasetVersionId"`
	MaterializationID     string          `json:"materializationId"`
	Lineage               []QueryEvidence `json:"lineage"`
	PermissionDecision    string          `json:"permissionDecision"`
	FreshnessDecision     string          `json:"freshnessDecision"`
	CompatibilityDecision string          `json:"compatibilityDecision"`
	ExecutionRevalidated  bool            `json:"executionRevalidated"`
}

type QueryPlanExecution struct {
	QueryPlan  QueryPlan                 `json:"queryPlan"`
	Result     dataset.PreviewResult     `json:"result"`
	Evidence   AnswerEvidence            `json:"evidence"`
	Comparison *QueryComparisonExecution `json:"comparison,omitempty"`
}

type QueryComparisonExecution struct {
	Mode          string                `json:"mode"`
	CurrentRange  QueryTimeRange        `json:"currentRange"`
	BaselineRange QueryTimeRange        `json:"baselineRange"`
	Baseline      dataset.PreviewResult `json:"baseline"`
}

type CreateQuestionTemplateInput struct {
	Code          string          `json:"code"`
	Name          string          `json:"name"`
	Intent        string          `json:"intent"`
	RequiredSlots json.RawMessage `json:"requiredSlots"`
}

type QuestionTemplate struct {
	ID            string          `json:"id"`
	Code          string          `json:"code"`
	Name          string          `json:"name"`
	Intent        string          `json:"intent"`
	RequiredSlots json.RawMessage `json:"requiredSlots"`
	Status        string          `json:"status"`
	CreatedAt     string          `json:"createdAt"`
	UpdatedAt     string          `json:"updatedAt"`
}

type CreateGoldenQuestionSetInput struct {
	Code                 string  `json:"code"`
	Name                 string  `json:"name"`
	BusinessDomain       string  `json:"businessDomain"`
	Version              int64   `json:"version"`
	CorrectnessThreshold float64 `json:"correctnessThreshold"`
	SafetyThreshold      float64 `json:"safetyThreshold"`
}

type GoldenQuestionSet struct {
	ID                   string  `json:"id"`
	Code                 string  `json:"code"`
	Name                 string  `json:"name"`
	BusinessDomain       string  `json:"businessDomain"`
	Version              int64   `json:"version"`
	CorrectnessThreshold float64 `json:"correctnessThreshold"`
	SafetyThreshold      float64 `json:"safetyThreshold"`
	Status               string  `json:"status"`
	RecordVersion        int64   `json:"recordVersion"`
	CreatedAt            string  `json:"createdAt"`
	UpdatedAt            string  `json:"updatedAt"`
}

type ActivateGoldenQuestionSetInput struct {
	ExpectedRecordVersion int64 `json:"expectedRecordVersion"`
}

type GoldenQuestionFixture struct {
	QueryPlan           QueryPlanInput `json:"queryPlan"`
	ExpectedFailureCode string         `json:"expectedFailureCode,omitempty"`
	SafetyCritical      bool           `json:"safetyCritical"`
}

type CreateGoldenQuestionInput struct {
	SetID            string                `json:"setId"`
	Question         string                `json:"question"`
	TemplateID       string                `json:"templateId,omitempty"`
	ExpectedPathHash string                `json:"expectedPathHash"`
	ExpectedStatus   string                `json:"expectedStatus"`
	Fixture          GoldenQuestionFixture `json:"fixture"`
}

type GoldenQuestion struct {
	ID               string                `json:"id"`
	SetID            string                `json:"setId"`
	QuestionHash     string                `json:"questionHash"`
	TemplateID       string                `json:"templateId,omitempty"`
	ExpectedPathHash string                `json:"expectedPathHash"`
	ExpectedStatus   string                `json:"expectedStatus"`
	Fixture          GoldenQuestionFixture `json:"fixture"`
	Status           string                `json:"status"`
	CreatedAt        string                `json:"createdAt"`
	UpdatedAt        string                `json:"updatedAt"`
	createdBy        string
}

type GoldenQuestionReplay struct {
	ID               string    `json:"id"`
	GoldenQuestionID string    `json:"goldenQuestionId"`
	Status           string    `json:"status"`
	FailureStage     string    `json:"failureStage,omitempty"`
	FailureCode      string    `json:"failureCode,omitempty"`
	QueryPlan        QueryPlan `json:"queryPlan"`
	CreatedAt        string    `json:"createdAt"`
}

type MaterializationRecommendation struct {
	DatasetID          string `json:"datasetId"`
	DatasetVersionID   string `json:"datasetVersionId"`
	DatasetCode        string `json:"datasetCode"`
	DatasetName        string `json:"datasetName"`
	QueryPlanHits      int64  `json:"queryPlanHits"`
	DistinctQuestions  int64  `json:"distinctQuestions"`
	AverageDurationMS  int64  `json:"averageDurationMs"`
	MaximumDurationMS  int64  `json:"maximumDurationMs"`
	ActiveMaterialized bool   `json:"activeMaterialized"`
	Recommendation     string `json:"recommendation"`
	ReasonCode         string `json:"reasonCode"`
}

type GraphStatus struct {
	Status                string `json:"status"`
	CurrentGenerationID   string `json:"currentGenerationId,omitempty"`
	CurrentGeneration     int64  `json:"currentGeneration,omitempty"`
	RequestedEventVersion int64  `json:"requestedEventVersion"`
	AppliedEventVersion   int64  `json:"appliedEventVersion"`
	NodeCount             int    `json:"nodeCount"`
	EdgeCount             int    `json:"edgeCount"`
	ErrorCode             string `json:"errorCode,omitempty"`
	UpdatedAt             string `json:"updatedAt"`
}

type graphClaim struct {
	TenantID              string
	RequestedEventVersion int64
	LeaseToken            string
	Attempt               int
	MaxAttempts           int
}

type graphGeneration struct {
	ID         string
	Generation int64
	NodeCount  int
	EdgeCount  int
}
