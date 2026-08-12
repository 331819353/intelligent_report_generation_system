package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/cognition"
	"intelligent-report-generation-system/internal/askdata/toolhost"
	"intelligent-report-generation-system/internal/config"
)

func TestDefaultRunBudgets(t *testing.T) {
	tests := []struct {
		class              RunBudgetClass
		runType            RunType
		llm, tool, primary int
		validation         int
		hard, p95          time.Duration
		concurrency        int
	}{
		{BudgetClassSingleQueryFast, RunTypeSingleQuery, 4, 6, 1, 1, 60 * time.Second, 30 * time.Second, 0},
		{BudgetClassSingleQueryComplex, RunTypeSingleQuery, 16, 16, 2, 3, 10 * time.Minute, 5 * time.Minute, 0},
		{BudgetClassBundle, RunTypeBundle, 8, 16, 6, 2, 5 * time.Minute, 3 * time.Minute, 4},
		{BudgetClassDefinition, RunTypeDefinition, 2, 4, 0, 0, 30 * time.Second, 15 * time.Second, 0},
	}
	for _, test := range tests {
		t.Run(string(test.class), func(t *testing.T) {
			budget, err := DefaultRunBudget(test.class)
			if err != nil {
				t.Fatalf("DefaultRunBudget() error = %v", err)
			}
			if budget.Validate() != nil || budget.RunType != test.runType ||
				budget.MaxLLMCalls != test.llm || budget.MaxToolCalls != test.tool ||
				budget.MaxPrimaryQueries != test.primary ||
				budget.MaxValidationQueries != test.validation ||
				budget.HardTimeout != test.hard || budget.P95Target != test.p95 ||
				budget.MaxConcurrentPlans != test.concurrency {
				t.Fatalf("budget = %+v", budget)
			}
			limits, err := budget.Limits()
			if err != nil || limits.MaxLLMCalls != test.llm || limits.MaxToolCalls != test.tool ||
				limits.MaxFormalQueries != test.primary || limits.MaxValidationQueries != test.validation ||
				limits.MaxDurationMS != int64(test.hard/time.Millisecond) {
				t.Fatalf("budget limits = %+v, error = %v", limits, err)
			}
		})
	}
}

func TestBudgetCatalogAppliesDomainOverride(t *testing.T) {
	domainID := "11111111-1111-4111-8111-111111111111"
	overrides, err := config.ParseAskDataBudgetOverrides(`[{"domainId":"` + domainID + `","budgetClass":"SINGLE_QUERY_FAST","maxLlmCalls":1,"maxToolCalls":3,"maxPrimaryQueries":1,"maxValidationQueries":1,"maxCandidateCompares":1,"maxJoinHops":2,"hardTimeout":"20s","p95Target":"7s","maxConcurrentPlans":0}]`)
	if err != nil {
		t.Fatalf("ParseAskDataBudgetOverrides() error = %v", err)
	}
	catalog, err := NewBudgetCatalog(overrides)
	if err != nil {
		t.Fatalf("NewBudgetCatalog() error = %v", err)
	}
	overridden, err := catalog.Resolve(askdata.ID(domainID), BudgetClassSingleQueryFast)
	if err != nil {
		t.Fatalf("Resolve(override) error = %v", err)
	}
	if overridden.MaxToolCalls != 3 || overridden.MaxJoinHops != 2 ||
		overridden.HardTimeout != 20*time.Second || overridden.P95Target != 7*time.Second {
		t.Fatalf("overridden budget = %+v", overridden)
	}
	other, err := catalog.Resolve("22222222-2222-4222-8222-222222222222", BudgetClassSingleQueryFast)
	if err != nil {
		t.Fatalf("Resolve(default) error = %v", err)
	}
	if other.MaxToolCalls != 6 || other.HardTimeout != 60*time.Second {
		t.Fatalf("default budget = %+v", other)
	}
}

func TestLoopEnforcesResolvedBudgetClass(t *testing.T) {
	request, conversation := loopRequestFixture(t, StateRetrieving, cognition.StageCandidateJudgment)
	override := config.AskDataBudgetOverride{
		DomainID: string(request.Run.DomainID), BudgetClass: string(BudgetClassSingleQueryFast),
		MaxLLMCalls: 1, MaxToolCalls: 3, MaxPrimaryQueries: 1,
		MaxValidationQueries: 1, MaxCandidateCompares: 1, MaxJoinHops: 2,
		HardTimeout: 20 * time.Second, P95Target: 7 * time.Second,
	}
	catalog, err := NewBudgetCatalog([]config.AskDataBudgetOverride{override})
	if err != nil {
		t.Fatalf("NewBudgetCatalog() error = %v", err)
	}
	runner := &scriptedLoopCognition{actions: []cognition.Action{
		searchToolAction(request, conversation),
		bindingAction(cognition.StageCandidateJudgment, loopEvidence("search-tool-result", askdata.EvidenceKindCandidateSet)),
	}}
	toolEvidence := loopEvidence("search-tool-result", askdata.EvidenceKindCandidateSet)
	tools := &fakeLoopTools{
		available: []toolhost.ToolName{toolhost.ToolSearchSemanticObjects},
		evidence:  toolEvidence, progress: true,
	}
	options := DefaultLoopOptions()
	options.BudgetCatalog = catalog
	loop, err := NewLoop(runner, tools, options)
	if err != nil {
		t.Fatalf("NewLoop() error = %v", err)
	}
	request.BudgetClass = BudgetClassSingleQueryFast
	result, err := loop.Run(context.Background(), request)
	if !errors.Is(err, ErrLoopBudgetExhausted) || !result.Usage.Exhausted ||
		result.Usage.LLMCallsUsed != 1 || result.Usage.ToolCallsUsed != 1 ||
		len(runner.requests) != 1 {
		t.Fatalf("result = %+v, requests = %d, error = %v", result, len(runner.requests), err)
	}
}

func TestActiveBudgetClockExcludesClarificationWait(t *testing.T) {
	started := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	clock, err := NewActiveBudgetClock(started, 2*time.Second)
	if err != nil {
		t.Fatalf("NewActiveBudgetClock() error = %v", err)
	}
	if err := clock.Freeze(started.Add(3 * time.Second)); err != nil {
		t.Fatalf("Freeze() error = %v", err)
	}
	if err := clock.Resume(started.Add(20*time.Minute + 3*time.Second)); err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	elapsed, err := clock.Elapsed(started.Add(20*time.Minute + 5*time.Second))
	if err != nil {
		t.Fatalf("Elapsed() error = %v", err)
	}
	if elapsed != 7*time.Second {
		t.Fatalf("elapsed = %s, want 7s", elapsed)
	}
	if remaining := 25*time.Second - elapsed; remaining != 18*time.Second {
		t.Fatalf("remaining = %s, want 18s", remaining)
	}
}

type recordingBudgetMetrics struct {
	values []BudgetTargetExceededMetric
}

func (recorder *recordingBudgetMetrics) RecordBudgetMetric(_ context.Context, metric BudgetTargetExceededMetric) {
	recorder.values = append(recorder.values, metric)
}

func TestBudgetMonitorP95DoesNotInterrupt(t *testing.T) {
	budget, _ := DefaultRunBudget(BudgetClassDefinition)
	recorder := &recordingBudgetMetrics{}
	monitor, err := NewBudgetMonitor("11111111-1111-4111-8111-111111111111", budget, recorder)
	if err != nil {
		t.Fatalf("NewBudgetMonitor() error = %v", err)
	}
	observation, err := monitor.Observe(context.Background(), RunBudgetUsage{ElapsedMS: 15_001})
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	if !observation.TargetExceeded || observation.HardTimeoutReached || observation.Interrupt ||
		len(recorder.values) != 1 || recorder.values[0].Name != MetricBudgetTargetExceeded {
		t.Fatalf("observation = %+v, metrics = %+v", observation, recorder.values)
	}
	if _, err := monitor.Observe(context.Background(), RunBudgetUsage{ElapsedMS: 16_000}); err != nil {
		t.Fatalf("second Observe() error = %v", err)
	}
	if len(recorder.values) != 1 {
		t.Fatalf("metric count = %d, want 1", len(recorder.values))
	}
	hard, err := monitor.Observe(context.Background(), RunBudgetUsage{ElapsedMS: 30_000})
	if err != nil || !hard.HardTimeoutReached || !hard.Interrupt {
		t.Fatalf("hard observation = %+v, error = %v", hard, err)
	}
}

func TestResolveHardTimeoutUsesOrderedFallback(t *testing.T) {
	tests := []struct {
		name     string
		evidence HardTimeoutEvidence
		want     HardTimeoutOutcome
	}{
		{"usable result", HardTimeoutEvidence{HasUsableResult: true, HasGovernedEvidence: true, ClarificationAvailable: true}, HardTimeoutPartial},
		{"governed clarification", HardTimeoutEvidence{HasGovernedEvidence: true, ClarificationAvailable: true}, HardTimeoutClarification},
		{"no evidence", HardTimeoutEvidence{ClarificationAvailable: true}, HardTimeoutTimeout},
		{"no clarification", HardTimeoutEvidence{HasGovernedEvidence: true}, HardTimeoutTimeout},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ResolveHardTimeout(test.evidence); got != test.want {
				t.Fatalf("ResolveHardTimeout() = %s, want %s", got, test.want)
			}
		})
	}
}

func TestBudgetConsumptionRecordsEveryDimension(t *testing.T) {
	budget, _ := DefaultRunBudget(BudgetClassSingleQueryComplex)
	usage := RunBudgetUsage{
		LLMCallsUsed: 4, ToolCallsUsed: 8, PrimaryQueriesUsed: 2,
		ValidationQueriesUsed: 3, CandidateComparesUsed: 2,
		MaxJoinHopsUsed: 4, ElapsedMS: 301_000,
	}
	consumption, err := SnapshotBudgetConsumption(budget, usage)
	if err != nil {
		t.Fatalf("SnapshotBudgetConsumption() error = %v", err)
	}
	encoded, err := consumption.JSON()
	if err != nil {
		t.Fatalf("JSON() error = %v", err)
	}
	var decoded struct {
		SchemaVersion     string              `json:"schemaVersion"`
		Limits            BudgetLimitSnapshot `json:"limits"`
		Usage             RunBudgetUsage      `json:"usage"`
		P95TargetExceeded bool                `json:"p95TargetExceeded"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if decoded.SchemaVersion != BudgetConsumptionSchemaVersion || decoded.Usage != usage ||
		decoded.Limits.HardTimeoutMS != 600_000 || decoded.Limits.P95TargetMS != 300_000 ||
		decoded.Limits.MaxCandidateCompares != 2 || decoded.Limits.MaxJoinHops != 4 ||
		!decoded.P95TargetExceeded {
		t.Fatalf("consumption = %+v", decoded)
	}
}
