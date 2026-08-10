package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/cognition"
	"intelligent-report-generation-system/internal/askdata/toolhost"
)

func TestPrepareLoopCheckpointBindsEveryDecisionToolAndBudget(t *testing.T) {
	loopRequest, conversation := loopRequestFixture(t, StateRetrieving, cognition.StageCandidateJudgment)
	toolEvidence := loopEvidence("search-tool-result", askdata.EvidenceKindCandidateSet)
	runner := &scriptedLoopCognition{actions: []cognition.Action{
		searchToolAction(loopRequest, conversation),
		bindingAction(cognition.StageCandidateJudgment, toolEvidence),
	}}
	tools := &fakeLoopTools{
		available: []toolhost.ToolName{toolhost.ToolSearchSemanticObjects},
		evidence:  toolEvidence, progress: true,
	}
	loop, _ := NewLoop(runner, tools)
	loopResult, err := loop.Run(context.Background(), loopRequest)
	if err != nil {
		t.Fatal(err)
	}
	request := LoopCheckpointRequest{
		RunID: loopRequest.Run.ID, ExpectedVersion: loopRequest.Run.RecordVersion,
		CheckpointID: "checkpoint-binding-1", Stage: cognition.StageCandidateJudgment,
		TargetState: StateBinding, Result: loopResult,
	}
	checkpointHash, err := computeLoopCheckpointHash(request)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareLoopCheckpoint(loopRequest.Run, request, checkpointHash)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.next.State != StateBinding || prepared.next.Usage != loopResult.Usage ||
		len(prepared.rounds) != 2 || len(prepared.tools) != 1 || prepared.completion != nil {
		t.Fatalf("prepared checkpoint = %#v", prepared)
	}
	tool := prepared.tools[0]
	if tool.call.CallID != "call-search-loop" || tool.call.Tool != toolhost.ToolSearchSemanticObjects ||
		tool.call.Status != string(EventSucceeded) || tool.call.ErrorCode != "" ||
		tool.call.RequestHash == "" || tool.call.ResultHash == "" || tool.call.CallHash == "" ||
		tool.charge != (toolhost.BudgetCharge{ToolCalls: 1}) ||
		len(tool.artifact.EvidenceIDs) != 1 || tool.artifact.EvidenceIDs[0] != toolEvidence.EvidenceID ||
		checkpointSummaryContainsUnsafeText(tool.artifact.Payload) {
		t.Fatalf("prepared tool audit = %#v / %s", tool.call, tool.artifact.Payload)
	}
	tool.call.ID = askdata.ID(uuid.NewString())
	tool.artifact.ID, tool.artifact.Index = askdata.ID(uuid.NewString()), 1
	if err := tool.call.validate(); err != nil {
		t.Fatalf("tool call audit invalid: %v", err)
	}
	if err := tool.artifact.Validate(); err != nil {
		t.Fatalf("tool replay artifact invalid: %v", err)
	}
	id, storedHash, ok := checkpointIdentity(prepared.transitionDetail)
	if !ok || id != request.CheckpointID || storedHash != checkpointHash {
		t.Fatalf("checkpoint identity = %q/%q/%v", id, storedHash, ok)
	}

	bad := request
	bad.Result.Usage.FormalQueriesUsed++
	badHash, _ := computeLoopCheckpointHash(bad)
	if _, err := prepareLoopCheckpoint(loopRequest.Run, bad, badHash); !errors.Is(err, ErrInvalidRun) {
		t.Fatalf("unbacked budget charge error = %v", err)
	}
}

func TestPrepareGraphToolAuditPersistsDegradedEvidence(t *testing.T) {
	loopRequest, conversation := loopRequestFixture(t, StateRetrieving, cognition.StageCandidateJudgment)
	arguments := toolhost.NewArguments(loopRequest.Run.Release)
	arguments.ModelVersionIDs = []askdata.ID{"model-sales-v1"}
	arguments.MetricVersionIDs = []askdata.ID{"metric-sales-v1"}
	action := cognition.Action{ToolCall: &toolhost.CallRequest{
		SchemaVersion: toolhost.SchemaVersion, CallID: "call-graph-loop",
		Tool: toolhost.ToolResolveGraphPlan, Arguments: arguments,
	}}
	result := json.RawMessage(`{"graphPlanHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","modelVersionIds":["model-sales-v1"],"relationshipIds":[],"risks":[],"fallbackUsed":true,"graphDegraded":true,"evidenceIds":["conversation-loop"]}`)
	execution := toolhost.Execution{
		DefinitionHash: askdata.HashBytes([]byte("graph-tool-definition")),
		Charge:         toolhost.BudgetCharge{ToolCalls: 1}, DurationMS: 1,
		Response: toolhost.Response{
			SchemaVersion: toolhost.SchemaVersion, CallID: "call-graph-loop",
			Tool: toolhost.ToolResolveGraphPlan, Status: toolhost.ResponseSuccess,
			Result: result, ResultHash: askdata.HashBytes(result), MadeProgress: true,
			EvidenceRefs: []askdata.EvidenceRef{conversation},
		},
	}
	prepared, err := prepareToolAudit(loopRequest.Run, action, execution)
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.graphDegraded || !strings.Contains(string(prepared.artifact.Payload), `"graphDegraded":true`) {
		t.Fatalf("degraded Evidence artifact = %s", prepared.artifact.Payload)
	}
	details := toolAuditDetails(prepared.call, prepared.graphDegraded)
	if !strings.Contains(string(details), `"graphDegraded":true`) ||
		strings.Contains(string(details), "graph-tool-definition") {
		t.Fatalf("public-safe graph audit details = %s", details)
	}
}

func TestBudgetTerminationChoosesClarificationOrFailsClosed(t *testing.T) {
	loopRequest, _ := loopRequestFixture(t, StateUnderstanding, cognition.StageUnderstanding)
	run := loopRequest.Run
	run.Usage.LLMCallsUsed = run.Limits.MaxLLMCalls
	run.Usage.StepCount = run.Limits.MaxLLMCalls
	usage := run.Usage
	usage.Exhausted = true
	termination, err := BuildBudgetTermination(BudgetTerminationRequest{
		Run: run, Usage: usage, Reason: BudgetStopLLMCalls,
		EvidenceIDs: []askdata.ID{"conversation-loop"}, PreferClarification: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if termination.TargetState != StateClarificationRequired ||
		termination.Completion.Type != ArtifactClarification ||
		termination.Completion.Code != "BUDGET_LLM_CALL_LIMIT" {
		t.Fatalf("clarification termination = %#v", termination)
	}
	request := LoopCheckpointRequest{
		RunID: run.ID, ExpectedVersion: run.RecordVersion, CheckpointID: "checkpoint-budget-1",
		Stage: cognition.StageUnderstanding, TargetState: termination.TargetState,
		Result: LoopResult{Usage: usage}, Failure: ClassifyLoopFailure(ErrLoopBudgetExhausted),
		Completion: &termination.Completion,
	}
	hash, err := computeLoopCheckpointHash(request)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareLoopCheckpoint(run, request, hash)
	if err != nil || !prepared.next.Terminal() || !prepared.next.Usage.Exhausted {
		t.Fatalf("budget checkpoint = %#v, %v", prepared.next, err)
	}

	contextReadyRequest, _ := loopRequestFixture(t, StateContextReady, cognition.StageUnderstanding)
	hardRun := contextReadyRequest.Run
	hardUsage := hardRun.Usage
	hardUsage.ElapsedMS, hardUsage.Exhausted = hardRun.Limits.MaxDurationMS, true
	hard, err := BuildBudgetTermination(BudgetTerminationRequest{
		Run: hardRun, Usage: hardUsage, Reason: BudgetStopDuration,
		PreferClarification: true,
	})
	if err != nil || hard.TargetState != StateBlocked || hard.Completion.Type != ArtifactBlock {
		t.Fatalf("hard termination = %#v, %v", hard, err)
	}

	invalid := usage
	invalid.LLMCallsUsed--
	if _, err := BuildBudgetTermination(BudgetTerminationRequest{
		Run: run, Usage: invalid, Reason: BudgetStopLLMCalls,
	}); !errors.Is(err, ErrInvalidRun) {
		t.Fatalf("unreached budget error = %v", err)
	}
}

func TestLoopFailureClassificationAndCheckpointReplayIdentity(t *testing.T) {
	tests := []struct {
		err    error
		code   string
		status EventStatus
	}{
		{context.Canceled, "LOOP_CANCELED", EventCanceled},
		{ErrLoopTimeout, "LOOP_TIMEOUT", EventBlocked},
		{ErrLoopNoProgress, "LOOP_NO_PROGRESS", EventFailed},
		{ErrLoopToolBlocked, "TOOL_BLOCKED", EventBlocked},
		{ErrInvalidLoop, "LOOP_CONTRACT_REJECTED", EventFailed},
	}
	for _, test := range tests {
		failure := ClassifyLoopFailure(test.err)
		if failure == nil || failure.Code != test.code || failure.Status != test.status || failure.Validate() != nil {
			t.Errorf("ClassifyLoopFailure(%v) = %#v", test.err, failure)
		}
	}
	if ClassifyLoopFailure(nil) != nil {
		t.Fatal("nil failure must remain nil")
	}

	hash := askdata.HashBytes([]byte("checkpoint-replay"))
	event := Event{
		Type: EventStateTransition, RunVersion: 2,
		Details: mustCanonicalAudit(map[string]any{
			"checkpointId": "checkpoint-replay-1", "checkpointHash": hash,
		}),
	}
	snapshot := ReplaySnapshot{Run: Run{RecordVersion: 2}, Events: []Event{event}}
	request := LoopCheckpointRequest{ExpectedVersion: 1, CheckpointID: "checkpoint-replay-1"}
	if replayed, conflict := exactCheckpointReplay(snapshot, request, hash); !replayed || conflict {
		t.Fatalf("exact replay = %v/%v", replayed, conflict)
	}
	if replayed, conflict := exactCheckpointReplay(snapshot, request, askdata.HashBytes([]byte("other"))); replayed || !conflict {
		t.Fatalf("colliding replay = %v/%v", replayed, conflict)
	}
}

func checkpointSummaryContainsUnsafeText(raw json.RawMessage) bool {
	text := strings.ToLower(string(raw))
	return strings.Contains(text, `"sql"`) || strings.Contains(text, `"rows"`) ||
		strings.Contains(text, `"prompt"`) || strings.Contains(text, `"reasoning"`)
}
