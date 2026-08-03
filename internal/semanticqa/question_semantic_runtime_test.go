package semanticqa

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/semanticgraph"
)

type questionGraphForTest struct{ deny bool }

func (graph questionGraphForTest) ExpandCandidates(
	_ context.Context, scope semanticgraph.Scope, starts []string,
) ([]semanticgraph.Candidate, semanticgraph.Evidence, error) {
	return []semanticgraph.Candidate{
		{VID: semanticgraph.StableVID(scope.TenantID, "dimension", "region", "1")},
		{VID: semanticgraph.StableVID(scope.TenantID, "dimension", "paid_at", "1")},
		{VID: semanticgraph.StableVID(scope.TenantID, "dataset", "orders", "1")},
	}, questionGraphEvidence(scope, "expand"), nil
}

func (graph questionGraphForTest) ValidateValueOwnership(
	_ context.Context, scope semanticgraph.Scope, request semanticgraph.ValueOwnershipRequest,
) (semanticgraph.ValueOwnership, semanticgraph.Evidence, error) {
	return semanticgraph.ValueOwnership{
		DimensionVID: request.DimensionVID, ValueVID: request.ValueVID, Certified: true,
	}, questionGraphEvidence(scope, "value"), nil
}

func (graph questionGraphForTest) ValidateBundle(
	_ context.Context, scope semanticgraph.Scope, _ semanticgraph.Bundle,
) (semanticgraph.BundleValidation, semanticgraph.Evidence, error) {
	return semanticgraph.BundleValidation{Valid: true}, questionGraphEvidence(scope, "bundle"), nil
}

func (graph questionGraphForTest) FindJoinPaths(
	_ context.Context, scope semanticgraph.Scope, _ semanticgraph.JoinPathRequest,
) ([]semanticgraph.JoinPath, semanticgraph.Evidence, error) {
	return nil, questionGraphEvidence(scope, "join"), semanticgraph.ErrNoCertifiedPath
}

func (graph questionGraphForTest) FilterAuthorized(
	_ context.Context, scope semanticgraph.Scope, request semanticgraph.AuthorizationRequest,
) ([]string, semanticgraph.Evidence, error) {
	if graph.deny {
		return request.CandidateVIDs[:len(request.CandidateVIDs)-1], questionGraphEvidence(scope, "auth"), nil
	}
	return request.CandidateVIDs, questionGraphEvidence(scope, "auth"), nil
}

func (graph questionGraphForTest) ImpactAnalysis(
	_ context.Context, scope semanticgraph.Scope, _ semanticgraph.ImpactRequest,
) ([]semanticgraph.ImpactedObject, semanticgraph.Evidence, error) {
	return nil, questionGraphEvidence(scope, "impact"), nil
}

func questionGraphEvidence(scope semanticgraph.Scope, operation string) semanticgraph.Evidence {
	return semanticgraph.Evidence{
		Source: "NEBULA_GRAPH", SemanticVersion: scope.SemanticVersion,
		ContentHash: scope.ContentHash, EvidenceID: "graph:" + operation,
	}
}

func questionSemanticFixture(plan QueryPlan) QuestionSemanticSnapshot {
	return QuestionSemanticSnapshot{
		TenantID: "tenant-a", ReleaseID: "11111111-1111-4111-8111-111111111111",
		SemanticVersion: "release-2026-08-03", ContentHash: strings.Repeat("a", 64),
		RoleCodes: []string{"analyst"}, Purpose: "analytics", EffectiveAt: time.Now().UTC(),
		Objects: []QuestionSemanticObject{
			{
				ObjectType: "METRIC", ObjectID: "paid_gmv", ObjectVersion: "1",
				Contract: map[string]any{
					"code": "paid_gmv", "title": "支付GMV",
					"nativeMetricId":         plan.SelectedMetricID,
					"nativeMetricVersionId":  plan.SelectedMetricVersionID,
					"sourceDatasetIds":       []any{"orders"},
					"defaultTimeDimensionId": "paid_at",
				},
			},
			{
				ObjectType: "DIMENSION", ObjectID: "region", ObjectVersion: "1",
				Contract: map[string]any{"code": "region", "title": "区域"},
			},
			{
				ObjectType: "TIME", ObjectID: "paid_at", ObjectVersion: "1",
				Contract: map[string]any{
					"code": "paid_at", "title": "支付时间",
				},
			},
			{
				ObjectType: "DIMENSION_VALUE", ObjectID: "east", ObjectVersion: "1",
				Contract: map[string]any{
					"canonicalCode": "EAST", "dimensionId": "region",
					"aliases": []any{"华东"},
				},
			},
			{
				ObjectType: "DATASET", ObjectID: "orders", ObjectVersion: "1",
				Contract: map[string]any{
					"code": "orders", "nativeDatasetVersionId": plan.SelectedDatasetVersionID,
				},
			},
			{
				ObjectType: "POLICY", ObjectID: "analyst_policy", ObjectVersion: "1",
				Contract: map[string]any{
					"roles": []any{"analyst"}, "purpose": "analytics", "effect": "ALLOW",
				},
			},
		},
	}
}

func TestValidateQuestionSemanticGraphPinsActiveReleaseAndAuthority(t *testing.T) {
	plan := readyQuestionPlan("paid_gmv", 7)
	snapshot := questionSemanticFixture(plan)
	graphPlan, err := validateQuestionSemanticGraph(
		context.Background(), questionGraphForTest{}, snapshot,
		understandQuestion("华东支付GMV", &snapshot), []QueryPlan{plan},
	)
	if err != nil {
		t.Fatalf("validate graph: %v", err)
	}
	if graphPlan.SemanticVersion != snapshot.SemanticVersion ||
		graphPlan.ContentHash != snapshot.ContentHash ||
		!strings.HasPrefix(graphPlan.ID, "graphplan:") ||
		!validHash(strings.TrimPrefix(graphPlan.ID, "graphplan:")) ||
		len(graphPlan.MetricVIDs) != 1 || len(graphPlan.DimensionVIDs) != 2 ||
		len(graphPlan.TimeDimensionVIDs) != 1 ||
		len(graphPlan.ValueBindings) != 1 || len(graphPlan.DatasetVIDs) != 1 ||
		!sameStringSet(graphPlan.AuthorizedVIDs, append(
			append(append([]string{}, graphPlan.MetricVIDs...), graphPlan.DimensionVIDs...),
			append(graphPlan.DatasetVIDs, graphPlan.ValueBindings[0].ValueVID)...,
		)) {
		t.Fatalf("unexpected governed graph plan: %+v", graphPlan)
	}
	_, ir, _, buildErr := buildQuestionContracts(
		QueryTurnPlan{Intent: "METRIC", Plans: []QueryPlan{plan}},
		snapshot.SemanticVersion, defaultQuestionBudgets(),
	)
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	ir.SemanticContentHash = graphPlan.ContentHash
	if guard := validateSemanticExecutionWithGraph(
		[]QueryPlan{plan}, ir, defaultQuestionBudgets(), &graphPlan,
	); guard.Status != "PASS" {
		t.Fatalf("valid graph plan must pass execution guard: %+v", guard)
	}
}

func TestValidateQuestionSemanticGraphFailsClosedOnPolicyPropagation(t *testing.T) {
	plan := readyQuestionPlan("paid_gmv", 7)
	snapshot := questionSemanticFixture(plan)
	_, err := validateQuestionSemanticGraph(
		context.Background(), questionGraphForTest{deny: true}, snapshot,
		QuestionUnderstanding{}, []QueryPlan{plan},
	)
	failure := &questionSemanticFailure{}
	if !errors.As(err, &failure) || failure.Code != "POLICY_DENIED" {
		t.Fatalf("partial graph authorization must fail closed: %v", err)
	}
}

func TestValidateGovernedMetricAmbiguityChecksTopBundlesBeforeClarifying(t *testing.T) {
	plan := readyQuestionPlan("paid_gmv", 7)
	snapshot := questionSemanticFixture(plan)
	snapshot.Objects[0].Contract["aliases"] = []any{"销售额"}
	snapshot.Objects = append(snapshot.Objects, QuestionSemanticObject{
		ObjectType: "METRIC", ObjectID: "recognized_revenue", ObjectVersion: "1",
		Contract: map[string]any{
			"code": "recognized_revenue", "title": "确认收入", "aliases": []any{"销售额"},
			"defaultTimeDimensionId": "paid_at", "sourceDatasetIds": []any{"orders"},
		},
	})
	understanding := understandQuestion("销售额", &snapshot)
	clarification, err := validateGovernedMetricAmbiguity(
		context.Background(), questionGraphForTest{}, snapshot, understanding,
		[]QueryPlan{plan}, nil,
	)
	if err != nil || clarification == nil || len(clarification.MetricCandidates) != 2 ||
		clarification.MetricCandidates[0].MatchMethod != "GOVERNED_EXACT_ALIAS_GRAPH_VALIDATED" {
		t.Fatalf("two legal graph bundles must clarify: clarification=%+v err=%v", clarification, err)
	}
	clarification, err = validateGovernedMetricAmbiguity(
		context.Background(), questionGraphForTest{}, snapshot, understanding,
		[]QueryPlan{plan}, []string{"paid_gmv"},
	)
	if err != nil || clarification != nil {
		t.Fatalf("confirmed legal graph bundle must proceed: clarification=%+v err=%v", clarification, err)
	}
}
