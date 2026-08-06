package cognition

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/toolhost"
)

type recordingInvoker struct {
	invocation ai.Invocation
	result     ai.InvocationResult
	err        error
}

func (invoker *recordingInvoker) Invoke(_ context.Context, input ai.Invocation) (ai.InvocationResult, error) {
	invoker.invocation = input
	return invoker.result, invoker.err
}

func TestExecutorUsesSemanticQuestionPurposeAndReturnsOnlyStructuredAction(t *testing.T) {
	action := validBlockAction(StageUnderstanding)
	raw, err := json.Marshal(action)
	if err != nil {
		t.Fatal(err)
	}
	invoker := &recordingInvoker{result: ai.InvocationResult{
		RequestID: "ai-request-1", Attempts: 1,
		ProviderResult: ai.ProviderResult{
			Content: raw, Model: "deepseek-v4-flash",
			Usage: ai.Usage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120},
		},
	}}
	executor := newTestExecutor(t, invoker)
	result, err := executor.Execute(context.Background(), validRoundRequest(StageUnderstanding))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if invoker.invocation.Purpose != ai.PurposeSemanticQuestion {
		t.Fatalf("purpose = %q", invoker.invocation.Purpose)
	}
	var schemaRoot map[string]any
	if err := json.Unmarshal(invoker.invocation.Request.ResponseSchema.Schema, &schemaRoot); err != nil {
		t.Fatal(err)
	}
	properties := schemaRoot["properties"].(map[string]any)
	if got := properties["stage"].(map[string]any)["const"]; got != string(StageUnderstanding) {
		t.Fatalf("provider stage schema const = %#v", got)
	}
	if result.Action.Action != ActionBlock || result.ProviderModel != "deepseek-v4-flash" {
		t.Fatalf("result = %#v", result)
	}
	if err := result.ActionHash.Validate(); err != nil {
		t.Fatalf("action hash = %q: %v", result.ActionHash, err)
	}
	if strings.Contains(string(raw), "reasoning_content") {
		t.Fatal("test fixture unexpectedly contains reasoning content")
	}
}

func TestExecutorFailsClosedForUnknownStageActionAndNoProgress(t *testing.T) {
	action := validBlockAction(StageUnderstanding)
	raw, _ := json.Marshal(action)

	t.Run("unknown action", func(t *testing.T) {
		invalid := strings.Replace(string(raw), `"action":"BLOCK"`, `"action":"INVENT_SQL"`, 1)
		invoker := &recordingInvoker{result: ai.InvocationResult{ProviderResult: ai.ProviderResult{Content: []byte(invalid)}}}
		_, err := newTestExecutor(t, invoker).Execute(context.Background(), validRoundRequest(StageUnderstanding))
		assertProviderCode(t, err, ai.ErrorCodeInvalidOutput)
	})

	t.Run("stage mismatch", func(t *testing.T) {
		invoker := &recordingInvoker{result: ai.InvocationResult{ProviderResult: ai.ProviderResult{Content: raw}}}
		_, err := newTestExecutor(t, invoker).Execute(context.Background(), validRoundRequest(StageCandidateJudgment))
		assertProviderCode(t, err, ai.ErrorCodeInvalidOutput)
	})

	t.Run("length finish", func(t *testing.T) {
		invoker := &recordingInvoker{result: ai.InvocationResult{ProviderResult: ai.ProviderResult{Content: raw, FinishReason: "length"}}}
		_, err := newTestExecutor(t, invoker).Execute(context.Background(), validRoundRequest(StageUnderstanding))
		assertProviderCode(t, err, ai.ErrorCodeInvalidOutput)
	})

	t.Run("same action hash", func(t *testing.T) {
		invoker := &recordingInvoker{result: ai.InvocationResult{ProviderResult: ai.ProviderResult{Content: raw}}}
		executor := newTestExecutor(t, invoker)
		first, err := executor.Execute(context.Background(), validRoundRequest(StageUnderstanding))
		if err != nil {
			t.Fatal(err)
		}
		request := validRoundRequest(StageUnderstanding)
		request.SeenActionHashes = []askdata.ContentHash{first.ActionHash}
		_, err = executor.Execute(context.Background(), request)
		assertProviderCode(t, err, ai.ErrorCodeToolNoProgress)
	})
}

func TestAssistantAndToolMessagesRoundTripThroughAIValidation(t *testing.T) {
	action := validBlockAction(StageUnderstanding)
	assistant, err := AssistantMessage(RoundResult{Action: action})
	if err != nil {
		t.Fatal(err)
	}
	result := json.RawMessage(`{"candidates":[]}`)
	toolResponse := toolhost.Response{
		SchemaVersion: toolhost.SchemaVersion,
		CallID:        "call-search-1", Tool: toolhost.ToolSearchSemanticObjects,
		Status: toolhost.ResponseSuccess, Result: result,
		EvidenceRefs: []askdata.EvidenceRef{{
			EvidenceID: "search-result", Kind: askdata.EvidenceKindCandidateSet,
			SourceID: "release-search-v1", ContentHash: askdata.HashBytes([]byte("search-result")),
		}},
		ResultHash: askdata.HashBytes(result), MadeProgress: true,
	}
	toolMessage, err := ToolMessage(toolResponse)
	if err != nil {
		t.Fatal(err)
	}
	request := ai.ProviderRequest{
		Messages: []ai.Message{
			{Role: ai.MessageRoleUser, Parts: []ai.ContentPart{{Type: ai.ContentTypeText, Text: "识别销售额"}}},
			assistant,
			toolMessage,
		},
		ResponseSchema: readActionSchema(t), MaxOutputTokens: 8_192,
	}
	if err := ai.ValidateProviderRequest(request); err != nil {
		t.Fatalf("ValidateProviderRequest() error = %v", err)
	}
	if toolMessage.ToolCallID != "call-search-1" || toolMessage.ToolName != string(toolhost.ToolSearchSemanticObjects) {
		t.Fatalf("tool message identity = %#v", toolMessage)
	}
}

func newTestExecutor(t *testing.T, invoker Invoker) *Executor {
	t.Helper()
	executor, err := NewExecutor(invoker, readActionSchema(t), ExecutorOptions{})
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}
	return executor
}

func readActionSchema(t *testing.T) ai.JSONSchema {
	t.Helper()
	raw, err := os.ReadFile("../../../api/schemas/cognition-action-v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	return ai.JSONSchema{Name: "cognition_action_v1", Description: "AskData cognition action", Schema: raw}
}

func validRoundRequest(stage Stage) RoundRequest {
	return RoundRequest{
		TenantID: "tenant-1", ActorID: "actor-1", Stage: stage,
		PromptVersion: "askdata-cognition-v1", ResourceType: "QUESTION_RUN", ResourceID: "run-1",
		Messages: []ai.Message{{
			Role:  ai.MessageRoleUser,
			Parts: []ai.ContentPart{{Type: ai.ContentTypeText, Text: "识别销售额"}},
		}},
	}
}

func validBlockAction(stage Stage) Action {
	evidence := askdata.EvidenceRef{
		EvidenceID: "evidence-1", Kind: askdata.EvidenceKindPolicy,
		SourceID: "policy-1", ContentHash: askdata.HashBytes([]byte("policy")),
	}
	return Action{
		SchemaVersion: SchemaVersion, Stage: stage, Action: ActionBlock,
		DecisionSummary: "当前证据不足，停止本轮。", EvidenceRefs: []askdata.EvidenceRef{evidence},
		Block: &BlockDecision{
			Code: "INSUFFICIENT_EVIDENCE", PublicMessage: "当前证据不足。",
			EvidenceRefs: []askdata.EvidenceRef{evidence},
		},
	}
}

func assertProviderCode(t *testing.T, err error, expected ai.ErrorCode) {
	t.Helper()
	var providerErr *ai.ProviderError
	if !errors.As(err, &providerErr) || providerErr.Code != expected {
		t.Fatalf("error = %v, want provider code %s", err, expected)
	}
}
