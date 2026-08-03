package semanticgraph

import "testing"

func TestRankJoinPathsRejectsUnsafeAndUsesRiskCost(t *testing.T) {
	paths := []JoinPath{
		pathForTest("fact", "bridge", "dim", 1, 4),
		pathForTest("fact", "wide", "dim", 1, 0),
		{
			VIDs: []string{"fact", "unknown", "dim"},
			Edges: []JoinEdge{{RelationID: "unsafe", Certified: true,
				AllowedForQuery: true, Cardinality: "unknown", BaseCost: 0.1}},
		},
	}
	ranked := RankJoinPaths(paths, 3)
	if len(ranked) != 2 {
		t.Fatalf("ranked paths = %#v", ranked)
	}
	if ranked[0].VIDs[1] != "wide" || ranked[0].Cost >= ranked[1].Cost {
		t.Fatalf("risk order is incorrect: %#v", ranked)
	}
	if ranked[0].PathHash == "" || ranked[0].PathHash == ranked[1].PathHash {
		t.Fatalf("path hashes are not stable identities: %#v", ranked)
	}
}

func TestRankJoinPathsEnforcesFourHopBound(t *testing.T) {
	edges := make([]JoinEdge, 5)
	for index := range edges {
		edges[index] = JoinEdge{RelationID: "edge", Certified: true,
			AllowedForQuery: true, Cardinality: "many_to_one", BaseCost: 1}
	}
	if result := RankJoinPaths([]JoinPath{{
		VIDs: []string{"a", "b", "c", "d", "e", "f"}, Edges: edges,
	}}, 3); len(result) != 0 {
		t.Fatalf("five-hop path was accepted: %#v", result)
	}
}

func pathForTest(first, middle, last string, base, fanout float64) JoinPath {
	return JoinPath{
		VIDs: []string{first, middle, last},
		Edges: []JoinEdge{
			{RelationID: first + "-" + middle, FromVID: first, ToVID: middle,
				Certified: true, AllowedForQuery: true, Cardinality: "many_to_one",
				BaseCost: base, FanoutPenalty: fanout},
			{RelationID: middle + "-" + last, FromVID: middle, ToVID: last,
				Certified: true, AllowedForQuery: true, Cardinality: "many_to_one",
				BaseCost: base},
		},
	}
}
