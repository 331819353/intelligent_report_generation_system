// Package cognition defines the bounded, provider-neutral actions emitted by
// the LLM cognition loop. Actions propose decisions; trusted tools, policy and
// release gates remain authoritative.
package cognition

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/ircontract"
	"intelligent-report-generation-system/internal/askdata/toolhost"
)

const (
	SchemaVersion         = "1.0"
	MaxDecisionSummary    = 1000
	MaxActionEvidence     = 64
	MaxBindings           = 64
	MaxVerificationChecks = 32
)

type Stage string

const (
	StageAssetReview         Stage = "ASSET_REVIEW"
	StageUnderstanding       Stage = "UNDERSTANDING"
	StageCandidateJudgment   Stage = "CANDIDATE_JUDGMENT"
	StageDisambiguation      Stage = "DISAMBIGUATION"
	StagePlanSelection       Stage = "PLAN_SELECTION"
	StageAnomalyAnalysis     Stage = "ANOMALY_ANALYSIS"
	StageResultVerification  Stage = "RESULT_VERIFICATION"
	StageFeedbackAttribution Stage = "FEEDBACK_ATTRIBUTION"
	StageReleaseReview       Stage = "RELEASE_REVIEW"
)

type ActionType string

const (
	ActionCallTool ActionType = "CALL_TOOL"
	// ActionProposeUnderstanding closes the UNDERSTANDING stage. Without it the
	// stage has no successful exit at all: Loop.Run returns on any non-CALL_TOOL
	// action, and UNDERSTANDING otherwise permits only CLARIFY and BLOCK, so a
	// model that understood the question could only clarify, block, or call tools
	// until the budget ran out.
	ActionProposeUnderstanding ActionType = "PROPOSE_UNDERSTANDING"
	ActionProposeBinding       ActionType = "PROPOSE_BINDING"
	ActionProposePlan          ActionType = "PROPOSE_PLAN"
	ActionAnalyzeAnomaly       ActionType = "ANALYZE_ANOMALY"
	ActionVerifyResult         ActionType = "VERIFY_RESULT"
	ActionFinalize             ActionType = "FINALIZE"
	ActionClarify              ActionType = "CLARIFY"
	ActionBlock                ActionType = "BLOCK"
)

type MetricBinding struct {
	MentionIndex    int        `json:"mentionIndex"`
	MetricVersionID askdata.ID `json:"metricVersionId"`
}

type DimensionBindingRole string

const (
	BindingRoleGroupBy DimensionBindingRole = "GROUP_BY"
	BindingRoleFilter  DimensionBindingRole = "FILTER"
	BindingRoleTime    DimensionBindingRole = "TIME"
	BindingRoleSort    DimensionBindingRole = "SORT"
)

type DimensionBinding struct {
	MentionIndex       int                  `json:"mentionIndex"`
	DimensionVersionID askdata.ID           `json:"dimensionVersionId"`
	Role               DimensionBindingRole `json:"role"`
}

type MemberBinding struct {
	MentionIndex       int        `json:"mentionIndex"`
	DimensionVersionID askdata.ID `json:"dimensionVersionId"`
	MemberVersionID    askdata.ID `json:"memberVersionId"`
}

type BindingProposal struct {
	ModelVersionID    askdata.ID                 `json:"modelVersionId"`
	MetricBindings    []MetricBinding            `json:"metricBindings"`
	DimensionBindings []DimensionBinding         `json:"dimensionBindings"`
	MemberBindings    []MemberBinding            `json:"memberBindings"`
	Confidence        askdata.ConfidenceEvidence `json:"confidence"`
}

type PlanProposal struct {
	SemanticIR ircontract.SemanticIR      `json:"semanticIr"`
	Confidence askdata.ConfidenceEvidence `json:"confidence"`
}

type AnomalyCategory string

const (
	AnomalyMetric       AnomalyCategory = "METRIC"
	AnomalyDimension    AnomalyCategory = "DIMENSION"
	AnomalyMember       AnomalyCategory = "MEMBER"
	AnomalyTime         AnomalyCategory = "TIME"
	AnomalyRelationship AnomalyCategory = "RELATIONSHIP"
	AnomalyPlan         AnomalyCategory = "PLAN"
	AnomalyData         AnomalyCategory = "DATA"
	AnomalySecurity     AnomalyCategory = "SECURITY"
	AnomalyPermission   AnomalyCategory = "PERMISSION"
	AnomalyExpression   AnomalyCategory = "EXPRESSION"
)

type RecommendedAction string

const (
	RecommendRetrieve      RecommendedAction = "RETRIEVE"
	RecommendRebind        RecommendedAction = "REBIND"
	RecommendReplan        RecommendedAction = "REPLAN"
	RecommendRetryValidate RecommendedAction = "RETRY_VALIDATE"
	RecommendClarify       RecommendedAction = "CLARIFY"
	RecommendBlock         RecommendedAction = "BLOCK"
)

type AnomalyAnalysis struct {
	Category          AnomalyCategory       `json:"category"`
	Summary           string                `json:"summary"`
	RecommendedAction RecommendedAction     `json:"recommendedAction"`
	EvidenceRefs      []askdata.EvidenceRef `json:"evidenceRefs"`
}

type VerificationVerdict string

const (
	VerificationPass    VerificationVerdict = "PASS"
	VerificationRetry   VerificationVerdict = "RETRY"
	VerificationClarify VerificationVerdict = "CLARIFY"
	VerificationBlock   VerificationVerdict = "BLOCK"
)

type VerificationCheck struct {
	Code         string                `json:"code"`
	Passed       bool                  `json:"passed"`
	EvidenceRefs []askdata.EvidenceRef `json:"evidenceRefs"`
}

type Verification struct {
	Verdict VerificationVerdict `json:"verdict"`
	Summary string              `json:"summary"`
	Checks  []VerificationCheck `json:"checks"`
}

type FinalOutcome string

const (
	FinalAnswer                FinalOutcome = "ANSWER"
	FinalNoData                FinalOutcome = "NO_DATA"
	FinalAssetReviewComplete   FinalOutcome = "ASSET_REVIEW_COMPLETE"
	FinalFeedbackRecorded      FinalOutcome = "FEEDBACK_RECORDED"
	FinalReleaseReviewComplete FinalOutcome = "RELEASE_REVIEW_COMPLETE"
)

type FinalDecision struct {
	Outcome      FinalOutcome          `json:"outcome"`
	Summary      string                `json:"summary"`
	EvidenceRefs []askdata.EvidenceRef `json:"evidenceRefs"`
}

// UnderstandingProposal is the model's reading of the question at the end of
// the UNDERSTANDING stage. It carries no bindings: naming a metric or dimension
// is the binder's job, and an understanding that could assert bindings would let
// the model skip joint binding entirely.
type UnderstandingProposal struct {
	IntentSummary   string   `json:"intentSummary"`
	UnresolvedSpans []string `json:"unresolvedSpans"`
}

func (proposal UnderstandingProposal) Validate() error {
	summary := strings.TrimSpace(proposal.IntentSummary)
	if summary == "" || len(summary) > 2048 {
		return errors.New("intentSummary must be 1-2048 characters")
	}
	if len(proposal.UnresolvedSpans) > 32 {
		return errors.New("unresolvedSpans exceeds the safe bound")
	}
	for _, span := range proposal.UnresolvedSpans {
		span = strings.TrimSpace(span)
		if span == "" || len(span) > 256 {
			return errors.New("each unresolved span must be 1-256 characters")
		}
	}
	return nil
}

type Clarification struct {
	ConflictCode string                         `json:"conflictCode"`
	Question     string                         `json:"question"`
	Options      []toolhost.ClarificationOption `json:"options"`
}

type BlockDecision struct {
	Code          string                `json:"code"`
	PublicMessage string                `json:"publicMessage"`
	EvidenceRefs  []askdata.EvidenceRef `json:"evidenceRefs"`
}

// Action contains a closed set of nullable payloads. Exactly one payload must
// be populated and it must match Action and Stage.
type Action struct {
	SchemaVersion   string                 `json:"schemaVersion"`
	Stage           Stage                  `json:"stage"`
	Action          ActionType             `json:"action"`
	DecisionSummary string                 `json:"decisionSummary"`
	EvidenceRefs    []askdata.EvidenceRef  `json:"evidenceRefs"`
	ToolCall        *toolhost.CallRequest  `json:"toolCall,omitempty"`
	Understanding   *UnderstandingProposal `json:"understanding,omitempty"`
	BindingProposal *BindingProposal       `json:"bindingProposal,omitempty"`
	PlanProposal    *PlanProposal          `json:"planProposal,omitempty"`
	AnomalyAnalysis *AnomalyAnalysis       `json:"anomalyAnalysis,omitempty"`
	Verification    *Verification          `json:"verification,omitempty"`
	FinalDecision   *FinalDecision         `json:"finalDecision,omitempty"`
	Clarification   *Clarification         `json:"clarification,omitempty"`
	Block           *BlockDecision         `json:"block,omitempty"`
}

func Decode(raw []byte) (Action, error) {
	var action Action
	if err := askdata.DecodeStrictJSON(raw, &action); err != nil {
		return Action{}, err
	}
	if err := action.Validate(); err != nil {
		return Action{}, err
	}
	return action, nil
}

func (action Action) Validate() error {
	if action.SchemaVersion != SchemaVersion {
		return fmt.Errorf("schemaVersion must be %q", SchemaVersion)
	}
	if !validStage(action.Stage) {
		return fmt.Errorf("unsupported stage %q", action.Stage)
	}
	if !validAction(action.Action) {
		return fmt.Errorf("unsupported action %q", action.Action)
	}
	if !stageAllowsAction(action.Stage, action.Action) {
		return fmt.Errorf("stage %s does not allow action %s", action.Stage, action.Action)
	}
	if strings.TrimSpace(action.DecisionSummary) == "" || !utf8.ValidString(action.DecisionSummary) || utf8.RuneCountInString(action.DecisionSummary) > MaxDecisionSummary {
		return fmt.Errorf("decisionSummary must contain at most %d Unicode code points", MaxDecisionSummary)
	}
	if len(action.EvidenceRefs) == 0 || len(action.EvidenceRefs) > MaxActionEvidence {
		return fmt.Errorf("evidenceRefs count must be between 1 and %d", MaxActionEvidence)
	}
	if err := validateEvidenceRefs("evidenceRefs", action.EvidenceRefs); err != nil {
		return err
	}
	payloadCount := 0
	for _, present := range []bool{
		action.ToolCall != nil, action.BindingProposal != nil,
		action.PlanProposal != nil, action.AnomalyAnalysis != nil,
		action.Verification != nil, action.FinalDecision != nil,
		action.Clarification != nil, action.Block != nil,
		action.Understanding != nil,
	} {
		if present {
			payloadCount++
		}
	}
	if payloadCount != 1 {
		return errors.New("exactly one action payload must be present")
	}
	switch action.Action {
	case ActionCallTool:
		if action.ToolCall == nil {
			return errors.New("CALL_TOOL requires toolCall")
		}
		if err := toolhost.ValidateCall(*action.ToolCall, toolhost.DefaultArgumentValidator{}); err != nil {
			return fmt.Errorf("toolCall: %w", err)
		}
	case ActionProposeUnderstanding:
		if action.Understanding == nil {
			return errors.New("PROPOSE_UNDERSTANDING requires understanding")
		}
		if err := action.Understanding.Validate(); err != nil {
			return fmt.Errorf("understanding: %w", err)
		}
	case ActionProposeBinding:
		if action.BindingProposal == nil {
			return errors.New("PROPOSE_BINDING requires bindingProposal")
		}
		if err := action.BindingProposal.Validate(); err != nil {
			return fmt.Errorf("bindingProposal: %w", err)
		}
	case ActionProposePlan:
		if action.PlanProposal == nil {
			return errors.New("PROPOSE_PLAN requires planProposal")
		}
		if err := action.PlanProposal.Validate(); err != nil {
			return fmt.Errorf("planProposal: %w", err)
		}
	case ActionAnalyzeAnomaly:
		if action.AnomalyAnalysis == nil {
			return errors.New("ANALYZE_ANOMALY requires anomalyAnalysis")
		}
		if err := action.AnomalyAnalysis.Validate(); err != nil {
			return fmt.Errorf("anomalyAnalysis: %w", err)
		}
	case ActionVerifyResult:
		if action.Verification == nil {
			return errors.New("VERIFY_RESULT requires verification")
		}
		if err := action.Verification.Validate(); err != nil {
			return fmt.Errorf("verification: %w", err)
		}
	case ActionFinalize:
		if action.FinalDecision == nil {
			return errors.New("FINALIZE requires finalDecision")
		}
		if err := action.FinalDecision.Validate(); err != nil {
			return fmt.Errorf("finalDecision: %w", err)
		}
	case ActionClarify:
		if action.Clarification == nil {
			return errors.New("CLARIFY requires clarification")
		}
		if err := action.Clarification.Validate(); err != nil {
			return fmt.Errorf("clarification: %w", err)
		}
	case ActionBlock:
		if action.Block == nil {
			return errors.New("BLOCK requires block")
		}
		if err := action.Block.Validate(); err != nil {
			return fmt.Errorf("block: %w", err)
		}
	}
	return nil
}

func (proposal BindingProposal) Validate() error {
	if err := proposal.ModelVersionID.Validate(); err != nil {
		return fmt.Errorf("modelVersionId: %w", err)
	}
	if len(proposal.MetricBindings) == 0 || len(proposal.MetricBindings) > MaxBindings {
		return fmt.Errorf("metricBindings count must be between 1 and %d", MaxBindings)
	}
	if len(proposal.DimensionBindings) > MaxBindings || len(proposal.MemberBindings) > MaxBindings {
		return fmt.Errorf("binding arrays cannot exceed %d items", MaxBindings)
	}
	seenMetrics := map[int]struct{}{}
	for index, binding := range proposal.MetricBindings {
		if binding.MentionIndex < 0 || binding.MentionIndex >= MaxBindings {
			return fmt.Errorf("metricBindings[%d].mentionIndex is invalid", index)
		}
		if _, exists := seenMetrics[binding.MentionIndex]; exists {
			return fmt.Errorf("metricBindings[%d].mentionIndex is duplicated", index)
		}
		seenMetrics[binding.MentionIndex] = struct{}{}
		if err := binding.MetricVersionID.Validate(); err != nil {
			return fmt.Errorf("metricBindings[%d].metricVersionId: %w", index, err)
		}
	}
	seenDimensions := map[int]struct{}{}
	for index, binding := range proposal.DimensionBindings {
		if binding.MentionIndex < 0 || binding.MentionIndex >= MaxBindings {
			return fmt.Errorf("dimensionBindings[%d].mentionIndex is invalid", index)
		}
		if _, exists := seenDimensions[binding.MentionIndex]; exists {
			return fmt.Errorf("dimensionBindings[%d].mentionIndex is duplicated", index)
		}
		seenDimensions[binding.MentionIndex] = struct{}{}
		if err := binding.DimensionVersionID.Validate(); err != nil {
			return fmt.Errorf("dimensionBindings[%d].dimensionVersionId: %w", index, err)
		}
		if !validBindingRole(binding.Role) {
			return fmt.Errorf("dimensionBindings[%d].role is invalid", index)
		}
	}
	seenMembers := map[int]struct{}{}
	for index, binding := range proposal.MemberBindings {
		if binding.MentionIndex < 0 || binding.MentionIndex >= MaxBindings {
			return fmt.Errorf("memberBindings[%d].mentionIndex is invalid", index)
		}
		if _, exists := seenMembers[binding.MentionIndex]; exists {
			return fmt.Errorf("memberBindings[%d].mentionIndex is duplicated", index)
		}
		seenMembers[binding.MentionIndex] = struct{}{}
		if err := binding.DimensionVersionID.Validate(); err != nil {
			return fmt.Errorf("memberBindings[%d].dimensionVersionId: %w", index, err)
		}
		if err := binding.MemberVersionID.Validate(); err != nil {
			return fmt.Errorf("memberBindings[%d].memberVersionId: %w", index, err)
		}
	}
	if err := proposal.Confidence.Validate(); err != nil {
		return fmt.Errorf("confidence: %w", err)
	}
	return nil
}

func (proposal PlanProposal) Validate() error {
	if err := proposal.SemanticIR.Validate(); err != nil {
		return fmt.Errorf("semanticIr: %w", err)
	}
	if err := proposal.Confidence.Validate(); err != nil {
		return fmt.Errorf("confidence: %w", err)
	}
	return nil
}

func (analysis AnomalyAnalysis) Validate() error {
	if !validAnomalyCategory(analysis.Category) {
		return errors.New("category is invalid")
	}
	if err := validateSummary("summary", analysis.Summary, 1000); err != nil {
		return err
	}
	if !validRecommendation(analysis.RecommendedAction) {
		return errors.New("recommendedAction is invalid")
	}
	if len(analysis.EvidenceRefs) == 0 {
		return errors.New("evidenceRefs is required")
	}
	return validateEvidenceRefs("evidenceRefs", analysis.EvidenceRefs)
}

func (verification Verification) Validate() error {
	if verification.Verdict != VerificationPass && verification.Verdict != VerificationRetry && verification.Verdict != VerificationClarify && verification.Verdict != VerificationBlock {
		return errors.New("verdict is invalid")
	}
	if err := validateSummary("summary", verification.Summary, 1000); err != nil {
		return err
	}
	if len(verification.Checks) == 0 || len(verification.Checks) > MaxVerificationChecks {
		return fmt.Errorf("checks count must be between 1 and %d", MaxVerificationChecks)
	}
	seen := map[string]struct{}{}
	for index, check := range verification.Checks {
		if !stableCode(check.Code) {
			return fmt.Errorf("checks[%d].code is invalid", index)
		}
		if _, exists := seen[check.Code]; exists {
			return fmt.Errorf("checks[%d].code is duplicated", index)
		}
		seen[check.Code] = struct{}{}
		if len(check.EvidenceRefs) == 0 {
			return fmt.Errorf("checks[%d].evidenceRefs is required", index)
		}
		if err := validateEvidenceRefs(fmt.Sprintf("checks[%d].evidenceRefs", index), check.EvidenceRefs); err != nil {
			return err
		}
	}
	return nil
}

func (decision FinalDecision) Validate() error {
	if decision.Outcome != FinalAnswer && decision.Outcome != FinalNoData && decision.Outcome != FinalAssetReviewComplete && decision.Outcome != FinalFeedbackRecorded && decision.Outcome != FinalReleaseReviewComplete {
		return errors.New("outcome is invalid")
	}
	if err := validateSummary("summary", decision.Summary, 1000); err != nil {
		return err
	}
	if len(decision.EvidenceRefs) == 0 {
		return errors.New("evidenceRefs is required")
	}
	return validateEvidenceRefs("evidenceRefs", decision.EvidenceRefs)
}

func (clarification Clarification) Validate() error {
	if !stableCode(clarification.ConflictCode) {
		return errors.New("conflictCode is invalid")
	}
	if err := validateSummary("question", clarification.Question, 512); err != nil {
		return err
	}
	arguments := toolhost.ToolArguments{
		Release:      askdata.ReleaseRef{ReleaseID: "validation-only", ContentHash: askdata.HashBytes([]byte("validation-only"))},
		ConflictCode: &clarification.ConflictCode, ClarificationQuestion: &clarification.Question,
		ClarificationOptions: clarification.Options,
	}
	return arguments.ValidateFor(toolhost.ToolRequestClarification)
}

func (block BlockDecision) Validate() error {
	if !stableCode(block.Code) {
		return errors.New("code is invalid")
	}
	if err := validateSummary("publicMessage", block.PublicMessage, 512); err != nil {
		return err
	}
	if len(block.EvidenceRefs) == 0 {
		return errors.New("evidenceRefs is required")
	}
	return validateEvidenceRefs("evidenceRefs", block.EvidenceRefs)
}

func validStage(value Stage) bool {
	switch value {
	case StageAssetReview, StageUnderstanding, StageCandidateJudgment, StageDisambiguation, StagePlanSelection, StageAnomalyAnalysis, StageResultVerification, StageFeedbackAttribution, StageReleaseReview:
		return true
	default:
		return false
	}
}

func validAction(value ActionType) bool {
	switch value {
	case ActionCallTool, ActionProposeUnderstanding, ActionProposeBinding, ActionProposePlan,
		ActionAnalyzeAnomaly, ActionVerifyResult, ActionFinalize, ActionClarify, ActionBlock:
		return true
	default:
		return false
	}
}

func stageAllowsAction(stage Stage, action ActionType) bool {
	if action == ActionCallTool {
		// Understanding is a pure intent-reading stage. Retrieval starts in
		// CandidateJudgment, where the run has a dedicated semantic-search step.
		// PLAN_SELECTION consumes the binding, graph and contract facts already
		// accepted upstream; compilation, validation and execution are performed
		// deterministically after PROPOSE_PLAN. Exposing those tools to the model
		// only encouraged it to repeat checks until the run budget was exhausted.
		return stage != StageUnderstanding && stage != StagePlanSelection
	}
	if action == ActionBlock {
		return true
	}
	switch stage {
	case StageAssetReview, StageFeedbackAttribution, StageReleaseReview:
		return action == ActionAnalyzeAnomaly || action == ActionFinalize
	case StageUnderstanding:
		return action == ActionProposeUnderstanding || action == ActionClarify
	case StageCandidateJudgment, StageDisambiguation:
		return action == ActionProposeBinding || action == ActionClarify
	case StagePlanSelection:
		return action == ActionProposePlan || action == ActionClarify
	case StageAnomalyAnalysis:
		return action == ActionAnalyzeAnomaly || action == ActionClarify
	case StageResultVerification:
		return action == ActionVerifyResult || action == ActionFinalize || action == ActionClarify
	default:
		return false
	}
}

func validBindingRole(value DimensionBindingRole) bool {
	return value == BindingRoleGroupBy || value == BindingRoleFilter || value == BindingRoleTime || value == BindingRoleSort
}

func validAnomalyCategory(value AnomalyCategory) bool {
	switch value {
	case AnomalyMetric, AnomalyDimension, AnomalyMember, AnomalyTime, AnomalyRelationship, AnomalyPlan, AnomalyData, AnomalySecurity, AnomalyPermission, AnomalyExpression:
		return true
	default:
		return false
	}
}

func validRecommendation(value RecommendedAction) bool {
	switch value {
	case RecommendRetrieve, RecommendRebind, RecommendReplan, RecommendRetryValidate, RecommendClarify, RecommendBlock:
		return true
	default:
		return false
	}
}

func validateEvidenceRefs(path string, evidenceRefs []askdata.EvidenceRef) error {
	if len(evidenceRefs) > MaxActionEvidence {
		return fmt.Errorf("%s exceeds %d items", path, MaxActionEvidence)
	}
	seen := map[askdata.ID]struct{}{}
	for index, evidence := range evidenceRefs {
		if err := evidence.Validate(); err != nil {
			return fmt.Errorf("%s[%d]: %w", path, index, err)
		}
		if _, exists := seen[evidence.EvidenceID]; exists {
			return fmt.Errorf("%s[%d].evidenceId is duplicated", path, index)
		}
		seen[evidence.EvidenceID] = struct{}{}
	}
	return nil
}

func validateSummary(name, value string, max int) error {
	if strings.TrimSpace(value) == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > max {
		return fmt.Errorf("%s is invalid", name)
	}
	return nil
}

func stableCode(value string) bool {
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

// StageAllowsActionForTest exposes the stage/action matrix so the orchestrator's
// AI-005 protocol table can assert it is a subset of this contract rather than
// duplicating it.
func StageAllowsActionForTest(stage Stage, action ActionType) bool {
	return stageAllowsAction(stage, action)
}
