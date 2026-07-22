package datasetai

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const (
	SchemaVersion       = "2.3"
	PromptVersion       = "dataset-dag-planner-v11"
	IntentPromptVersion = "dataset-dag-intent-v8"

	maxInstructionRunes = 4000
	maxPlanNodes        = 16
	maxPlanComponents   = 32
	maxHintTables       = 16
	maxHintFields       = 32
)

const (
	InvalidOutputStageIntentResponse      = "INTENT_RESPONSE"
	InvalidOutputStageIntentValidation    = "INTENT_VALIDATION"
	InvalidOutputStagePlannerResponse     = "PLANNER_RESPONSE"
	InvalidOutputStagePlanValidation      = "PLAN_VALIDATION"
	InvalidOutputStageChangeSetValidation = "CHANGESET_VALIDATION"

	InvalidOutputReasonResponseFormat    = "RESPONSE_FORMAT_INVALID"
	InvalidOutputReasonProviderResponse  = "PROVIDER_RESPONSE_INVALID"
	InvalidOutputReasonSchema            = "SCHEMA_INVALID"
	InvalidOutputReasonGraph             = "GRAPH_INVALID"
	InvalidOutputReasonTableReference    = "TABLE_REFERENCE_INVALID"
	InvalidOutputReasonFieldReference    = "FIELD_REFERENCE_INVALID"
	InvalidOutputReasonFieldCaseMismatch = "FIELD_CASE_MISMATCH"
	InvalidOutputReasonAggregationField  = "AGGREGATION_FIELD_INVALID"
	InvalidOutputReasonJoin              = "JOIN_INVALID"
	InvalidOutputReasonGroup             = "GROUP_INVALID"
	InvalidOutputReasonTransform         = "TRANSFORM_REQUIRED"
	InvalidOutputReasonOutput            = "OUTPUT_INVALID"
	InvalidOutputReasonChangeScope       = "CHANGE_SCOPE_INVALID"
	InvalidOutputReasonUnknown           = "INVALID_OUTPUT"
)

var (
	ErrInvalidRequest      = errors.New("dataset AI request is invalid")
	ErrCurrentRequired     = errors.New("current dataset graph is required for modification")
	ErrNoAssets            = errors.New("no mapped dataset assets are available")
	ErrProviderUnavailable = errors.New("dataset AI provider is not configured")
	ErrInvalidOutput       = errors.New("dataset AI output is invalid")

	planIdentifierPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,127}$`)
	physicalFieldPattern  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_$#]{0,127}$`)
)

// InvalidOutputError exposes only stable, non-sensitive failure metadata at the HTTP
// boundary. Detail is retained for server-side diagnostics and repair classification, but
// must never be serialized or returned to callers.
type InvalidOutputError struct {
	ReasonCode      string `json:"reasonCode"`
	Stage           string `json:"stage"`
	RepairAttempted bool   `json:"repairAttempted"`
	RequestID       string `json:"requestId,omitempty"`
	Detail          string `json:"-"`
}

func (e *InvalidOutputError) Error() string {
	if e == nil {
		return ErrInvalidOutput.Error()
	}
	if e.Detail == "" {
		return fmt.Sprintf("%s: %s at %s", ErrInvalidOutput, e.ReasonCode, e.Stage)
	}
	return fmt.Sprintf("%s: %s at %s: %s", ErrInvalidOutput, e.ReasonCode, e.Stage, e.Detail)
}

func (e *InvalidOutputError) Unwrap() error { return ErrInvalidOutput }

// PlanRequest asks the model for a complete candidate graph. Current is optional so the
// same endpoint supports both a blank canvas and iterative edits without persisting chat text.
type PlanRequest struct {
	Instruction string     `json:"instruction"`
	Current     *GraphPlan `json:"current,omitempty"`
	Hints       *PlanHints `json:"hints,omitempty"`
}

// PlanHints are optional, user-supplied planning preferences. They are never trusted as an
// asset grant: loadCatalog resolves every referenced table and field again under the caller's
// tenant before the hints are sent to the model.
type PlanHints struct {
	PreferredTableIDs []string        `json:"preferredTableIds"`
	Aggregation       string          `json:"aggregation"`
	MeasureFields     []PlanFieldHint `json:"measureFields"`
	TimeField         *PlanFieldHint  `json:"timeField,omitempty"`
	DimensionFields   []PlanFieldHint `json:"dimensionFields"`
	TimeGrain         string          `json:"timeGrain"`
}

// TransformRequirement is a deterministic CREATE-mode constraint derived from explicit
// transformation language in the user's instruction. It is sent to the planner and checked
// again after generation so a valid JSON response cannot silently omit required components.
type TransformRequirement struct {
	ComponentType string `json:"componentType"`
	Reason        string `json:"reason"`
}

type PlanFieldHint struct {
	TableID string `json:"tableId"`
	Column  string `json:"column"`
}

// Proposal is reviewable UI state. It never writes a dataset and never contains SQL.
type Proposal struct {
	SchemaVersion string    `json:"schemaVersion"`
	Mode          string    `json:"mode"`
	Summary       string    `json:"summary"`
	Assumptions   []string  `json:"assumptions"`
	Warnings      []string  `json:"warnings"`
	ChangeSet     ChangeSet `json:"changeSet"`
	Plan          GraphPlan `json:"plan"`
}

// ChangeSet is the independently extracted and locked edit scope shown to the user.
// The planner receives this value as trusted input but cannot redefine it in its output.
type ChangeSet struct {
	Operations   []ChangeOperation `json:"operations"`
	FieldChanges []FieldChange     `json:"fieldChanges"`
}

// FieldChange locks the physical field and its complete desired use in the resulting graph.
// Uses are final-state declarations rather than prose: a field can intentionally remain internal
// to a join/group without being exposed by END.
type FieldChange struct {
	Field           FieldBinding     `json:"field"`
	SelectionAction string           `json:"selectionAction"`
	Purpose         string           `json:"purpose"`
	GroupUses       []FieldGroupUse  `json:"groupUses"`
	JoinUses        []FieldJoinUse   `json:"joinUses"`
	OutputUses      []FieldOutputUse `json:"outputUses"`
}

type FieldGroupUse struct {
	GroupID     string `json:"groupId"`
	Role        string `json:"role"`
	Grouping    string `json:"grouping"`
	Aggregation string `json:"aggregation"`
}

type FieldJoinUse struct {
	JoinID string       `json:"joinId"`
	Side   string       `json:"side"`
	Peer   FieldBinding `json:"peer"`
}

type FieldOutputUse struct {
	EndID string `json:"endId"`
	Name  string `json:"name"`
	Code  string `json:"code"`
}

type ChangeOperation struct {
	Action        string        `json:"action"`
	ComponentKind string        `json:"componentKind"`
	ComponentID   string        `json:"componentId"`
	ComponentName string        `json:"componentName"`
	Fields        []string      `json:"fields"`
	InputChanges  []InputChange `json:"inputChanges"`
	Description   string        `json:"description"`
}

type InputChange struct {
	Field string    `json:"field"`
	From  PlanInput `json:"from"`
	To    PlanInput `json:"to"`
}

type ComponentRef struct {
	ComponentKind string `json:"componentKind"`
	ComponentID   string `json:"componentId"`
}

// ChangeIntent is the first model call's complete output. It deliberately contains no
// candidate DAG, so a mistaken plan cannot broaden its own authorization scope.
type ChangeIntent struct {
	Status     string         `json:"status"`
	Question   string         `json:"question"`
	Candidates []ComponentRef `json:"candidates"`
	ChangeSet  ChangeSet      `json:"changeSet"`
}

// ClarificationRequiredError preserves a model-generated, bounded question for an
// ambiguous modification while preventing the planner from guessing a target.
type ClarificationRequiredError struct {
	Question string
}

func (e *ClarificationRequiredError) Error() string {
	if e == nil {
		return "dataset AI modification needs clarification"
	}
	return "dataset AI modification needs clarification: " + e.Question
}

type PlanResult struct {
	RequestID string   `json:"requestId"`
	Proposal  Proposal `json:"proposal"`
}

type GraphPlan struct {
	Dataset    PlanDataset     `json:"dataset"`
	Nodes      []PlanNode      `json:"nodes"`
	Joins      []PlanJoin      `json:"joins"`
	Groups     []PlanGroup     `json:"groups"`
	Transforms []PlanTransform `json:"transforms"`
	End        PlanEnd         `json:"end"`
}

type PlanDataset struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type PlanNode struct {
	ID              string   `json:"id"`
	TableID         string   `json:"tableId"`
	Alias           string   `json:"alias"`
	SelectedColumns []string `json:"selectedColumns"`
}

type PlanInput struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type FieldBinding struct {
	NodeID  string `json:"nodeId"`
	TableID string `json:"tableId"`
	Column  string `json:"column"`
}

type PlanJoinCondition struct {
	LeftNodeID  string `json:"leftNodeId"`
	LeftColumn  string `json:"leftColumn"`
	RightNodeID string `json:"rightNodeId"`
	RightColumn string `json:"rightColumn"`
}

type PlanJoin struct {
	ID         string              `json:"id"`
	Name       string              `json:"name"`
	Left       PlanInput           `json:"left"`
	Right      PlanInput           `json:"right"`
	JoinType   string              `json:"joinType"`
	Conditions []PlanJoinCondition `json:"conditions"`
}

type PlanDimension struct {
	NodeID   string `json:"nodeId"`
	Column   string `json:"column"`
	Grouping string `json:"grouping"`
}

type PlanMetric struct {
	NodeID      string `json:"nodeId"`
	Column      string `json:"column"`
	Aggregation string `json:"aggregation"`
}

type PlanGroup struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	Input      PlanInput       `json:"input"`
	Dimensions []PlanDimension `json:"dimensions"`
	Metrics    []PlanMetric    `json:"metrics"`
}

type PlanTransformOutput struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Code          string `json:"code"`
	CanonicalType string `json:"canonicalType"`
}

type PlanConditionValue struct {
	ID    string `json:"id"`
	Mode  string `json:"mode"`
	Value string `json:"value"`
}

type PlanTransformRule struct {
	ID                string               `json:"id"`
	Operation         string               `json:"operation"`
	InputKeys         []string             `json:"inputKeys"`
	Output            PlanTransformOutput  `json:"output"`
	Unit              string               `json:"unit,omitempty"`
	TargetType        string               `json:"targetType,omitempty"`
	MatchValue        string               `json:"matchValue,omitempty"`
	ThenValue         string               `json:"thenValue,omitempty"`
	ElseValue         string               `json:"elseValue,omitempty"`
	ConditionOperator string               `json:"conditionOperator,omitempty"`
	ConditionValues   []PlanConditionValue `json:"conditionValues,omitempty"`
	FallbackMode      string               `json:"fallbackMode,omitempty"`
	FallbackValue     string               `json:"fallbackValue,omitempty"`
	Separator         string               `json:"separator,omitempty"`
	Precision         *int                 `json:"precision,omitempty"`
	Start             *int                 `json:"start,omitempty"`
	Length            *int                 `json:"length,omitempty"`
	SearchValue       string               `json:"searchValue,omitempty"`
	ReplacementValue  string               `json:"replacementValue,omitempty"`
	ReplaceSourceKey  string               `json:"replaceSourceKey,omitempty"`
}

type PlanTransform struct {
	ID            string              `json:"id"`
	Name          string              `json:"name"`
	Family        string              `json:"family"`
	ComponentType string              `json:"componentType"`
	Input         PlanInput           `json:"input"`
	Rules         []PlanTransformRule `json:"rules"`
}

type PlanOutput struct {
	NodeID string `json:"nodeId"`
	Column string `json:"column"`
	Key    string `json:"key,omitempty"`
	Name   string `json:"name"`
	Code   string `json:"code"`
}

type PlanEnd struct {
	Name    string       `json:"name"`
	Input   PlanInput    `json:"input"`
	Outputs []PlanOutput `json:"outputs"`
}

// CatalogTable is the minimal, non-secret asset context sent to the model.
type CatalogTable struct {
	ID                  string          `json:"id"`
	DataSourceID        string          `json:"dataSourceId"`
	DataSourceName      string          `json:"dataSourceName"`
	DataSourceType      string          `json:"dataSourceType"`
	SchemaName          string          `json:"schemaName"`
	TableName           string          `json:"tableName"`
	BusinessName        string          `json:"businessName"`
	BusinessDescription string          `json:"businessDescription"`
	Tags                []string        `json:"tags"`
	Columns             []CatalogColumn `json:"columns"`
}

type CatalogColumn struct {
	Name                string   `json:"name"`
	BusinessName        string   `json:"businessName"`
	BusinessDescription string   `json:"businessDescription"`
	Tags                []string `json:"tags"`
	CanonicalType       string   `json:"canonicalType"`
	SemanticType        string   `json:"semanticType"`
	Nullable            bool     `json:"nullable"`
}

type plannerPromptEnvelope struct {
	Instruction           string                 `json:"instruction"`
	Mode                  string                 `json:"mode"`
	Current               *GraphPlan             `json:"current,omitempty"`
	Hints                 *PlanHints             `json:"hints,omitempty"`
	TransformRequirements []TransformRequirement `json:"transformRequirements"`
	ChangeSet             ChangeSet              `json:"changeSet"`
	Assets                []CatalogTable         `json:"assets"`
}

type intentPromptEnvelope struct {
	Instruction string             `json:"instruction"`
	Current     GraphPlan          `json:"current"`
	Hints       *PlanHints         `json:"hints,omitempty"`
	EditContext *promptEditContext `json:"editContext,omitempty"`
	Assets      []CatalogTable     `json:"assets"`
}

func normalizePlanRequest(input PlanRequest) (PlanRequest, error) {
	input.Instruction = strings.TrimSpace(input.Instruction)
	if input.Instruction == "" || len([]rune(input.Instruction)) > maxInstructionRunes {
		return PlanRequest{}, fmt.Errorf("%w: instruction must contain 1 to %d characters", ErrInvalidRequest, maxInstructionRunes)
	}
	if input.Current != nil {
		current := normalizeGraphPlan(*input.Current)
		if err := validateGraphShape(current); err != nil {
			return PlanRequest{}, fmt.Errorf("%w: current graph: %v", ErrInvalidRequest, err)
		}
		input.Current = &current
	}
	if input.Hints != nil {
		hints, err := normalizePlanHints(*input.Hints)
		if err != nil {
			return PlanRequest{}, err
		}
		input.Hints = &hints
	}
	return input, nil
}

func normalizePlanHints(value PlanHints) (PlanHints, error) {
	if len(value.PreferredTableIDs) > maxHintTables || len(value.MeasureFields) > maxHintFields || len(value.DimensionFields) > maxHintFields {
		return PlanHints{}, fmt.Errorf("%w: planning hints exceed limits", ErrInvalidRequest)
	}
	value.Aggregation = strings.ToUpper(strings.TrimSpace(value.Aggregation))
	if !oneOf(value.Aggregation, "", "SUM", "AVG", "COUNT", "COUNT_DISTINCT", "MIN", "MAX") {
		return PlanHints{}, fmt.Errorf("%w: hint aggregation is invalid", ErrInvalidRequest)
	}
	value.TimeGrain = strings.ToUpper(strings.TrimSpace(value.TimeGrain))
	if !oneOf(value.TimeGrain, "", "DAY", "WEEK", "MONTH", "QUARTER", "YEAR") {
		return PlanHints{}, fmt.Errorf("%w: hint time grain is invalid", ErrInvalidRequest)
	}
	value.PreferredTableIDs = normalizeTextList(value.PreferredTableIDs)
	for _, tableID := range value.PreferredTableIDs {
		if !boundedText(tableID, 1, 128) {
			return PlanHints{}, fmt.Errorf("%w: preferred table id is invalid", ErrInvalidRequest)
		}
	}
	var err error
	if value.MeasureFields, err = normalizePlanFieldHints(value.MeasureFields); err != nil {
		return PlanHints{}, err
	}
	if value.DimensionFields, err = normalizePlanFieldHints(value.DimensionFields); err != nil {
		return PlanHints{}, err
	}
	if value.TimeField != nil {
		timeField, fieldErr := normalizePlanFieldHint(*value.TimeField)
		if fieldErr != nil {
			return PlanHints{}, fieldErr
		}
		value.TimeField = &timeField
	}
	tableIDs := map[string]bool{}
	for _, tableID := range value.PreferredTableIDs {
		tableIDs[tableID] = true
	}
	for _, field := range value.MeasureFields {
		tableIDs[field.TableID] = true
	}
	if value.TimeField != nil {
		tableIDs[value.TimeField.TableID] = true
	}
	for _, field := range value.DimensionFields {
		tableIDs[field.TableID] = true
	}
	if len(tableIDs) > maxHintTables {
		return PlanHints{}, fmt.Errorf("%w: planning hints reference too many tables", ErrInvalidRequest)
	}
	return value, nil
}

func normalizePlanFieldHints(values []PlanFieldHint) ([]PlanFieldHint, error) {
	if values == nil {
		return []PlanFieldHint{}, nil
	}
	result := make([]PlanFieldHint, 0, len(values))
	seen := map[string]bool{}
	for _, raw := range values {
		value, err := normalizePlanFieldHint(raw)
		if err != nil {
			return nil, err
		}
		key := value.TableID + "\x00" + value.Column
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, value)
	}
	return result, nil
}

func normalizePlanFieldHint(value PlanFieldHint) (PlanFieldHint, error) {
	value.TableID = strings.TrimSpace(value.TableID)
	value.Column = strings.TrimSpace(value.Column)
	if !boundedText(value.TableID, 1, 128) || !validPhysicalIdentifier(value.Column) {
		return PlanFieldHint{}, fmt.Errorf("%w: hint field reference is invalid", ErrInvalidRequest)
	}
	return value, nil
}

func normalizeProposal(value Proposal, mode string) Proposal {
	value.SchemaVersion = strings.TrimSpace(value.SchemaVersion)
	// Mode is derived from the trusted request rather than from model prose.
	value.Mode = mode
	value.Summary = strings.TrimSpace(value.Summary)
	value.Assumptions = normalizeTextList(value.Assumptions)
	value.Warnings = normalizeTextList(value.Warnings)
	if value.ChangeSet.Operations == nil {
		value.ChangeSet.Operations = []ChangeOperation{}
	}
	if value.ChangeSet.FieldChanges == nil {
		value.ChangeSet.FieldChanges = []FieldChange{}
	}
	value.Plan = normalizeGraphPlan(value.Plan)
	if mode == "CREATE" {
		value.Plan = canonicalizeEndOutputKeys(value.Plan)
	}
	return value
}

func normalizeGraphPlan(value GraphPlan) GraphPlan {
	value.Dataset.Name = strings.TrimSpace(value.Dataset.Name)
	value.Dataset.Description = strings.TrimSpace(value.Dataset.Description)
	for i := range value.Nodes {
		value.Nodes[i].ID = strings.TrimSpace(value.Nodes[i].ID)
		value.Nodes[i].TableID = strings.TrimSpace(value.Nodes[i].TableID)
		value.Nodes[i].Alias = strings.TrimSpace(value.Nodes[i].Alias)
		value.Nodes[i].SelectedColumns = normalizeTextList(value.Nodes[i].SelectedColumns)
	}
	for i := range value.Joins {
		value.Joins[i].ID = strings.TrimSpace(value.Joins[i].ID)
		value.Joins[i].Name = strings.TrimSpace(value.Joins[i].Name)
		value.Joins[i].Left = normalizeInput(value.Joins[i].Left)
		value.Joins[i].Right = normalizeInput(value.Joins[i].Right)
		value.Joins[i].JoinType = strings.ToUpper(strings.TrimSpace(value.Joins[i].JoinType))
		for j := range value.Joins[i].Conditions {
			condition := &value.Joins[i].Conditions[j]
			condition.LeftNodeID = strings.TrimSpace(condition.LeftNodeID)
			condition.LeftColumn = strings.TrimSpace(condition.LeftColumn)
			condition.RightNodeID = strings.TrimSpace(condition.RightNodeID)
			condition.RightColumn = strings.TrimSpace(condition.RightColumn)
		}
	}
	for i := range value.Groups {
		value.Groups[i].ID = strings.TrimSpace(value.Groups[i].ID)
		value.Groups[i].Name = strings.TrimSpace(value.Groups[i].Name)
		value.Groups[i].Input = normalizeInput(value.Groups[i].Input)
		for j := range value.Groups[i].Dimensions {
			dimension := &value.Groups[i].Dimensions[j]
			dimension.NodeID = strings.TrimSpace(dimension.NodeID)
			dimension.Column = strings.TrimSpace(dimension.Column)
			dimension.Grouping = strings.ToUpper(strings.TrimSpace(dimension.Grouping))
		}
		for j := range value.Groups[i].Metrics {
			metric := &value.Groups[i].Metrics[j]
			metric.NodeID = strings.TrimSpace(metric.NodeID)
			metric.Column = strings.TrimSpace(metric.Column)
			metric.Aggregation = strings.ToUpper(strings.TrimSpace(metric.Aggregation))
		}
	}
	if value.Transforms == nil {
		value.Transforms = []PlanTransform{}
	}
	for i := range value.Transforms {
		transform := &value.Transforms[i]
		transform.ID = strings.TrimSpace(transform.ID)
		transform.Name = strings.TrimSpace(transform.Name)
		transform.Family = strings.ToUpper(strings.TrimSpace(transform.Family))
		transform.ComponentType = strings.ToUpper(strings.TrimSpace(transform.ComponentType))
		transform.Input = normalizeInput(transform.Input)
		if transform.Rules == nil {
			transform.Rules = []PlanTransformRule{}
		}
		for j := range transform.Rules {
			rule := &transform.Rules[j]
			rule.ID = strings.TrimSpace(rule.ID)
			rule.Operation = strings.ToUpper(strings.TrimSpace(rule.Operation))
			rule.InputKeys = normalizeTextList(rule.InputKeys)
			rule.Output.ID = strings.TrimSpace(rule.Output.ID)
			rule.Output.Name = strings.TrimSpace(rule.Output.Name)
			rule.Output.Code = strings.TrimSpace(rule.Output.Code)
			rule.Output.CanonicalType = strings.ToUpper(strings.TrimSpace(rule.Output.CanonicalType))
			rule.Unit = strings.ToUpper(strings.TrimSpace(rule.Unit))
			rule.TargetType = strings.ToUpper(strings.TrimSpace(rule.TargetType))
			rule.ConditionOperator = strings.ToUpper(strings.TrimSpace(rule.ConditionOperator))
			rule.FallbackMode = strings.ToUpper(strings.TrimSpace(rule.FallbackMode))
			rule.ReplaceSourceKey = strings.TrimSpace(rule.ReplaceSourceKey)
			for k := range rule.ConditionValues {
				rule.ConditionValues[k].ID = strings.TrimSpace(rule.ConditionValues[k].ID)
				rule.ConditionValues[k].Mode = strings.ToUpper(strings.TrimSpace(rule.ConditionValues[k].Mode))
				rule.ConditionValues[k].Value = strings.TrimSpace(rule.ConditionValues[k].Value)
			}
		}
	}
	value.End.Name = strings.TrimSpace(value.End.Name)
	value.End.Input = normalizeInput(value.End.Input)
	for i := range value.End.Outputs {
		value.End.Outputs[i].NodeID = strings.TrimSpace(value.End.Outputs[i].NodeID)
		value.End.Outputs[i].Column = strings.TrimSpace(value.End.Outputs[i].Column)
		value.End.Outputs[i].Key = strings.TrimSpace(value.End.Outputs[i].Key)
		value.End.Outputs[i].Name = strings.TrimSpace(value.End.Outputs[i].Name)
		value.End.Outputs[i].Code = strings.TrimSpace(value.End.Outputs[i].Code)
	}
	return value
}

func normalizeInput(value PlanInput) PlanInput {
	value.Kind = strings.ToUpper(strings.TrimSpace(value.Kind))
	value.ID = strings.TrimSpace(value.ID)
	return value
}

func normalizeTextList(values []string) []string {
	if values == nil {
		return []string{}
	}
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

func validateProposal(value Proposal, catalog []CatalogTable) error {
	if value.SchemaVersion != SchemaVersion || (value.Mode != "CREATE" && value.Mode != "MODIFY") {
		return invalidOutput("schemaVersion or mode is invalid")
	}
	if !boundedText(value.Summary, 1, 500) || len(value.Assumptions) > 12 || len(value.Warnings) > 12 {
		return invalidOutput("summary, assumptions, or warnings exceed limits")
	}
	for _, list := range [][]string{value.Assumptions, value.Warnings} {
		for _, item := range list {
			if !boundedText(item, 1, 500) {
				return invalidOutput("assumption or warning exceeds limits")
			}
		}
	}
	if value.Mode == "CREATE" {
		for _, group := range value.Plan.Groups {
			for _, dimension := range group.Dimensions {
				if dimension.Grouping != "" {
					return invalidOutputWithReason(InvalidOutputReasonTransform, "group dimensions cannot perform date conversion; use a DATE_FORMAT transform before GROUP")
				}
			}
		}
	}
	return validateGraphPlan(value.Plan, catalog)
}

func validateGraphShape(value GraphPlan) error {
	if !boundedText(value.Dataset.Name, 0, 200) || !boundedText(value.Dataset.Description, 0, 2000) {
		return errors.New("dataset metadata exceeds limits")
	}
	if len(value.Nodes) > maxPlanNodes || len(value.Joins)+len(value.Groups)+len(value.Transforms) > maxPlanComponents {
		return errors.New("graph exceeds component limits")
	}
	for _, node := range value.Nodes {
		if !validIdentifier(node.ID) || !validIdentifier(node.Alias) || !boundedText(node.TableID, 1, 128) || len(node.SelectedColumns) > 512 {
			return errors.New("node identity or projection is invalid")
		}
	}
	for _, join := range value.Joins {
		if !validIdentifier(join.ID) || !boundedText(join.Name, 1, 200) || len(join.Conditions) > 16 {
			return errors.New("join identity or conditions are invalid")
		}
	}
	for _, group := range value.Groups {
		if !validIdentifier(group.ID) || !boundedText(group.Name, 1, 200) || len(group.Dimensions) > 128 || len(group.Metrics) > 128 {
			return errors.New("group identity or fields are invalid")
		}
	}
	for _, transform := range value.Transforms {
		if !validIdentifier(transform.ID) || !boundedText(transform.Name, 1, 200) || len(transform.Rules) < 1 || len(transform.Rules) > 64 {
			return fmt.Errorf("transform %s identity or rule count is invalid", transform.ID)
		}
		if !validTransformComponent(transform) {
			return fmt.Errorf("transform %s family %s does not match component type %s", transform.ID, transform.Family, transform.ComponentType)
		}
		for _, rule := range transform.Rules {
			if !validIdentifier(rule.ID) || !validIdentifier(rule.Output.ID) || !validIdentifier(rule.Output.Code) || !boundedText(rule.Output.Name, 1, 200) || len(rule.InputKeys) < 1 || len(rule.InputKeys) > 16 {
				return fmt.Errorf("transform %s rule %s identity, output metadata, or input count is invalid", transform.ID, rule.ID)
			}
			if detail := transformRuleValidationDetail(transform.ComponentType, rule); detail != "" {
				return fmt.Errorf("transform %s rule %s is invalid: %s", transform.ID, rule.ID, detail)
			}
		}
	}
	if !boundedText(value.End.Name, 0, 200) || len(value.End.Outputs) > 512 {
		return errors.New("end component exceeds limits")
	}
	return nil
}

func validTransformComponent(value PlanTransform) bool {
	families := map[string]string{
		"TEXT_UPPER": "TEXT", "TEXT_TRIM": "TEXT", "TEXT_REPLACE": "TEXT", "TEXT_LOWER": "TEXT", "TEXT_SUBSTRING": "TEXT", "TEXT_CONCAT": "TEXT",
		"NUMBER_ABSOLUTE": "NUMBER", "NUMBER_ROUNDING": "NUMBER", "NUMBER_ARITHMETIC": "NUMBER", "DATE_FORMAT": "DATE",
		"NULL": "NULL", "CAST": "CAST", "CONDITION": "CONDITION",
	}
	return families[value.ComponentType] == value.Family
}

func validTransformRule(componentType string, rule PlanTransformRule) bool {
	return transformRuleValidationDetail(componentType, rule) == ""
}

// transformRuleValidationDetail gives the repair model a precise, locally derived reason
// without including provider prose or source-row values.
func transformRuleValidationDetail(componentType string, rule PlanTransformRule) string {
	operations := map[string]map[string]bool{
		"TEXT_UPPER": {"UPPER": true}, "TEXT_TRIM": {"TRIM": true}, "TEXT_REPLACE": {"REPLACE": true}, "TEXT_LOWER": {"LOWER": true},
		"TEXT_SUBSTRING": {"SUBSTRING": true}, "TEXT_CONCAT": {"CONCAT": true}, "NUMBER_ABSOLUTE": {"ABS": true},
		"NUMBER_ROUNDING": {"ROUND": true, "FLOOR": true, "CEIL": true}, "NUMBER_ARITHMETIC": {"ADD": true, "SUBTRACT": true, "MULTIPLY": true, "DIVIDE": true},
		"DATE_FORMAT": {"DATE_FORMAT": true}, "NULL": {"COALESCE": true}, "CAST": {"CAST": true}, "CONDITION": {"CASE": true},
	}
	if !operations[componentType][rule.Operation] || !oneOf(rule.Output.CanonicalType, "STRING", "INTEGER", "DECIMAL", "BOOLEAN", "DATE", "DATETIME") {
		return fmt.Sprintf("operation %s is not allowed for %s or output canonical type %s is unsupported", rule.Operation, componentType, rule.Output.CanonicalType)
	}
	requiredInputs := 1
	if oneOf(rule.Operation, "ADD", "SUBTRACT", "MULTIPLY", "DIVIDE", "CONCAT") || rule.Operation == "COALESCE" && rule.FallbackMode == "FIELD" {
		requiredInputs = 2
	}
	if len(rule.InputKeys) != requiredInputs {
		return fmt.Sprintf("operation %s requires %d input keys but received %d", rule.Operation, requiredInputs, len(rule.InputKeys))
	}
	if rule.Operation == "DATE_FORMAT" && !oneOf(rule.Unit, "YEAR", "MONTH", "QUARTER", "DAY") {
		return fmt.Sprintf("DATE_FORMAT unit %s must be YEAR, MONTH, QUARTER, or DAY", rule.Unit)
	}
	if rule.Operation == "CAST" && !oneOf(rule.TargetType, "STRING", "INTEGER", "DECIMAL", "BOOLEAN", "DATE", "DATETIME") {
		return fmt.Sprintf("CAST target type %s is unsupported", rule.TargetType)
	}
	if rule.Operation == "CASE" {
		if !oneOf(rule.ConditionOperator, "EQUALS", "NOT_EQUALS", "GT", "GTE", "LT", "LTE", "CONTAINS", "NOT_CONTAINS", "IN", "IS_NULL", "IS_NOT_NULL") {
			return fmt.Sprintf("CASE condition operator %s is unsupported", rule.ConditionOperator)
		}
		if rule.ConditionOperator == "IN" {
			if len(rule.ConditionValues) == 0 || len(rule.ConditionValues) > 64 {
				return "CASE IN requires between 1 and 64 condition values"
			}
			for _, item := range rule.ConditionValues {
				if !validIdentifier(item.ID) || !oneOf(item.Mode, "LITERAL", "FIELD") || strings.TrimSpace(item.Value) == "" {
					return fmt.Sprintf("CASE IN condition value %s has an invalid id, mode, or empty value", item.ID)
				}
			}
		} else if !oneOf(rule.ConditionOperator, "IS_NULL", "IS_NOT_NULL") && strings.TrimSpace(rule.MatchValue) == "" {
			return fmt.Sprintf("CASE operator %s requires a non-empty match value", rule.ConditionOperator)
		}
	}
	if rule.Operation == "COALESCE" && !oneOf(rule.FallbackMode, "LITERAL", "FIELD") {
		return fmt.Sprintf("COALESCE fallback mode %s must be LITERAL or FIELD", rule.FallbackMode)
	}
	if rule.Operation == "ROUND" && (rule.Precision == nil || *rule.Precision < -10 || *rule.Precision > 10) {
		return "ROUND precision must be present and between -10 and 10"
	}
	if rule.Operation == "SUBSTRING" && (rule.Start == nil || rule.Length == nil || *rule.Start < 1 || *rule.Length < 0) {
		return "SUBSTRING start must be at least 1 and length must be non-negative"
	}
	if rule.Operation == "REPLACE" && rule.SearchValue == "" {
		return "REPLACE search value must not be empty"
	}
	return ""
}

func validateGraphPlan(value GraphPlan, catalog []CatalogTable) error {
	if err := validateGraphShape(value); err != nil {
		return invalidOutput(err.Error())
	}
	if !boundedText(value.Dataset.Name, 1, 200) || len(value.Nodes) < 1 || len(value.Nodes) > maxPlanNodes {
		return invalidOutput("dataset name and 1 to 16 nodes are required")
	}

	catalogColumns := make(map[string]map[string]CatalogColumn, len(catalog))
	for _, table := range catalog {
		columns := make(map[string]CatalogColumn, len(table.Columns))
		for _, column := range table.Columns {
			columns[column.Name] = column
		}
		catalogColumns[table.ID] = columns
	}

	nodes := make(map[string]PlanNode, len(value.Nodes))
	aliases := make(map[string]bool, len(value.Nodes))
	componentIDs := make(map[string]bool, len(value.Nodes)+len(value.Joins)+len(value.Groups)+len(value.Transforms)+1)
	selected := make(map[string]map[string]bool, len(value.Nodes))
	for _, node := range value.Nodes {
		if componentIDs[node.ID] || aliases[node.Alias] {
			return invalidOutput("node ids and aliases must be unique")
		}
		columns, ok := catalogColumns[node.TableID]
		if !ok {
			return invalidOutput("node references an unavailable mapped table")
		}
		if len(node.SelectedColumns) < 1 {
			return invalidOutput("every node requires at least one selected column")
		}
		projection := make(map[string]bool, len(node.SelectedColumns))
		for _, column := range node.SelectedColumns {
			_, exists := columns[column]
			if projection[column] || !exists || !validPhysicalIdentifier(column) {
				return invalidOutputWithReason(invalidColumnReason(column, columns), "node projection references an unavailable, case-mismatched, synthetic, or duplicate column")
			}
			projection[column] = true
		}
		nodes[node.ID] = node
		aliases[node.Alias] = true
		componentIDs[node.ID] = true
		selected[node.ID] = projection
	}

	joins := make(map[string]PlanJoin, len(value.Joins))
	for _, join := range value.Joins {
		if componentIDs[join.ID] || !oneOf(join.JoinType, "INNER", "LEFT") || len(join.Conditions) < 1 {
			return invalidOutput("join identity, type, or conditions are invalid")
		}
		componentIDs[join.ID] = true
		joins[join.ID] = join
	}
	groups := make(map[string]PlanGroup, len(value.Groups))
	for _, group := range value.Groups {
		if componentIDs[group.ID] || group.Input.Kind == "GROUP" || len(group.Dimensions) < 1 || len(group.Metrics) < 1 {
			return invalidOutput("group identity, dimensions, or metrics are invalid")
		}
		componentIDs[group.ID] = true
		groups[group.ID] = group
	}
	transforms := make(map[string]PlanTransform, len(value.Transforms))
	for _, transform := range value.Transforms {
		if componentIDs[transform.ID] || !validTransformComponent(transform) || len(transform.Rules) < 1 {
			return invalidOutput("transform identity, type, or rules are invalid")
		}
		componentIDs[transform.ID] = true
		transforms[transform.ID] = transform
	}
	if componentIDs["end_1"] {
		return invalidOutput("end component id conflicts with another component")
	}
	consumers := make(map[string]int, len(joins)+len(groups)+len(transforms))
	countConsumer := func(input PlanInput) {
		if input.Kind == "JOIN" || input.Kind == "GROUP" || input.Kind == "TRANSFORM" {
			consumers[input.Kind+":"+input.ID]++
		}
	}
	for _, join := range joins {
		countConsumer(join.Left)
		countConsumer(join.Right)
		for _, input := range []PlanInput{join.Left, join.Right} {
			if input.Kind == "GROUP" {
				group, ok := groups[input.ID]
				if !ok {
					return invalidOutput("a pre-join group references an unknown component")
				}
				leaves := leavesForInput(group.Input, nodes, joins, groups, transforms, map[string]bool{})
				if len(leaves) != 1 || inputContainsComponentKind(group.Input, "JOIN", joins, groups, transforms, map[string]bool{}) || inputContainsComponentKind(group.Input, "GROUP", joins, groups, transforms, map[string]bool{}) {
					return invalidOutput("a pre-join group must consume one node branch with optional transforms only")
				}
			}
		}
	}
	for _, group := range groups {
		countConsumer(group.Input)
	}
	for _, transform := range transforms {
		countConsumer(transform.Input)
	}
	countConsumer(value.End.Input)
	for id := range joins {
		if consumers["JOIN:"+id] != 1 {
			return invalidOutput("every join must have exactly one downstream consumer")
		}
	}
	for id := range groups {
		if consumers["GROUP:"+id] != 1 {
			return invalidOutput("every group must have exactly one downstream consumer")
		}
	}
	for id := range transforms {
		if consumers["TRANSFORM:"+id] != 1 {
			return invalidOutput("every transform must have exactly one downstream consumer")
		}
	}

	type fieldSet map[string]bool
	producedMemo := make(map[string]fieldSet)
	visiting := make(map[string]bool)
	var produced func(PlanInput) (fieldSet, map[string]bool, error)
	produced = func(input PlanInput) (fieldSet, map[string]bool, error) {
		key := input.Kind + ":" + input.ID
		if fields, ok := producedMemo[key]; ok {
			return cloneSet(fields), leavesForInput(input, nodes, joins, groups, transforms, map[string]bool{}), nil
		}
		if visiting[key] {
			return nil, nil, errors.New("graph contains a cycle")
		}
		visiting[key] = true
		defer delete(visiting, key)
		switch input.Kind {
		case "NODE":
			if _, ok := nodes[input.ID]; !ok {
				return nil, nil, errors.New("input references an unknown node")
			}
			fields := make(fieldSet, len(selected[input.ID]))
			for column := range selected[input.ID] {
				fields[fieldKey(input.ID, column)] = true
			}
			producedMemo[key] = cloneSet(fields)
			return fields, map[string]bool{input.ID: true}, nil
		case "GROUP":
			group, ok := groups[input.ID]
			if !ok {
				return nil, nil, errors.New("input references an unknown group")
			}
			upstream, leaves, err := produced(group.Input)
			if err != nil {
				return nil, nil, err
			}
			fields := fieldSet{}
			for _, dimension := range group.Dimensions {
				field := fieldKey(dimension.NodeID, dimension.Column)
				if !upstream[field] {
					available := map[string]CatalogColumn{}
					if node, ok := nodes[dimension.NodeID]; ok {
						available = catalogColumns[node.TableID]
					}
					return nil, nil, invalidOutputWithReason(
						invalidColumnReason(dimension.Column, available),
						fmt.Sprintf("group %s dimension %s references an unavailable physical or transform-produced field at input %s:%s", group.ID, field, group.Input.Kind, group.Input.ID),
					)
				}
				if !oneOf(dimension.Grouping, "", "DAY", "WEEK", "MONTH", "QUARTER", "YEAR") || fields[field] {
					return nil, nil, errors.New("group dimension is duplicated or has invalid granularity")
				}
				canonicalType := producedFieldCanonicalType(group.Input, dimension.NodeID, dimension.Column, nodes, joins, groups, transforms, catalogColumns, map[string]bool{})
				if dimension.Grouping != "" && !isDateGroupingType(canonicalType) {
					return nil, nil, errors.New("date grouping requires a DATE or DATETIME field")
				}
				fields[field] = true
			}
			for _, metric := range group.Metrics {
				field := fieldKey(metric.NodeID, metric.Column)
				if !upstream[field] {
					available := map[string]CatalogColumn{}
					if node, ok := nodes[metric.NodeID]; ok {
						available = catalogColumns[node.TableID]
					}
					return nil, nil, invalidOutputWithReason(
						invalidColumnReason(metric.Column, available),
						fmt.Sprintf("group %s metric %s references an unavailable physical or transform-produced field at input %s:%s", group.ID, field, group.Input.Kind, group.Input.ID),
					)
				}
				if !oneOf(metric.Aggregation, "SUM", "AVG", "COUNT", "COUNT_DISTINCT", "MIN", "MAX") || fields[field] {
					return nil, nil, errors.New("group metric is duplicated or has invalid aggregation")
				}
				canonicalType := producedFieldCanonicalType(group.Input, metric.NodeID, metric.Column, nodes, joins, groups, transforms, catalogColumns, map[string]bool{})
				if oneOf(metric.Aggregation, "SUM", "AVG") && !isNumericCanonicalType(canonicalType) {
					return nil, nil, errors.New("SUM and AVG require a numeric field")
				}
				fields[field] = true
			}
			producedMemo[key] = cloneSet(fields)
			return fields, leaves, nil
		case "TRANSFORM":
			transform, ok := transforms[input.ID]
			if !ok {
				return nil, nil, errors.New("input references an unknown transform")
			}
			upstream, leaves, err := produced(transform.Input)
			if err != nil {
				return nil, nil, err
			}
			fields := cloneSet(upstream)
			for _, rule := range transform.Rules {
				if detail := transformRuleValidationDetail(transform.ComponentType, rule); detail != "" {
					return nil, nil, fmt.Errorf("transform %s rule %s is invalid: %s", transform.ID, rule.ID, detail)
				}
				for _, inputKey := range rule.InputKeys {
					if !upstream[inputKey] {
						return nil, nil, errors.New("transform rule references an unavailable input field")
					}
				}
				for _, item := range rule.ConditionValues {
					if item.Mode == "FIELD" && !upstream[item.Value] {
						return nil, nil, errors.New("transform condition references an unavailable field")
					}
				}
				if rule.ReplaceSourceKey != "" {
					if !upstream[rule.ReplaceSourceKey] {
						return nil, nil, errors.New("transform replacement references an unavailable field")
					}
					delete(fields, rule.ReplaceSourceKey)
				}
				outputKey := fieldKey(transform.ID, rule.Output.ID)
				if fields[outputKey] {
					return nil, nil, errors.New("transform output field is duplicated")
				}
				fields[outputKey] = true
			}
			producedMemo[key] = cloneSet(fields)
			return fields, leaves, nil
		case "JOIN":
			join, ok := joins[input.ID]
			if !ok {
				return nil, nil, errors.New("input references an unknown join")
			}
			leftFields, leftLeaves, err := produced(join.Left)
			if err != nil {
				return nil, nil, err
			}
			rightFields, rightLeaves, err := produced(join.Right)
			if err != nil {
				return nil, nil, err
			}
			for nodeID := range leftLeaves {
				if rightLeaves[nodeID] {
					return nil, nil, errors.New("join sides overlap")
				}
			}
			var conditionPair string
			for _, condition := range join.Conditions {
				leftKey := fieldKey(condition.LeftNodeID, condition.LeftColumn)
				rightKey := fieldKey(condition.RightNodeID, condition.RightColumn)
				if !leftLeaves[condition.LeftNodeID] || !rightLeaves[condition.RightNodeID] || !leftFields[leftKey] || !rightFields[rightKey] {
					return nil, nil, errors.New("join condition references a field outside its side")
				}
				leftType := producedFieldCanonicalType(join.Left, condition.LeftNodeID, condition.LeftColumn, nodes, joins, groups, transforms, catalogColumns, map[string]bool{})
				rightType := producedFieldCanonicalType(join.Right, condition.RightNodeID, condition.RightColumn, nodes, joins, groups, transforms, catalogColumns, map[string]bool{})
				if !compatibleJoinTypes(leftType, rightType) {
					return nil, nil, errors.New("join condition fields have incompatible canonical types")
				}
				pair := condition.LeftNodeID + "\x00" + condition.RightNodeID
				if conditionPair != "" && pair != conditionPair {
					return nil, nil, errors.New("all conditions in one join must use the same leaf-node pair")
				}
				conditionPair = pair
			}
			fields := cloneSet(leftFields)
			for field := range rightFields {
				fields[field] = true
			}
			leaves := cloneSet(leftLeaves)
			for nodeID := range rightLeaves {
				leaves[nodeID] = true
			}
			producedMemo[key] = cloneSet(fields)
			return fields, leaves, nil
		default:
			return nil, nil, errors.New("input kind must be NODE, JOIN, GROUP, or TRANSFORM")
		}
	}

	rootFields, rootLeaves, err := produced(value.End.Input)
	if err != nil {
		if errors.Is(err, ErrInvalidOutput) {
			return err
		}
		return invalidOutput(err.Error())
	}
	if len(rootLeaves) != len(nodes) {
		return invalidOutput("end component must include every data node")
	}
	if len(value.End.Outputs) < 1 {
		return invalidOutput("end component requires at least one output")
	}
	outputCodes := map[string]bool{}
	outputFields := map[string]bool{}
	for _, output := range value.End.Outputs {
		field := output.Key
		if field == "" {
			field = fieldKey(output.NodeID, output.Column)
		}
		if !rootFields[field] {
			return invalidOutputWithReason(InvalidOutputReasonOutput, fmt.Sprintf("end output key %s is unavailable at end input; group dimensions and metrics keep their physical nodeId.column key", field))
		}
		if outputFields[field] {
			return invalidOutputWithReason(InvalidOutputReasonOutput, fmt.Sprintf("end output key %s is duplicated", field))
		}
		if !validIdentifier(output.Code) {
			return invalidOutputWithReason(InvalidOutputReasonOutput, fmt.Sprintf("end output key %s has invalid code", field))
		}
		if outputCodes[output.Code] {
			return invalidOutputWithReason(InvalidOutputReasonOutput, fmt.Sprintf("end output code %s is duplicated", output.Code))
		}
		if !boundedText(output.Name, 1, 200) {
			return invalidOutputWithReason(InvalidOutputReasonOutput, fmt.Sprintf("end output key %s has an invalid name", field))
		}
		outputFields[field] = true
		outputCodes[output.Code] = true
	}

	// Every declared intermediate component must participate in the end-to-end graph.
	reachable := map[string]bool{}
	collectReachable(value.End.Input, joins, groups, transforms, reachable)
	if len(reachable) != len(joins)+len(groups)+len(transforms) {
		return invalidOutput("graph contains an orphan join, group, or transform")
	}
	return nil
}

func leavesForInput(input PlanInput, nodes map[string]PlanNode, joins map[string]PlanJoin, groups map[string]PlanGroup, transforms map[string]PlanTransform, visiting map[string]bool) map[string]bool {
	key := input.Kind + ":" + input.ID
	if visiting[key] {
		return map[string]bool{}
	}
	visiting[key] = true
	defer delete(visiting, key)
	switch input.Kind {
	case "NODE":
		if _, ok := nodes[input.ID]; ok {
			return map[string]bool{input.ID: true}
		}
	case "GROUP":
		if group, ok := groups[input.ID]; ok {
			return leavesForInput(group.Input, nodes, joins, groups, transforms, visiting)
		}
	case "TRANSFORM":
		if transform, ok := transforms[input.ID]; ok {
			return leavesForInput(transform.Input, nodes, joins, groups, transforms, visiting)
		}
	case "JOIN":
		if join, ok := joins[input.ID]; ok {
			result := leavesForInput(join.Left, nodes, joins, groups, transforms, visiting)
			for id := range leavesForInput(join.Right, nodes, joins, groups, transforms, visiting) {
				result[id] = true
			}
			return result
		}
	}
	return map[string]bool{}
}

func inputContainsComponentKind(input PlanInput, kind string, joins map[string]PlanJoin, groups map[string]PlanGroup, transforms map[string]PlanTransform, visiting map[string]bool) bool {
	key := input.Kind + ":" + input.ID
	if visiting[key] {
		return false
	}
	visiting[key] = true
	defer delete(visiting, key)
	if input.Kind == kind {
		return true
	}
	switch input.Kind {
	case "GROUP":
		if group, ok := groups[input.ID]; ok {
			return inputContainsComponentKind(group.Input, kind, joins, groups, transforms, visiting)
		}
	case "TRANSFORM":
		if transform, ok := transforms[input.ID]; ok {
			return inputContainsComponentKind(transform.Input, kind, joins, groups, transforms, visiting)
		}
	case "JOIN":
		if join, ok := joins[input.ID]; ok {
			return inputContainsComponentKind(join.Left, kind, joins, groups, transforms, visiting) || inputContainsComponentKind(join.Right, kind, joins, groups, transforms, visiting)
		}
	}
	return false
}

func collectReachable(input PlanInput, joins map[string]PlanJoin, groups map[string]PlanGroup, transforms map[string]PlanTransform, result map[string]bool) {
	key := input.Kind + ":" + input.ID
	if result[key] || input.Kind == "NODE" {
		return
	}
	result[key] = true
	if input.Kind == "GROUP" {
		if group, ok := groups[input.ID]; ok {
			collectReachable(group.Input, joins, groups, transforms, result)
		}
		return
	}
	if input.Kind == "TRANSFORM" {
		if transform, ok := transforms[input.ID]; ok {
			collectReachable(transform.Input, joins, groups, transforms, result)
		}
		return
	}
	if join, ok := joins[input.ID]; ok {
		collectReachable(join.Left, joins, groups, transforms, result)
		collectReachable(join.Right, joins, groups, transforms, result)
	}
}

func fieldKey(nodeID, column string) string { return nodeID + "." + column }

func cloneSet[T comparable](value map[T]bool) map[T]bool {
	result := make(map[T]bool, len(value))
	for key, present := range value {
		result[key] = present
	}
	return result
}

func boundedText(value string, minimum, maximum int) bool {
	length := len([]rune(strings.TrimSpace(value)))
	return length >= minimum && length <= maximum
}

func validIdentifier(value string) bool { return planIdentifierPattern.MatchString(value) }

func validPhysicalIdentifier(value string) bool {
	return physicalFieldPattern.MatchString(strings.TrimSpace(value))
}

func isNumericCanonicalType(value string) bool {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "NUMBER", "INT", "INTEGER", "BIGINT", "SMALLINT", "TINYINT", "DECIMAL", "NUMERIC", "FLOAT", "DOUBLE", "REAL":
		return true
	default:
		return false
	}
}

func isDateGroupingType(value string) bool {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "DATE", "DATETIME", "TIMESTAMP":
		return true
	default:
		return false
	}
}

func canonicalJoinFamily(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	if isNumericCanonicalType(value) {
		return "NUMERIC"
	}
	switch value {
	case "STRING", "TEXT", "VARCHAR", "CHAR", "CLOB":
		return "STRING"
	case "BOOL", "BOOLEAN":
		return "BOOLEAN"
	case "DATETIME", "TIMESTAMP":
		return "DATETIME"
	default:
		return value
	}
}

func compatibleJoinTypes(left, right string) bool {
	left, right = canonicalJoinFamily(left), canonicalJoinFamily(right)
	return left != "" && left == right
}

func producedFieldCanonicalType(input PlanInput, nodeID, column string, nodes map[string]PlanNode, joins map[string]PlanJoin, groups map[string]PlanGroup, transforms map[string]PlanTransform, catalog map[string]map[string]CatalogColumn, visiting map[string]bool) string {
	key := input.Kind + ":" + input.ID
	if visiting[key] {
		return ""
	}
	visiting[key] = true
	defer delete(visiting, key)
	switch input.Kind {
	case "NODE":
		if input.ID != nodeID {
			return ""
		}
		node, ok := nodes[nodeID]
		if !ok {
			return ""
		}
		return catalog[node.TableID][column].CanonicalType
	case "JOIN":
		join, ok := joins[input.ID]
		if !ok {
			return ""
		}
		if value := producedFieldCanonicalType(join.Left, nodeID, column, nodes, joins, groups, transforms, catalog, visiting); value != "" {
			return value
		}
		return producedFieldCanonicalType(join.Right, nodeID, column, nodes, joins, groups, transforms, catalog, visiting)
	case "GROUP":
		group, ok := groups[input.ID]
		if !ok {
			return ""
		}
		for _, dimension := range group.Dimensions {
			if dimension.NodeID == nodeID && dimension.Column == column {
				return producedFieldCanonicalType(group.Input, nodeID, column, nodes, joins, groups, transforms, catalog, visiting)
			}
		}
		for _, metric := range group.Metrics {
			if metric.NodeID != nodeID || metric.Column != column {
				continue
			}
			switch metric.Aggregation {
			case "COUNT", "COUNT_DISTINCT":
				return "INTEGER"
			case "SUM", "AVG":
				return "DECIMAL"
			default:
				return producedFieldCanonicalType(group.Input, nodeID, column, nodes, joins, groups, transforms, catalog, visiting)
			}
		}
	case "TRANSFORM":
		if transform, ok := transforms[input.ID]; ok {
			if transform.ID == nodeID {
				for _, rule := range transform.Rules {
					if rule.Output.ID == column {
						return rule.Output.CanonicalType
					}
				}
			}
			return producedFieldCanonicalType(transform.Input, nodeID, column, nodes, joins, groups, transforms, catalog, visiting)
		}
	}
	return ""
}

func oneOf(value string, values ...string) bool {
	for _, candidate := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func invalidOutput(reason string) error {
	return invalidOutputWithReason(classifyInvalidOutputReason(reason), reason)
}

func invalidOutputWithReason(reasonCode, detail string) error {
	if strings.TrimSpace(reasonCode) == "" {
		reasonCode = InvalidOutputReasonUnknown
	}
	return &InvalidOutputError{
		ReasonCode: reasonCode,
		Stage:      InvalidOutputStagePlanValidation,
		Detail:     strings.TrimSpace(detail),
	}
}

func classifyInvalidOutputReason(detail string) string {
	normalized := strings.ToLower(detail)
	switch {
	case strings.Contains(normalized, "changeset"), strings.Contains(normalized, "fieldchange"), strings.Contains(normalized, "locked"), strings.Contains(normalized, "outside locked"), strings.Contains(normalized, "change operation"), strings.Contains(normalized, "inputchange"), strings.Contains(normalized, "input change"), strings.Contains(normalized, "must have empty fields"), strings.Contains(normalized, "requires at least one field"), strings.Contains(normalized, "contains invalid field"), strings.Contains(normalized, "undeclared"), strings.Contains(normalized, "plan changed fields"), strings.Contains(normalized, "plan changes differ"):
		return InvalidOutputReasonChangeScope
	case strings.Contains(normalized, "count(*)"), strings.Contains(normalized, "count_distinct("), strings.Contains(normalized, "count("):
		return InvalidOutputReasonAggregationField
	case strings.Contains(normalized, "schemaversion"), strings.Contains(normalized, "schema"):
		return InvalidOutputReasonSchema
	case strings.Contains(normalized, "mapped table"), strings.Contains(normalized, "unavailable table"):
		return InvalidOutputReasonTableReference
	case strings.Contains(normalized, "projection"), strings.Contains(normalized, "column"), strings.Contains(normalized, "field binding"), strings.Contains(normalized, "field reference"):
		return InvalidOutputReasonFieldReference
	case strings.Contains(normalized, "graph"), strings.Contains(normalized, "cycle"), strings.Contains(normalized, "consumer"), strings.Contains(normalized, "component id"):
		return InvalidOutputReasonGraph
	case strings.Contains(normalized, "join"):
		return InvalidOutputReasonJoin
	case strings.Contains(normalized, "group"):
		return InvalidOutputReasonGroup
	case strings.Contains(normalized, "transform"):
		return InvalidOutputReasonTransform
	case strings.Contains(normalized, "end"), strings.Contains(normalized, "output"):
		return InvalidOutputReasonOutput
	default:
		return InvalidOutputReasonGraph
	}
}

func annotateInvalidOutput(err error, stage string, repairAttempted bool, requestID string) error {
	if err == nil || !errors.Is(err, ErrInvalidOutput) {
		return err
	}
	metadata := InvalidOutputError{ReasonCode: InvalidOutputReasonUnknown, Stage: stage, RepairAttempted: repairAttempted, RequestID: strings.TrimSpace(requestID)}
	var current *InvalidOutputError
	if errors.As(err, &current) && current != nil {
		metadata = *current
		if stage != "" {
			metadata.Stage = stage
		}
		metadata.RepairAttempted = metadata.RepairAttempted || repairAttempted
		if strings.TrimSpace(requestID) != "" {
			metadata.RequestID = strings.TrimSpace(requestID)
		}
	}
	if metadata.ReasonCode == "" {
		metadata.ReasonCode = InvalidOutputReasonUnknown
	}
	if metadata.Stage == "" {
		metadata.Stage = InvalidOutputStagePlanValidation
	}
	return &metadata
}

func invalidOutputMetadata(err error) InvalidOutputError {
	annotated := annotateInvalidOutput(err, "", false, "")
	var result *InvalidOutputError
	if errors.As(annotated, &result) && result != nil {
		return *result
	}
	return InvalidOutputError{ReasonCode: InvalidOutputReasonUnknown, Stage: InvalidOutputStagePlanValidation}
}

func invalidColumnReason(column string, available map[string]CatalogColumn) string {
	trimmed := strings.TrimSpace(column)
	normalized := strings.ToUpper(trimmed)
	if trimmed == "*" || strings.Contains(normalized, "COUNT(") || strings.Contains(normalized, "COUNT_DISTINCT(") {
		return InvalidOutputReasonAggregationField
	}
	for name := range available {
		if name != trimmed && strings.EqualFold(name, trimmed) {
			return InvalidOutputReasonFieldCaseMismatch
		}
	}
	return InvalidOutputReasonFieldReference
}
