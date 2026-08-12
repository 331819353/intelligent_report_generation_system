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
	"intelligent-report-generation-system/internal/askdata/ircontract"
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
	available      []toolhost.ToolName
	evidence       askdata.EvidenceRef
	progress       bool
	status         toolhost.ResponseStatus
	charge         toolhost.BudgetCharge
	queryScanBytes int64
	responseCall   askdata.ID
	responseTool   toolhost.ToolName
	calls          []toolhost.Invocation
}

type planPipelineTools struct {
	calls          []toolhost.Invocation
	evidence       askdata.EvidenceRef
	queryScanBytes int64
}

type deterministicBindingTools struct {
	calls    []toolhost.Invocation
	evidence askdata.EvidenceRef
}

func (tools *deterministicBindingTools) AvailableTools(
	_ toolhost.AuthorizationContext,
	budget toolhost.BudgetAllowance,
) ([]toolhost.ToolName, error) {
	if budget.ToolCallsRemaining < 1 {
		return []toolhost.ToolName{}, nil
	}
	return []toolhost.ToolName{
		toolhost.ToolSearchSemanticObjects, toolhost.ToolGetSemanticContracts,
		toolhost.ToolResolveGraphPlan, toolhost.ToolValidateSemanticBundle,
	}, nil
}

func (tools *deterministicBindingTools) Execute(
	_ context.Context,
	invocation toolhost.Invocation,
) (toolhost.Execution, error) {
	tools.calls = append(tools.calls, invocation)
	var value any
	switch invocation.Call.Tool {
	case toolhost.ToolSearchSemanticObjects:
		value = toolhost.SearchSemanticObjectsResult{
			Candidates: []toolhost.CandidateSummary{
				{ObjectType: toolhost.ObjectTypeMetric, ObjectVersionID: "metric-sales-v1", Score: 1, MatchType: "EXACT", Status: "CERTIFIED"},
				{ObjectType: toolhost.ObjectTypeModel, ObjectVersionID: "model-sales-v1", Score: 0.9, MatchType: "LEXICAL", Status: "CERTIFIED"},
			},
			EvidenceIDs: []askdata.ID{tools.evidence.EvidenceID},
		}
	case toolhost.ToolGetSemanticContracts:
		value = toolhost.GetSemanticContractsResult{
			Contracts: []toolhost.SemanticContractSummary{
				{ObjectType: toolhost.ObjectTypeMetric, ObjectVersionID: "metric-sales-v1", Name: "销售额", Definition: "销售额", Unit: "CNY", OwnerID: "owner-finance", Status: "CERTIFIED", ContentHash: askdata.HashBytes([]byte("metric-contract"))},
				{ObjectType: toolhost.ObjectTypeModel, ObjectVersionID: "model-sales-v1", Name: "销售模型", Definition: "销售模型", OwnerID: "owner-finance", Status: "CERTIFIED", ContentHash: askdata.HashBytes([]byte("model-contract"))},
			},
			EvidenceIDs: []askdata.ID{tools.evidence.EvidenceID},
		}
	case toolhost.ToolResolveGraphPlan:
		value = toolhost.ResolveGraphPlanResult{
			GraphPlanHash:   askdata.HashBytes([]byte("host-binding-graph")),
			ModelVersionIDs: []askdata.ID{"model-sales-v1"}, RelationshipIDs: []askdata.ID{},
			Risks: []toolhost.GraphRisk{}, EvidenceIDs: []askdata.ID{tools.evidence.EvidenceID},
		}
	case toolhost.ToolValidateSemanticBundle:
		value = toolhost.ValidateSemanticBundleResult{
			Valid: true, MissingObjectVersionIDs: []askdata.ID{}, Conflicts: []toolhost.BundleConflict{},
			ConfidencePermillion: 1_000_000, EvidenceIDs: []askdata.ID{tools.evidence.EvidenceID},
		}
	default:
		return toolhost.Execution{}, errors.New("unexpected deterministic binding tool")
	}
	payload := mustJSON(value)
	execution := toolhost.Execution{
		DefinitionHash: askdata.HashBytes([]byte("host-binding-definition:" + string(invocation.Call.Tool))),
		Charge:         toolhost.BudgetCharge{ToolCalls: 1}, DurationMS: 1,
		Response: toolhost.Response{
			SchemaVersion: toolhost.SchemaVersion, CallID: invocation.Call.CallID, Tool: invocation.Call.Tool,
			Status: toolhost.ResponseSuccess, Result: payload, ResultHash: askdata.HashBytes(payload),
			MadeProgress: true, EvidenceRefs: []askdata.EvidenceRef{tools.evidence},
		},
	}
	if execution.Validate() != nil {
		return toolhost.Execution{}, errors.New("invalid deterministic binding execution")
	}
	return execution, nil
}

func (tools *planPipelineTools) AvailableTools(
	_ toolhost.AuthorizationContext,
	budget toolhost.BudgetAllowance,
) ([]toolhost.ToolName, error) {
	available := []toolhost.ToolName{}
	if budget.ToolCallsRemaining > 0 {
		available = append(available, toolhost.ToolCompileSemanticQuery, toolhost.ToolValidateQueryPlan)
		if budget.FormalQueriesRemaining > 0 {
			available = append(available, toolhost.ToolExecuteQueryPlan)
		}
	}
	return available, nil
}

func (tools *planPipelineTools) Execute(
	_ context.Context,
	invocation toolhost.Invocation,
) (toolhost.Execution, error) {
	tools.calls = append(tools.calls, invocation)
	planHash := askdata.HashBytes([]byte("deterministic-plan"))
	var result any
	charge := toolhost.BudgetCharge{ToolCalls: 1}
	switch invocation.Call.Tool {
	case toolhost.ToolCompileSemanticQuery:
		result = toolhost.CompileSemanticQueryResult{
			PlanHash: planHash, SemanticIRHash: askdata.HashBytes([]byte("semantic-ir")),
			PlanCount: 1, ParameterShapes: []toolhost.ParameterShapeSummary{}, MaxRows: 100,
			EvidenceIDs: []askdata.ID{tools.evidence.EvidenceID},
		}
	case toolhost.ToolValidateQueryPlan:
		result = toolhost.ValidateQueryPlanResult{
			Allowed: true, ValidationHash: askdata.HashBytes([]byte("plan-validation")),
			MaxCost: 1, MaxPlanRows: 1, Risks: []toolhost.PlanRiskSummary{}, EvidenceIDs: []askdata.ID{tools.evidence.EvidenceID},
		}
	case toolhost.ToolExecuteQueryPlan:
		charge.FormalQueries = 1
		result = toolhost.ExecuteQueryPlanResult{
			ResultHash:       askdata.HashBytes([]byte("query-result")),
			VerificationHash: askdata.HashBytes([]byte("query-verification")), Verdict: "PASS",
			RowCount:    1,
			Columns:     []toolhost.ResultColumnSummary{{Code: "sales_amount", CanonicalType: "DECIMAL", NullCount: 0, DistinctCount: 1}},
			Metrics:     []toolhost.ResultMetricSummary{{Code: "sales_amount", NonNullCount: 1, Sum: "100.00"}},
			EvidenceIDs: []askdata.ID{tools.evidence.EvidenceID},
		}
	default:
		return toolhost.Execution{}, errors.New("unexpected plan pipeline tool")
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return toolhost.Execution{}, err
	}
	execution := toolhost.Execution{
		DefinitionHash: askdata.HashBytes([]byte("plan-pipeline-definition:" + string(invocation.Call.Tool))),
		Charge:         charge, DurationMS: 1,
		Response: toolhost.Response{
			SchemaVersion: toolhost.SchemaVersion, CallID: invocation.Call.CallID, Tool: invocation.Call.Tool,
			Status: toolhost.ResponseSuccess, Result: payload, ResultHash: askdata.HashBytes(payload), MadeProgress: true,
			EvidenceRefs: []askdata.EvidenceRef{tools.evidence},
		},
	}
	if invocation.Call.Tool == toolhost.ToolExecuteQueryPlan {
		execution.QueryScanBytes = tools.queryScanBytes
	}
	if execution.Validate() != nil {
		return toolhost.Execution{}, errors.New("invalid plan pipeline execution")
	}
	return execution, nil
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
		switch invocation.Call.Tool {
		case toolhost.ToolResolveGraphPlan:
			response.Result = mustJSON(toolhost.ResolveGraphPlanResult{
				GraphPlanHash:   askdata.HashBytes([]byte("binding-graph-plan")),
				ModelVersionIDs: []askdata.ID{"model-sales-v1"}, RelationshipIDs: []askdata.ID{},
				Risks: []toolhost.GraphRisk{}, EvidenceIDs: []askdata.ID{tools.evidence.EvidenceID},
			})
		case toolhost.ToolValidateSemanticBundle:
			response.Result = mustJSON(toolhost.ValidateSemanticBundleResult{
				Valid: true, MissingObjectVersionIDs: []askdata.ID{}, Conflicts: []toolhost.BundleConflict{},
				ConfidencePermillion: 950_000, EvidenceIDs: []askdata.ID{tools.evidence.EvidenceID},
			})
		default:
			response.Result = json.RawMessage(`{"candidates":[],"evidenceIds":["search-tool-result"]}`)
		}
		response.EvidenceRefs = []askdata.EvidenceRef{tools.evidence}
		response.ResultHash = askdata.HashBytes(response.Result)
	} else {
		response.Error = &toolhost.ToolError{Code: "TOOL_FAILED", Message: "工具失败。", Retryable: true}
		response.MadeProgress = false
	}
	execution := toolhost.Execution{
		DefinitionHash: askdata.HashBytes([]byte("loop-tool-definition")), Charge: charge,
		QueryScanBytes: tools.queryScanBytes, DurationMS: 1, Response: response,
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

func TestLoopUsesDeterministicUnderstandingWhenCalendarRulesAreComplete(t *testing.T) {
	request, _ := loopRequestFixture(t, StateUnderstanding, cognition.StageUnderstanding)
	fact, err := cognition.NewPromptFact(
		"conversation-rules-complete", cognition.FactConversation,
		json.RawMessage(`{"question":"本月销售额是多少"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	evidence := askdata.EvidenceRef{
		EvidenceID: fact.EvidenceID, Kind: askdata.EvidenceKindConversation,
		SourceID: request.Run.ID, ContentHash: fact.ContentHash,
	}
	request.Facts = []GovernedFact{{Fact: fact, Evidence: evidence}}
	runner := &scriptedLoopCognition{}
	loop, err := NewLoop(runner, &fakeLoopTools{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := loop.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.requests) != 0 || len(result.CognitionRounds) != 0 || result.Usage.LLMCallsUsed != 0 ||
		result.Usage.StepCount != 0 || result.Decision.Action.Action != cognition.ActionProposeUnderstanding {
		t.Fatalf("unexpected deterministic understanding: %#v requests=%#v", result, runner.requests)
	}
	checkpoint := LoopCheckpointRequest{
		Scope: request.Authorization.Scope, DomainID: request.Run.DomainID,
		RunID: request.Run.ID, ExpectedVersion: request.Run.RecordVersion,
		CheckpointID: "checkpoint-host-understanding", Stage: cognition.StageUnderstanding,
		TargetState: StateRetrieving, Result: result,
	}
	hash, err := computeLoopCheckpointHash(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := prepareLoopCheckpoint(request.Run, checkpoint, hash); err != nil {
		t.Fatal(err)
	}
}

func TestLoopFeedsTypedToolEvidenceBackToLLM(t *testing.T) {
	request, conversation := loopRequestFixture(t, StateRetrieving, cognition.StageCandidateJudgment)
	toolEvidence := loopEvidence("search-tool-result", askdata.EvidenceKindCandidateSet)
	search := searchToolAction(request, conversation)
	binding := bindingAction(cognition.StageCandidateJudgment, toolEvidence)
	runner := &scriptedLoopCognition{actions: []cognition.Action{search, binding}}
	tools := &fakeLoopTools{
		available: []toolhost.ToolName{
			toolhost.ToolSearchSemanticObjects, toolhost.ToolResolveGraphPlan, toolhost.ToolValidateSemanticBundle,
		},
		evidence: toolEvidence, progress: true,
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
		result.Usage.ToolCallsUsed != 3 || result.Usage.StepCount != 5 || len(result.ToolExecutions) != 3 ||
		len(result.CognitionRounds) != 2 || len(result.Transcript) != 4 ||
		len(result.SeenActionHashes) != 2 || len(result.SeenToolCallIDs) != 3 {
		t.Fatalf("unexpected tool loop result: %#v", result)
	}
	if len(runner.requests) != 2 || len(runner.requests[0].Messages) != 2 || len(runner.requests[1].Messages) != 4 ||
		!reflect.DeepEqual(runner.requests[1].SeenToolCallIDs, []askdata.ID{"call-search-loop"}) {
		t.Fatalf("tool transcript was not returned to cognition: %#v", runner.requests)
	}
	if len(tools.calls) != 3 || tools.calls[0].Call.Tool != toolhost.ToolSearchSemanticObjects ||
		tools.calls[1].Call.Tool != toolhost.ToolResolveGraphPlan ||
		tools.calls[2].Call.Tool != toolhost.ToolValidateSemanticBundle ||
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

func TestLoopAcceptsUnambiguousCertifiedBindingWithoutThirdModelRound(t *testing.T) {
	request, conversation := loopRequestFixture(t, StateRetrieving, cognition.StageCandidateJudgment)
	toolEvidence := loopEvidence("certified-binding-evidence", askdata.EvidenceKindSemanticContract)
	search := searchToolAction(request, conversation)
	contractArguments := toolhost.NewArguments(request.Run.Release)
	contractArguments.ObjectVersionIDs = []askdata.ID{"metric-sales-v1", "model-sales-v1"}
	contracts := cognition.Action{
		SchemaVersion: cognition.SchemaVersion, Stage: request.Stage, Action: cognition.ActionCallTool,
		DecisionSummary: "读取候选对象的已认证语义契约。", EvidenceRefs: []askdata.EvidenceRef{toolEvidence},
		ToolCall: &toolhost.CallRequest{
			SchemaVersion: toolhost.SchemaVersion, CallID: "call-contracts-loop",
			Tool: toolhost.ToolGetSemanticContracts, Arguments: contractArguments,
		},
	}
	runner := &scriptedLoopCognition{actions: []cognition.Action{search, contracts}}
	tools := &deterministicBindingTools{evidence: toolEvidence}
	loop, err := NewLoop(runner, tools)
	if err != nil {
		t.Fatal(err)
	}
	result, err := loop.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.requests) != 1 || len(result.CognitionRounds) != 1 ||
		result.Decision.Action.Action != cognition.ActionProposeBinding || result.Decision.AIRequestID != "" ||
		result.Usage.LLMCallsUsed != 1 || result.Usage.ToolCallsUsed != 4 || result.Usage.StepCount != 5 ||
		len(result.ToolExecutions) != 4 {
		t.Fatalf("unexpected host binding result: %#v requests=%#v", result, runner.requests)
	}
	checkpoint := LoopCheckpointRequest{
		Scope: request.Authorization.Scope, DomainID: request.Run.DomainID,
		RunID: request.Run.ID, ExpectedVersion: request.Run.RecordVersion,
		CheckpointID: "checkpoint-host-binding", Stage: cognition.StageCandidateJudgment,
		TargetState: StateBinding, Result: result,
	}
	hash, err := computeLoopCheckpointHash(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareLoopCheckpoint(request.Run, checkpoint, hash)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.rounds) != 1 || len(prepared.tools) != 4 {
		t.Fatalf("host binding audit = %#v", prepared)
	}
}

func TestLoopDeterministicallyCompilesValidatesAndExecutesProposedPlan(t *testing.T) {
	request, conversation := loopRequestFixture(t, StateIRReady, cognition.StagePlanSelection)
	action := planProposalAction(request, conversation)
	runner := &scriptedLoopCognition{actions: []cognition.Action{action}}
	tools := &planPipelineTools{evidence: conversation}
	loop, err := NewLoop(runner, tools)
	if err != nil {
		t.Fatal(err)
	}
	result, err := loop.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision.Action.Action != cognition.ActionProposePlan || result.Usage.LLMCallsUsed != 1 ||
		result.Usage.ToolCallsUsed != 3 || result.Usage.FormalQueriesUsed != 1 || result.Usage.StepCount != 4 ||
		len(result.CognitionRounds) != 1 || len(result.ToolRequests) != 3 || len(result.ToolExecutions) != 3 ||
		len(result.SeenToolCallIDs) != 3 || len(tools.calls) != 3 {
		t.Fatalf("unexpected deterministic plan result: %#v calls=%#v", result, tools.calls)
	}
	wantTools := []toolhost.ToolName{
		toolhost.ToolCompileSemanticQuery, toolhost.ToolValidateQueryPlan, toolhost.ToolExecuteQueryPlan,
	}
	for index, want := range wantTools {
		if result.ToolRequests[index].Tool != want || tools.calls[index].Call.Tool != want {
			t.Fatalf("plan tool %d = %q/%q, want %q", index, result.ToolRequests[index].Tool, tools.calls[index].Call.Tool, want)
		}
	}
	checkpoint := LoopCheckpointRequest{
		Scope: request.Authorization.Scope, RunID: request.Run.ID, ExpectedVersion: request.Run.RecordVersion,
		CheckpointID: "checkpoint-deterministic-plan", Stage: cognition.StagePlanSelection,
		TargetState: StatePlanValidating, Result: result,
	}
	hash, err := computeLoopCheckpointHash(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareLoopCheckpoint(request.Run, checkpoint, hash)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.rounds) != 1 || len(prepared.tools) != 3 {
		t.Fatalf("deterministic plan audit = %#v", prepared)
	}
}

func TestLoopBuildsPlanFromValidatedBindingWithoutModelRound(t *testing.T) {
	request, _ := loopRequestFixture(t, StateIRReady, cognition.StagePlanSelection)
	bindingPayload, err := json.Marshal(bindingFactPayload(cognition.BindingProposal{
		ModelVersionID:    "model-sales-v1",
		MetricBindings:    []cognition.MetricBinding{{MentionIndex: 0, MetricVersionID: "metric-sales-v1"}},
		DimensionBindings: []cognition.DimensionBinding{}, MemberBindings: []cognition.MemberBinding{},
		Confidence: askdata.ConfidenceEvidence{
			Score: 1, Margin: 1, Evidence: []askdata.EvidenceRef{request.Facts[0].Evidence},
			ReasonCodes: []string{"CERTIFIED_MATCH"},
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	bindingFact, err := cognition.NewPromptFact("binding-plan-loop", cognition.FactBindingEvidence, bindingPayload)
	if err != nil {
		t.Fatal(err)
	}
	bindingEvidence := askdata.EvidenceRef{
		EvidenceID: bindingFact.EvidenceID, Kind: askdata.EvidenceKindRule,
		SourceID: request.Run.ID, ContentHash: bindingFact.ContentHash,
	}
	request.Facts = append(request.Facts, GovernedFact{Fact: bindingFact, Evidence: bindingEvidence})

	runner := &scriptedLoopCognition{}
	tools := &planPipelineTools{evidence: bindingEvidence}
	loop, err := NewLoop(runner, tools)
	if err != nil {
		t.Fatal(err)
	}
	result, err := loop.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.requests) != 0 || result.Usage.LLMCallsUsed != 0 || result.Usage.StepCount != 3 ||
		len(result.CognitionRounds) != 0 || len(result.ToolExecutions) != 3 ||
		result.Decision.Action.Action != cognition.ActionProposePlan || result.Decision.AIRequestID != "" {
		t.Fatalf("unexpected deterministic planner result: %#v requests=%#v", result, runner.requests)
	}
	checkpoint := LoopCheckpointRequest{
		Scope: request.Authorization.Scope, DomainID: request.Run.DomainID,
		RunID: request.Run.ID, ExpectedVersion: request.Run.RecordVersion,
		CheckpointID: "checkpoint-host-built-plan", Stage: cognition.StagePlanSelection,
		TargetState: StatePlanValidating, Result: result,
	}
	hash, err := computeLoopCheckpointHash(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareLoopCheckpoint(request.Run, checkpoint, hash)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.rounds) != 0 || len(prepared.tools) != 3 {
		t.Fatalf("host-built plan audit = %#v", prepared)
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

	request, conversation = loopRequestFixture(t, StateRetrieving, cognition.StageCandidateJudgment)
	request.Run.Usage.ToolCallsUsed = request.Run.Limits.MaxToolCalls
	runner = &scriptedLoopCognition{actions: []cognition.Action{searchToolAction(request, conversation)}}
	tools = &fakeLoopTools{available: []toolhost.ToolName{toolhost.ToolSearchSemanticObjects}}
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

func TestLoopRejectsRepeatedCandidateToolWithoutSpendingTheRunBudget(t *testing.T) {
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
	if !errors.Is(err, ErrLoopNoProgress) || result.Usage.Exhausted ||
		result.Usage.LLMCallsUsed != 2 || result.Usage.ToolCallsUsed != 1 ||
		len(tools.calls) != 1 || len(runner.requests) != 2 {
		t.Fatalf("repeated-tool result = %#v, %v", result, err)
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

func planProposalAction(request LoopRequest, evidence askdata.EvidenceRef) cognition.Action {
	return cognition.Action{
		SchemaVersion: cognition.SchemaVersion, Stage: request.Stage, Action: cognition.ActionProposePlan,
		DecisionSummary: "使用已绑定的认证销售额指标生成受控查询计划。", EvidenceRefs: []askdata.EvidenceRef{evidence},
		PlanProposal: &cognition.PlanProposal{
			SemanticIR: ircontract.SemanticIR{
				IRVersion: ircontract.Version, SemanticReleaseID: request.Run.Release.ReleaseID,
				SemanticContentHash: request.Run.Release.ContentHash, DomainID: request.Run.DomainID,
				ModelVersionID: "model-sales-v1",
				Metrics:        []ircontract.Metric{{MetricVersionID: "metric-sales-v1", Alias: "sales_amount"}},
				GroupBy:        []ircontract.GroupBy{}, Filters: []ircontract.Filter{}, Sort: []ircontract.Sort{},
				Limit: 100, OtherPolicy: ircontract.OtherNone, TieBreaking: ircontract.TieDeterministicCut,
			},
			Confidence: askdata.ConfidenceEvidence{
				Score: 0.95, Margin: 0.8, Evidence: []askdata.EvidenceRef{evidence},
				ReasonCodes: []string{"CERTIFIED_MATCH"},
			},
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

func mustJSON(value any) json.RawMessage {
	payload, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return payload
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
