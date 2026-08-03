package semanticqa

import (
	"encoding/json"
	"errors"

	"intelligent-report-generation-system/internal/dataset"
)

const maxSemanticMemberSetSize = 128

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
	MetricCandidateCount  int                              `json:"-"`
	MetricMatchMethod     string                           `json:"-"`
	Domain                string                           `json:"-"`
	DimensionValueLookups []QueryDimensionValueLookupTrace `json:"-"`
	// DimensionResolutionComplete is set only by the server-side conversational
	// interpreter after every current-turn value has been resolved to governed
	// dimension members. It prevents PlanQuery from repeating an unconstrained
	// exact lookup and reintroducing an ambiguity already resolved with field
	// metadata and the persisted decision graph.
	DimensionResolutionComplete bool `json:"-"`
}

// QueryTurnInput is the conversation-level contract used by the assistant.
// A turn may resolve to several independently governed QueryPlans. Keeping the
// executable leaf as QueryPlan preserves the existing permission, freshness,
// compatibility and lineage gates for every requested metric.
type QueryTurnInput struct {
	Question string `json:"question"`
	// Timezone is used to turn relative time words found during Jieba semantic
	// enrichment into explicit query boundaries before planning.
	Timezone string `json:"timezone,omitempty"`
	// PriorQuestions contains at most the two immediately preceding user
	// questions. Together with Question this is the transient three-turn
	// window used to explain conversational resolution. Raw questions are
	// never persisted in a QueryPlan.
	PriorQuestions       []string                 `json:"priorQuestions,omitempty"`
	ContextQueryPlanIDs  []string                 `json:"contextQueryPlanIds,omitempty"`
	MaximumPathHops      int                      `json:"maximumPathHops,omitempty"`
	ConfirmedMetricCodes []string                 `json:"confirmedMetricCodes,omitempty"`
	ConfirmedDecisions   []QueryConfirmedDecision `json:"confirmedDecisions,omitempty"`
	// SemanticHints are untrusted names and values produced by the preceding
	// bounded LLM candidate selector. The server resolves them again against
	// published metric/dimension catalogs and persisted decisions; callers
	// cannot supply fields, tables, predicates or SQL.
	SemanticHints QuerySemanticHints `json:"semanticHints,omitempty"`
}

type QueryConfirmedDecision struct {
	MetricCode string `json:"metricCode"`
	DecisionID string `json:"decisionId"`
}

type QuerySemanticHints struct {
	Intent          string                       `json:"intent,omitempty"`
	MetricNames     []string                     `json:"metricNames,omitempty"`
	DimensionValues []QuerySemanticDimensionHint `json:"dimensionValues,omitempty"`
}

type QuerySemanticDimensionHint struct {
	SourceToken   string          `json:"sourceToken,omitempty"`
	Value         string          `json:"value"`
	DimensionName string          `json:"dimensionName"`
	DimensionCode string          `json:"dimensionCode"`
	DimensionType string          `json:"dimensionType,omitempty"`
	ValueType     string          `json:"valueType,omitempty"`
	TimeRange     *QueryTimeRange `json:"timeRange,omitempty"`
}

// QueryTokenizeInput is deliberately independent from query planning. It lets
// callers inspect deterministic segmentation even when no governed metric path
// can be proven for the question.
type QueryTokenizeInput struct {
	Question string `json:"question"`
	Timezone string `json:"timezone,omitempty"`
}

type QueryTokenization struct {
	QuestionHash          string                        `json:"questionHash"`
	Strategy              string                        `json:"strategy"`
	Tokens                []QueryToken                  `json:"tokens"`
	EntityCount           int                           `json:"entityCount"`
	DictionaryEntityCount int                           `json:"dictionaryEntityCount"`
	IndexPrerequisites    []QuerySemanticIndexStatus    `json:"indexPrerequisites"`
	QuestionEmbedding     QueryEmbeddingTrace           `json:"questionEmbedding"`
	QuestionMetricTop5    []QueryTokenSemanticCandidate `json:"questionMetricTop5"`
	SemanticRetrievalMode string                        `json:"semanticRetrievalMode"`
	SemanticRetrievals    []QueryTokenSemanticRetrieval `json:"semanticRetrievals"`
	LLMCompletion         QueryTokenLLMCompletion       `json:"llmCompletion"`
}

type QuerySemanticIndexStatus struct {
	IndexType  string `json:"indexType"`
	KeyShape   string `json:"keyShape"`
	ValueShape string `json:"valueShape"`
	Total      int64  `json:"total"`
	Ready      int64  `json:"ready"`
	Pending    int64  `json:"pending"`
	Status     string `json:"status"`
	Model      string `json:"model,omitempty"`
}

type QueryEmbeddingTrace struct {
	Status     string `json:"status"`
	Model      string `json:"model,omitempty"`
	Dimensions int    `json:"dimensions,omitempty"`
}

// QueryToken offsets are Unicode code-point offsets [Start, End), rather than
// UTF-8 byte offsets, so the browser can highlight Chinese text reliably.
type QueryToken struct {
	Text         string  `json:"text"`
	Normalized   string  `json:"normalized"`
	PartOfSpeech string  `json:"partOfSpeech,omitempty"`
	EntityType   string  `json:"entityType"`
	EntityName   string  `json:"entityName,omitempty"`
	EntityCode   string  `json:"entityCode,omitempty"`
	Start        int     `json:"start"`
	End          int     `json:"end"`
	Source       string  `json:"source"`
	Confidence   float64 `json:"confidence"`
}

type QueryTokenSemanticRetrieval struct {
	Token               string                        `json:"token"`
	PartOfSpeech        string                        `json:"partOfSpeech,omitempty"`
	EntityType          string                        `json:"entityType"`
	Start               int                           `json:"start"`
	End                 int                           `json:"end"`
	RetrievalStatus     string                        `json:"retrievalStatus"`
	MetricCandidates    []QueryTokenSemanticCandidate `json:"metricCandidates"`
	DimensionCandidates []QueryTokenSemanticCandidate `json:"dimensionCandidates"`
}

type QueryTokenSemanticCandidate struct {
	SemanticType  string  `json:"semanticType"`
	Name          string  `json:"name"`
	Code          string  `json:"code"`
	Description   string  `json:"description,omitempty"`
	DimensionName string  `json:"dimensionName,omitempty"`
	DimensionCode string  `json:"dimensionCode,omitempty"`
	DimensionType string  `json:"dimensionType,omitempty"`
	ValueType     string  `json:"valueType,omitempty"`
	FieldID       string  `json:"fieldId,omitempty"`
	Value         string  `json:"value,omitempty"`
	Geographic    bool    `json:"geographic,omitempty"`
	Score         float64 `json:"score"`
	MatchMethod   string  `json:"matchMethod"`
}

type QueryTokenLLMCompletion struct {
	Status            string                   `json:"status"`
	Model             string                   `json:"model,omitempty"`
	Intent            string                   `json:"intent"`
	AugmentedQuestion string                   `json:"augmentedQuestion"`
	MetricNames       []string                 `json:"metricNames"`
	DimensionValues   []QueryLLMDimensionValue `json:"dimensionValues"`
	ReferenceTime     string                   `json:"referenceTime,omitempty"`
	Timezone          string                   `json:"timezone,omitempty"`
	Confidence        float64                  `json:"confidence"`
	ErrorCode         string                   `json:"errorCode,omitempty"`
}

type QueryLLMDimensionValue struct {
	SourceToken   string          `json:"sourceToken"`
	Value         string          `json:"value"`
	DimensionName string          `json:"dimensionName"`
	DimensionCode string          `json:"dimensionCode"`
	DimensionType string          `json:"dimensionType,omitempty"`
	ValueType     string          `json:"valueType,omitempty"`
	FieldID       string          `json:"fieldId,omitempty"`
	TimeRange     *QueryTimeRange `json:"timeRange,omitempty"`
	Confidence    float64         `json:"confidence"`
}

type QueryTurnPlan struct {
	QuestionRunID       string               `json:"questionRunId"`
	State               QuestionState        `json:"state"`
	Lifecycle           []QuestionStateEvent `json:"lifecycle"`
	QuestionHash        string               `json:"questionHash"`
	Status              string               `json:"status"`
	Intent              string               `json:"intent"`
	MetricCodes         []string             `json:"metricCodes"`
	ContextQueryPlanIDs []string             `json:"contextQueryPlanIds"`
	ContextInherited    bool                 `json:"contextInherited"`
	Tokenization        *QueryTokenization   `json:"tokenization,omitempty"`
	Clarification       *QueryClarification  `json:"clarification,omitempty"`
	Plans               []QueryPlan          `json:"plans"`
	Trace               QueryTurnTrace       `json:"trace"`
}

type QueryClarification struct {
	Type                string                          `json:"type"`
	Message             string                          `json:"message"`
	MetricCandidates    []QueryMetricCandidateTrace     `json:"metricCandidates,omitempty"`
	DimensionCandidates []QueryDimensionCandidateChoice `json:"dimensionCandidates,omitempty"`
}

type QueryDimensionCandidateChoice struct {
	MetricCode     string `json:"metricCode"`
	Term           string `json:"term"`
	DecisionID     string `json:"decisionId"`
	DimensionCode  string `json:"dimensionCode"`
	DimensionName  string `json:"dimensionName"`
	CanonicalValue string `json:"canonicalValue"`
	TableSchema    string `json:"tableSchema"`
	TableName      string `json:"tableName"`
}

// QueryTurnTrace is a transient, reader-facing audit trail. It is assembled
// from the same governed candidates and selected QueryPlans that drive
// execution; the frontend must not reconstruct these decisions from labels.
type QueryTurnTrace struct {
	ConversationQuestions []string                         `json:"conversationQuestions"`
	ContextPolicy         string                           `json:"contextPolicy"`
	StandaloneQuestion    string                           `json:"standaloneQuestion"`
	MetricToolLoop        *QueryMetricToolLoopTrace        `json:"metricToolLoop,omitempty"`
	DimensionToolLoops    []QueryDimensionToolLoopTrace    `json:"dimensionToolLoops,omitempty"`
	Extraction            QueryTurnExtraction              `json:"extraction"`
	MetricCandidates      []QueryMetricCandidateTrace      `json:"metricCandidates"`
	DimensionValueLookups []QueryDimensionValueLookupTrace `json:"dimensionValueLookups"`
	FinalSelections       []QueryFinalSelectionTrace       `json:"finalSelections"`
	Assessments           []QueryTraceAssessment           `json:"assessments"`
}

type QueryMetricToolLoopTrace struct {
	AuditRequestID string                    `json:"auditRequestId"`
	Model          string                    `json:"model"`
	Rounds         int                       `json:"rounds"`
	ToolCalls      int                       `json:"toolCalls"`
	Steps          []QueryMetricToolLoopStep `json:"steps"`
}

type QueryMetricToolLoopStep struct {
	Round            int      `json:"round"`
	ToolName         string   `json:"toolName"`
	ArgumentsHash    string   `json:"argumentsHash"`
	StateHash        string   `json:"stateHash"`
	EvidenceIDs      []string `json:"evidenceIds"`
	NewEvidenceCount int      `json:"newEvidenceCount"`
	ErrorCode        string   `json:"errorCode,omitempty"`
	Terminal         bool     `json:"terminal"`
}

type QueryDimensionToolLoopTrace struct {
	MetricCode     string                    `json:"metricCode"`
	AuditRequestID string                    `json:"auditRequestId"`
	Model          string                    `json:"model"`
	Rounds         int                       `json:"rounds"`
	ToolCalls      int                       `json:"toolCalls"`
	Steps          []QueryMetricToolLoopStep `json:"steps"`
}

type QueryTurnExtraction struct {
	Intent              string   `json:"intent"`
	MetricTerms         []string `json:"metricTerms"`
	DimensionValueTerms []string `json:"dimensionValueTerms"`
}

type QueryMetricCandidateTrace struct {
	Code             string  `json:"code"`
	Label            string  `json:"label"`
	Domain           string  `json:"domain,omitempty"`
	DatasetVersionID string  `json:"datasetVersionId,omitempty"`
	TableSchema      string  `json:"tableSchema,omitempty"`
	TableName        string  `json:"tableName,omitempty"`
	MatchedTerm      string  `json:"matchedTerm,omitempty"`
	MatchMethod      string  `json:"matchMethod"`
	Score            float64 `json:"score"`
	Selected         bool    `json:"selected"`
	Source           string  `json:"source"`
}

type QueryDimensionValueLookupTrace struct {
	Term                      string                    `json:"term"`
	CanonicalValue            string                    `json:"canonicalValue,omitempty"`
	AliasValues               []string                  `json:"aliasValues,omitempty"`
	MetricCode                string                    `json:"metricCode"`
	MetricName                string                    `json:"metricName,omitempty"`
	MetricFieldID             string                    `json:"metricFieldId"`
	MetricVersionID           string                    `json:"metricVersionId,omitempty"`
	DatasetVersionID          string                    `json:"datasetVersionId,omitempty"`
	MaterializationID         string                    `json:"materializationId,omitempty"`
	TableSchema               string                    `json:"tableSchema,omitempty"`
	TableName                 string                    `json:"tableName,omitempty"`
	DecisionID                string                    `json:"decisionId,omitempty"`
	DimensionID               string                    `json:"dimensionId,omitempty"`
	DimensionCode             string                    `json:"dimensionCode"`
	DimensionName             string                    `json:"dimensionName"`
	DimensionFieldID          string                    `json:"dimensionFieldId"`
	DimensionFieldName        string                    `json:"dimensionFieldName"`
	DimensionFieldDescription string                    `json:"dimensionFieldDescription"`
	VectorQuery               string                    `json:"vectorQuery"`
	VectorModel               string                    `json:"vectorModel,omitempty"`
	VectorDimensions          int                       `json:"vectorDimensions,omitempty"`
	VectorEmbedding           []float32                 `json:"-"`
	VectorSearchStatus        string                    `json:"vectorSearchStatus"`
	VectorCandidateCount      int                       `json:"vectorCandidateCount"`
	VectorCandidateMemberKeys []string                  `json:"vectorCandidateMemberKeys,omitempty"`
	VectorTopScore            float64                   `json:"vectorTopScore,omitempty"`
	DecisionCandidates        []QueryDecisionCandidate  `json:"decisionCandidates,omitempty"`
	WhereDesignStatus         string                    `json:"whereDesignStatus"`
	WhereDesignOperator       string                    `json:"whereDesignOperator,omitempty"`
	WhereDesignReason         string                    `json:"whereDesignReason,omitempty"`
	WhereDesignModel          string                    `json:"whereDesignModel,omitempty"`
	MatchMethod               string                    `json:"matchMethod"`
	CandidateCount            int                       `json:"candidateCount"`
	CandidateMemberKeys       []string                  `json:"candidateMemberKeys,omitempty"`
	SelectedMemberKeys        []string                  `json:"selectedMemberKeys,omitempty"`
	WhereCondition            string                    `json:"whereCondition"`
	CompiledCondition         string                    `json:"compiledCondition"`
	CandidateFilter           QueryCandidateFilterTrace `json:"candidateFilter"`
	Selected                  bool                      `json:"selected"`
	Source                    string                    `json:"source"`
	Sensitive                 bool                      `json:"sensitive"`
}

type QueryDecisionCandidate struct {
	DecisionID        string  `json:"decisionId"`
	CanonicalValue    string  `json:"canonicalValue"`
	MemberValue       string  `json:"memberValue,omitempty"`
	MetricCode        string  `json:"metricCode"`
	MetricName        string  `json:"metricName"`
	TableSchema       string  `json:"tableSchema"`
	TableName         string  `json:"tableName"`
	WhereCondition    string  `json:"whereCondition"`
	CompiledCondition string  `json:"compiledCondition"`
	PredicateOperator string  `json:"predicateOperator"`
	Score             float64 `json:"score"`
	Selected          bool    `json:"selected"`
}

type QueryCandidateFilterTrace struct {
	InputCount    int      `json:"inputCount"`
	AcceptedCount int      `json:"acceptedCount"`
	RejectedCount int      `json:"rejectedCount"`
	Status        string   `json:"status"`
	Rules         []string `json:"rules"`
}

type QueryFinalSelectionTrace struct {
	MetricCode        string                     `json:"metricCode"`
	MetricName        string                     `json:"metricName"`
	MetricFieldID     string                     `json:"metricFieldId"`
	MetricVersionID   string                     `json:"metricVersionId"`
	DatasetVersionID  string                     `json:"datasetVersionId"`
	Dimensions        []QueryFinalDimensionTrace `json:"dimensions"`
	TimeRange         *QueryTimeRange            `json:"timeRange,omitempty"`
	WhereCondition    string                     `json:"whereCondition"`
	CompiledCondition string                     `json:"compiledCondition"`
	PlanID            string                     `json:"planId"`
	PlanStatus        string                     `json:"planStatus"`
}

type QueryFinalDimensionTrace struct {
	DimensionCode string   `json:"dimensionCode"`
	DimensionName string   `json:"dimensionName"`
	MemberKeys    []string `json:"memberKeys"`
}

type QueryTraceAssessment struct {
	Step     string `json:"step"`
	Status   string `json:"status"`
	Decision string `json:"decision"`
	Detail   string `json:"detail"`
}

type QueryMemberFilterInput struct {
	DimensionCode string `json:"dimensionCode"`
	MemberValue   string `json:"memberValue,omitempty"`
	// MemberValues is a governed semantic set. It is compiled as one IN
	// predicate, never as several EQUALS predicates joined with AND.
	MemberValues []string `json:"memberValues,omitempty"`
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
	DimensionID string   `json:"dimensionId"`
	FieldID     string   `json:"fieldId"`
	MemberKey   string   `json:"memberKey,omitempty"`
	MemberKeys  []string `json:"memberKeys,omitempty"`
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
	DimensionCode string   `json:"dimensionCode"`
	DimensionID   string   `json:"dimensionId"`
	MemberKey     string   `json:"memberKey,omitempty"`
	MemberKeys    []string `json:"memberKeys,omitempty"`
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
	MetricFieldID             string                 `json:"metricFieldId,omitempty"`
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
	// PlanningTrace is response-only and intentionally absent from the
	// persisted normalized request. It exposes the actual dimension/member
	// candidate set considered while this plan was created.
	PlanningTrace []QueryDimensionValueLookupTrace `json:"planningTrace,omitempty"`
	CreatedAt     string                           `json:"createdAt"`
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
	SemanticVersion       string          `json:"semanticVersion"`
	PathHash              string          `json:"pathHash"`
	QueryPlanHash         string          `json:"queryPlanHash"`
	ResultHash            string          `json:"resultHash"`
	QueryTraceID          string          `json:"queryTraceId"`
	VerifiedAt            string          `json:"verifiedAt"`
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
	ValidatorChecks       []string        `json:"validatorChecks"`
}

type QueryPlanExecution struct {
	QuestionRunID string                    `json:"questionRunId"`
	State         QuestionState             `json:"state"`
	Lifecycle     []QuestionStateEvent      `json:"lifecycle"`
	QueryPlan     QueryPlan                 `json:"queryPlan"`
	Result        dataset.PreviewResult     `json:"result"`
	Evidence      AnswerEvidence            `json:"evidence"`
	Comparison    *QueryComparisonExecution `json:"comparison,omitempty"`
}

type SubmitQueryFeedbackInput struct {
	Rating  string `json:"rating"`
	Comment string `json:"comment,omitempty"`
}

type QueryFeedback struct {
	ID          string `json:"id"`
	QueryPlanID string `json:"queryPlanId"`
	Rating      string `json:"rating"`
	Comment     string `json:"comment,omitempty"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
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
