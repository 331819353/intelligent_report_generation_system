package observability

import (
	"encoding/json"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/askdata"
)

func TestParseBudgetUsageEvidenceReusesPersistedContract(t *testing.T) {
	evidence, err := ParseBudgetUsageEvidence(json.RawMessage(`{
		"schemaVersion":"run-budget-consumption-v1","runType":"SINGLE_QUERY",
		"budgetClass":"SINGLE_QUERY_COMPLEX",
		"limits":{"maxLlmCalls":4,"maxToolCalls":8,"maxPrimaryQueries":2,"maxValidationQueries":3,"maxCandidateCompares":2,"maxJoinHops":4,"hardTimeoutMs":25000,"p95TargetMs":18000,"maxConcurrentPlans":0},
		"usage":{"llmCallsUsed":2,"toolCallsUsed":3,"primaryQueriesUsed":1,"validationQueriesUsed":1,"candidateComparesUsed":2,"maxJoinHopsUsed":1,"elapsedMs":5000},
		"p95TargetExceeded":false
	}`))
	if err != nil || evidence.LLMCallsUsed != 2 || evidence.PrimaryQueries != 1 || evidence.ValidationQueries != 1 {
		t.Fatalf("unexpected evidence: %+v err=%v", evidence, err)
	}
	if _, err = ParseBudgetUsageEvidence(json.RawMessage(`{"schemaVersion":"wrong","runType":"x","budgetClass":"x","usage":{}}`)); err == nil {
		t.Fatal("foreign budget contract must be rejected")
	}
}

func TestAggregateCostsSupportsFourGovernanceDimensions(t *testing.T) {
	records := []CostRecord{
		costRecord("00000000-0000-4000-8000-000000000011", "00000000-0000-4000-8000-000000000101", "FAST", 2),
		costRecord("00000000-0000-4000-8000-000000000012", "00000000-0000-4000-8000-000000000101", "FAST", 3),
		costRecord("00000000-0000-4000-8000-000000000013", "00000000-0000-4000-8000-000000000102", "COMPLEX", 5),
	}
	for _, dimension := range []CostGroupDimension{CostByTenant, CostByDomain, CostByUser, CostByQuestionType} {
		aggregates, err := AggregateCosts(records, dimension)
		if err != nil || len(aggregates) == 0 {
			t.Fatalf("dimension %s failed: aggregates=%+v err=%v", dimension, aggregates, err)
		}
		var total int64
		for _, aggregate := range aggregates {
			total += aggregate.CostCents
		}
		if total != 10 {
			t.Fatalf("dimension %s lost cost: %d", dimension, total)
		}
	}
}

func costRecord(id, actor, questionType string, cents int64) CostRecord {
	return CostRecord{
		ID: askdata.ID(id), RunID: askdata.ID("00000000-0000-4000-8000-000000000020"),
		TenantID: askdata.ID("00000000-0000-4000-8000-000000000001"),
		DomainID: askdata.ID("00000000-0000-4000-8000-000000000002"),
		ActorID:  askdata.ID(actor), QuestionType: questionType,
		Provider: "openai", Model: "governed-model", PromptTokens: 10,
		CompletionTokens: 5, CostCents: cents, CreatedAt: time.Now().UTC(),
	}
}
