package graph

import (
	"strings"
	"testing"
)

func TestQueryBuilderUsesOnlyFixedBoundedTemplates(t *testing.T) {
	request, err := graphTestRequest(t).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	metricQuery, err := buildMetricModelQuery(request)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(metricQuery.statement, "MATCH (metric:metric)-[modeled:MODELED_BY]->(model:semantic_model)") ||
		!strings.Contains(metricQuery.statement, `"tenant-graph-a:metric:metric-orders:1"`) ||
		!strings.Contains(metricQuery.statement, "modeled.release_hash == $release_hash") {
		t.Fatalf("metric query is missing fixed scope constraints:\n%s", metricQuery.statement)
	}
	assertQueryParameters(t, request, metricQuery)

	dimensionQuery, ok, err := buildDimensionQuery(request, request.ModelRefs)
	if err != nil || !ok {
		t.Fatalf("dimension query: ok=%t err=%v", ok, err)
	}
	if !strings.Contains(dimensionQuery.statement, "[compatible:HAS_DIMENSION]") ||
		!strings.Contains(dimensionQuery.statement, "compatible.tenant_id == $tenant_id") {
		t.Fatalf("dimension query is not scoped:\n%s", dimensionQuery.statement)
	}
	assertQueryParameters(t, request, dimensionQuery)

	memberQuery, ok, err := buildMemberQuery(request)
	if err != nil || !ok {
		t.Fatalf("member query: ok=%t err=%v", ok, err)
	}
	if !strings.Contains(memberQuery.statement, "[owns:HAS_MEMBER]") ||
		!strings.Contains(memberQuery.statement, "owns.release_hash == $release_hash") {
		t.Fatalf("member query is not scoped:\n%s", memberQuery.statement)
	}
	assertQueryParameters(t, request, memberQuery)

	pathQuery, ok, err := buildJoinPathQuery(request, request.ModelRefs)
	if err != nil || !ok {
		t.Fatalf("path query: ok=%t err=%v", ok, err)
	}
	for _, required := range []string{
		"[joins:JOINS_TO*1..3]", "ALL(model IN nodes(join_path)",
		"id(model) IN", "join_edge.certified == true", "LIMIT 16",
		"length(join_path) AS path_length", "ORDER BY path_length, source_vid, target_vid",
	} {
		if !strings.Contains(pathQuery.statement, required) {
			t.Fatalf("path query missing %q:\n%s", required, pathQuery.statement)
		}
	}
	if strings.Contains(pathQuery.statement, "JOINS_TO*]") || strings.Contains(pathQuery.statement, "JOINS_TO*]-") {
		t.Fatalf("path traversal is unbounded:\n%s", pathQuery.statement)
	}
	assertQueryParameters(t, request, pathQuery)
}

func TestQueryBuilderRejectsInjectionBeforeCompiling(t *testing.T) {
	request := graphTestRequest(t)
	request.ModelRefs[0].ObjectID = `model\"]; MATCH (secret)--(leak); --`
	if _, err := request.Normalize(); err == nil {
		t.Fatal("injection-shaped stable ID was accepted")
	}
	request = graphTestRequest(t)
	request.MaxJoinHops = 99
	if _, err := request.Normalize(); err == nil {
		t.Fatal("unbounded traversal was accepted")
	}
}

func assertQueryParameters(t *testing.T, request PlanRequest, query compiledQuery) {
	t.Helper()
	if query.parameters["tenant_id"] != string(request.Scope.TenantID) ||
		query.parameters["domain_id"] != string(request.DomainID) ||
		query.parameters["release_hash"] != string(request.Scope.Release.ContentHash) {
		t.Fatalf("query parameters do not match scope: %#v", query.parameters)
	}
	if strings.Contains(query.statement, string(request.Scope.Release.ContentHash)) {
		t.Fatal("release hash was interpolated instead of bound")
	}
}
