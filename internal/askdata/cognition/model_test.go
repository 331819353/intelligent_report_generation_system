package cognition_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/cognition"
	"intelligent-report-generation-system/internal/askdata/toolhost"
)

func TestCognitionCallToolRoundTripAndSchema(t *testing.T) {
	evidence := testEvidence()
	release := askdata.ReleaseRef{ReleaseID: "release-2026-08", ContentHash: askdata.HashBytes([]byte("release"))}
	arguments := toolhost.NewArguments(release)
	mention := "销售额"
	limit := 10
	arguments.Mention = &mention
	arguments.ObjectTypes = []toolhost.ObjectType{toolhost.ObjectTypeMetric}
	arguments.DomainIDs = []askdata.ID{"sales"}
	arguments.Limit = &limit
	action := cognition.Action{
		SchemaVersion:   cognition.SchemaVersion,
		Stage:           cognition.StageCandidateJudgment,
		Action:          cognition.ActionCallTool,
		DecisionSummary: "需要检索已发布且有权限的销售额指标候选。",
		EvidenceRefs:    []askdata.EvidenceRef{evidence},
		ToolCall: &toolhost.CallRequest{
			SchemaVersion: toolhost.SchemaVersion,
			CallID:        "call-search-metric-1",
			Tool:          toolhost.ToolSearchSemanticObjects,
			Arguments:     arguments,
		},
	}
	raw, err := json.Marshal(action)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	decoded, err := cognition.Decode(raw)
	if err != nil {
		t.Fatalf("cognition.Decode() error = %v", err)
	}
	if decoded.ToolCall == nil || decoded.ToolCall.Tool != toolhost.ToolSearchSemanticObjects {
		t.Fatalf("decoded action = %#v", decoded)
	}

	schemaRaw, err := os.ReadFile("../../../api/schemas/cognition-action-v1.schema.json")
	if err != nil {
		t.Fatalf("os.ReadFile(schema) error = %v", err)
	}
	if _, err := ai.ValidateStructuredOutput(ai.JSONSchema{Name: "cognition_action_v1", Schema: schemaRaw}, raw); err != nil {
		t.Fatalf("schema validation error = %v", err)
	}
}

func TestCognitionActionRejectsStageMismatchAndMultiplePayloads(t *testing.T) {
	evidence := testEvidence()
	proposal := &cognition.AnomalyAnalysis{
		Category: cognition.AnomalyData, Summary: "结果为空，需要验证时间覆盖。",
		RecommendedAction: cognition.RecommendRetryValidate,
		EvidenceRefs:      []askdata.EvidenceRef{evidence},
	}
	action := cognition.Action{
		SchemaVersion: cognition.SchemaVersion, Stage: cognition.StageUnderstanding,
		Action: cognition.ActionAnalyzeAnomaly, DecisionSummary: "分析空结果原因。",
		EvidenceRefs: []askdata.EvidenceRef{evidence}, AnomalyAnalysis: proposal,
	}
	if err := action.Validate(); err == nil || !strings.Contains(err.Error(), "does not allow") {
		t.Fatalf("Validate() error = %v, want stage/action rejection", err)
	}

	action.Stage = cognition.StageAnomalyAnalysis
	action.Block = &cognition.BlockDecision{Code: "DATA_UNAVAILABLE", PublicMessage: "数据暂不可用。", EvidenceRefs: []askdata.EvidenceRef{evidence}}
	if err := action.Validate(); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("Validate() error = %v, want multiple payload rejection", err)
	}
}

func TestCognitionActionRejectsUnknownPhysicalFields(t *testing.T) {
	evidence := testEvidence()
	action := cognition.Action{
		SchemaVersion:   cognition.SchemaVersion,
		Stage:           cognition.StageUnderstanding,
		Action:          cognition.ActionBlock,
		DecisionSummary: "输入包含不允许的执行指令。",
		EvidenceRefs:    []askdata.EvidenceRef{evidence},
		Block:           &cognition.BlockDecision{Code: "UNSAFE_REQUEST", PublicMessage: "无法处理该请求。", EvidenceRefs: []askdata.EvidenceRef{evidence}},
	}
	raw, err := json.Marshal(action)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	unsafe := strings.Replace(string(raw), `"decisionSummary":`, `"sql":"select * from orders","decisionSummary":`, 1)
	if _, err := cognition.Decode([]byte(unsafe)); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("Decode() error = %v, want unknown field rejection", err)
	}
}

func testEvidence() askdata.EvidenceRef {
	return askdata.EvidenceRef{
		EvidenceID:  "evidence-question-1",
		Kind:        askdata.EvidenceKindConversation,
		SourceID:    "question-run-1",
		ContentHash: askdata.HashBytes([]byte("sanitized question")),
	}
}
