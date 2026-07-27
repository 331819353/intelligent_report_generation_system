package semanticqa

import (
	"os"
	"slices"
	"strings"
	"testing"
)

func TestGraphProjectionIncludesImmutableLineageClosure(t *testing.T) {
	raw, err := os.ReadFile("graph_worker.go")
	if err != nil {
		t.Fatalf("read graph worker: %v", err)
	}
	source := string(raw)
	for _, fragment := range []string{
		"WITH RECURSIVE lineage AS (",
		"dependency.dataset_version_id=lineage.id",
		"dependency.source_type='DATASET_VERSION'",
		"dependency.source_id::uuid",
		"JOIN platform.semantic_graph_nodes AS version_node",
		"'dataset_version:'||dependency.dataset_version_id::text",
	} {
		if !strings.Contains(source, fragment) {
			t.Errorf("graph projection is missing immutable lineage fragment %q", fragment)
		}
	}
}

func TestMemberLookupTokensExcludeMetricAndTimeLanguage(t *testing.T) {
	tokens := memberLookupTokens(
		"截止到6月查询华东骑手总数", "骑手总数",
	)
	if !slices.Contains(tokens, "华东") {
		t.Fatalf("business member token is missing: %#v", tokens)
	}
	for _, forbidden := range []string{"6月", "骑手", "总数", "截止"} {
		if slices.Contains(tokens, forbidden) {
			t.Fatalf("non-member token %q escaped filtering: %#v", forbidden, tokens)
		}
	}
}

func TestSelectMetricScopedMemberMatchesSupportsDifferentDimensions(t *testing.T) {
	selected, ambiguous := selectMetricScopedMemberMatches(
		[]scopedMemberMatch{
			{
				MemberValue: "华东", DimensionID: "region-id",
				DimensionCode: "region", DimensionName: "区域",
				MatchedValue: "华东",
			},
			{
				MemberValue: "直营网", DimensionID: "channel-id",
				DimensionCode: "channel", DimensionName: "渠道",
				MatchedValue: "直营网",
			},
		},
		"华东直营网销售额",
	)
	if ambiguous || len(selected) != 2 ||
		selected[0].DimensionCode != "channel" ||
		selected[1].DimensionCode != "region" {
		t.Fatalf("selected=%#v ambiguous=%v", selected, ambiguous)
	}
}

func TestSelectMetricScopedMemberMatchesDoesNotGuessSameValueDimension(t *testing.T) {
	matches := []scopedMemberMatch{
		{
			MemberValue: "直营", DimensionID: "channel-id",
			DimensionCode: "channel", DimensionName: "渠道",
			MatchedValue: "直营",
		},
		{
			MemberValue: "直营", DimensionID: "merchant-type-id",
			DimensionCode: "merchant_type", DimensionName: "商户类型",
			MatchedValue: "直营",
		},
	}
	if _, ambiguous := selectMetricScopedMemberMatches(
		matches, "直营销售额",
	); !ambiguous {
		t.Fatal("same member value across dimensions was guessed")
	}
	selected, ambiguous := selectMetricScopedMemberMatches(
		matches, "渠道为直营的销售额",
	)
	if ambiguous || len(selected) != 1 ||
		selected[0].DimensionCode != "channel" {
		t.Fatalf("selected=%#v ambiguous=%v", selected, ambiguous)
	}
}

func TestSelectMetricScopedMemberMatchesRejectsImplicitMemberSet(t *testing.T) {
	_, ambiguous := selectMetricScopedMemberMatches(
		[]scopedMemberMatch{
			{
				MemberValue: "华东", DimensionID: "region-id",
				DimensionCode: "region", DimensionName: "区域",
				MatchedValue: "华东",
			},
			{
				MemberValue: "华南", DimensionID: "region-id",
				DimensionCode: "region", DimensionName: "区域",
				MatchedValue: "华南",
			},
		},
		"华东和华南销售额",
	)
	if !ambiguous {
		t.Fatal("multi-value filter was silently reduced to one member")
	}
	_, ambiguous = selectMetricScopedMemberMatches(
		[]scopedMemberMatch{
			{
				MemberValue: "华东地区", DimensionID: "region-id",
				DimensionCode: "region", DimensionName: "区域",
				MatchedValue: "华东地区",
			},
			{
				MemberValue: "上海", DimensionID: "region-id",
				DimensionCode: "region", DimensionName: "区域",
				MatchedValue: "上海",
			},
		},
		"华东地区和上海销售额",
	)
	if !ambiguous {
		t.Fatal("different-length member set was silently reduced to one member")
	}
}
