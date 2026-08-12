package cognition

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"intelligent-report-generation-system/internal/ai"
)

// 包内嵌入的动作协议必须与 api/schemas 下的权威副本逐字节一致。
// 两份副本一旦漂移，部署出去的二进制就会用一套契约、评审看到另一套。
func TestEmbeddedActionSchemaMatchesTheCanonicalContract(t *testing.T) {
	canonical, err := os.ReadFile("../../../api/schemas/cognition-action-v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimSpace(canonical), bytes.TrimSpace(actionSchemaJSON)) {
		t.Fatal("embedded cognition action schema has drifted from api/schemas/cognition-action-v1.schema.json")
	}
}

// SemanticIR 的 Provider 契约必须覆盖 Go 侧所有无默认值的必填字段。
// 若这里遗漏，模型会按 schema 合法地省略字段，却在本地 Decode 时失败。
func TestSemanticIRSchemaCarriesPinnedScopeAndResultPolicies(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal(actionSchemaJSON, &schema); err != nil {
		t.Fatal(err)
	}
	definitions := schema["$defs"].(map[string]any)
	semanticIR := definitions["semanticIr"].(map[string]any)
	requiredValues := semanticIR["required"].([]any)
	required := make(map[string]bool, len(requiredValues))
	for _, value := range requiredValues {
		required[value.(string)] = true
	}
	properties := semanticIR["properties"].(map[string]any)
	for _, name := range []string{"domainId", "otherPolicy", "tieBreaking"} {
		if !required[name] || properties[name] == nil {
			t.Fatalf("semanticIr schema does not require %s", name)
		}
	}
	limit := properties["limit"].(map[string]any)
	if maximum, ok := limit["maximum"].(float64); !ok || maximum != 1000 {
		t.Fatalf("semanticIr limit maximum = %v, want 1000", limit["maximum"])
	}
}

// 嵌入的 schema 必须能直接作为 Provider 契约使用，并且能被逐阶段收窄。
func TestActionSchemaIsAUsableProviderContractForEveryStage(t *testing.T) {
	base := ActionSchema()
	if err := ai.ValidateProviderRequest(ai.ProviderRequest{
		Messages: []ai.Message{{
			Role:  ai.MessageRoleUser,
			Parts: []ai.ContentPart{{Type: ai.ContentTypeText, Text: "validate"}},
		}},
		ResponseSchema: base,
	}); err != nil {
		t.Fatalf("embedded schema is not a valid provider contract: %v", err)
	}
	for _, stage := range []Stage{
		StageAssetReview, StageUnderstanding, StageCandidateJudgment,
		StageDisambiguation, StagePlanSelection, StageAnomalyAnalysis,
		StageResultVerification, StageFeedbackAttribution, StageReleaseReview,
	} {
		if _, err := SchemaForStage(base, stage); err != nil {
			t.Fatalf("SchemaForStage(%s) error = %v", stage, err)
		}
	}
}

// ActionSchema 必须返回独立副本：调用方修改结果不能污染其他调用。
func TestActionSchemaReturnsAnIsolatedCopy(t *testing.T) {
	first := ActionSchema()
	if len(first.Schema) == 0 {
		t.Fatal("embedded schema is empty")
	}
	first.Schema[0] = 'x'
	if second := ActionSchema(); second.Schema[0] == 'x' {
		t.Fatal("ActionSchema returned mutable shared bytes")
	}
}
