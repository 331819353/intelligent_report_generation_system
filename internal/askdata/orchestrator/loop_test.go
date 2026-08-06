package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/cognition"
	"intelligent-report-generation-system/internal/askdata/toolhost"
)

type scriptedLoopCognition struct {
	mu       sync.Mutex
	actions  []cognition.Action
	requests []cognition.RoundRequest
	wait     bool
}

func (runner *scriptedLoopCognition) Execute(
	ctx context.Context,
	request cognition.RoundRequest,
) (cognition.RoundResult, error) {
	runner.mu.Lock()
	index := len(runner.requests)
	runner.requests = append(runner.requests, request)
	runner.mu.Unlock()
	if runner.wait {
		<-ctx.Done()
		return cognition.RoundResult{}, ctx.Err()
	}
	if index >= len(runner.actions) {
		return cognition.RoundResult{}, errors.New("scripted cognition exhausted")
	}
	action := runner.actions[index]
	payload, err := json.Marshal(action)
	if err != nil {
		return cognition.RoundResult{}, err
	}
	return cognition.RoundResult{
		Action: action, ActionHash: askdata.HashBytes(payload),
		AIRequestID: uuid.NewString(), ProviderModel: "synthetic-model", Attempts: 1,
		Usage: ai.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}, nil
}

type fakeLoopTools struct {
	available    []toolhost.ToolName
	evidence     askdata.EvidenceRef
	progress     bool
	status       toolhost.ResponseStatus
	charge       toolhost.BudgetCharge
	responseCall askdata.ID
	responseTool toolhost.ToolName
	calls        []toolhost.Invocation
}

func (tools *fakeLoopTools) AvailableTools(
	_ toolhost.AuthorizationContext,
	budget toolhost.BudgetAllowance,
) ([]toolhost.ToolName, error) {
	result := []toolhost.ToolName{}
	for _, name := range tools.available {
		switch name {
		case toolhost.ToolExecuteQueryPlan:
			if budget.FormalQueriesRemaining < 1 {
				continue
			}
		case toolhost.ToolCompareCandidateResults:
			if budget.FormalQueriesRemaining < 2 {
				continue
			}
		case toolhost.ToolProbeJoinCardinality, toolhost.ToolExecuteValidationQuery:
			if budget.ValidationQueriesRemaining < 1 {
				continue
			}
		}
		if budget.ToolCallsRemaining > 0 {
			result = append(result, name)
		}
	}
	return result, nil
}

func (tools *fakeLoopTools) Execute(
	_ context.Context,
	invocation toolhost.Invocation,
) (toolhost.Execution, error) {
	tools.calls = append(tools.calls, invocation)
	status := tools.status
	if status == "" {
		status = toolhost.ResponseSuccess
	}
	charge := tools.charge
	if charge == (toolhost.BudgetCharge{}) {
		charge = toolhost.BudgetCharge{ToolCalls: 1}
	}
	response := toolhost.Response{
		SchemaVersion: toolhost.SchemaVersion, CallID: invocation.Call.CallID, Tool: invocation.Call.Tool,
		Status: status, MadeProgress: tools.progress,
	}
	if tools.responseCall != "" {
		response.CallID = tools.responseCall
	}
	if tools.responseTool != "" {
		response.Tool = tools.responseTool
	}
	if status == toolhost.ResponseSuccess {
		response.Result = json.RawMessage(`{"candidates":[],"evidenceIds":["search-tool-result"]}`)
		response.EvidenceRefs = []askdata.EvidenceRef{tools.evidence}
		response.ResultHash = askdata.HashBytes(response.Result)
	} else {
		response.Error = &toolhost.ToolError{Code: "TOOL_FAILED", Message: "工具失败。", Retryable: true}
		response.MadeProgress = false
	}
	execution := toolhost.Execution{
		DefinitionHash: askdata.HashBytes([]byte("loop-tool-definition")), Charge: charge,
		DurationMS: 1, Response: response,
	}
	if execution.Validate() != nil {
		return toolhost.Execution{}, errors.New("invalid fake tool execution")
	}
	return execution, nil
}

func TestLoopFastPathStillRequiresOneLLMDecision(t *testing.T) {
	request, conversation := loopRequestFixture(t, StateUnderstanding, cognition.StageUnderstanding)
	action := clarificationAction(cognition.StageUnderstanding, conversation)
	runner := &scriptedLoopCognition{actions: []cognition.Action{action}}
	tools := &fakeLoopTools{available: []toolhost.ToolName{toolhost.ToolRequestClarification}}
	loop, err := NewLoop(runner, tools)
	if err != nil {
		t.Fatal(err)
	}
	result, err := loop.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision.Action.Action != cognition.ActionClarify || result.Usage.LLMCallsUsed != 1 ||
		result.Usage.ToolCallsUsed != 0 || result.Usage.StepCount != 1 || len(result.ToolExecutions) != 0 ||
		len(result.CognitionRounds) != 1 || len(runner.requests) != 1 || len(result.Transcript) != 2 {
		t.Fatalf("unexpected fast path: %#v requests=%d", result, len(runner.requests))
	}
	if len(runner.requests[0].SeenActionHashes) != 0 {
		t.Fatalf("unexpected replay hashes: %#v", runner.requests[0].SeenActionHashes)
	}
}

func TestLoopFeedsTypedToolEvidenceBackToLLM(t *testing.T) {
	request, conversation := loopRequestFixture(t, StateRetrieving, cognition.StageCandidateJudgment)
	toolEvidence := loopEvidence("search-tool-result", askdata.EvidenceKindCandidateSet)
	search := searchToolAction(request, conversation)
	binding := bindingAction(cognition.StageCandidateJudgment, toolEvidence)
	runner := &scriptedLoopCognition{actions: []cognition.Action{search, binding}}
	tools := &fakeLoopTools{
		available: []toolhost.ToolName{toolhost.ToolSearchSemanticObjects},
		evidence:  toolEvidence, progress: true,
	}
	loop, err := NewLoop(runner, tools)
	if err != nil {
		t.Fatal(err)
	}
	result, err := loop.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision.Action.Action != cognition.ActionProposeBinding || result.Usage.LLMCallsUsed != 2 ||
		result.Usage.ToolCallsUsed != 1 || result.Usage.StepCount != 3 || len(result.ToolExecutions) != 1 ||
		len(result.CognitionRounds) != 2 || len(result.Transcript) != 4 ||
		len(result.SeenActionHashes) != 2 || len(result.SeenToolCallIDs) != 1 {
		t.Fatalf("unexpected tool loop result: %#v", result)
	}
	if len(runner.requests) != 2 || len(runner.requests[0].Messages) != 2 || len(runner.requests[1].Messages) != 4 ||
		!reflect.DeepEqual(runner.requests[1].SeenToolCallIDs, []askdata.ID{"call-search-loop"}) {
		t.Fatalf("tool transcript was not returned to cognition: %#v", runner.requests)
	}
	if len(tools.calls) != 1 || tools.calls[0].Call.Tool != toolhost.ToolSearchSemanticObjects ||
		tools.calls[0].Authorization.Scope.Release != request.Run.Release {
		t.Fatalf("unexpected tool invocation: %#v", tools.calls)
	}
	for _, message := range result.Transcript {
		for _, part := range message.Parts {
			if containsUnsafeLoopText(part.Text) {
				t.Fatalf("loop transcript leaked forbidden result: %s", part.Text)
			}
		}
	}
}

func TestLoopRejectsInventedEvidenceAndRepeatedAction(t *testing.T) {
	request, _ := loopRequestFixture(t, StateUnderstanding, cognition.StageUnderstanding)
	invented := loopEvidence("invented-loop-evidence", askdata.EvidenceKindRule)
	action := blockAction(cognition.StageUnderstanding, invented)
	runner := &scriptedLoopCognition{actions: []cognition.Action{action}}
	loop, _ := NewLoop(runner, &fakeLoopTools{})
	if _, err := loop.Run(context.Background(), request); !errors.Is(err, ErrLoopEvidenceRejected) {
		t.Fatalf("invented evidence error = %v", err)
	}

	request, conversation := loopRequestFixture(t, StateUnderstanding, cognition.StageUnderstanding)
	action = clarificationAction(cognition.StageUnderstanding, conversation)
	payload, _ := json.Marshal(action)
	request.SeenActionHashes = []askdata.ContentHash{askdata.HashBytes(payload)}
	runner = &scriptedLoopCognition{actions: []cognition.Action{action}}
	loop, _ = NewLoop(runner, &fakeLoopTools{})
	if _, err := loop.Run(context.Background(), request); !errors.Is(err, ErrLoopNoProgress) {
		t.Fatalf("repeated action error = %v", err)
	}
}

func TestLoopStopsOnNoProgressAndUnavailableBudgetedTool(t *testing.T) {
	request, conversation := loopRequestFixture(t, StateRetrieving, cognition.StageCandidateJudgment)
	runner := &scriptedLoopCognition{actions: []cognition.Action{searchToolAction(request, conversation)}}
	tools := &fakeLoopTools{
		available: []toolhost.ToolName{toolhost.ToolSearchSemanticObjects},
		evidence:  loopEvidence("search-tool-result", askdata.EvidenceKindCandidateSet), progress: false,
	}
	loop, _ := NewLoop(runner, tools)
	result, err := loop.Run(context.Background(), request)
	if !errors.Is(err, ErrLoopNoProgress) || result.Usage.LLMCallsUsed != 1 ||
		result.Usage.ToolCallsUsed != 1 || len(result.ToolExecutions) != 1 {
		t.Fatalf("no-progress result = %#v, %v", result, err)
	}

	request, conversation = loopRequestFixture(t, StateIRReady, cognition.StagePlanSelection)
	request.Run.Usage.FormalQueriesUsed = request.Run.Limits.MaxFormalQueries
	execute := executeQueryToolAction(request, conversation)
	runner = &scriptedLoopCognition{actions: []cognition.Action{execute}}
	tools = &fakeLoopTools{available: []toolhost.ToolName{toolhost.ToolExecuteQueryPlan}}
	loop, _ = NewLoop(runner, tools)
	result, err = loop.Run(context.Background(), request)
	if !errors.Is(err, ErrLoopToolUnavailable) || len(tools.calls) != 0 || result.Usage.LLMCallsUsed != 1 {
		t.Fatalf("budgeted tool result = %#v, %v", result, err)
	}
}

func TestLoopHonorsTotalTimeoutAndCallerCancellation(t *testing.T) {
	request, _ := loopRequestFixture(t, StateUnderstanding, cognition.StageUnderstanding)
	request.Run.Limits.MaxDurationMS = 100
	runner := &scriptedLoopCognition{wait: true}
	loop, _ := NewLoop(runner, &fakeLoopTools{})
	started := time.Now()
	result, err := loop.Run(context.Background(), request)
	if !errors.Is(err, ErrLoopTimeout) || time.Since(started) > time.Second || !result.Usage.Exhausted ||
		result.Usage.LLMCallsUsed != 1 || result.Usage.StepCount != 1 {
		t.Fatalf("timeout result = %#v, %v, duration=%s", result, err, time.Since(started))
	}

	request, _ = loopRequestFixture(t, StateUnderstanding, cognition.StageUnderstanding)
	runner = &scriptedLoopCognition{wait: true}
	loop, _ = NewLoop(runner, &fakeLoopTools{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := loop.Run(ctx, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("caller cancellation error = %v", err)
	}
}

func TestLoopBoundsComplexCorrectionAtFourLLMCalls(t *testing.T) {
	request, conversation := loopRequestFixture(t, StateRetrieving, cognition.StageCandidateJudgment)
	actions := make([]cognition.Action, request.Run.Limits.MaxLLMCalls+1)
	for index := range actions {
		actions[index] = searchToolAction(request, conversation)
		actions[index].ToolCall.CallID = askdata.ID("call-search-loop-" + string(rune('a'+index)))
	}
	runner := &scriptedLoopCognition{actions: actions}
	tools := &fakeLoopTools{
		available: []toolhost.ToolName{toolhost.ToolSearchSemanticObjects},
		evidence:  loopEvidence("search-tool-result", askdata.EvidenceKindCandidateSet), progress: true,
	}
	loop, _ := NewLoop(runner, tools)
	result, err := loop.Run(context.Background(), request)
	if !errors.Is(err, ErrLoopBudgetExhausted) || !result.Usage.Exhausted ||
		result.Usage.LLMCallsUsed != request.Run.Limits.MaxLLMCalls ||
		result.Usage.ToolCallsUsed != request.Run.Limits.MaxLLMCalls ||
		len(tools.calls) != request.Run.Limits.MaxLLMCalls || len(runner.requests) != request.Run.Limits.MaxLLMCalls {
		t.Fatalf("bounded correction result = %#v, %v", result, err)
	}
}

func TestLoopRejectsMismatchedOrOverBudgetToolExecution(t *testing.T) {
	request, conversation := loopRequestFixture(t, StateRetrieving, cognition.StageCandidateJudgment)
	runner := &scriptedLoopCognition{actions: []cognition.Action{searchToolAction(request, conversation)}}
	tools := &fakeLoopTools{
		available: []toolhost.ToolName{toolhost.ToolSearchSemanticObjects},
		evidence:  loopEvidence("search-tool-result", askdata.EvidenceKindCandidateSet), progress: true,
		responseCall: "different-loop-call",
	}
	loop, _ := NewLoop(runner, tools)
	if _, err := loop.Run(context.Background(), request); !errors.Is(err, ErrInvalidLoop) {
		t.Fatalf("mismatched response error = %v", err)
	}

	request, conversation = loopRequestFixture(t, StateRetrieving, cognition.StageCandidateJudgment)
	request.Run.Usage.FormalQueriesUsed = 1
	runner = &scriptedLoopCognition{actions: []cognition.Action{searchToolAction(request, conversation)}}
	tools = &fakeLoopTools{
		available: []toolhost.ToolName{toolhost.ToolSearchSemanticObjects},
		evidence:  loopEvidence("search-tool-result", askdata.EvidenceKindCandidateSet), progress: true,
		charge: toolhost.BudgetCharge{ToolCalls: 1, FormalQueries: 2},
	}
	loop, _ = NewLoop(runner, tools)
	if _, err := loop.Run(context.Background(), request); !errors.Is(err, ErrInvalidLoop) {
		t.Fatalf("over-budget execution error = %v", err)
	}
}

func TestLoopRejectsInvalidStageAndExhaustedLLMBudget(t *testing.T) {
	request, _ := loopRequestFixture(t, StateUnderstanding, cognition.StagePlanSelection)
	loop, _ := NewLoop(&scriptedLoopCognition{}, &fakeLoopTools{})
	if _, err := loop.Run(context.Background(), request); !errors.Is(err, ErrInvalidLoop) {
		t.Fatalf("invalid stage error = %v", err)
	}

	request, _ = loopRequestFixture(t, StateUnderstanding, cognition.StageUnderstanding)
	request.Run.Usage.LLMCallsUsed = request.Run.Limits.MaxLLMCalls
	result, err := loop.Run(context.Background(), request)
	if !errors.Is(err, ErrLoopBudgetExhausted) || !result.Usage.Exhausted {
		t.Fatalf("exhausted result = %#v, %v", result, err)
	}

	request, conversation := loopRequestFixture(t, StateRetrieving, cognition.StageCandidateJudgment)
	runner := &scriptedLoopCognition{actions: []cognition.Action{
		clarificationAction(cognition.StageDisambiguation, conversation),
	}}
	loop, _ = NewLoop(runner, &fakeLoopTools{})
	if _, err := loop.Run(context.Background(), request); !errors.Is(err, ErrInvalidLoop) {
		t.Fatalf("mismatched cognition stage error = %v", err)
	}
}

func loopRequestFixture(
	t *testing.T,
	state State,
	stage cognition.Stage,
) (LoopRequest, askdata.EvidenceRef) {
	t.Helper()
	release := askdata.ReleaseRef{ReleaseID: askdata.ID(uuid.NewString()), ContentHash: askdata.HashBytes([]byte("loop-release"))}
	tenantID, actorID, domainID := askdata.ID(uuid.NewString()), askdata.ID(uuid.NewString()), askdata.ID(uuid.NewString())
	scope, err := askdata.NewPolicyScope(tenantID, actorID, []askdata.ID{domainID}, []askdata.ID{"analyst"}, release)
	if err != nil {
		t.Fatal(err)
	}
	run := Run{
		ID: askdata.ID(uuid.NewString()), TenantID: tenantID, DomainID: domainID, ActorID: actorID,
		TraceID: askdata.ID(uuid.NewString()), IdempotencyKeyHash: askdata.HashBytes([]byte("loop-idempotency")),
		QuestionHash: askdata.HashBytes([]byte("loop-question")), PolicyScopeHash: scope.PolicyHash,
		Release: release, State: state, Disposition: DispositionPending, Limits: DefaultBudgetLimits(),
		RecordVersion: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := run.Validate(); err != nil {
		t.Fatal(err)
	}
	fact, err := cognition.NewPromptFact(
		"conversation-loop", cognition.FactConversation,
		json.RawMessage(`{"questionSummary":"销售额"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	evidence := askdata.EvidenceRef{
		EvidenceID: fact.EvidenceID, Kind: askdata.EvidenceKindConversation,
		SourceID: run.ID, ContentHash: fact.ContentHash,
	}
	permissions := []toolhost.Permission{
		toolhost.PermissionSemanticRead, toolhost.PermissionDimensionValueRead,
		toolhost.PermissionGraphResolve, toolhost.PermissionQualityRead,
		toolhost.PermissionQueryCompile, toolhost.PermissionQueryValidate,
		toolhost.PermissionCardinalityProbe, toolhost.PermissionQueryExecute,
		toolhost.PermissionValidationQueryExecute, toolhost.PermissionClarificationRequest,
	}
	return LoopRequest{
		Run: run, Stage: stage, Facts: []GovernedFact{{Fact: fact, Evidence: evidence}},
		Authorization:    toolhost.AuthorizationContext{Scope: scope, DomainID: domainID, Permissions: permissions},
		SeenActionHashes: []askdata.ContentHash{}, SeenToolCallIDs: []askdata.ID{},
	}, evidence
}

func searchToolAction(request LoopRequest, evidence askdata.EvidenceRef) cognition.Action {
	arguments := toolhost.NewArguments(request.Run.Release)
	mention, limit := "销售额", 10
	arguments.Mention, arguments.Limit = &mention, &limit
	arguments.ObjectTypes = []toolhost.ObjectType{toolhost.ObjectTypeMetric}
	arguments.DomainIDs = []askdata.ID{request.Run.DomainID}
	return cognition.Action{
		SchemaVersion: cognition.SchemaVersion, Stage: request.Stage, Action: cognition.ActionCallTool,
		DecisionSummary: "先检索当前发布版本中的指标候选。", EvidenceRefs: []askdata.EvidenceRef{evidence},
		ToolCall: &toolhost.CallRequest{
			SchemaVersion: toolhost.SchemaVersion, CallID: "call-search-loop",
			Tool: toolhost.ToolSearchSemanticObjects, Arguments: arguments,
		},
	}
}

func executeQueryToolAction(request LoopRequest, evidence askdata.EvidenceRef) cognition.Action {
	arguments := toolhost.NewArguments(request.Run.Release)
	planHash, maxRows := askdata.HashBytes([]byte("loop-plan")), 100
	arguments.PlanHash, arguments.MaxRows = &planHash, &maxRows
	return cognition.Action{
		SchemaVersion: cognition.SchemaVersion, Stage: request.Stage, Action: cognition.ActionCallTool,
		DecisionSummary: "执行已经通过验证的受控查询计划。", EvidenceRefs: []askdata.EvidenceRef{evidence},
		ToolCall: &toolhost.CallRequest{
			SchemaVersion: toolhost.SchemaVersion, CallID: "call-execute-loop",
			Tool: toolhost.ToolExecuteQueryPlan, Arguments: arguments,
		},
	}
}

func bindingAction(stage cognition.Stage, evidence askdata.EvidenceRef) cognition.Action {
	return cognition.Action{
		SchemaVersion: cognition.SchemaVersion, Stage: stage, Action: cognition.ActionProposeBinding,
		DecisionSummary: "候选证据支持绑定已认证销售额指标。", EvidenceRefs: []askdata.EvidenceRef{evidence},
		BindingProposal: &cognition.BindingProposal{
			ModelVersionID:    "model-sales-v1",
			MetricBindings:    []cognition.MetricBinding{{MentionIndex: 0, MetricVersionID: "metric-sales-v1"}},
			DimensionBindings: []cognition.DimensionBinding{}, MemberBindings: []cognition.MemberBinding{},
			Confidence: askdata.ConfidenceEvidence{
				Score: 0.9, Margin: 0.5, Evidence: []askdata.EvidenceRef{evidence},
				ReasonCodes: []string{"CERTIFIED_MATCH"},
			},
		},
	}
}

func clarificationAction(stage cognition.Stage, evidence askdata.EvidenceRef) cognition.Action {
	return cognition.Action{
		SchemaVersion: cognition.SchemaVersion, Stage: stage, Action: cognition.ActionClarify,
		DecisionSummary: "需要用户选择指标含义。", EvidenceRefs: []askdata.EvidenceRef{evidence},
		Clarification: &cognition.Clarification{
			ConflictCode: "METRIC_AMBIGUOUS", Question: "请选择指标口径。",
			Options: []toolhost.ClarificationOption{{
				OptionID: "metric-sales", Label: "销售额", EvidenceRefs: []askdata.EvidenceRef{evidence},
			}},
		},
	}
}

func blockAction(stage cognition.Stage, evidence askdata.EvidenceRef) cognition.Action {
	return cognition.Action{
		SchemaVersion: cognition.SchemaVersion, Stage: stage, Action: cognition.ActionBlock,
		DecisionSummary: "证据不足，停止执行。", EvidenceRefs: []askdata.EvidenceRef{evidence},
		Block: &cognition.BlockDecision{
			Code: "EVIDENCE_MISSING", PublicMessage: "当前证据不足，无法安全回答。",
			EvidenceRefs: []askdata.EvidenceRef{evidence},
		},
	}
}

func loopEvidence(id askdata.ID, kind askdata.EvidenceKind) askdata.EvidenceRef {
	return askdata.EvidenceRef{
		EvidenceID: id, Kind: kind, SourceID: "loop-source",
		ContentHash: askdata.HashBytes([]byte("loop-evidence:" + string(id))),
	}
}

func containsUnsafeLoopText(value string) bool {
	return stringsContainsAny(value, []string{`"rows"`, `"sql"`, `"args"`, `"reasoning"`})
}

func stringsContainsAny(value string, patterns []string) bool {
	for _, pattern := range patterns {
		if len(pattern) > 0 && json.Valid([]byte(value)) && containsString(value, pattern) {
			return true
		}
	}
	return false
}

func containsString(value, pattern string) bool {
	for index := 0; index+len(pattern) <= len(value); index++ {
		if value[index:index+len(pattern)] == pattern {
			return true
		}
	}
	return false
}
