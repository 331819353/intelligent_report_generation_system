package toolhost

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/ircontract"
)

type ToolOutput[T any] struct {
	Result       T
	EvidenceRefs []askdata.EvidenceRef
	MadeProgress bool
	// QueryScanBytes is populated only by governed warehouse query handlers.
	// It never comes from a model or a client-supplied tool argument.
	QueryScanBytes int64
}

type SearchSemanticObjectsInput struct {
	Mention     string
	ObjectTypes []ObjectType
	DomainIDs   []askdata.ID
	Limit       int
}

type GetSemanticContractsInput struct{ ObjectVersionIDs []askdata.ID }

type LookupDimensionValuesInput struct {
	Mention            string
	DimensionVersionID askdata.ID
	Limit              int
}

type GetCertifiedExamplesInput struct {
	QuestionSummary string
	DomainIDs       []askdata.ID
	Limit           int
}

type SemanticBundleInput struct {
	ModelVersionIDs     []askdata.ID
	MetricVersionIDs    []askdata.ID
	DimensionVersionIDs []askdata.ID
	MemberVersionIDs    []askdata.ID
}

type DataQualityInput struct {
	ModelVersionIDs  []askdata.ID
	MetricVersionIDs []askdata.ID
	TimeRange        ircontract.TimeRange
}

type CompileSemanticQueryInput struct{ SemanticIR ircontract.SemanticIR }
type ValidateQueryPlanInput struct{ PlanHash askdata.ContentHash }

type ProbeJoinCardinalityInput struct {
	GraphPlanHash askdata.ContentHash
	TimeRange     ircontract.TimeRange
}

type ExecuteQueryPlanInput struct {
	PlanHash askdata.ContentHash
	MaxRows  int
}

type ExecuteValidationQueryInput struct {
	PlanHash       askdata.ContentHash
	ValidationType ValidationType
}

type CompareCandidateResultsInput struct {
	LeftPlanHash  askdata.ContentHash
	RightPlanHash askdata.ContentHash
	MaxRows       int
}

type RequestClarificationInput struct {
	ConflictCode string
	Question     string
	Options      []ClarificationOption
}

type Handlers struct {
	SearchSemanticObjects   func(context.Context, AuthorizationContext, SearchSemanticObjectsInput) (ToolOutput[SearchSemanticObjectsResult], error)
	GetSemanticContracts    func(context.Context, AuthorizationContext, GetSemanticContractsInput) (ToolOutput[GetSemanticContractsResult], error)
	LookupDimensionValues   func(context.Context, AuthorizationContext, LookupDimensionValuesInput) (ToolOutput[LookupDimensionValuesResult], error)
	GetCertifiedExamples    func(context.Context, AuthorizationContext, GetCertifiedExamplesInput) (ToolOutput[GetCertifiedExamplesResult], error)
	ResolveGraphPlan        func(context.Context, AuthorizationContext, SemanticBundleInput) (ToolOutput[ResolveGraphPlanResult], error)
	ValidateSemanticBundle  func(context.Context, AuthorizationContext, SemanticBundleInput) (ToolOutput[ValidateSemanticBundleResult], error)
	GetDataQualityStatus    func(context.Context, AuthorizationContext, DataQualityInput) (ToolOutput[GetDataQualityStatusResult], error)
	CompileSemanticQuery    func(context.Context, AuthorizationContext, CompileSemanticQueryInput) (ToolOutput[CompileSemanticQueryResult], error)
	ValidateQueryPlan       func(context.Context, AuthorizationContext, ValidateQueryPlanInput) (ToolOutput[ValidateQueryPlanResult], error)
	ProbeJoinCardinality    func(context.Context, AuthorizationContext, ProbeJoinCardinalityInput) (ToolOutput[ProbeJoinCardinalityResult], error)
	ExecuteQueryPlan        func(context.Context, AuthorizationContext, ExecuteQueryPlanInput) (ToolOutput[ExecuteQueryPlanResult], error)
	ExecuteValidationQuery  func(context.Context, AuthorizationContext, ExecuteValidationQueryInput) (ToolOutput[ExecuteValidationQueryResult], error)
	CompareCandidateResults func(context.Context, AuthorizationContext, CompareCandidateResultsInput) (ToolOutput[CompareCandidateResultsResult], error)
	RequestClarification    func(context.Context, AuthorizationContext, RequestClarificationInput) (ToolOutput[RequestClarificationResult], error)
}

type CandidateSummary struct {
	ObjectType      ObjectType           `json:"objectType"`
	ObjectVersionID askdata.ID           `json:"objectVersionId"`
	Score           float64              `json:"score"`
	MatchType       string               `json:"matchType"`
	Status          string               `json:"status"`
	ReportSource    *ReportSourceSummary `json:"reportSource,omitempty"`
}

// ReportSourceSummary is the immutable, permission-checked identity of a
// certified report component. It contains no report prose or historical query;
// the report is a presentation prior and a verifiable source link only.
type ReportSourceSummary struct {
	ReportID          askdata.ID          `json:"reportId"`
	ReportVersionID   askdata.ID          `json:"reportVersionId"`
	ComponentID       askdata.ID          `json:"componentId"`
	ReportTitle       string              `json:"reportTitle"`
	ComponentTitle    string              `json:"componentTitle,omitempty"`
	ComponentType     string              `json:"componentType"`
	ComponentVersion  string              `json:"componentVersion"`
	SemanticReleaseID askdata.ID          `json:"semanticReleaseId"`
	ComponentHash     askdata.ContentHash `json:"componentHash"`
}

type SearchSemanticObjectsResult struct {
	Candidates  []CandidateSummary `json:"candidates"`
	Truncated   bool               `json:"truncated"`
	EvidenceIDs []askdata.ID       `json:"evidenceIds"`
}

type FormulaSummary struct {
	FormulaHash          askdata.ContentHash `json:"formulaHash"`
	OperatorCodes        []string            `json:"operatorCodes"`
	ReferencedVersionIDs []askdata.ID        `json:"referencedVersionIds"`
}

type SemanticContractSummary struct {
	ObjectType      ObjectType          `json:"objectType"`
	ObjectVersionID askdata.ID          `json:"objectVersionId"`
	Name            string              `json:"name"`
	Definition      string              `json:"definition"`
	Unit            string              `json:"unit,omitempty"`
	OwnerID         askdata.ID          `json:"ownerId"`
	Status          string              `json:"status"`
	Grain           string              `json:"grain,omitempty"`
	ContentHash     askdata.ContentHash `json:"contentHash"`
	Formula         *FormulaSummary     `json:"formula,omitempty"`
}

type GetSemanticContractsResult struct {
	Contracts   []SemanticContractSummary `json:"contracts"`
	EvidenceIDs []askdata.ID              `json:"evidenceIds"`
}

type DimensionValueSummary struct {
	MemberVersionID askdata.ID   `json:"memberVersionId"`
	DisplayLabel    string       `json:"displayLabel,omitempty"`
	Aliases         []string     `json:"aliases"`
	HierarchyPath   []askdata.ID `json:"hierarchyPath"`
	Sensitive       bool         `json:"sensitive"`
}

type LookupDimensionValuesResult struct {
	DimensionVersionID askdata.ID              `json:"dimensionVersionId"`
	Members            []DimensionValueSummary `json:"members"`
	Truncated          bool                    `json:"truncated"`
	EvidenceIDs        []askdata.ID            `json:"evidenceIds"`
}

// CertifiedExampleSummary is the governed binding signature of a certified
// question. The source question text is deliberately omitted: replay/audit
// artifacts must never persist another user's raw or summarized question.
//
// It deliberately carries expected *components* rather than a serialised
// Semantic IR. askdata.certified_example_versions stores exactly these columns
// and no IR, so an IR-shaped contract could only ever be satisfied by
// synthesising one at read time. Carrying components is also the safer
// contract: an example is a retrieval prior, and the binder must re-resolve it
// against the release currently pinned to the run rather than replay a frozen
// plan that may reference superseded object versions.
type CertifiedExampleSummary struct {
	ExampleID                askdata.ID          `json:"exampleId"`
	ExpectedMetricVersionIDs []askdata.ID        `json:"expectedMetricVersionIds"`
	ExpectedDimensionIDs     []askdata.ID        `json:"expectedDimensionVersionIds"`
	ExpectedTimeExpression   string              `json:"expectedTimeExpression,omitempty"`
	ContentHash              askdata.ContentHash `json:"contentHash"`
	SimilarityPermillion     int                 `json:"similarityPermillion"`
}

type GetCertifiedExamplesResult struct {
	Examples    []CertifiedExampleSummary `json:"examples"`
	EvidenceIDs []askdata.ID              `json:"evidenceIds"`
}

type GraphRisk struct {
	Code     string `json:"code"`
	Blocking bool   `json:"blocking"`
}

type ResolveGraphPlanResult struct {
	GraphPlanHash   askdata.ContentHash `json:"graphPlanHash"`
	ModelVersionIDs []askdata.ID        `json:"modelVersionIds"`
	RelationshipIDs []askdata.ID        `json:"relationshipIds"`
	Risks           []GraphRisk         `json:"risks"`
	FallbackUsed    bool                `json:"fallbackUsed"`
	GraphDegraded   bool                `json:"graphDegraded"`
	EvidenceIDs     []askdata.ID        `json:"evidenceIds"`
}

type BundleConflict struct {
	Code     string `json:"code"`
	Blocking bool   `json:"blocking"`
}

type ValidateSemanticBundleResult struct {
	Valid                   bool             `json:"valid"`
	MissingObjectVersionIDs []askdata.ID     `json:"missingObjectVersionIds"`
	Conflicts               []BundleConflict `json:"conflicts"`
	ConfidencePermillion    int              `json:"confidencePermillion"`
	EvidenceIDs             []askdata.ID     `json:"evidenceIds"`
}

type QualityRuleSummary struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Passed   bool   `json:"passed"`
}

type GetDataQualityStatusResult struct {
	Status        string               `json:"status"`
	DataAsOf      string               `json:"dataAsOf"`
	CoverageStart string               `json:"coverageStart"`
	CoverageEnd   string               `json:"coverageEnd"`
	Rules         []QualityRuleSummary `json:"rules"`
	EvidenceIDs   []askdata.ID         `json:"evidenceIds"`
}

type ParameterShapeSummary struct {
	Code        string `json:"code"`
	DataType    string `json:"dataType"`
	MultiValue  bool   `json:"multiValue"`
	Required    bool   `json:"required"`
	Cardinality int    `json:"cardinality"`
}

type CompileSemanticQueryResult struct {
	PlanHash askdata.ContentHash `json:"planHash"`
	// SemanticIRHash is the compiler's canonical hash of the IR it accepted,
	// not of the IR the model submitted. The orchestrator records it as the
	// run's governed SemanticIR link, so it has to come from the component that
	// normalized and validated the IR — recomputing it anywhere else would let
	// a run be certified against an IR the compiler never saw.
	SemanticIRHash  askdata.ContentHash     `json:"semanticIrHash"`
	PlanCount       int                     `json:"planCount"`
	ParameterShapes []ParameterShapeSummary `json:"parameterShapes"`
	MaxRows         int                     `json:"maxRows"`
	EvidenceIDs     []askdata.ID            `json:"evidenceIds"`
}

type PlanRiskSummary struct {
	Code     string `json:"code"`
	Count    int    `json:"count"`
	Blocking bool   `json:"blocking"`
}

type ValidateQueryPlanResult struct {
	Allowed        bool                `json:"allowed"`
	ValidationHash askdata.ContentHash `json:"validationHash"`
	MaxCost        float64             `json:"maxCost"`
	MaxPlanRows    int64               `json:"maxPlanRows"`
	Risks          []PlanRiskSummary   `json:"risks"`
	EvidenceIDs    []askdata.ID        `json:"evidenceIds"`
}

type ProbeJoinCardinalityResult struct {
	LeftCount        int64        `json:"leftCount"`
	RightCount       int64        `json:"rightCount"`
	JoinedCount      int64        `json:"joinedCount"`
	FanoutPermillion int64        `json:"fanoutPermillion"`
	Safe             bool         `json:"safe"`
	EvidenceIDs      []askdata.ID `json:"evidenceIds"`
}

type ResultColumnSummary struct {
	Code          string `json:"code"`
	CanonicalType string `json:"canonicalType"`
	NullCount     int    `json:"nullCount"`
	DistinctCount int    `json:"distinctCount"`
}

type ResultMetricSummary struct {
	Code         string `json:"code"`
	NonNullCount int    `json:"nonNullCount"`
	NullCount    int    `json:"nullCount"`
	Minimum      string `json:"minimum,omitempty"`
	Maximum      string `json:"maximum,omitempty"`
	Sum          string `json:"sum,omitempty"`
}

type ExecuteQueryPlanResult struct {
	ResultHash       askdata.ContentHash   `json:"resultHash"`
	VerificationHash askdata.ContentHash   `json:"verificationHash"`
	Verdict          string                `json:"verdict"`
	NoDataConfirmed  bool                  `json:"noDataConfirmed"`
	RowCount         int                   `json:"rowCount"`
	Columns          []ResultColumnSummary `json:"columns"`
	Metrics          []ResultMetricSummary `json:"metrics"`
	EvidenceIDs      []askdata.ID          `json:"evidenceIds"`
}

type ExecuteValidationQueryResult struct {
	ValidationType ValidationType      `json:"validationType"`
	Count          int64               `json:"count"`
	DistinctCount  int64               `json:"distinctCount"`
	Covered        bool                `json:"covered"`
	SummaryHash    askdata.ContentHash `json:"summaryHash"`
	EvidenceIDs    []askdata.ID        `json:"evidenceIds"`
}

type MetricDifferenceSummary struct {
	Code                     string `json:"code"`
	Direction                string `json:"direction"`
	RelativeChangePermillion int64  `json:"relativeChangePermillion"`
}

type CompareCandidateResultsResult struct {
	LeftResultHash  askdata.ContentHash       `json:"leftResultHash"`
	RightResultHash askdata.ContentHash       `json:"rightResultHash"`
	Equivalent      bool                      `json:"equivalent"`
	DifferenceCount int                       `json:"differenceCount"`
	Differences     []MetricDifferenceSummary `json:"differences"`
	EvidenceIDs     []askdata.ID              `json:"evidenceIds"`
}

type RequestClarificationResult struct {
	ConflictCode string `json:"conflictCode"`
	// ClarificationCopy is assistant-authored display text. The JSON key avoids
	// the reserved audit vocabulary used for retained raw user questions.
	Question    string                `json:"clarificationCopy"`
	Options     []ClarificationOption `json:"options"`
	EvidenceIDs []askdata.ID          `json:"evidenceIds"`
}

func (result SearchSemanticObjectsResult) ValidateResult(known map[askdata.ID]askdata.EvidenceRef) error {
	if len(result.Candidates) > 100 || validateEvidenceIDs(result.EvidenceIDs, known) != nil {
		return ErrInvalidInvocation
	}
	seen := map[askdata.ID]bool{}
	for _, candidate := range result.Candidates {
		if !validObjectType(candidate.ObjectType) || candidate.ObjectVersionID.Validate() != nil ||
			seen[candidate.ObjectVersionID] || !finiteRange(candidate.Score, 0, 1) ||
			!boundedText(candidate.MatchType, 64) || !boundedText(candidate.Status, 64) {
			return ErrInvalidInvocation
		}
		if candidate.ObjectType == ObjectTypeReportAsset {
			if candidate.ReportSource == nil || candidate.ReportSource.Validate() != nil {
				return ErrInvalidInvocation
			}
		} else if candidate.ReportSource != nil {
			return ErrInvalidInvocation
		}
		seen[candidate.ObjectVersionID] = true
	}
	return nil
}

func (result GetSemanticContractsResult) ValidateResult(known map[askdata.ID]askdata.EvidenceRef) error {
	if len(result.Contracts) > MaxArgumentIDs || validateEvidenceIDs(result.EvidenceIDs, known) != nil {
		return fmt.Errorf("%w: semantic contract evidence closure is invalid", ErrInvalidInvocation)
	}
	seen := map[askdata.ID]bool{}
	for index, contract := range result.Contracts {
		if !validObjectType(contract.ObjectType) || contract.ObjectVersionID.Validate() != nil ||
			seen[contract.ObjectVersionID] || !boundedText(contract.Name, 512) ||
			!boundedText(contract.Definition, 4096) || !optionalBoundedText(contract.Unit, 128) ||
			contract.OwnerID.Validate() != nil || !boundedText(contract.Status, 64) ||
			!optionalBoundedText(contract.Grain, 256) || contract.ContentHash.Validate() != nil ||
			validateFormulaSummary(contract.Formula) != nil {
			return fmt.Errorf("%w: semantic contract %d is invalid", ErrInvalidInvocation, index)
		}
		seen[contract.ObjectVersionID] = true
	}
	return nil
}

func (result LookupDimensionValuesResult) ValidateResult(known map[askdata.ID]askdata.EvidenceRef) error {
	if result.DimensionVersionID.Validate() != nil || len(result.Members) > 100 ||
		validateEvidenceIDs(result.EvidenceIDs, known) != nil {
		return ErrInvalidInvocation
	}
	seen := map[askdata.ID]bool{}
	for _, member := range result.Members {
		if member.MemberVersionID.Validate() != nil || seen[member.MemberVersionID] ||
			member.Sensitive && (member.DisplayLabel != "" || len(member.Aliases) != 0) ||
			!member.Sensitive && !boundedText(member.DisplayLabel, 512) || len(member.Aliases) > 16 ||
			validateStrings(member.Aliases, 512) != nil || validateStableIDs(member.HierarchyPath, 32) != nil {
			return ErrInvalidInvocation
		}
		seen[member.MemberVersionID] = true
	}
	return nil
}

func (result GetCertifiedExamplesResult) ValidateResult(known map[askdata.ID]askdata.EvidenceRef) error {
	if len(result.Examples) > 100 || validateEvidenceIDs(result.EvidenceIDs, known) != nil {
		return ErrInvalidInvocation
	}
	seen := map[askdata.ID]bool{}
	for _, example := range result.Examples {
		// Expected objects are bounded and must be canonical stable IDs: an
		// example may not smuggle an unbounded or malformed object set into the
		// binder, and it may not carry any executable plan of its own.
		if example.ExampleID.Validate() != nil || seen[example.ExampleID] ||
			validateStableIDs(example.ExpectedMetricVersionIDs, MaxArgumentIDs) != nil ||
			validateStableIDs(example.ExpectedDimensionIDs, MaxArgumentIDs) != nil ||
			!optionalBoundedText(example.ExpectedTimeExpression, 512) ||
			example.ContentHash.Validate() != nil || example.SimilarityPermillion < 0 ||
			example.SimilarityPermillion > 1_000_000 {
			return ErrInvalidInvocation
		}
		seen[example.ExampleID] = true
	}
	return nil
}

func (result ResolveGraphPlanResult) ValidateResult(known map[askdata.ID]askdata.EvidenceRef) error {
	if result.GraphPlanHash.Validate() != nil || validateStableIDs(result.ModelVersionIDs, MaxArgumentIDs) != nil ||
		len(result.ModelVersionIDs) < 1 || validateStableIDs(result.RelationshipIDs, MaxArgumentIDs) != nil ||
		validateGraphRisks(result.Risks) != nil || validateEvidenceIDs(result.EvidenceIDs, known) != nil {
		return ErrInvalidInvocation
	}
	return nil
}

func (result ValidateSemanticBundleResult) ValidateResult(known map[askdata.ID]askdata.EvidenceRef) error {
	if validateStableIDs(result.MissingObjectVersionIDs, MaxArgumentIDs) != nil || len(result.Conflicts) > 64 ||
		result.ConfidencePermillion < 0 || result.ConfidencePermillion > 1_000_000 ||
		validateEvidenceIDs(result.EvidenceIDs, known) != nil {
		return ErrInvalidInvocation
	}
	seen := map[string]bool{}
	blocking := false
	for _, conflict := range result.Conflicts {
		if !isUpperCode(conflict.Code) || seen[conflict.Code] {
			return ErrInvalidInvocation
		}
		seen[conflict.Code], blocking = true, blocking || conflict.Blocking
	}
	if result.Valid && (len(result.MissingObjectVersionIDs) > 0 || blocking) {
		return ErrInvalidInvocation
	}
	return nil
}

func (result GetDataQualityStatusResult) ValidateResult(known map[askdata.ID]askdata.EvidenceRef) error {
	if (result.Status != "PASS" && result.Status != "WARNING" && result.Status != "FAIL" && result.Status != "UNKNOWN") ||
		!validTime(result.DataAsOf) || !validTimeOrDate(result.CoverageStart) ||
		!validTimeOrDate(result.CoverageEnd) || len(result.Rules) > 64 ||
		validateEvidenceIDs(result.EvidenceIDs, known) != nil {
		return ErrInvalidInvocation
	}
	seen := map[string]bool{}
	for _, rule := range result.Rules {
		if !isUpperCode(rule.Code) || seen[rule.Code] ||
			(rule.Severity != "INFO" && rule.Severity != "WARNING" && rule.Severity != "BLOCKING") {
			return ErrInvalidInvocation
		}
		seen[rule.Code] = true
	}
	return nil
}

func (result CompileSemanticQueryResult) ValidateResult(known map[askdata.ID]askdata.EvidenceRef) error {
	if result.PlanHash.Validate() != nil || result.SemanticIRHash.Validate() != nil ||
		result.PlanCount < 1 || result.PlanCount > 2 ||
		result.MaxRows < 1 || result.MaxRows > ircontract.MaxLimit || len(result.ParameterShapes) > 128 ||
		validateEvidenceIDs(result.EvidenceIDs, known) != nil {
		return ErrInvalidInvocation
	}
	seen := map[string]bool{}
	for _, parameter := range result.ParameterShapes {
		if !boundedText(parameter.Code, 128) || seen[parameter.Code] || !boundedText(parameter.DataType, 64) ||
			parameter.Cardinality < 1 || parameter.Cardinality > MaxArgumentIDs {
			return ErrInvalidInvocation
		}
		seen[parameter.Code] = true
	}
	return nil
}

func (result ValidateQueryPlanResult) ValidateResult(known map[askdata.ID]askdata.EvidenceRef) error {
	if result.ValidationHash.Validate() != nil || !finiteRange(result.MaxCost, 0, 1e12) ||
		result.MaxPlanRows < 0 || len(result.Risks) > 64 || validateEvidenceIDs(result.EvidenceIDs, known) != nil {
		return ErrInvalidInvocation
	}
	seen := map[string]bool{}
	blocking := false
	for _, risk := range result.Risks {
		if !isUpperCode(risk.Code) || seen[risk.Code] || risk.Count < 0 {
			return ErrInvalidInvocation
		}
		seen[risk.Code], blocking = true, blocking || risk.Blocking
	}
	if result.Allowed && blocking {
		return ErrInvalidInvocation
	}
	return nil
}

func (result ProbeJoinCardinalityResult) ValidateResult(known map[askdata.ID]askdata.EvidenceRef) error {
	if result.LeftCount < 0 || result.RightCount < 0 || result.JoinedCount < 0 ||
		result.FanoutPermillion < 0 || result.FanoutPermillion > 1_000_000_000 ||
		validateEvidenceIDs(result.EvidenceIDs, known) != nil {
		return ErrInvalidInvocation
	}
	return nil
}

func (result ExecuteQueryPlanResult) ValidateResult(known map[askdata.ID]askdata.EvidenceRef) error {
	if result.ResultHash.Validate() != nil || result.VerificationHash.Validate() != nil ||
		(result.Verdict != "PASS" && result.Verdict != "RETRY" && result.Verdict != "CLARIFY" && result.Verdict != "BLOCK") ||
		result.RowCount < 0 || result.RowCount > 2*ircontract.MaxLimit || len(result.Columns) < 1 ||
		len(result.Columns) > 1024 || len(result.Metrics) > 1024 ||
		validateEvidenceIDs(result.EvidenceIDs, known) != nil {
		return ErrInvalidInvocation
	}
	seenColumns := map[string]bool{}
	for _, column := range result.Columns {
		if !boundedText(column.Code, 128) || seenColumns[column.Code] || !boundedText(column.CanonicalType, 64) ||
			column.NullCount < 0 || column.NullCount > result.RowCount ||
			column.DistinctCount < 0 || column.DistinctCount > result.RowCount {
			return ErrInvalidInvocation
		}
		seenColumns[column.Code] = true
	}
	seenMetrics := map[string]bool{}
	for _, metric := range result.Metrics {
		if !seenColumns[metric.Code] || seenMetrics[metric.Code] || metric.NonNullCount < 0 || metric.NullCount < 0 ||
			metric.NonNullCount+metric.NullCount != result.RowCount ||
			!validDecimalSummary(metric.Minimum) || !validDecimalSummary(metric.Maximum) || !validDecimalSummary(metric.Sum) {
			return ErrInvalidInvocation
		}
		seenMetrics[metric.Code] = true
	}
	return nil
}

func (result ExecuteValidationQueryResult) ValidateResult(known map[askdata.ID]askdata.EvidenceRef) error {
	if !validValidationType(result.ValidationType) || result.Count < 0 || result.DistinctCount < 0 ||
		result.DistinctCount > result.Count || result.SummaryHash.Validate() != nil ||
		validateEvidenceIDs(result.EvidenceIDs, known) != nil {
		return ErrInvalidInvocation
	}
	return nil
}

func (result CompareCandidateResultsResult) ValidateResult(known map[askdata.ID]askdata.EvidenceRef) error {
	if result.LeftResultHash.Validate() != nil || result.RightResultHash.Validate() != nil ||
		result.DifferenceCount < 0 || result.DifferenceCount < len(result.Differences) || len(result.Differences) > 64 ||
		result.Equivalent && result.DifferenceCount != 0 || validateEvidenceIDs(result.EvidenceIDs, known) != nil {
		return ErrInvalidInvocation
	}
	seen := map[string]bool{}
	for _, difference := range result.Differences {
		if !boundedText(difference.Code, 128) || seen[difference.Code] ||
			(difference.Direction != "INCREASE" && difference.Direction != "DECREASE" && difference.Direction != "CHANGED") ||
			difference.RelativeChangePermillion < 0 || difference.RelativeChangePermillion > 1_000_000_000 {
			return ErrInvalidInvocation
		}
		seen[difference.Code] = true
	}
	return nil
}

func (result RequestClarificationResult) ValidateResult(known map[askdata.ID]askdata.EvidenceRef) error {
	if !isUpperCode(result.ConflictCode) || !boundedText(result.Question, 512) ||
		len(result.Options) < 1 || len(result.Options) > MaxClarificationOptions ||
		validateEvidenceIDs(result.EvidenceIDs, known) != nil {
		return ErrInvalidInvocation
	}
	seen := map[askdata.ID]bool{}
	for _, option := range result.Options {
		if option.OptionID.Validate() != nil || seen[option.OptionID] || !boundedText(option.Label, 256) ||
			len(option.EvidenceRefs) < 1 || len(option.EvidenceRefs) > 16 {
			return ErrInvalidInvocation
		}
		seen[option.OptionID] = true
		for _, evidence := range option.EvidenceRefs {
			if expected, exists := known[evidence.EvidenceID]; !exists || expected != evidence {
				return ErrInvalidInvocation
			}
		}
	}
	return nil
}

type toolRegistration struct {
	definition Definition
	execute    func(context.Context, AuthorizationContext, ToolArguments) (toolExecutionOutput, error)
}

func catalogRegistrations(handlers Handlers) ([]toolRegistration, error) {
	if handlers.SearchSemanticObjects == nil || handlers.GetSemanticContracts == nil ||
		handlers.LookupDimensionValues == nil || handlers.GetCertifiedExamples == nil ||
		handlers.ResolveGraphPlan == nil || handlers.ValidateSemanticBundle == nil ||
		handlers.GetDataQualityStatus == nil || handlers.CompileSemanticQuery == nil ||
		handlers.ValidateQueryPlan == nil || handlers.ProbeJoinCardinality == nil ||
		handlers.ExecuteQueryPlan == nil || handlers.ExecuteValidationQuery == nil ||
		handlers.CompareCandidateResults == nil || handlers.RequestClarification == nil {
		return nil, ErrInvalidRegistry
	}
	result := make([]toolRegistration, 0, len(validTools))
	add := func(
		name ToolName,
		permission Permission,
		charge BudgetCharge,
		timeoutMS, maxResultBytes int,
		resultFields []string,
		execute func(context.Context, AuthorizationContext, ToolArguments) (toolExecutionOutput, error),
	) error {
		argumentSchema, err := argumentSchemaFor(name)
		if err != nil {
			return err
		}
		resultSchema, err := resultSchemaFor(resultFields)
		if err != nil {
			return err
		}
		definition, err := newDefinition(
			name, permission, charge, timeoutMS, maxResultBytes, argumentSchema, resultSchema,
		)
		if err != nil {
			return err
		}
		result = append(result, toolRegistration{definition: definition, execute: execute})
		return nil
	}
	standard := BudgetCharge{ToolCalls: 1}
	validation := BudgetCharge{ToolCalls: 1, ValidationQueries: 1}
	formal := BudgetCharge{ToolCalls: 1, FormalQueries: 1}
	formalPair := BudgetCharge{ToolCalls: 1, FormalQueries: 2}

	if err := add(ToolSearchSemanticObjects, PermissionSemanticRead, standard, 2_000, 128<<10,
		[]string{"candidates", "truncated", "evidenceIds"},
		wrapTypedHandler(handlers.SearchSemanticObjects, func(arguments ToolArguments) SearchSemanticObjectsInput {
			return SearchSemanticObjectsInput{*arguments.Mention, cloneObjectTypes(arguments.ObjectTypes), cloneIDs(arguments.DomainIDs), *arguments.Limit}
		}, nil)); err != nil {
		return nil, err
	}
	if err := add(ToolGetSemanticContracts, PermissionSemanticRead, standard, 2_000, 256<<10,
		[]string{"contracts", "evidenceIds"},
		wrapTypedHandler(handlers.GetSemanticContracts, func(arguments ToolArguments) GetSemanticContractsInput {
			return GetSemanticContractsInput{cloneIDs(arguments.ObjectVersionIDs)}
		}, nil)); err != nil {
		return nil, err
	}
	if err := add(ToolLookupDimensionValues, PermissionDimensionValueRead, standard, 2_000, 128<<10,
		[]string{"dimensionVersionId", "members", "truncated", "evidenceIds"},
		wrapTypedHandler(handlers.LookupDimensionValues, func(arguments ToolArguments) LookupDimensionValuesInput {
			return LookupDimensionValuesInput{*arguments.Mention, *arguments.DimensionVersionID, *arguments.Limit}
		}, sanitizeDimensionValues)); err != nil {
		return nil, err
	}
	if err := add(ToolGetCertifiedExamples, PermissionSemanticRead, standard, 2_000, 256<<10,
		[]string{"examples", "evidenceIds"},
		wrapTypedHandler(handlers.GetCertifiedExamples, func(arguments ToolArguments) GetCertifiedExamplesInput {
			return GetCertifiedExamplesInput{*arguments.QuestionSummary, cloneIDs(arguments.DomainIDs), *arguments.Limit}
		}, nil)); err != nil {
		return nil, err
	}
	bundleInput := func(arguments ToolArguments) SemanticBundleInput {
		return SemanticBundleInput{
			cloneIDs(arguments.ModelVersionIDs), cloneIDs(arguments.MetricVersionIDs),
			cloneIDs(arguments.DimensionVersionIDs), cloneIDs(arguments.MemberVersionIDs),
		}
	}
	if err := add(ToolResolveGraphPlan, PermissionGraphResolve, standard, 3_000, 128<<10,
		[]string{"graphPlanHash", "modelVersionIds", "relationshipIds", "risks", "fallbackUsed", "graphDegraded", "evidenceIds"},
		wrapTypedHandler(handlers.ResolveGraphPlan, bundleInput, nil)); err != nil {
		return nil, err
	}
	if err := add(ToolValidateSemanticBundle, PermissionGraphResolve, standard, 2_000, 128<<10,
		[]string{"valid", "missingObjectVersionIds", "conflicts", "confidencePermillion", "evidenceIds"},
		wrapTypedHandler(handlers.ValidateSemanticBundle, bundleInput, nil)); err != nil {
		return nil, err
	}
	if err := add(ToolGetDataQualityStatus, PermissionQualityRead, standard, 2_000, 128<<10,
		[]string{"status", "dataAsOf", "coverageStart", "coverageEnd", "rules", "evidenceIds"},
		wrapTypedHandler(handlers.GetDataQualityStatus, func(arguments ToolArguments) DataQualityInput {
			return DataQualityInput{cloneIDs(arguments.ModelVersionIDs), cloneIDs(arguments.MetricVersionIDs), *arguments.TimeRange}
		}, nil)); err != nil {
		return nil, err
	}
	if err := add(ToolCompileSemanticQuery, PermissionQueryCompile, standard, 3_000, 128<<10,
		[]string{"planHash", "planCount", "parameterShapes", "maxRows", "evidenceIds"},
		wrapTypedHandler(handlers.CompileSemanticQuery, func(arguments ToolArguments) CompileSemanticQueryInput {
			return CompileSemanticQueryInput{*arguments.SemanticIR}
		}, nil)); err != nil {
		return nil, err
	}
	if err := add(ToolValidateQueryPlan, PermissionQueryValidate, standard, 3_000, 128<<10,
		[]string{"allowed", "validationHash", "maxCost", "maxPlanRows", "risks", "evidenceIds"},
		wrapTypedHandler(handlers.ValidateQueryPlan, func(arguments ToolArguments) ValidateQueryPlanInput {
			return ValidateQueryPlanInput{*arguments.PlanHash}
		}, nil)); err != nil {
		return nil, err
	}
	if err := add(ToolProbeJoinCardinality, PermissionCardinalityProbe, validation, 5_000, 64<<10,
		[]string{"leftCount", "rightCount", "joinedCount", "fanoutPermillion", "safe", "evidenceIds"},
		wrapTypedHandler(handlers.ProbeJoinCardinality, func(arguments ToolArguments) ProbeJoinCardinalityInput {
			return ProbeJoinCardinalityInput{*arguments.GraphPlanHash, *arguments.TimeRange}
		}, nil)); err != nil {
		return nil, err
	}
	if err := add(ToolExecuteQueryPlan, PermissionQueryExecute, formal, 20_000, 256<<10,
		[]string{"resultHash", "verificationHash", "verdict", "noDataConfirmed", "rowCount", "columns", "metrics", "evidenceIds"},
		wrapTypedHandler(handlers.ExecuteQueryPlan, func(arguments ToolArguments) ExecuteQueryPlanInput {
			return ExecuteQueryPlanInput{*arguments.PlanHash, *arguments.MaxRows}
		}, nil)); err != nil {
		return nil, err
	}
	if err := add(ToolExecuteValidationQuery, PermissionValidationQueryExecute, validation, 5_000, 64<<10,
		[]string{"validationType", "count", "distinctCount", "covered", "summaryHash", "evidenceIds"},
		wrapTypedHandler(handlers.ExecuteValidationQuery, func(arguments ToolArguments) ExecuteValidationQueryInput {
			return ExecuteValidationQueryInput{*arguments.PlanHash, *arguments.ValidationType}
		}, nil)); err != nil {
		return nil, err
	}
	if err := add(ToolCompareCandidateResults, PermissionQueryExecute, formalPair, 15_000, 128<<10,
		[]string{"leftResultHash", "rightResultHash", "equivalent", "differenceCount", "differences", "evidenceIds"},
		wrapTypedHandler(handlers.CompareCandidateResults, func(arguments ToolArguments) CompareCandidateResultsInput {
			return CompareCandidateResultsInput{*arguments.LeftPlanHash, *arguments.RightPlanHash, *arguments.MaxRows}
		}, nil)); err != nil {
		return nil, err
	}
	if err := add(ToolRequestClarification, PermissionClarificationRequest, standard, 1_000, 64<<10,
		[]string{"conflictCode", "question", "options", "evidenceIds"},
		wrapTypedHandler(handlers.RequestClarification, func(arguments ToolArguments) RequestClarificationInput {
			return RequestClarificationInput{*arguments.ConflictCode, *arguments.ClarificationQuestion, cloneClarificationOptions(arguments.ClarificationOptions)}
		}, nil)); err != nil {
		return nil, err
	}
	return result, nil
}

func wrapTypedHandler[I any, R resultContract](
	handler func(context.Context, AuthorizationContext, I) (ToolOutput[R], error),
	input func(ToolArguments) I,
	sanitize func(R) R,
) func(context.Context, AuthorizationContext, ToolArguments) (toolExecutionOutput, error) {
	return func(ctx context.Context, authorization AuthorizationContext, arguments ToolArguments) (toolExecutionOutput, error) {
		output, err := handler(ctx, authorization, input(arguments))
		if err != nil {
			return toolExecutionOutput{}, err
		}
		result := output.Result
		if sanitize != nil {
			result = sanitize(result)
		}
		return toolExecutionOutput{
			result: result, evidenceRefs: append([]askdata.EvidenceRef(nil), output.EvidenceRefs...),
			madeProgress: output.MadeProgress, queryScanBytes: output.QueryScanBytes,
		}, nil
	}
}

func sanitizeDimensionValues(result LookupDimensionValuesResult) LookupDimensionValuesResult {
	result.Members = append([]DimensionValueSummary(nil), result.Members...)
	for index := range result.Members {
		result.Members[index].Aliases = append([]string(nil), result.Members[index].Aliases...)
		result.Members[index].HierarchyPath = cloneIDs(result.Members[index].HierarchyPath)
		if result.Members[index].Sensitive {
			result.Members[index].DisplayLabel = ""
			result.Members[index].Aliases = []string{}
		}
	}
	result.EvidenceIDs = cloneIDs(result.EvidenceIDs)
	return result
}

func argumentSchemaFor(tool ToolName) (json.RawMessage, error) {
	rule, exists := toolArgumentRules[tool]
	if !exists {
		return nil, ErrInvalidRegistry
	}
	properties := map[string]any{"release": map[string]any{"type": "object"}}
	required := []string{"release"}
	for _, field := range argumentSchemaFields() {
		if rule.allowed&field.mask == 0 {
			continue
		}
		properties[field.name] = field.schema
		if rule.required&field.mask != 0 {
			required = append(required, field.name)
		}
	}
	sort.Strings(required)
	return json.Marshal(map[string]any{
		"$schema": "https://json-schema.org/draft/2020-12/schema", "type": "object",
		"additionalProperties": false, "required": required, "properties": properties,
	})
}

func resultSchemaFor(fields []string) (json.RawMessage, error) {
	properties := map[string]any{}
	required := append([]string(nil), fields...)
	for _, field := range fields {
		schema, err := resultFieldSchema(field)
		if err != nil {
			return nil, err
		}
		properties[field] = schema
	}
	sort.Strings(required)
	return json.Marshal(map[string]any{
		"$schema": "https://json-schema.org/draft/2020-12/schema", "type": "object",
		"additionalProperties": false, "required": required, "properties": properties,
	})
}

func resultFieldSchema(field string) (any, error) {
	stringSchema := map[string]any{"type": "string"}
	integerSchema := map[string]any{"type": "integer"}
	objectArray := map[string]any{"type": "array", "items": map[string]any{"type": "object"}}
	switch field {
	case "truncated", "fallbackUsed", "graphDegraded", "valid", "allowed", "safe", "noDataConfirmed", "covered", "equivalent":
		return map[string]any{"type": "boolean"}, nil
	case "planCount", "maxRows", "maxPlanRows", "leftCount", "rightCount", "joinedCount",
		"fanoutPermillion", "rowCount", "count", "distinctCount", "differenceCount", "confidencePermillion":
		return integerSchema, nil
	case "maxCost":
		return map[string]any{"type": "number"}, nil
	case "candidates", "contracts", "members", "examples", "risks", "conflicts", "rules",
		"parameterShapes", "columns", "metrics", "differences", "options":
		return objectArray, nil
	case "modelVersionIds", "relationshipIds", "missingObjectVersionIds", "evidenceIds":
		return map[string]any{"type": "array", "items": stringSchema}, nil
	case "dimensionVersionId", "graphPlanHash", "status", "dataAsOf", "coverageStart", "coverageEnd",
		"planHash", "validationHash", "resultHash", "verificationHash", "verdict", "validationType",
		"summaryHash", "leftResultHash", "rightResultHash", "conflictCode", "question":
		return stringSchema, nil
	default:
		return nil, ErrInvalidRegistry
	}
}

type schemaField struct {
	mask   argumentField
	name   string
	schema any
}

func argumentSchemaFields() []schemaField {
	stringSchema := map[string]any{"type": "string", "minLength": 1}
	idArray := map[string]any{"type": "array", "items": stringSchema, "maxItems": MaxArgumentIDs}
	return []schemaField{
		{fieldMention, "mention", stringSchema}, {fieldObjectTypes, "objectTypes", idArray},
		{fieldDomainIDs, "domainIds", idArray}, {fieldObjectVersionIDs, "objectVersionIds", idArray},
		{fieldDimensionVersionID, "dimensionVersionId", stringSchema},
		{fieldQuestionSummary, "questionSummary", stringSchema}, {fieldModelVersionIDs, "modelVersionIds", idArray},
		{fieldMetricVersionIDs, "metricVersionIds", idArray}, {fieldDimensionVersionIDs, "dimensionVersionIds", idArray},
		{fieldMemberVersionIDs, "memberVersionIds", idArray}, {fieldTimeRange, "timeRange", map[string]any{"type": "object"}},
		{fieldSemanticIR, "semanticIr", map[string]any{"type": "object"}}, {fieldPlanHash, "planHash", stringSchema},
		{fieldGraphPlanHash, "graphPlanHash", stringSchema}, {fieldLeftPlanHash, "leftPlanHash", stringSchema},
		{fieldRightPlanHash, "rightPlanHash", stringSchema}, {fieldLimit, "limit", map[string]any{"type": "integer", "minimum": 1, "maximum": 100}},
		{fieldMaxRows, "maxRows", map[string]any{"type": "integer", "minimum": 1, "maximum": ircontract.MaxLimit}},
		{fieldValidationType, "validationType", stringSchema}, {fieldConflictCode, "conflictCode", stringSchema},
		{fieldClarificationQuestion, "clarificationQuestion", stringSchema},
		{fieldClarificationOptions, "clarificationOptions", map[string]any{"type": "array", "maxItems": MaxClarificationOptions}},
	}
}

func validateEvidenceIDs(values []askdata.ID, known map[askdata.ID]askdata.EvidenceRef) error {
	if len(values) < 1 || len(values) != len(known) {
		return ErrInvalidInvocation
	}
	seen := map[askdata.ID]bool{}
	for index, value := range values {
		if _, exists := known[value]; !exists || seen[value] {
			return ErrInvalidInvocation
		}
		if index > 0 && values[index-1] >= value {
			return ErrInvalidInvocation
		}
		seen[value] = true
	}
	return nil
}

func validateFormulaSummary(formula *FormulaSummary) error {
	if formula == nil {
		return nil
	}
	if formula.FormulaHash.Validate() != nil || len(formula.OperatorCodes) < 1 || len(formula.OperatorCodes) > 64 ||
		validateUpperCodes(formula.OperatorCodes) != nil || validateStableIDs(formula.ReferencedVersionIDs, MaxArgumentIDs) != nil {
		return ErrInvalidInvocation
	}
	return nil
}

func validateGraphRisks(values []GraphRisk) error {
	if len(values) > 64 {
		return ErrInvalidInvocation
	}
	seen := map[string]bool{}
	for _, value := range values {
		if !isUpperCode(value.Code) || seen[value.Code] {
			return ErrInvalidInvocation
		}
		seen[value.Code] = true
	}
	return nil
}

func validateStableIDs(values []askdata.ID, max int) error {
	if len(values) > max {
		return ErrInvalidInvocation
	}
	seen := map[askdata.ID]bool{}
	for _, value := range values {
		if value.Validate() != nil || seen[value] {
			return ErrInvalidInvocation
		}
		seen[value] = true
	}
	return nil
}

func validateStrings(values []string, maxRunes int) error {
	seen := map[string]bool{}
	for _, value := range values {
		if !boundedText(value, maxRunes) || seen[value] {
			return ErrInvalidInvocation
		}
		seen[value] = true
	}
	return nil
}

func validateUpperCodes(values []string) error {
	seen := map[string]bool{}
	for _, value := range values {
		if !isUpperCode(value) || seen[value] {
			return ErrInvalidInvocation
		}
		seen[value] = true
	}
	return nil
}

func validObjectType(value ObjectType) bool {
	return value == ObjectTypeMetric || value == ObjectTypeDimension || value == ObjectTypeModel || value == ObjectTypeTerm || value == ObjectTypeReportAsset
}

func (source ReportSourceSummary) Validate() error {
	for _, id := range []askdata.ID{source.ReportID, source.ReportVersionID, source.ComponentID, source.SemanticReleaseID} {
		if id.Validate() != nil {
			return ErrInvalidInvocation
		}
	}
	if !boundedText(source.ReportTitle, 512) || !optionalBoundedText(source.ComponentTitle, 512) ||
		!boundedText(source.ComponentType, 128) || !boundedText(source.ComponentVersion, 64) ||
		source.ComponentHash.Validate() != nil {
		return ErrInvalidInvocation
	}
	return nil
}

func boundedText(value string, maxRunes int) bool {
	return strings.TrimSpace(value) != "" && utf8.ValidString(value) && utf8.RuneCountInString(value) <= maxRunes
}

func optionalBoundedText(value string, maxRunes int) bool {
	return value == "" || boundedText(value, maxRunes)
}

func finiteRange(value, minimum, maximum float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= minimum && value <= maximum
}

func validTime(value string) bool {
	_, err := time.Parse(time.RFC3339Nano, value)
	return err == nil
}

func validTimeOrDate(value string) bool {
	if _, err := time.Parse("2006-01-02", value); err == nil {
		return true
	}
	return validTime(value)
}

func validDecimalSummary(value string) bool {
	if value == "" {
		return true
	}
	_, ok := new(big.Rat).SetString(value)
	return ok
}

func cloneIDs(values []askdata.ID) []askdata.ID         { return append([]askdata.ID(nil), values...) }
func cloneObjectTypes(values []ObjectType) []ObjectType { return append([]ObjectType(nil), values...) }

func cloneClarificationOptions(values []ClarificationOption) []ClarificationOption {
	result := make([]ClarificationOption, len(values))
	for index, value := range values {
		result[index] = value
		result[index].EvidenceRefs = append([]askdata.EvidenceRef(nil), value.EvidenceRefs...)
	}
	return result
}
