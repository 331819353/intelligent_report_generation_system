package semanticgraph

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRuntimeUsesBoundedParameterizedJoinPathQuery(t *testing.T) {
	executor := &executorForTest{responses: []QueryResult{{
		Rows: []map[string]any{
			{
				"graph_path": rawGraphPath{
					VIDs: []string{"fact", "dim"},
					Edges: []JoinEdge{{
						RelationID: "safe", FromVID: "fact", ToVID: "dim",
						Cardinality: "many_to_one", Certified: true,
						AllowedForQuery: true, BaseCost: 1,
					}},
				},
			},
		},
	}}}
	runtime := NewRuntime(executor)
	paths, evidence, err := runtime.FindJoinPaths(context.Background(), graphScopeForTest(), JoinPathRequest{
		FactDatasetVID: "dataset:fact:1", DimensionDatasetVID: "dataset:dim:1", MaxHops: 4, Limit: 20,
	})
	if err != nil || len(paths) != 1 {
		t.Fatalf("find join paths: paths=%#v err=%v", paths, err)
	}
	if strings.Contains(executor.statements[0], "dataset:fact:1") ||
		!strings.Contains(executor.statements[0], "*1..4") ||
		executor.parameters[0]["fact_vid"] != "dataset:fact:1" {
		t.Fatalf("query is not bounded and parameterized: %s %#v", executor.statements[0], executor.parameters[0])
	}
	if evidence.SemanticVersion != "release-1" || evidence.ContentHash != strings64("scope") {
		t.Fatalf("evidence did not pin release: %#v", evidence)
	}
}

func TestRuntimeFailsClosedOnUncertifiedBundle(t *testing.T) {
	executor := &executorForTest{responses: []QueryResult{{Rows: nil}}}
	runtime := NewRuntime(executor)
	validation, _, err := runtime.ValidateBundle(context.Background(), graphScopeForTest(), Bundle{
		MetricVIDs: []string{"metric:gmv:1"}, DimensionVIDs: []string{"dimension:region:1"},
	})
	if !errors.Is(err, ErrNoCertifiedPath) || validation.Valid {
		t.Fatalf("uncertified bundle result=%#v error=%v", validation, err)
	}
}

func TestNebulaParametersNormalizeFixedWidthIntegers(t *testing.T) {
	parameters, err := normalizeNebulaParameters(map[string]any{
		"timestamp": int64(1785715200), "rank": uint32(7),
		"nested": []any{int32(2), "unchanged"},
	})
	if err != nil {
		t.Fatalf("normalize parameters: %v", err)
	}
	if _, ok := parameters["timestamp"].(int); !ok {
		t.Fatalf("timestamp was not normalized: %#v", parameters)
	}
	if _, ok := parameters["rank"].(int); !ok {
		t.Fatalf("rank was not normalized: %#v", parameters)
	}
	if nested, ok := parameters["nested"].([]any); !ok {
		t.Fatalf("nested list was not preserved: %#v", parameters)
	} else if _, ok := nested[0].(int); !ok {
		t.Fatalf("nested integer was not normalized: %#v", nested)
	}
}

func TestResilientGraphOnlyUsesExactVersionCertifiedCache(t *testing.T) {
	scope := graphScopeForTest()
	request := JoinPathRequest{FactDatasetVID: "fact", DimensionDatasetVID: "dim", MaxHops: 2}
	cache := &cacheForTest{item: CachedGraphPlan{Scope: scope, RequestHash: graphRequestHash(request),
		Paths: []JoinPath{pathForTest("fact", "bridge", "dim", 1, 0)}, Certified: true,
		ExpiresAt: time.Now().Add(time.Minute), Evidence: Evidence{Source: "NEBULA_GRAPH"}}}
	graph := NewResilientGraph(graphUnavailableForTest{}, cache, time.Minute)
	paths, evidence, err := graph.FindJoinPaths(context.Background(), scope, request)
	if err != nil || len(paths) != 1 || !evidence.Cached {
		t.Fatalf("exact cache was not used: %#v %#v %v", paths, evidence, err)
	}
	wrongVersion := scope
	wrongVersion.SemanticVersion = "release-2"
	if _, _, err := graph.FindJoinPaths(context.Background(), wrongVersion, request); !errors.Is(err, ErrGraphUnavailable) {
		t.Fatalf("cross-version cache was accepted: %v", err)
	}
}

type executorForTest struct {
	responses  []QueryResult
	statements []string
	parameters []map[string]any
}

func (executor *executorForTest) Execute(_ context.Context, statement string, parameters map[string]any) (QueryResult, error) {
	executor.statements = append(executor.statements, statement)
	executor.parameters = append(executor.parameters, parameters)
	if len(executor.responses) == 0 {
		return QueryResult{}, nil
	}
	result := executor.responses[0]
	executor.responses = executor.responses[1:]
	return result, nil
}

type graphUnavailableForTest struct{}

func (graphUnavailableForTest) ExpandCandidates(context.Context, Scope, []string) ([]Candidate, Evidence, error) {
	return nil, Evidence{}, ErrGraphUnavailable
}
func (graphUnavailableForTest) ValidateValueOwnership(context.Context, Scope, ValueOwnershipRequest) (ValueOwnership, Evidence, error) {
	return ValueOwnership{}, Evidence{}, ErrGraphUnavailable
}
func (graphUnavailableForTest) ValidateBundle(context.Context, Scope, Bundle) (BundleValidation, Evidence, error) {
	return BundleValidation{}, Evidence{}, ErrGraphUnavailable
}
func (graphUnavailableForTest) FindJoinPaths(context.Context, Scope, JoinPathRequest) ([]JoinPath, Evidence, error) {
	return nil, Evidence{}, ErrGraphUnavailable
}
func (graphUnavailableForTest) FilterAuthorized(context.Context, Scope, AuthorizationRequest) ([]string, Evidence, error) {
	return nil, Evidence{}, ErrGraphUnavailable
}
func (graphUnavailableForTest) ImpactAnalysis(context.Context, Scope, ImpactRequest) ([]ImpactedObject, Evidence, error) {
	return nil, Evidence{}, ErrGraphUnavailable
}

type cacheForTest struct{ item CachedGraphPlan }

func (cache *cacheForTest) Get(_ context.Context, scope Scope, requestHash string) (CachedGraphPlan, bool, error) {
	if cache.item.Scope.TenantID == scope.TenantID && cache.item.Scope.SemanticVersion == scope.SemanticVersion &&
		cache.item.Scope.ContentHash == scope.ContentHash && cache.item.RequestHash == requestHash {
		return cache.item, true, nil
	}
	return CachedGraphPlan{}, false, nil
}
func (cache *cacheForTest) Put(_ context.Context, item CachedGraphPlan) error {
	cache.item = item
	return nil
}

func graphScopeForTest() Scope {
	return Scope{TenantID: "11111111-1111-4111-8111-111111111111", SemanticVersion: "release-1",
		ContentHash: strings64("scope"), RoleIDs: []string{"analyst"}, Purpose: "analytics", EffectiveAt: time.Now().UTC()}
}
