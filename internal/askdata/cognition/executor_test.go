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

type scriptedInvoker struct {
	invocations []ai.Invocation
	results     []ai.InvocationResult
	errors      []error
	models      []string
}

func (invoker *scriptedInvoker) ConfiguredModels() []string {
	return append([]string(nil), invoker.models...)
}

func (invoker *scriptedInvoker) Invoke(_ context.Context, input ai.Invocation) (ai.InvocationResult, error) {
	invoker.invocations = append(invoker.invocations, input)
	index := len(invoker.invocations) - 1
	if index >= len(invoker.results) || index >= len(invoker.errors) {
		return ai.InvocationResult{}, errors.New("unexpected invocation")
	}
	return invoker.results[index], invoker.errors[index]
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
	branches := schemaRoot["oneOf"].([]any)
	if len(branches) == 0 {
		t.Fatal("provider stage schema has no action branches")
	}
	for _, value := range branches {
		properties := value.(map[string]any)["properties"].(map[string]any)
		if got := properties["stage"].(map[string]any)["const"]; got != string(StageUnderstanding) {
			t.Fatalf("provider stage schema const = %#v", got)
		}
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

func TestExecutorRepairsOneInvalidStructuredCandidateWithAFreshAuditedInvocation(t *testing.T) {
	action := validBlockAction(StageUnderstanding)
	valid, err := json.Marshal(action)
	if err != nil {
		t.Fatal(err)
	}
	stageSchema, err := SchemaForStage(readActionSchema(t), StageUnderstanding)
	if err != nil {
		t.Fatal(err)
	}
	_, invalid := ai.ValidateStructuredOutput(stageSchema, []byte(`{"schemaVersion":"1.0"}`))
	if invalid == nil {
		t.Fatal("invalid fixture unexpectedly passed the stage schema")
	}
	invoker := &scriptedInvoker{
		results: []ai.InvocationResult{{}, {
			RequestID: "ai-request-repair", Attempts: 1,
			ProviderResult: ai.ProviderResult{
				Content: valid, Model: "deepseek-v4-flash", FinishReason: "stop",
				Usage: ai.Usage{PromptTokens: 120, CompletionTokens: 30, TotalTokens: 150},
			},
		}},
		errors: []error{invalid, nil},
	}
	result, err := newTestExecutor(t, invoker).Execute(context.Background(), validRoundRequest(StageUnderstanding))
	if err != nil || result.AIRequestID != "ai-request-repair" || len(invoker.invocations) != 2 {
		t.Fatalf("repair result = %#v, calls=%d, err=%v", result, len(invoker.invocations), err)
	}
	second := invoker.invocations[1]
	if second.PreferredModel != "" || len(second.Request.Messages) != 3 ||
		second.Request.Messages[1].Role != ai.MessageRoleAssistant ||
		!strings.Contains(second.Request.Messages[2].Parts[0].Text, "只修正 JSON 结构") {
		t.Fatalf("repair invocation = %#v", second)
	}
}

func TestExecutorRetriesMalformedProviderEnvelopeThroughTheModelPool(t *testing.T) {
	action := validBlockAction(StageUnderstanding)
	valid, err := json.Marshal(action)
	if err != nil {
		t.Fatal(err)
	}
	malformedEnvelope := &ai.ProviderError{
		Code: ai.ErrorCodeInvalidResponse, Message: "AI provider returned an invalid response",
	}
	invoker := &scriptedInvoker{
		models: []string{"deepseek-v4-flash", "glm-5.2"},
		results: []ai.InvocationResult{{
			ProviderResult: ai.ProviderResult{Model: "glm-5.2"},
		}, {
			RequestID: "ai-request-fallback", Attempts: 1,
			ProviderResult: ai.ProviderResult{
				Content: valid, Model: "deepseek-v4-flash", FinishReason: "stop",
				Usage: ai.Usage{PromptTokens: 120, CompletionTokens: 30, TotalTokens: 150},
			},
		}},
		errors: []error{malformedEnvelope, nil},
	}
	result, err := newTestExecutor(t, invoker).Execute(context.Background(), validRoundRequest(StageUnderstanding))
	if err != nil || result.ProviderModel != "deepseek-v4-flash" || len(invoker.invocations) != 2 {
		t.Fatalf("fallback result = %#v, calls=%d, err=%v", result, len(invoker.invocations), err)
	}
	if invoker.invocations[1].PreferredModel != "deepseek-v4-flash" || len(invoker.invocations[1].Request.Messages) != 1 {
		t.Fatalf("fallback invocation = %#v", invoker.invocations[1])
	}
}

func TestExecutorRestartsOnAlternateModelWhenStructuredRepairFails(t *testing.T) {
	action := validBlockAction(StageUnderstanding)
	valid, err := json.Marshal(action)
	if err != nil {
		t.Fatal(err)
	}
	stageSchema, err := SchemaForStage(readActionSchema(t), StageUnderstanding)
	if err != nil {
		t.Fatal(err)
	}
	_, invalid := ai.ValidateStructuredOutput(stageSchema, []byte(`{"schemaVersion":"1.0"}`))
	if invalid == nil {
		t.Fatal("invalid fixture unexpectedly passed the stage schema")
	}
	invoker := &scriptedInvoker{
		models: []string{"deepseek-v4-flash", "glm-5.2"},
		results: []ai.InvocationResult{
			{ProviderResult: ai.ProviderResult{Model: "deepseek-v4-flash"}},
			{ProviderResult: ai.ProviderResult{Model: "deepseek-v4-flash"}},
			{RequestID: "ai-request-alternate", ProviderResult: ai.ProviderResult{Content: valid, Model: "glm-5.2", FinishReason: "stop"}},
		},
		errors: []error{invalid, invalid, nil},
	}
	result, err := newTestExecutor(t, invoker).Execute(context.Background(), validRoundRequest(StageUnderstanding))
	if err != nil || result.AIRequestID != "ai-request-alternate" || len(invoker.invocations) != 3 {
		t.Fatalf("alternate result = %#v, calls=%d, err=%v", result, len(invoker.invocations), err)
	}
	if invoker.invocations[1].PreferredModel != "deepseek-v4-flash" ||
		invoker.invocations[2].PreferredModel != "glm-5.2" ||
		len(invoker.invocations[2].Request.Messages) != 1 {
		t.Fatalf("alternate invocations = %#v", invoker.invocations)
	}
}

func TestExecutorRepairsCandidateThatFailsTypedToolContract(t *testing.T) {
	evidence := validBlockAction(StageCandidateJudgment).EvidenceRefs[0]
	release := askdata.ReleaseRef{ReleaseID: "release-1", ContentHash: askdata.HashBytes([]byte("release-1"))}
	arguments := toolhost.NewArguments(release)
	arguments.ObjectTypes = []toolhost.ObjectType{toolhost.ObjectTypeMetric}
	arguments.DomainIDs = []askdata.ID{"domain-1"}
	limit := 10
	arguments.Limit = &limit
	invalid := Action{
		SchemaVersion: SchemaVersion, Stage: StageCandidateJudgment, Action: ActionCallTool,
		DecisionSummary: "检索候选。", EvidenceRefs: []askdata.EvidenceRef{evidence},
		ToolCall: &toolhost.CallRequest{
			SchemaVersion: toolhost.SchemaVersion, CallID: "call-search-1",
			Tool: toolhost.ToolSearchSemanticObjects, Arguments: arguments,
		},
	}
	invalidRaw, err := json.Marshal(invalid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(invalidRaw); err == nil || !strings.Contains(err.Error(), "required argument fields") {
		t.Fatalf("invalid fixture error = %v", err)
	}
	repaired := validBlockAction(StageCandidateJudgment)
	repairedRaw, err := json.Marshal(repaired)
	if err != nil {
		t.Fatal(err)
	}
	invoker := &scriptedInvoker{
		results: []ai.InvocationResult{
			{RequestID: "ai-request-original", ProviderResult: ai.ProviderResult{Content: invalidRaw, Model: "deepseek-v4-flash", FinishReason: "stop"}},
			{RequestID: "ai-request-repair", ProviderResult: ai.ProviderResult{Content: repairedRaw, Model: "deepseek-v4-flash", FinishReason: "stop"}},
		},
		errors: []error{nil, nil},
	}
	result, err := newTestExecutor(t, invoker).Execute(context.Background(), validRoundRequest(StageCandidateJudgment))
	if err != nil || result.AIRequestID != "ai-request-repair" || result.Action.Action != ActionBlock {
		t.Fatalf("typed repair result = %#v, err=%v", result, err)
	}
	if len(invoker.invocations) != 2 || invoker.invocations[1].PreferredModel != "deepseek-v4-flash" ||
		len(invoker.invocations[1].Request.Messages) != 3 ||
		!strings.Contains(invoker.invocations[1].Request.Messages[2].Parts[0].Text, "工具参数") {
		t.Fatalf("typed repair invocation = %#v", invoker.invocations)
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
	if toolMessage.Role != ai.MessageRoleUser || toolMessage.ToolCallID != "" || toolMessage.ToolName != "" ||
		!strings.HasPrefix(toolMessage.Parts[0].Text, "GOVERNED_TOOL_RESULT\n") {
		t.Fatalf("governed tool result message = %#v", toolMessage)
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
