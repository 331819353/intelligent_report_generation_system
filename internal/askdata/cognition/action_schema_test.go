package cognition

import (
	"bytes"
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
