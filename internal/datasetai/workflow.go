package datasetai

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// The modeling workflow is the fixed stage protocol every DAG build follows
// (docs/10_dag-modeling-workflow.md §2). Stages are an ordered enumeration; each
// model kind marks some of them as not applicable, but a stage is never absent — it
// is recorded as SKIPPED with a reason so the trail stays auditable.
//
// The protocol runs in two blueprint phases. BUSINESS fixes grain and metric
// definitions before source retrieval can bias them. PHYSICAL runs after primary
// and dimension source confirmation and binds joins, fields, metrics, transforms,
// filters, and outputs. INTAKE/KIND and source decisions live in the session;
// GENERATE is the later graph-planner turn. Every blueprint stage carries a
// status, source and confidence, and the server — not the model — decides whether
// the user must confirm it before the next protected transition.

const (
	BlueprintPhaseBusiness = "BUSINESS"
	BlueprintPhasePhysical = "PHYSICAL"

	StageIntake           = "INTAKE"
	StageKind             = "KIND"
	StageGrain            = "GRAIN"
	StageMetricDefinition = "METRIC_DEFINITION"
	StagePrimarySource    = "PRIMARY_SOURCE"
	StageDimensionSource  = "DIMENSION_SOURCE"
	StageJoin             = "JOIN"
	StageMetricBinding    = "METRIC_BINDING"
	StageTransform        = "TRANSFORM"
	StageFilter           = "FILTER"
	StageOutput           = "OUTPUT"
	StageGenerate         = "GENERATE"

	StageStatusProposed      = "PROPOSED"
	StageStatusAutoConfirmed = "AUTO_CONFIRMED"
	StageStatusUserConfirmed = "USER_CONFIRMED"
	StageStatusSkipped       = "SKIPPED"

	DecisionSourceRule      = "RULE"
	DecisionSourceRetrieval = "RETRIEVAL"
	DecisionSourceLLM       = "LLM"
	DecisionSourceUser      = "USER"

	ScopeTableRolePrimary   = "PRIMARY"
	ScopeTableRoleDimension = "DIMENSION"

	StageActionConfirm = "CONFIRM"
	StageActionSkip    = "SKIP"
	StageActionReopen  = "REOPEN"

	JoinProvenanceRegistry   = "REGISTRY"
	JoinProvenanceForeignKey = "FOREIGN_KEY"
	JoinProvenanceNameMatch  = "NAME_MATCH"
	JoinProvenanceLLM        = "LLM"

	MetricOriginRegistry = "REGISTRY"
	MetricOriginNew      = "NEW"

	MetricBindingModeAggregate   = "AGGREGATE"
	MetricBindingModePassthrough = "PASSTHROUGH"
	MetricBindingModeDerived     = "DERIVED"

	TransformPlacementBeforeGroup = "BEFORE_GROUP"
	TransformPlacementAfterGroup  = "AFTER_GROUP"

	// autoConfirmConfidence is the floor above which a model-proposed stage with no
	// alternatives is confirmed automatically. Below it the user must confirm.
	autoConfirmConfidence = 0.85

	maxBlueprintItems = 64
)

// WorkflowStageOrder is the canonical stage sequence shared with the client.
var WorkflowStageOrder = []string{
	StageIntake, StageKind, StageGrain, StageMetricDefinition, StagePrimarySource, StageDimensionSource,
	StageJoin, StageMetricBinding, StageTransform, StageFilter, StageOutput, StageGenerate,
}

// blueprintStageOrder is the complete confirmation order across both blueprint phases.
var blueprintStageOrder = []string{
	StageGrain, StageMetricDefinition, StageJoin, StageMetricBinding, StageTransform, StageFilter, StageOutput,
}

// stageApplicability records which stages a model kind must decide. Absent stages
// are skipped with the kind as the reason. Optional stages (value false in
// mustDecide) may also be skipped by the blueprint when the intent has nothing for them.
var stageApplicability = map[string]map[string]bool{
	"DIM": {StageGrain: true, StageJoin: true, StageTransform: true, StageFilter: true, StageOutput: true},
	"DWD": {StageGrain: true, StageJoin: true, StageTransform: true, StageFilter: true, StageOutput: true},
	"DWS": {StageGrain: true, StageMetricDefinition: true, StageJoin: true, StageMetricBinding: true, StageTransform: true, StageFilter: true, StageOutput: true},
	"ADS": {StageGrain: true, StageMetricDefinition: true, StageJoin: true, StageMetricBinding: true, StageTransform: true, StageFilter: true, StageOutput: true},
}

// stageAlwaysRequired names the stages that cannot be skipped by the model for an
// applicable kind: a build without a grain or an output is not a build.
var stageAlwaysRequired = map[string]bool{StageGrain: true, StageOutput: true}

// stageDependents lists the stages whose confirmation must be revisited when the
// key stage is reopened: changing joins can invalidate bindings and outputs, etc.
var stageDependents = map[string][]string{
	StageGrain:            {StageMetricBinding, StageTransform, StageOutput},
	StageMetricDefinition: {StageMetricBinding, StageOutput},
	StageJoin:             {StageMetricBinding, StageFilter, StageOutput},
	StageMetricBinding:    {StageOutput},
	StageTransform:        {StageOutput},
	StageFilter:           {},
	StageOutput:           {},
}

var stageLabels = map[string]string{
	StageIntake: "需求理解", StageKind: "模型类型", StageGrain: "粒度与时间口径", StageMetricDefinition: "指标定义",
	StagePrimarySource: "主来源表", StageDimensionSource: "维度表", StageJoin: "关联关系", StageMetricBinding: "指标实现口径",
	StageTransform: "字段转换", StageFilter: "过滤条件", StageOutput: "输出字段", StageGenerate: "生成 DAG",
}

func StageApplicable(modelKind, stage string) bool {
	return stageApplicability[strings.ToUpper(modelKind)][stage]
}

func isBlueprintStage(stage string) bool {
	for _, candidate := range blueprintStageOrder {
		if candidate == stage {
			return true
		}
	}
	return false
}

// ModelingBlueprint is the persisted, per-stage decision record. It never contains
// a graph: it fixes the business and physical calibre the planner must realise, and
// the planner's output is checked against it.
type ModelingBlueprint struct {
	// Phase is BUSINESS before table retrieval (grain and metric definition) and
	// PHYSICAL after the source scope is confirmed. Empty means PHYSICAL for
	// sessions written by the first implementation.
	Phase         string          `json:"phase,omitempty"`
	RequestID     string          `json:"requestId,omitempty"`
	PromptVersion string          `json:"promptVersion,omitempty"`
	Summary       string          `json:"summary,omitempty"`
	GeneratedAt   time.Time       `json:"generatedAt"`
	Stages        []StageDecision `json:"stages"`
	// Knowledge summarizes the governed business knowledge the turn had available.
	Knowledge *KnowledgeSummary `json:"knowledge,omitempty"`
	// Revisions are the natural-language turns applied to this blueprint, newest last.
	Revisions []BlueprintRevision `json:"revisions,omitempty"`
}

type KnowledgeSummary struct {
	Available      bool     `json:"available"`
	Terms          int      `json:"terms,omitempty"`
	Metrics        int      `json:"metrics,omitempty"`
	Dimensions     int      `json:"dimensions,omitempty"`
	Relationships  int      `json:"relationships,omitempty"`
	MetricCodes    []string `json:"metricCodes,omitempty"`
	Degraded       bool     `json:"degraded,omitempty"`
	DegradedReason string   `json:"degradedReason,omitempty"`
}

type BlueprintRevision struct {
	Instruction   string    `json:"instruction"`
	Summary       string    `json:"summary,omitempty"`
	ChangedStages []string  `json:"changedStages"`
	At            time.Time `json:"at"`
}

const maxBlueprintRevisions = 16

// StageDecision is one stage's decision. Exactly the payload field matching Stage
// is populated; the rest stay nil so the JSON document stays small and unambiguous.
type StageDecision struct {
	Stage                 string  `json:"stage"`
	Status                string  `json:"status"`
	Source                string  `json:"source"`
	Confidence            float64 `json:"confidence"`
	NeedsUserConfirmation bool    `json:"needsUserConfirmation"`
	Reason                string  `json:"reason,omitempty"`

	Grain      *GrainDecision      `json:"grain,omitempty"`
	Metrics    []MetricDefinition  `json:"metrics,omitempty"`
	Joins      []JoinDecision      `json:"joins,omitempty"`
	Bindings   []MetricBinding     `json:"bindings,omitempty"`
	Transforms []TransformDecision `json:"transforms,omitempty"`
	Filters    []FilterDecision    `json:"filters,omitempty"`
	Outputs    []OutputDecision    `json:"outputs,omitempty"`

	DecidedAt time.Time `json:"decidedAt"`
}

// FieldRef addresses a physical column inside the confirmed scope.
type FieldRef struct {
	TableID string `json:"tableId"`
	Column  string `json:"column"`
}

type GrainDecision struct {
	Description string    `json:"description"`
	Keys        []string  `json:"keys"`
	TimeField   *FieldRef `json:"timeField,omitempty"`
	TimeGrain   string    `json:"timeGrain,omitempty"`
}

type MetricDefinition struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Definition   string `json:"definition"`
	Origin       string `json:"origin"`
	RegistryCode string `json:"registryCode,omitempty"`
}

type JoinKey struct {
	LeftColumn  string `json:"leftColumn"`
	RightColumn string `json:"rightColumn"`
}

type JoinAlternative struct {
	Keys   []JoinKey `json:"keys"`
	Reason string    `json:"reason"`
}

type JoinDecision struct {
	ID           string            `json:"id"`
	LeftTableID  string            `json:"leftTableId"`
	RightTableID string            `json:"rightTableId"`
	JoinType     string            `json:"joinType"`
	Keys         []JoinKey         `json:"keys"`
	Cardinality  string            `json:"cardinality,omitempty"`
	Provenance   string            `json:"provenance"`
	Reason       string            `json:"reason,omitempty"`
	Alternatives []JoinAlternative `json:"alternatives,omitempty"`
	// SampleCompatibility / SampleOverlap are measured on real sample values by
	// the server (screening.go); the model reads them, never sets them.
	SampleCompatibility string `json:"sampleCompatibility,omitempty"`
	SampleOverlap       int    `json:"sampleOverlap,omitempty"`
}

type MetricBinding struct {
	MetricID string `json:"metricId"`
	// Mode makes ADS semantics explicit: AGGREGATE creates a GROUP metric,
	// PASSTHROUGH exposes an already-computed upstream metric, and DERIVED
	// calculates a metric from ordered physical inputs. Empty is accepted only
	// for sessions written before this field existed and normalizes to AGGREGATE.
	Mode        string     `json:"mode,omitempty"`
	TableID     string     `json:"tableId,omitempty"`
	Column      string     `json:"column,omitempty"`
	Inputs      []FieldRef `json:"inputs,omitempty"`
	Operation   string     `json:"operation,omitempty"`
	Aggregation string     `json:"aggregation"`
	Distinct    bool       `json:"distinct"`
	Note        string     `json:"note,omitempty"`
}

type TransformDecision struct {
	ComponentType string     `json:"componentType"`
	Operation     string     `json:"operation,omitempty"`
	Inputs        []FieldRef `json:"inputs"`
	Description   string     `json:"description"`
	Placement     string     `json:"placement"`
}

type FilterDecision struct {
	TableID   string `json:"tableId"`
	Column    string `json:"column"`
	Operator  string `json:"operator"`
	Value     string `json:"value"`
	ValueMode string `json:"valueMode"`
}

type OutputDecision struct {
	Name     string    `json:"name"`
	Code     string    `json:"code"`
	Source   *FieldRef `json:"source,omitempty"`
	MetricID string    `json:"metricId,omitempty"`
}

// StageResolution is the client's structured decision on one blueprint stage.
// CONFIRM may carry an edited payload; SKIP and REOPEN carry only a reason.
type StageResolution struct {
	Stage    string         `json:"stage"`
	Action   string         `json:"action"`
	Reason   string         `json:"reason,omitempty"`
	Decision *StageDecision `json:"decision,omitempty"`
}

var (
	ErrBlueprintRequired = errors.New("dataset AI modeling blueprint is not ready")
	ErrScopeRequired     = errors.New("dataset AI modeling scope is not confirmed")
)

// SetBlueprint replaces the whole blueprint. It is called after generation; the
// stage statuses have already been computed by the generator.
func (state *ModelingSessionState) SetBlueprint(blueprint ModelingBlueprint) {
	copied := blueprint
	copied.Stages = append([]StageDecision(nil), blueprint.Stages...)
	copied.Revisions = append([]BlueprintRevision(nil), blueprint.Revisions...)
	state.Blueprint = &copied
}

// StageDecisionFor returns the recorded decision of one blueprint stage.
func (state ModelingSessionState) StageDecisionFor(stage string) (StageDecision, bool) {
	if state.Blueprint == nil {
		return StageDecision{}, false
	}
	for _, decision := range state.Blueprint.Stages {
		if decision.Stage == stage {
			return decision, true
		}
	}
	return StageDecision{}, false
}

// PendingBlueprintStages lists the stages that still block generation: applicable,
// not skipped, and not yet confirmed by the user or the auto-confirm rule.
func (state ModelingSessionState) PendingBlueprintStages() []string {
	if state.Blueprint == nil {
		return append([]string(nil), blueprintStageOrder...)
	}
	pending := []string{}
	for _, decision := range state.Blueprint.Stages {
		if decision.Status == StageStatusProposed {
			pending = append(pending, decision.Stage)
		}
	}
	return pending
}

// BlueprintReady reports whether generation may start.
func (state ModelingSessionState) BlueprintReady() bool {
	return state.Blueprint != nil && state.Blueprint.Phase != BlueprintPhaseBusiness && len(state.PendingBlueprintStages()) == 0
}

// ResolveStage applies the user's decision on one blueprint stage.
//
// CONFIRM marks the stage USER_CONFIRMED, replacing its payload when the client
// sent an edited one; the payload is validated by the caller against the catalog
// before it reaches here (see Service.ResolveBlueprintStage). SKIP is allowed only
// for stages the kind does not strictly require. REOPEN returns the stage — and
// every dependent stage that was already confirmed — to PROPOSED so the user must
// look at them again; a reopened stage keeps its last payload as the starting point.
func (state *ModelingSessionState) ResolveStage(resolution StageResolution, now time.Time) error {
	if state.Blueprint == nil {
		return fmt.Errorf("%w: generate the blueprint before resolving stages", ErrBlueprintRequired)
	}
	stage := strings.ToUpper(strings.TrimSpace(resolution.Stage))
	if !isBlueprintStage(stage) {
		return fmt.Errorf("%w: %q is not a blueprint stage", ErrInvalidRequest, resolution.Stage)
	}
	if !boundedText(resolution.Reason, 0, maxSessionReasonRunes) {
		return fmt.Errorf("%w: stage reason is too long", ErrInvalidRequest)
	}
	index := -1
	for position, decision := range state.Blueprint.Stages {
		if decision.Stage == stage {
			index = position
			break
		}
	}
	if index < 0 {
		return fmt.Errorf("%w: stage %s is not part of the blueprint", ErrInvalidRequest, stage)
	}
	current := &state.Blueprint.Stages[index]
	timestamp := now.UTC()
	switch strings.ToUpper(strings.TrimSpace(resolution.Action)) {
	case StageActionConfirm:
		if current.Status == StageStatusSkipped && resolution.Decision == nil {
			return fmt.Errorf("%w: a skipped stage needs a decision payload to be confirmed", ErrInvalidRequest)
		}
		if resolution.Decision != nil {
			edited := *resolution.Decision
			edited.Stage = stage
			if err := validateStagePayloadShape(state.ModelKind, edited); err != nil {
				return err
			}
			current.Grain, current.Metrics, current.Joins, current.Bindings = edited.Grain, edited.Metrics, edited.Joins, edited.Bindings
			current.Transforms, current.Filters, current.Outputs = edited.Transforms, edited.Filters, edited.Outputs
			current.Source = DecisionSourceUser
			current.Confidence = 1
		}
		current.Status = StageStatusUserConfirmed
		current.NeedsUserConfirmation = false
		if resolution.Reason != "" {
			current.Reason = resolution.Reason
		}
		current.DecidedAt = timestamp
	case StageActionSkip:
		if stageAlwaysRequired[stage] && StageApplicable(state.ModelKind, stage) {
			return fmt.Errorf("%w: stage %s cannot be skipped for %s", ErrInvalidRequest, stage, state.ModelKind)
		}
		current.Status = StageStatusSkipped
		current.NeedsUserConfirmation = false
		current.Source = DecisionSourceUser
		current.Reason = firstNonEmpty(resolution.Reason, "用户选择跳过该阶段")
		current.DecidedAt = timestamp
	case StageActionReopen:
		if !StageApplicable(state.ModelKind, stage) {
			return fmt.Errorf("%w: stage %s does not apply to %s", ErrInvalidRequest, stage, state.ModelKind)
		}
		current.Status = StageStatusProposed
		current.NeedsUserConfirmation = true
		if resolution.Reason != "" {
			current.Reason = resolution.Reason
		}
		current.DecidedAt = timestamp
		for _, dependent := range stageDependents[stage] {
			for position := range state.Blueprint.Stages {
				target := &state.Blueprint.Stages[position]
				if target.Stage != dependent || target.Status == StageStatusSkipped {
					continue
				}
				target.Status = StageStatusProposed
				target.NeedsUserConfirmation = true
				target.DecidedAt = timestamp
			}
		}
	default:
		return fmt.Errorf("%w: stage action must be CONFIRM, SKIP or REOPEN", ErrInvalidRequest)
	}
	return nil
}

// ConfirmAllProposedStages is the "确认并生成" shortcut: every PROPOSED stage
// becomes USER_CONFIRMED with its current payload.
func (state *ModelingSessionState) ConfirmAllProposedStages(now time.Time) error {
	if state.Blueprint == nil {
		return fmt.Errorf("%w: generate the blueprint before confirming it", ErrBlueprintRequired)
	}
	timestamp := now.UTC()
	for index := range state.Blueprint.Stages {
		decision := &state.Blueprint.Stages[index]
		if decision.Status != StageStatusProposed {
			continue
		}
		decision.Status = StageStatusUserConfirmed
		decision.NeedsUserConfirmation = false
		decision.DecidedAt = timestamp
	}
	return nil
}

// validateStagePayloadShape checks the structural shape of a stage payload: kinds
// of values, bounds, enums, and that only the payload matching the stage is set.
// Catalog-level checks (table in scope, column exists) live in blueprint.go.
func validateStagePayloadShape(modelKind string, decision StageDecision) error {
	invalid := func(format string, args ...any) error {
		return fmt.Errorf("%w: stage %s: %s", ErrInvalidRequest, decision.Stage, fmt.Sprintf(format, args...))
	}
	populated := 0
	if decision.Grain != nil {
		populated++
	}
	for _, count := range []int{len(decision.Metrics), len(decision.Joins), len(decision.Bindings), len(decision.Transforms), len(decision.Filters), len(decision.Outputs)} {
		if count > 0 {
			populated++
		}
		if count > maxBlueprintItems {
			return invalid("too many items")
		}
	}
	if populated > 1 {
		return invalid("only the payload of the stage itself may be set")
	}
	switch decision.Stage {
	case StageGrain:
		if decision.Grain == nil {
			return invalid("grain is required")
		}
		if !boundedText(decision.Grain.Description, 1, 300) || len(decision.Grain.Keys) > 16 {
			return invalid("grain description or keys are invalid")
		}
		for _, key := range decision.Grain.Keys {
			if !boundedText(key, 1, 200) {
				return invalid("grain key is invalid")
			}
		}
		if !oneOf(decision.Grain.TimeGrain, "", "DAY", "WEEK", "MONTH", "QUARTER", "YEAR") {
			return invalid("time grain is invalid")
		}
		if decision.Grain.TimeField != nil && !validFieldRef(*decision.Grain.TimeField) {
			return invalid("time field is invalid")
		}
	case StageMetricDefinition:
		if populated == 0 {
			return invalid("metric definitions are required")
		}
		seen := map[string]bool{}
		for _, metric := range decision.Metrics {
			if !validIdentifier(metric.ID) || seen[metric.ID] || !boundedText(metric.Name, 1, 200) || !boundedText(metric.Definition, 0, 1000) {
				return invalid("metric definition is invalid")
			}
			if !oneOf(metric.Origin, MetricOriginRegistry, MetricOriginNew) || !boundedText(metric.RegistryCode, 0, 200) {
				return invalid("metric origin is invalid")
			}
			seen[metric.ID] = true
		}
	case StageJoin:
		if populated == 0 {
			return invalid("joins are required")
		}
		seen := map[string]bool{}
		for _, join := range decision.Joins {
			if !validIdentifier(join.ID) || seen[join.ID] || !boundedText(join.LeftTableID, 1, 128) || !boundedText(join.RightTableID, 1, 128) || join.LeftTableID == join.RightTableID {
				return invalid("join identity is invalid")
			}
			if !oneOf(join.JoinType, "INNER", "LEFT") || len(join.Keys) < 1 || len(join.Keys) > 16 {
				return invalid("join type or keys are invalid")
			}
			for _, key := range join.Keys {
				if !validPhysicalIdentifier(key.LeftColumn) || !validPhysicalIdentifier(key.RightColumn) {
					return invalid("join key is invalid")
				}
			}
			if !oneOf(join.Cardinality, "", "ONE_TO_ONE", "MANY_TO_ONE", "ONE_TO_MANY", "MANY_TO_MANY", "UNKNOWN") {
				return invalid("join cardinality is invalid")
			}
			if !oneOf(join.Provenance, JoinProvenanceRegistry, JoinProvenanceForeignKey, JoinProvenanceNameMatch, JoinProvenanceLLM, DecisionSourceUser) {
				return invalid("join provenance is invalid")
			}
			if len(join.Alternatives) > 8 || !boundedText(join.Reason, 0, 500) {
				return invalid("join alternatives or reason exceed limits")
			}
			seen[join.ID] = true
		}
	case StageMetricBinding:
		if populated == 0 {
			return invalid("metric bindings are required")
		}
		seen := map[string]bool{}
		for _, binding := range decision.Bindings {
			mode := firstNonEmpty(strings.ToUpper(strings.TrimSpace(binding.Mode)), MetricBindingModeAggregate)
			if !validIdentifier(binding.MetricID) || seen[binding.MetricID] {
				return invalid("metric binding identity is invalid")
			}
			if !oneOf(mode, MetricBindingModeAggregate, MetricBindingModePassthrough, MetricBindingModeDerived) || !boundedText(binding.Note, 0, 500) {
				return invalid("metric binding mode is invalid")
			}
			switch mode {
			case MetricBindingModeAggregate:
				if !validFieldRef(FieldRef{binding.TableID, binding.Column}) || !oneOf(binding.Aggregation, "SUM", "AVG", "COUNT", "COUNT_DISTINCT", "MIN", "MAX") || len(binding.Inputs) != 0 || binding.Operation != "" {
					return invalid("aggregate metric binding is invalid")
				}
			case MetricBindingModePassthrough:
				if strings.ToUpper(modelKind) != "ADS" || !validFieldRef(FieldRef{binding.TableID, binding.Column}) || binding.Aggregation != "NONE" || len(binding.Inputs) != 0 || binding.Operation != "" {
					return invalid("passthrough metric binding is only valid for an ADS physical field")
				}
			case MetricBindingModeDerived:
				if strings.ToUpper(modelKind) != "ADS" || binding.TableID != "" || binding.Column != "" || binding.Aggregation != "NONE" || len(binding.Inputs) != 2 || !oneOf(binding.Operation, "ADD", "SUBTRACT", "MULTIPLY", "DIVIDE") {
					return invalid("derived ADS metric binding requires two ordered inputs and an arithmetic operation")
				}
				for _, input := range binding.Inputs {
					if !validFieldRef(input) {
						return invalid("derived metric input is invalid")
					}
				}
			}
			if binding.Distinct != (binding.Aggregation == "COUNT_DISTINCT") {
				return invalid("metric binding aggregation is invalid")
			}
			seen[binding.MetricID] = true
		}
	case StageTransform:
		if populated == 0 {
			return invalid("transforms are required")
		}
		for _, transform := range decision.Transforms {
			if !oneOf(transform.ComponentType, "TEXT_CASE", "TEXT_TRIM", "TEXT_REPLACE", "TEXT_SUBSTRING", "TEXT_CONCAT", "NUMBER_ABSOLUTE", "NUMBER_ROUNDING", "NUMBER_ARITHMETIC", "DATE_CALCULATION", "DATE_FORMAT", "NULL", "CAST", "CONDITION") {
				return invalid("transform component type is invalid")
			}
			if len(transform.Inputs) < 1 || len(transform.Inputs) > 16 || !boundedText(transform.Description, 1, 500) {
				return invalid("transform inputs or description are invalid")
			}
			if !validBlueprintTransformOperation(transform.ComponentType, transform.Operation) {
				return invalid("transform operation does not match its component type")
			}
			for _, input := range transform.Inputs {
				if !validFieldRef(input) {
					return invalid("transform input is invalid")
				}
			}
			if !oneOf(transform.Placement, TransformPlacementBeforeGroup, TransformPlacementAfterGroup) {
				return invalid("transform placement is invalid")
			}
		}
	case StageFilter:
		if populated == 0 {
			return invalid("filters are required")
		}
		for _, filter := range decision.Filters {
			if !validFieldRef(FieldRef{filter.TableID, filter.Column}) {
				return invalid("filter field is invalid")
			}
			if !oneOf(filter.Operator, "EQUALS", "NOT_EQUALS", "GT", "GTE", "LT", "LTE", "CONTAINS", "NOT_CONTAINS", "IN", "NOT_IN", "IS_NULL", "IS_NOT_NULL") {
				return invalid("filter operator is invalid")
			}
			if !oneOf(filter.ValueMode, "LITERAL", "FIELD") || !boundedText(filter.Value, 0, 500) {
				return invalid("filter value is invalid")
			}
			if filter.Value == "" && !oneOf(filter.Operator, "IS_NULL", "IS_NOT_NULL") {
				return invalid("filter value is required for this operator")
			}
		}
	case StageOutput:
		if len(decision.Outputs) < 1 {
			return invalid("at least one output is required")
		}
		codes := map[string]bool{}
		for _, output := range decision.Outputs {
			if !boundedText(output.Name, 1, 200) || !validIdentifier(output.Code) || codes[output.Code] {
				return invalid("output name or code is invalid")
			}
			if (output.Source == nil) == (output.MetricID == "") {
				return invalid("output must reference exactly one of a source field or a metric")
			}
			if output.Source != nil && !validFieldRef(*output.Source) {
				return invalid("output source is invalid")
			}
			if output.MetricID != "" && !validIdentifier(output.MetricID) {
				return invalid("output metric id is invalid")
			}
			codes[output.Code] = true
		}
	default:
		return invalid("unknown stage")
	}
	return nil
}

func validBlueprintTransformOperation(componentType, operation string) bool {
	allowed := map[string]map[string]bool{
		"TEXT_CASE": {"UPPER": true, "LOWER": true}, "TEXT_TRIM": {"TRIM": true}, "TEXT_REPLACE": {"REPLACE": true},
		"TEXT_SUBSTRING": {"SUBSTRING": true}, "TEXT_CONCAT": {"CONCAT": true}, "NUMBER_ABSOLUTE": {"ABS": true},
		"NUMBER_ROUNDING": {"ROUND": true, "FLOOR": true, "CEIL": true}, "NUMBER_ARITHMETIC": {"ADD": true, "SUBTRACT": true, "MULTIPLY": true, "DIVIDE": true},
		"DATE_CALCULATION": {"CURRENT_DATE": true, "DATE_DIFF": true, "DATE_EXTRACT": true, "DATE_START": true, "DATE_END": true},
		"DATE_FORMAT":      {"DATE_FORMAT": true}, "NULL": {"COALESCE": true}, "CAST": {"CAST": true}, "CONDITION": {"CASE": true},
	}
	return allowed[strings.ToUpper(strings.TrimSpace(componentType))][strings.ToUpper(strings.TrimSpace(operation))]
}

func validFieldRef(value FieldRef) bool {
	return boundedText(value.TableID, 1, 128) && validPhysicalIdentifier(value.Column)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// StageLabel returns the user-facing Chinese label of a stage.
func StageLabel(stage string) string {
	if label, ok := stageLabels[stage]; ok {
		return label
	}
	return stage
}
