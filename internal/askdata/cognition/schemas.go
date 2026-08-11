package cognition

import (
	"encoding/json"
	"errors"
	"fmt"

	"intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/askdata"
)

var actionOrder = []ActionType{
	ActionCallTool, ActionProposeUnderstanding, ActionProposeBinding, ActionProposePlan,
	ActionAnalyzeAnomaly, ActionVerifyResult, ActionFinalize,
	ActionClarify, ActionBlock,
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
	canonical, err := json.Marshal(root)
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
		return ai.JSONSchema{}, fmt.Errorf("validate cognition stage schema: %w", err)
	}
	return result, nil
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
