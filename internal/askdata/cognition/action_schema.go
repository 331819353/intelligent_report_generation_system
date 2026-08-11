package cognition

import (
	_ "embed"

	"intelligent-report-generation-system/internal/ai"
)

// The canonical copy lives in api/schemas/. It is embedded here so a deployed
// binary carries its own provider contract instead of reading the repository
// tree at runtime. TestEmbeddedActionSchemaMatchesTheCanonicalContract keeps the
// two copies from drifting.
//
//go:embed schemas/cognition-action-v1.schema.json
var actionSchemaJSON []byte

// ActionSchemaName identifies the closed action protocol to the provider.
const ActionSchemaName = "cognition_action_v1"

// ActionSchema returns the base action schema every cognition round is bound
// to. SchemaForStage narrows it per stage so the provider cannot emit an action
// that is impossible in the current round.
func ActionSchema() ai.JSONSchema {
	return ai.JSONSchema{
		Name:        ActionSchemaName,
		Description: "AskData cognition action",
		Schema:      append([]byte(nil), actionSchemaJSON...),
	}
}
