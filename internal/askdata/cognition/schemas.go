package cognition

import (
	"encoding/json"
	"errors"
	"fmt"

	"intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/toolhost"
)

var actionOrder = []ActionType{
	ActionCallTool, ActionProposeUnderstanding, ActionProposeBinding, ActionProposePlan,
	ActionAnalyzeAnomaly, ActionVerifyResult, ActionFinalize,
	ActionClarify, ActionBlock,
}

var commonActionProperties = []string{
	"schemaVersion", "stage", "action", "decisionSummary", "evidenceRefs",
}

// SchemaForStage derives a strict provider contract from the canonical action
// schema. Go validation remains authoritative, while the provider is prevented
// from emitting actions or a stage that are impossible in the current round.
func SchemaForStage(base ai.JSONSchema, stage Stage) (ai.JSONSchema, error) {
	if !validStage(stage) {
		return ai.JSONSchema{}, fmt.Errorf("unsupported cognition stage %q", stage)
	}
	var root map[string]any
	if err := askdata.DecodeStrictJSON(base.Schema, &root); err != nil {
		return ai.JSONSchema{}, fmt.Errorf("decode cognition schema: %w", err)
	}
	properties, ok := root["properties"].(map[string]any)
	if !ok {
		return ai.JSONSchema{}, errors.New("cognition schema properties are missing")
	}
	stageSchema, ok := properties["stage"].(map[string]any)
	if !ok {
		return ai.JSONSchema{}, errors.New("cognition stage schema is missing")
	}
	actionSchema, ok := properties["action"].(map[string]any)
	if !ok {
		return ai.JSONSchema{}, errors.New("cognition action schema is missing")
	}
	delete(stageSchema, "enum")
	stageSchema["const"] = string(stage)
	allowed := allowedActions(stage)
	values := make([]any, len(allowed))
	for index, action := range allowed {
		values[index] = string(action)
	}
	actionSchema["enum"] = values
	// Stage-specific branches expose only the common envelope and the selected
	// payload. This both enforces exactly one payload and avoids asking prompt-
	// mode providers to manufacture a long list of irrelevant null fields.
	branches := make([]any, 0, len(allowed))
	for _, action := range allowed {
		payload, ok := actionPayloadProperty(action)
		if !ok {
			return ai.JSONSchema{}, fmt.Errorf("action %s has no payload schema", action)
		}
		branchProperties := make(map[string]any, len(commonActionProperties)+1)
		for _, name := range commonActionProperties {
			branchProperties[name] = properties[name]
		}
		branchProperties["action"] = map[string]any{"const": string(action)}
		if action == ActionCallTool {
			toolCallSchema, schemaErr := strictToolCallSchema(root, stage)
			if schemaErr != nil {
				return ai.JSONSchema{}, schemaErr
			}
			branchProperties[payload] = toolCallSchema
		} else {
			definition := payload
			if payload == "understanding" {
				definition = "understandingProposal"
			}
			branchProperties[payload] = map[string]any{"$ref": "#/$defs/" + definition}
		}
		branches = append(branches, map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             append(stringValues(commonActionProperties), payload),
			"properties":           branchProperties,
		})
	}
	stageRoot := map[string]any{
		"title": root["title"], "description": root["description"],
		"type": "object", "oneOf": branches, "$defs": root["$defs"],
	}
	canonical, err := json.Marshal(stageRoot)
	if err != nil {
		return ai.JSONSchema{}, fmt.Errorf("encode cognition schema: %w", err)
	}
	result := ai.JSONSchema{
		Name: base.Name, Description: base.Description, Schema: canonical,
	}
	// Run the common strict-schema validator after mutation, so unsupported
	// keywords or an accidentally weakened object boundary fail at startup.
	if err := ai.ValidateProviderRequest(ai.ProviderRequest{
		Messages: []ai.Message{{
			Role:  ai.MessageRoleUser,
			Parts: []ai.ContentPart{{Type: ai.ContentTypeText, Text: "validate stage schema"}},
		}},
		ResponseSchema: result,
	}); err != nil {
		var providerError *ai.ProviderError
		if errors.As(err, &providerError) && providerError.Cause != nil {
			return ai.JSONSchema{}, fmt.Errorf("validate cognition stage schema: %w: %v", err, providerError.Cause)
		}
		return ai.JSONSchema{}, fmt.Errorf("validate cognition stage schema: %w", err)
	}
	return result, nil
}

var toolArgumentFields = []string{
	"mention", "objectTypes", "domainIds", "objectVersionIds", "dimensionVersionId",
	"questionSummary", "modelVersionIds", "metricVersionIds", "dimensionVersionIds",
	"memberVersionIds", "timeRange", "semanticIr", "planHash", "graphPlanHash",
	"leftPlanHash", "rightPlanHash", "limit", "maxRows", "validationType",
	"conflictCode", "clarificationQuestion", "clarificationOptions",
}

var allowedToolArguments = map[toolhost.ToolName]map[string]bool{
	toolhost.ToolSearchSemanticObjects:   fields("mention", "objectTypes", "domainIds", "limit"),
	toolhost.ToolGetSemanticContracts:    fields("objectVersionIds"),
	toolhost.ToolLookupDimensionValues:   fields("mention", "dimensionVersionId", "limit"),
	toolhost.ToolGetCertifiedExamples:    fields("questionSummary", "domainIds", "limit"),
	toolhost.ToolResolveGraphPlan:        fields("modelVersionIds", "metricVersionIds", "dimensionVersionIds", "memberVersionIds"),
	toolhost.ToolValidateSemanticBundle:  fields("modelVersionIds", "metricVersionIds", "dimensionVersionIds", "memberVersionIds"),
	toolhost.ToolGetDataQualityStatus:    fields("modelVersionIds", "metricVersionIds", "timeRange"),
	toolhost.ToolCompileSemanticQuery:    fields("semanticIr"),
	toolhost.ToolValidateQueryPlan:       fields("planHash"),
	toolhost.ToolProbeJoinCardinality:    fields("graphPlanHash", "timeRange"),
	toolhost.ToolExecuteQueryPlan:        fields("planHash", "maxRows"),
	toolhost.ToolExecuteValidationQuery:  fields("planHash", "validationType"),
	toolhost.ToolCompareCandidateResults: fields("leftPlanHash", "rightPlanHash", "maxRows"),
	toolhost.ToolRequestClarification:    fields("conflictCode", "clarificationQuestion", "clarificationOptions"),
}

func fields(values ...string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}

func strictToolCallSchema(root map[string]any, stage Stage) (map[string]any, error) {
	definitions, ok := root["$defs"].(map[string]any)
	if !ok {
		return nil, errors.New("cognition schema definitions are missing")
	}
	toolCall, ok := definitions["toolCall"].(map[string]any)
	if !ok {
		return nil, errors.New("toolCall definition is missing")
	}
	toolCallProperties, ok := toolCall["properties"].(map[string]any)
	if !ok {
		return nil, errors.New("toolCall properties are missing")
	}
	arguments, ok := definitions["toolArguments"].(map[string]any)
	if !ok {
		return nil, errors.New("toolArguments definition is missing")
	}
	argumentProperties, ok := arguments["properties"].(map[string]any)
	if !ok {
		return nil, errors.New("toolArguments properties are missing")
	}
	toolNames := cognitionToolsForStage(stage)
	if len(toolNames) == 0 {
		return nil, errors.New("tool action is unavailable for this cognition stage")
	}
	branches := make([]any, 0, len(toolNames))
	for _, tool := range toolNames {
		allowed, ok := allowedToolArguments[tool]
		if !ok {
			return nil, fmt.Errorf("tool %s has no argument contract", tool)
		}
		strictArguments := make(map[string]any, len(allowed)+1)
		strictArguments["release"] = argumentProperties["release"]
		for _, name := range toolArgumentFields {
			if allowed[name] {
				strictArguments[name] = argumentProperties[name]
			}
		}
		requiredArguments := []any{"release"}
		for _, name := range toolArgumentFields {
			if allowed[name] {
				requiredArguments = append(requiredArguments, name)
			}
		}
		argumentBranch := map[string]any{
			"type": "object", "additionalProperties": false,
			"required": requiredArguments, "properties": strictArguments,
		}
		properties := make(map[string]any, len(toolCallProperties))
		for name, schema := range toolCallProperties {
			properties[name] = schema
		}
		properties["tool"] = map[string]any{"const": string(tool)}
		properties["arguments"] = argumentBranch
		branches = append(branches, map[string]any{
			"type": "object", "additionalProperties": false,
			"required": toolCall["required"], "properties": properties,
		})
	}
	return map[string]any{"oneOf": branches}, nil
}

func stringValues(values []string) []any {
	result := make([]any, len(values))
	for index, value := range values {
		result[index] = value
	}
	return result
}

func cognitionToolsForStage(stage Stage) []toolhost.ToolName {
	clarification := toolhost.ToolRequestClarification
	switch stage {
	case StageCandidateJudgment:
		return []toolhost.ToolName{
			toolhost.ToolSearchSemanticObjects, toolhost.ToolGetSemanticContracts,
			toolhost.ToolGetCertifiedExamples, toolhost.ToolGetDataQualityStatus, clarification,
		}
	case StageDisambiguation:
		return []toolhost.ToolName{
			toolhost.ToolSearchSemanticObjects, toolhost.ToolGetSemanticContracts,
			toolhost.ToolLookupDimensionValues, toolhost.ToolGetCertifiedExamples,
			toolhost.ToolResolveGraphPlan, toolhost.ToolValidateSemanticBundle, clarification,
		}
	case StagePlanSelection:
		return nil
	case StageAnomalyAnalysis:
		return []toolhost.ToolName{
			toolhost.ToolGetDataQualityStatus, toolhost.ToolValidateQueryPlan,
			toolhost.ToolCompareCandidateResults, clarification,
		}
	case StageResultVerification:
		return []toolhost.ToolName{
			toolhost.ToolGetDataQualityStatus,
			toolhost.ToolCompareCandidateResults, clarification,
		}
	case StageAssetReview, StageFeedbackAttribution, StageReleaseReview:
		return []toolhost.ToolName{toolhost.ToolGetSemanticContracts, toolhost.ToolGetDataQualityStatus}
	default:
		return nil
	}
}

var actionPayloadProperties = []string{
	"toolCall", "understanding", "bindingProposal", "planProposal",
	"anomalyAnalysis", "verification", "finalDecision", "clarification", "block",
}

func actionPayloadProperty(action ActionType) (string, bool) {
	switch action {
	case ActionCallTool:
		return "toolCall", true
	case ActionProposeUnderstanding:
		return "understanding", true
	case ActionProposeBinding:
		return "bindingProposal", true
	case ActionProposePlan:
		return "planProposal", true
	case ActionAnalyzeAnomaly:
		return "anomalyAnalysis", true
	case ActionVerifyResult:
		return "verification", true
	case ActionFinalize:
		return "finalDecision", true
	case ActionClarify:
		return "clarification", true
	case ActionBlock:
		return "block", true
	default:
		return "", false
	}
}

func allowedActions(stage Stage) []ActionType {
	result := make([]ActionType, 0, len(actionOrder))
	for _, action := range actionOrder {
		if stageAllowsAction(stage, action) {
			result = append(result, action)
		}
	}
	return result
}
