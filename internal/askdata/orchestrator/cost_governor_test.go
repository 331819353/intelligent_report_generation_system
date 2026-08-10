package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/cognition"
	"intelligent-report-generation-system/internal/askdata/observability"
	"intelligent-report-generation-system/internal/askdata/toolhost"
)

type fakeLoopCostGovernor struct {
	records   []observability.CostRecord
	checks    []observability.QuotaCheckRequest
	decisions []observability.QuotaDecision
	recordErr error
	checkErr  error
}

func (governor *fakeLoopCostGovernor) RecordCost(
	ctx context.Context,
	record observability.CostRecord,
) (bool, error) {
	if ctx == nil || ctx.Err() != nil {
		return false, errors.New("accounting context is canceled")
	}
	if governor.recordErr != nil {
		return false, governor.recordErr
	}
	if err := record.Validate(); err != nil {
		return false, err
	}
	governor.records = append(governor.records, record)
	return true, nil
}

func (governor *fakeLoopCostGovernor) Check(
	ctx context.Context,
	request observability.QuotaCheckRequest,
) (observability.QuotaDecision, error) {
	if ctx == nil || ctx.Err() != nil {
		return observability.QuotaDecision{}, errors.New("accounting context is canceled")
	}
	governor.checks = append(governor.checks, request)
	if governor.checkErr != nil {
		return observability.QuotaDecision{}, governor.checkErr
	}
	index := len(governor.checks) - 1
	if index < len(governor.decisions) {
		return governor.decisions[index], nil
	}
	return observability.QuotaDecision{Status: observability.QuotaAvailable, Allowed: true}, nil
}

func TestLoopRecordsModelCostAndStopsOnRunCostLimit(t *testing.T) {
	request, evidence := loopRequestFixture(t, StateUnderstanding, cognition.StageUnderstanding)
	decision := runCostExceededDecision(request.Run.ID)
	governor := &fakeLoopCostGovernor{decisions: []observability.QuotaDecision{decision}}
	options := DefaultLoopOptions()
	options.CostGovernor = governor
	loop, err := NewLoop(
		&scriptedLoopCognition{actions: []cognition.Action{clarificationAction(cognition.StageUnderstanding, evidence)}},
		&fakeLoopTools{available: []toolhost.ToolName{toolhost.ToolRequestClarification}},
		options,
	)
	if err != nil {
		t.Fatal(err)
	}

	result, runErr := loop.Run(context.Background(), request)
	var exceeded *RunCostExceededError
	if !errors.Is(runErr, ErrLoopRunCostExceeded) || !errors.As(runErr, &exceeded) ||
		!reflect.DeepEqual(exceeded.Decision, decision) {
		t.Fatalf("cost limit error = %#v, %v", exceeded, runErr)
	}
	if result.Usage.LLMCallsUsed != 1 || len(governor.records) != 1 || len(governor.checks) != 1 {
		t.Fatalf("cost accounting was incomplete: result=%#v records=%#v checks=%#v", result, governor.records, governor.checks)
	}
	record := governor.records[0]
	if record.RunID != request.Run.ID || record.QuestionType != string(BudgetClassSingleQueryComplex) ||
		record.PromptTokens != 10 || record.CompletionTokens != 5 ||
		!strings.HasPrefix(record.Provider, "governed-ai-") || record.Model != "synthetic-model" {
		t.Fatalf("unexpected model cost record: %#v", record)
	}
	if governor.checks[0].RunID != request.Run.ID || governor.checks[0].Reserve != (observability.QuotaUsage{}) {
		t.Fatalf("unexpected cost quota check: %#v", governor.checks[0])
	}

	termination, err := BuildRunCostExceededTermination(request.Run, result.Usage, decision, []askdata.ID{evidence.EvidenceID})
	if err != nil {
		t.Fatal(err)
	}
	if termination.TargetState != StateClarificationRequired ||
		termination.Completion.Code != RunCostExceededCompletionCode ||
		termination.Completion.Type != ArtifactClarification ||
		termination.Completion.SchemaVersion != "run-cost-exceeded-v1" {
		t.Fatalf("unexpected run cost termination: %#v", termination)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(termination.Completion.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload) != 2 || payload["limiters"] == nil || payload["restoreAt"] == nil {
		t.Fatalf("termination leaked an unexpected field: %s", termination.Completion.Payload)
	}
}

func TestLoopRecordsQueryScanCostAndFailsClosedWhenMeasurementIsMissing(t *testing.T) {
	request, conversation := loopRequestFixture(t, StatePlanValidating, cognition.StagePlanSelection)
	queryEvidence := loopEvidence("query-cost-result", askdata.EvidenceKindQueryResult)
	runner := &scriptedLoopCognition{actions: []cognition.Action{
		executeQueryToolAction(request, conversation),
		clarificationAction(cognition.StagePlanSelection, queryEvidence),
	}}
	tools := &fakeLoopTools{
		available: []toolhost.ToolName{toolhost.ToolExecuteQueryPlan}, evidence: queryEvidence,
		progress: true, charge: toolhost.BudgetCharge{ToolCalls: 1, FormalQueries: 1},
		queryScanBytes: 64 << 20,
	}
	governor := &fakeLoopCostGovernor{}
	options := DefaultLoopOptions()
	options.CostGovernor = governor
	loop, err := NewLoop(runner, tools, options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loop.Run(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if len(governor.records) != 3 || len(governor.checks) != 3 {
		t.Fatalf("expected model/query/model accounting, got records=%#v checks=%#v", governor.records, governor.checks)
	}
	queryRecord := governor.records[1]
	if queryRecord.Provider != "warehouse" || queryRecord.Model != string(toolhost.ToolExecuteQueryPlan) ||
		queryRecord.QueryScanBytes != 64<<20 || queryRecord.PromptTokens != 0 || queryRecord.CompletionTokens != 0 {
		t.Fatalf("unexpected query cost record: %#v", queryRecord)
	}

	request, conversation = loopRequestFixture(t, StatePlanValidating, cognition.StagePlanSelection)
	governor = &fakeLoopCostGovernor{}
	options.CostGovernor = governor
	loop, _ = NewLoop(
		&scriptedLoopCognition{actions: []cognition.Action{executeQueryToolAction(request, conversation)}},
		&fakeLoopTools{
			available: []toolhost.ToolName{toolhost.ToolExecuteQueryPlan}, evidence: queryEvidence,
			progress: true, charge: toolhost.BudgetCharge{ToolCalls: 1, FormalQueries: 1},
		},
		options,
	)
	if _, err := loop.Run(context.Background(), request); !errors.Is(err, ErrLoopCostAccountingFailed) {
		t.Fatalf("missing query scan measurement error = %v", err)
	}
	if len(governor.records) != 1 {
		t.Fatalf("only the preceding model call should be charged: %#v", governor.records)
	}
}

func TestLoopCostAccountingFailuresAreFailClosedAndRecordIDsAreStable(t *testing.T) {
	request, evidence := loopRequestFixture(t, StateUnderstanding, cognition.StageUnderstanding)
	options := DefaultLoopOptions()
	options.CostGovernor = &fakeLoopCostGovernor{recordErr: errors.New("ledger unavailable")}
	loop, _ := NewLoop(
		&scriptedLoopCognition{actions: []cognition.Action{clarificationAction(cognition.StageUnderstanding, evidence)}},
		&fakeLoopTools{}, options,
	)
	if _, err := loop.Run(context.Background(), request); !errors.Is(err, ErrLoopCostAccountingFailed) {
		t.Fatalf("record failure error = %v", err)
	}
	first := deterministicCostRecordID(request.Run.ID, "llm", "request-1")
	second := deterministicCostRecordID(request.Run.ID, "llm", "request-1")
	query := deterministicCostRecordID(request.Run.ID, "query", "request-1")
	if first != second || first == query || first.Validate() != nil {
		t.Fatalf("cost record IDs are not stable and isolated: %q %q %q", first, second, query)
	}
}

func TestLoopCostAccountingSurvivesQuestionCancellation(t *testing.T) {
	request, _ := loopRequestFixture(t, StateUnderstanding, cognition.StageUnderstanding)
	governor := &fakeLoopCostGovernor{}
	options := DefaultLoopOptions()
	options.CostGovernor = governor
	loop, err := NewLoop(&scriptedLoopCognition{}, &fakeLoopTools{}, options)
	if err != nil {
		t.Fatal(err)
	}
	questionContext, cancel := context.WithCancel(context.Background())
	cancel()
	now := time.Now().UTC()
	record := observability.CostRecord{
		ID:    deterministicCostRecordID(request.Run.ID, "llm", "account-after-cancel"),
		RunID: request.Run.ID, TenantID: request.Run.TenantID, DomainID: request.Run.DomainID,
		ActorID: request.Run.ActorID, QuestionType: string(BudgetClassSingleQueryComplex),
		Provider: "synthetic", Model: "synthetic-model", PromptTokens: 1, CompletionTokens: 1,
		CreatedAt: now,
	}
	if err := loop.recordAndCheckCost(questionContext, request.Run, record, now); err != nil {
		t.Fatalf("durable accounting must outlive the question context: %v", err)
	}
	if len(governor.records) != 1 || len(governor.checks) != 1 {
		t.Fatalf("accounting was not completed after cancellation: %#v %#v", governor.records, governor.checks)
	}
}

func runCostExceededDecision(runID askdata.ID) observability.QuotaDecision {
	return observability.QuotaDecision{
		Status: observability.QuotaRunCostExceeded, Allowed: false, RequireClarification: true,
		Limiters: []observability.QuotaLimiter{{
			Scope: observability.QuotaScopeRun, ScopeID: runID, Period: observability.QuotaPeriodRun,
			Dimension: observability.QuotaDimensionCostCents, Used: 101, Limit: 100,
			Remaining: 0, PercentUsed: 101, ResetAt: time.Now().UTC().Add(time.Hour), Exceeded: true,
		}},
	}
}
