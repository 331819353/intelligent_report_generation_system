// Package toolhost defines the provider-neutral, typed tool boundary used by
// the cognition loop. Tools execute trusted facts and capabilities; an LLM can
// request a tool but cannot change the pinned release or policy scope.
package toolhost

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/ircontract"
)

const (
	SchemaVersion           = "1.0"
	MaxArgumentIDs          = 64
	MaxToolResultBytes      = 1 << 20
	MaxClarificationOptions = 8
)

type ToolName string

const (
	ToolSearchSemanticObjects   ToolName = "search_semantic_objects"
	ToolGetSemanticContracts    ToolName = "get_semantic_contracts"
	ToolLookupDimensionValues   ToolName = "lookup_dimension_values"
	ToolGetCertifiedExamples    ToolName = "get_certified_examples"
	ToolResolveGraphPlan        ToolName = "resolve_graph_plan"
	ToolValidateSemanticBundle  ToolName = "validate_semantic_bundle"
	ToolGetDataQualityStatus    ToolName = "get_data_quality_status"
	ToolCompileSemanticQuery    ToolName = "compile_semantic_query"
	ToolValidateQueryPlan       ToolName = "validate_query_plan"
	ToolProbeJoinCardinality    ToolName = "probe_join_cardinality"
	ToolExecuteQueryPlan        ToolName = "execute_query_plan"
	ToolExecuteValidationQuery  ToolName = "execute_validation_query"
	ToolCompareCandidateResults ToolName = "compare_candidate_results"
	ToolRequestClarification    ToolName = "request_clarification"
)

var validTools = map[ToolName]struct{}{
	ToolSearchSemanticObjects: {}, ToolGetSemanticContracts: {},
	ToolLookupDimensionValues: {}, ToolGetCertifiedExamples: {},
	ToolResolveGraphPlan: {}, ToolValidateSemanticBundle: {},
	ToolGetDataQualityStatus: {}, ToolCompileSemanticQuery: {},
	ToolValidateQueryPlan: {}, ToolProbeJoinCardinality: {},
	ToolExecuteQueryPlan: {}, ToolExecuteValidationQuery: {},
	ToolCompareCandidateResults: {}, ToolRequestClarification: {},
}

// IsKnownTool lets prompt and orchestration layers validate an allowlist
// without gaining access to the registry's mutable implementation details.
func IsKnownTool(tool ToolName) bool {
	_, exists := validTools[tool]
	return exists
}

type ObjectType string

const (
	ObjectTypeMetric    ObjectType = "METRIC"
	ObjectTypeDimension ObjectType = "DIMENSION"
	ObjectTypeModel     ObjectType = "MODEL"
	ObjectTypeTerm      ObjectType = "TERM"
)

type ValidationType string

const (
	ValidationMemberExists           ValidationType = "MEMBER_EXISTS"
	ValidationTimeCoverage           ValidationType = "TIME_COVERAGE"
	ValidationJoinCardinality        ValidationType = "JOIN_CARDINALITY"
	ValidationAggregationConsistency ValidationType = "AGGREGATION_CONSISTENCY"
	ValidationEmptyResult            ValidationType = "EMPTY_RESULT"
)

type ClarificationOption struct {
	OptionID     askdata.ID            `json:"optionId"`
	Label        string                `json:"label"`
	EvidenceRefs []askdata.EvidenceRef `json:"evidenceRefs"`
}

// ToolArguments is a closed parameter vocabulary shared by all tools.
// Tool-specific validation requires and permits only the subset assigned to
// the selected tool; unused fields are omitted from provider JSON to keep the
// structured-output contract small and unambiguous.
type ToolArguments struct {
	Release               askdata.ReleaseRef     `json:"release"`
	Mention               *string                `json:"mention,omitempty"`
	ObjectTypes           []ObjectType           `json:"objectTypes,omitempty"`
	DomainIDs             []askdata.ID           `json:"domainIds,omitempty"`
	ObjectVersionIDs      []askdata.ID           `json:"objectVersionIds,omitempty"`
	DimensionVersionID    *askdata.ID            `json:"dimensionVersionId,omitempty"`
	QuestionSummary       *string                `json:"questionSummary,omitempty"`
	ModelVersionIDs       []askdata.ID           `json:"modelVersionIds,omitempty"`
	MetricVersionIDs      []askdata.ID           `json:"metricVersionIds,omitempty"`
	DimensionVersionIDs   []askdata.ID           `json:"dimensionVersionIds,omitempty"`
	MemberVersionIDs      []askdata.ID           `json:"memberVersionIds,omitempty"`
	TimeRange             *ircontract.TimeRange  `json:"timeRange,omitempty"`
	SemanticIR            *ircontract.SemanticIR `json:"semanticIr,omitempty"`
	PlanHash              *askdata.ContentHash   `json:"planHash,omitempty"`
	GraphPlanHash         *askdata.ContentHash   `json:"graphPlanHash,omitempty"`
	LeftPlanHash          *askdata.ContentHash   `json:"leftPlanHash,omitempty"`
	RightPlanHash         *askdata.ContentHash   `json:"rightPlanHash,omitempty"`
	Limit                 *int                   `json:"limit,omitempty"`
	MaxRows               *int                   `json:"maxRows,omitempty"`
	ValidationType        *ValidationType        `json:"validationType,omitempty"`
	ConflictCode          *string                `json:"conflictCode,omitempty"`
	ClarificationQuestion *string                `json:"clarificationQuestion,omitempty"`
	ClarificationOptions  []ClarificationOption  `json:"clarificationOptions,omitempty"`
}

// NewArguments returns the schema-valid empty parameter vocabulary pinned to
// a release. Callers then populate only fields allowed by the selected tool.
func NewArguments(release askdata.ReleaseRef) ToolArguments {
	return ToolArguments{
		Release:              release,
		ObjectTypes:          []ObjectType{},
		DomainIDs:            []askdata.ID{},
		ObjectVersionIDs:     []askdata.ID{},
		ModelVersionIDs:      []askdata.ID{},
		MetricVersionIDs:     []askdata.ID{},
		DimensionVersionIDs:  []askdata.ID{},
		MemberVersionIDs:     []askdata.ID{},
		ClarificationOptions: []ClarificationOption{},
	}
}

type CallRequest struct {
	SchemaVersion string        `json:"schemaVersion"`
	CallID        askdata.ID    `json:"callId"`
	Tool          ToolName      `json:"tool"`
	Arguments     ToolArguments `json:"arguments"`
}

// ArgumentValidator is deliberately separate from the outer cognition action
// schema. A Tool Host must perform this second validation immediately before
// execution, after the orchestrator has checked release and policy scope.
type ArgumentValidator interface {
	ValidateArguments(tool ToolName, arguments ToolArguments) error
}

type DefaultArgumentValidator struct{}

func (DefaultArgumentValidator) ValidateArguments(tool ToolName, arguments ToolArguments) error {
	return arguments.ValidateFor(tool)
}

func ValidateCall(request CallRequest, validator ArgumentValidator) error {
	if request.SchemaVersion != SchemaVersion {
		return fmt.Errorf("schemaVersion must be %q", SchemaVersion)
	}
	if err := request.CallID.Validate(); err != nil {
		return fmt.Errorf("callId: %w", err)
	}
	if _, exists := validTools[request.Tool]; !exists {
		return fmt.Errorf("unknown tool %q", request.Tool)
	}
	if validator == nil {
		return errors.New("tool argument validator is required")
	}
	if err := validator.ValidateArguments(request.Tool, request.Arguments); err != nil {
		return fmt.Errorf("validate %s arguments: %w", request.Tool, err)
	}
	return nil
}

type argumentField uint64

const (
	fieldMention argumentField = 1 << iota
	fieldObjectTypes
	fieldDomainIDs
	fieldObjectVersionIDs
	fieldDimensionVersionID
	fieldQuestionSummary
	fieldModelVersionIDs
	fieldMetricVersionIDs
	fieldDimensionVersionIDs
	fieldMemberVersionIDs
	fieldTimeRange
	fieldSemanticIR
	fieldPlanHash
	fieldGraphPlanHash
	fieldLeftPlanHash
	fieldRightPlanHash
	fieldLimit
	fieldMaxRows
	fieldValidationType
	fieldConflictCode
	fieldClarificationQuestion
	fieldClarificationOptions
)

type argumentRule struct {
	required argumentField
	allowed  argumentField
}

var toolArgumentRules = map[ToolName]argumentRule{
	ToolSearchSemanticObjects: {
		required: fieldMention | fieldObjectTypes | fieldDomainIDs | fieldLimit,
		allowed:  fieldMention | fieldObjectTypes | fieldDomainIDs | fieldLimit,
	},
	ToolGetSemanticContracts: {
		required: fieldObjectVersionIDs,
		allowed:  fieldObjectVersionIDs,
	},
	ToolLookupDimensionValues: {
		required: fieldMention | fieldDimensionVersionID | fieldLimit,
		allowed:  fieldMention | fieldDimensionVersionID | fieldLimit,
	},
	ToolGetCertifiedExamples: {
		required: fieldQuestionSummary | fieldDomainIDs | fieldLimit,
		allowed:  fieldQuestionSummary | fieldDomainIDs | fieldLimit,
	},
	ToolResolveGraphPlan: {
		required: fieldModelVersionIDs | fieldMetricVersionIDs,
		allowed:  fieldModelVersionIDs | fieldMetricVersionIDs | fieldDimensionVersionIDs | fieldMemberVersionIDs,
	},
	ToolValidateSemanticBundle: {
		required: fieldModelVersionIDs | fieldMetricVersionIDs,
		allowed:  fieldModelVersionIDs | fieldMetricVersionIDs | fieldDimensionVersionIDs | fieldMemberVersionIDs,
	},
	ToolGetDataQualityStatus: {
		required: fieldModelVersionIDs | fieldMetricVersionIDs | fieldTimeRange,
		allowed:  fieldModelVersionIDs | fieldMetricVersionIDs | fieldTimeRange,
	},
	ToolCompileSemanticQuery: {
		required: fieldSemanticIR,
		allowed:  fieldSemanticIR,
	},
	ToolValidateQueryPlan: {
		required: fieldPlanHash,
		allowed:  fieldPlanHash,
	},
	ToolProbeJoinCardinality: {
		required: fieldGraphPlanHash | fieldTimeRange,
		allowed:  fieldGraphPlanHash | fieldTimeRange,
	},
	ToolExecuteQueryPlan: {
		required: fieldPlanHash | fieldMaxRows,
		allowed:  fieldPlanHash | fieldMaxRows,
	},
	ToolExecuteValidationQuery: {
		required: fieldPlanHash | fieldValidationType,
		allowed:  fieldPlanHash | fieldValidationType,
	},
	ToolCompareCandidateResults: {
		required: fieldLeftPlanHash | fieldRightPlanHash | fieldMaxRows,
		allowed:  fieldLeftPlanHash | fieldRightPlanHash | fieldMaxRows,
	},
	ToolRequestClarification: {
		required: fieldConflictCode | fieldClarificationQuestion | fieldClarificationOptions,
		allowed:  fieldConflictCode | fieldClarificationQuestion | fieldClarificationOptions,
	},
}

func (arguments ToolArguments) ValidateFor(tool ToolName) error {
	rule, exists := toolArgumentRules[tool]
	if !exists {
		return fmt.Errorf("unknown tool %q", tool)
	}
	if err := arguments.Release.Validate(); err != nil {
		return fmt.Errorf("release: %w", err)
	}
	populated := arguments.populatedFields()
	if missing := rule.required &^ populated; missing != 0 {
		return fmt.Errorf("required argument fields are missing (mask %d)", missing)
	}
	if forbidden := populated &^ rule.allowed; forbidden != 0 {
		return fmt.Errorf("arguments contain fields not allowed for %s (mask %d)", tool, forbidden)
	}
	if err := validateOptionalText("mention", arguments.Mention, 512); err != nil {
		return err
	}
	if err := validateOptionalText("questionSummary", arguments.QuestionSummary, 2048); err != nil {
		return err
	}
	if err := validateOptionalText("conflictCode", arguments.ConflictCode, 64); err != nil {
		return err
	}
	if arguments.ConflictCode != nil && !isUpperCode(*arguments.ConflictCode) {
		return errors.New("conflictCode must be an uppercase stable code")
	}
	if err := validateOptionalText("clarificationQuestion", arguments.ClarificationQuestion, 512); err != nil {
		return err
	}
	if err := validateObjectTypes(arguments.ObjectTypes); err != nil {
		return err
	}
	for name, ids := range map[string][]askdata.ID{
		"domainIds": arguments.DomainIDs, "objectVersionIds": arguments.ObjectVersionIDs,
		"modelVersionIds": arguments.ModelVersionIDs, "metricVersionIds": arguments.MetricVersionIDs,
		"dimensionVersionIds": arguments.DimensionVersionIDs, "memberVersionIds": arguments.MemberVersionIDs,
	} {
		if err := validateIDs(name, ids); err != nil {
			return err
		}
	}
	if arguments.DimensionVersionID != nil {
		if err := arguments.DimensionVersionID.Validate(); err != nil {
			return fmt.Errorf("dimensionVersionId: %w", err)
		}
	}
	if arguments.TimeRange != nil {
		if err := arguments.TimeRange.Validate(); err != nil {
			return fmt.Errorf("timeRange: %w", err)
		}
	}
	if arguments.SemanticIR != nil {
		if err := arguments.SemanticIR.Validate(); err != nil {
			return fmt.Errorf("semanticIr: %w", err)
		}
		if arguments.SemanticIR.SemanticReleaseID != arguments.Release.ReleaseID || arguments.SemanticIR.SemanticContentHash != arguments.Release.ContentHash {
			return errors.New("semanticIr release does not match the pinned tool release")
		}
	}
	for name, hash := range map[string]*askdata.ContentHash{
		"planHash": arguments.PlanHash, "graphPlanHash": arguments.GraphPlanHash,
		"leftPlanHash": arguments.LeftPlanHash, "rightPlanHash": arguments.RightPlanHash,
	} {
		if hash != nil {
			if err := hash.Validate(); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		}
	}
	if arguments.Limit != nil && (*arguments.Limit < 1 || *arguments.Limit > 100) {
		return errors.New("limit must be between 1 and 100")
	}
	if arguments.MaxRows != nil && (*arguments.MaxRows < 1 || *arguments.MaxRows > ircontract.MaxLimit) {
		return fmt.Errorf("maxRows must be between 1 and %d", ircontract.MaxLimit)
	}
	if arguments.ValidationType != nil && !validValidationType(*arguments.ValidationType) {
		return errors.New("validationType is invalid")
	}
	if len(arguments.ClarificationOptions) > MaxClarificationOptions {
		return fmt.Errorf("clarificationOptions exceeds %d items", MaxClarificationOptions)
	}
	seenOptions := map[askdata.ID]struct{}{}
	for index, option := range arguments.ClarificationOptions {
		if err := option.OptionID.Validate(); err != nil {
			return fmt.Errorf("clarificationOptions[%d].optionId: %w", index, err)
		}
		if _, exists := seenOptions[option.OptionID]; exists {
			return fmt.Errorf("clarificationOptions[%d].optionId is duplicated", index)
		}
		seenOptions[option.OptionID] = struct{}{}
		if strings.TrimSpace(option.Label) == "" || !utf8.ValidString(option.Label) || utf8.RuneCountInString(option.Label) > 256 {
			return fmt.Errorf("clarificationOptions[%d].label is invalid", index)
		}
		if len(option.EvidenceRefs) == 0 || len(option.EvidenceRefs) > 16 {
			return fmt.Errorf("clarificationOptions[%d].evidenceRefs count is invalid", index)
		}
		for evidenceIndex, evidence := range option.EvidenceRefs {
			if err := evidence.Validate(); err != nil {
				return fmt.Errorf("clarificationOptions[%d].evidenceRefs[%d]: %w", index, evidenceIndex, err)
			}
		}
	}
	return nil
}

func (arguments ToolArguments) populatedFields() argumentField {
	var fields argumentField
	if arguments.Mention != nil {
		fields |= fieldMention
	}
	if len(arguments.ObjectTypes) > 0 {
		fields |= fieldObjectTypes
	}
	if len(arguments.DomainIDs) > 0 {
		fields |= fieldDomainIDs
	}
	if len(arguments.ObjectVersionIDs) > 0 {
		fields |= fieldObjectVersionIDs
	}
	if arguments.DimensionVersionID != nil {
		fields |= fieldDimensionVersionID
	}
	if arguments.QuestionSummary != nil {
		fields |= fieldQuestionSummary
	}
	if len(arguments.ModelVersionIDs) > 0 {
		fields |= fieldModelVersionIDs
	}
	if len(arguments.MetricVersionIDs) > 0 {
		fields |= fieldMetricVersionIDs
	}
	if len(arguments.DimensionVersionIDs) > 0 {
		fields |= fieldDimensionVersionIDs
	}
	if len(arguments.MemberVersionIDs) > 0 {
		fields |= fieldMemberVersionIDs
	}
	if arguments.TimeRange != nil {
		fields |= fieldTimeRange
	}
	if arguments.SemanticIR != nil {
		fields |= fieldSemanticIR
	}
	if arguments.PlanHash != nil {
		fields |= fieldPlanHash
	}
	if arguments.GraphPlanHash != nil {
		fields |= fieldGraphPlanHash
	}
	if arguments.LeftPlanHash != nil {
		fields |= fieldLeftPlanHash
	}
	if arguments.RightPlanHash != nil {
		fields |= fieldRightPlanHash
	}
	if arguments.Limit != nil {
		fields |= fieldLimit
	}
	if arguments.MaxRows != nil {
		fields |= fieldMaxRows
	}
	if arguments.ValidationType != nil {
		fields |= fieldValidationType
	}
	if arguments.ConflictCode != nil {
		fields |= fieldConflictCode
	}
	if arguments.ClarificationQuestion != nil {
		fields |= fieldClarificationQuestion
	}
	if len(arguments.ClarificationOptions) > 0 {
		fields |= fieldClarificationOptions
	}
	return fields
}

type ResponseStatus string

const (
	ResponseSuccess  ResponseStatus = "SUCCESS"
	ResponseRejected ResponseStatus = "REJECTED"
	ResponseFailed   ResponseStatus = "FAILED"
)

type ToolError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

type Response struct {
	SchemaVersion string                `json:"schemaVersion"`
	CallID        askdata.ID            `json:"callId"`
	Tool          ToolName              `json:"tool"`
	Status        ResponseStatus        `json:"status"`
	Result        json.RawMessage       `json:"result"`
	EvidenceRefs  []askdata.EvidenceRef `json:"evidenceRefs"`
	ResultHash    askdata.ContentHash   `json:"resultHash"`
	MadeProgress  bool                  `json:"madeProgress"`
	Error         *ToolError            `json:"error"`
}

func (response Response) Validate() error {
	if response.SchemaVersion != SchemaVersion {
		return fmt.Errorf("schemaVersion must be %q", SchemaVersion)
	}
	if err := response.CallID.Validate(); err != nil {
		return fmt.Errorf("callId: %w", err)
	}
	if _, exists := validTools[response.Tool]; !exists {
		return fmt.Errorf("unknown tool %q", response.Tool)
	}
	if response.Status != ResponseSuccess && response.Status != ResponseRejected && response.Status != ResponseFailed {
		return errors.New("status is invalid")
	}
	if len(response.Result) > MaxToolResultBytes {
		return fmt.Errorf("result exceeds %d bytes", MaxToolResultBytes)
	}
	if response.Status == ResponseSuccess {
		if response.Error != nil || len(bytes.TrimSpace(response.Result)) == 0 ||
			len(response.EvidenceRefs) < 1 {
			return errors.New("successful response requires result and no error")
		}
		var result any
		if err := askdata.DecodeStrictJSON(response.Result, &result); err != nil {
			return fmt.Errorf("result: %w", err)
		}
		if _, ok := result.(map[string]any); !ok {
			return errors.New("result must be a JSON object")
		}
		if err := response.ResultHash.Validate(); err != nil {
			return fmt.Errorf("resultHash: %w", err)
		}
		if askdata.HashBytes(response.Result) != response.ResultHash {
			return errors.New("resultHash does not match result bytes")
		}
		if err := rejectUnsafeResultKeys(response.Result); err != nil {
			return errors.New("result contains a forbidden field")
		}
	} else if response.Error == nil || len(bytes.TrimSpace(response.Result)) != 0 ||
		response.ResultHash != "" || response.MadeProgress || len(response.EvidenceRefs) != 0 {
		return errors.New("rejected or failed response requires error and no result")
	}
	seenEvidence := map[askdata.ID]bool{}
	for index, evidence := range response.EvidenceRefs {
		if err := evidence.Validate(); err != nil {
			return fmt.Errorf("evidenceRefs[%d]: %w", index, err)
		}
		if seenEvidence[evidence.EvidenceID] {
			return fmt.Errorf("evidenceRefs[%d] is duplicated", index)
		}
		seenEvidence[evidence.EvidenceID] = true
	}
	if response.Error != nil {
		if !isUpperCode(response.Error.Code) || strings.TrimSpace(response.Error.Message) == "" || utf8.RuneCountInString(response.Error.Message) > 512 {
			return errors.New("error contract is invalid")
		}
	}
	return nil
}

func validateOptionalText(name string, value *string, max int) error {
	if value == nil {
		return nil
	}
	if strings.TrimSpace(*value) == "" || !utf8.ValidString(*value) || utf8.RuneCountInString(*value) > max {
		return fmt.Errorf("%s is invalid", name)
	}
	return nil
}

func validateObjectTypes(values []ObjectType) error {
	seen := map[ObjectType]struct{}{}
	for index, value := range values {
		if value != ObjectTypeMetric && value != ObjectTypeDimension && value != ObjectTypeModel && value != ObjectTypeTerm {
			return fmt.Errorf("objectTypes[%d] is invalid", index)
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("objectTypes[%d] is duplicated", index)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateIDs(name string, values []askdata.ID) error {
	if len(values) > MaxArgumentIDs {
		return fmt.Errorf("%s exceeds %d items", name, MaxArgumentIDs)
	}
	seen := map[askdata.ID]struct{}{}
	for index, value := range values {
		if err := value.Validate(); err != nil {
			return fmt.Errorf("%s[%d]: %w", name, index, err)
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("%s[%d] is duplicated", name, index)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validValidationType(value ValidationType) bool {
	switch value {
	case ValidationMemberExists, ValidationTimeCoverage, ValidationJoinCardinality, ValidationAggregationConsistency, ValidationEmptyResult:
		return true
	default:
		return false
	}
}

func isUpperCode(value string) bool {
	if value == "" || len(value) > 64 || value[0] < 'A' || value[0] > 'Z' {
		return false
	}
	for _, character := range value {
		if (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '_' {
			continue
		}
		return false
	}
	return true
}
