package semanticasset

import (
	"testing"
	"time"
)

func TestEvaluateCatalogReadinessEnablesOnlyCompleteBlockingContracts(t *testing.T) {
	now := time.Date(2026, 8, 3, 10, 0, 0, 0, time.UTC)
	result := evaluateCatalogReadiness(ReadinessSnapshot{
		MetricTotal: 2, MetricReady: 2,
		DimensionPublished: 3, DimensionReady: 3,
		TermActive: 4, TermProjected: 3,
		ParsingRuleTotal: 5, ParsingRuleActive: 5,
		DecisionCount: 8, DecisionGroupTotal: 3, DecisionGroupReady: 2,
		GraphState: "READY", GraphGenerationID: "generation-id",
		GraphGeneration: 7, GraphGenerationState: "READY",
		GraphRequestedVersion: 12, GraphAppliedVersion: 12,
	}, now)
	if result.Status != ReadinessWarn || !result.QuestionEnabled ||
		result.SemanticVersion != "graph:7:event:12" ||
		len(result.BlockerCodes) != 0 || !result.GeneratedAt.Equal(now) {
		t.Fatalf("unexpected readiness: %#v", result)
	}
}

func TestEvaluateCatalogReadinessBlocksStaleGraphAndMissingMetrics(t *testing.T) {
	result := evaluateCatalogReadiness(ReadinessSnapshot{
		MetricTotal: 2, MetricReady: 0,
		DimensionPublished: 1, DimensionReady: 1,
		GraphState: "READY", GraphGenerationID: "generation-id",
		GraphGeneration: 7, GraphGenerationState: "READY",
		GraphRequestedVersion: 12, GraphAppliedVersion: 11,
	}, time.Now())
	if result.Status != ReadinessBlocked || result.QuestionEnabled ||
		result.SemanticVersion != "" || len(result.BlockerCodes) != 2 ||
		result.BlockerCodes[0] != "METRIC_CONTRACT_READY" ||
		result.BlockerCodes[1] != "SEMANTIC_GRAPH_READY" {
		t.Fatalf("unexpected blockers: %#v", result)
	}
}
