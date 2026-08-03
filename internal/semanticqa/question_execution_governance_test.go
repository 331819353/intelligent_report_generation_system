package semanticqa

import (
	"strings"
	"testing"

	"intelligent-report-generation-system/internal/metric"
)

func TestAddGovernedExecutionChecksRequiresRegistryAndExplainProof(t *testing.T) {
	plan := readyQuestionPlan("paid_gmv", 7)
	contentHash := strings.Repeat("1", 64)
	registry := QuestionExecutionRegistryProof{
		SemanticContentHash:       contentHash,
		ProjectionResourceVersion: "execution:" + contentHash,
		QualityDecision:           "PASS", FreshnessObservedAt: "2026-08-03T00:00:00Z",
		QualityRuleIDs: []string{"freshness"}, ProofHash: strings.Repeat("2", 64),
	}
	proof := metric.QueryPreflightProof{
		Dialect: "POSTGRESQL", QueryHash: strings.Repeat("3", 64),
		ParameterHash:      strings.Repeat("4", 64),
		DatasetVersionID:   plan.SelectedDatasetVersionID,
		MaterializationIDs: []string{plan.SelectedMaterializationID},
		ParserDecision:     "POSTGRESQL_EXPLAIN_PARSED",
		AllowlistDecision:  "POSTGRESQL_AST_RELATION_ALLOWLIST",
		ExplainDecision:    "COST_WITHIN_BUDGET", EstimatedRows: 1,
		MaximumEstimatedRows: 10, EstimatedTotalCost: 1, MaximumEstimatedCost: 10,
	}
	decision := addGovernedExecutionChecks(
		SQLGuardDecision{Status: "PASS"}, []QueryPlan{plan}, registry,
		map[string][]metric.QueryPreflightProof{plan.ID: {proof}}, contentHash,
	)
	if decision.Status != "PASS" {
		t.Fatalf("valid execution governance proof rejected: %+v", decision)
	}
	proof.ExplainDecision = ""
	decision = addGovernedExecutionChecks(
		SQLGuardDecision{Status: "PASS"}, []QueryPlan{plan}, registry,
		map[string][]metric.QueryPreflightProof{plan.ID: {proof}}, contentHash,
	)
	if decision.Status != "BLOCKED" {
		t.Fatalf("missing EXPLAIN decision must block: %+v", decision)
	}
}

func TestQuestionToolBudgetEnforcesDocumentedLimits(t *testing.T) {
	tracker := questionToolBudgetTracker{budgets: defaultQuestionBudgets()}
	for range tracker.budgets.MaximumExplainQueries {
		if err := tracker.reserve("explain_query_plan"); err != nil {
			t.Fatal(err)
		}
	}
	if err := tracker.reserve("explain_query_plan"); err == nil {
		t.Fatal("EXPLAIN calls beyond the documented budget must be rejected")
	}
}
