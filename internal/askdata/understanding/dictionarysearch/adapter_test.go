package dictionarysearch

import (
	"testing"

	"github.com/google/uuid"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/search"
	"intelligent-report-generation-system/internal/askdata/understanding"
)

func TestExactHitsFiltersScopeMentionAndUnsupportedTargets(t *testing.T) {
	domainID := askdata.ID(uuid.NewString())
	release := askdata.ReleaseRef{
		ReleaseID: askdata.ID(uuid.NewString()), ContentHash: askdata.HashBytes([]byte("release")),
	}
	scope, err := askdata.NewPolicyScope(
		askdata.ID(uuid.NewString()), askdata.ID(uuid.NewString()), []askdata.ID{domainID},
		[]askdata.ID{askdata.ID(uuid.NewString())}, release,
	)
	if err != nil {
		t.Fatal(err)
	}
	metricID := askdata.ID(uuid.NewString())
	base := understanding.DictionaryHit{
		DomainID:       domainID,
		OriginalSpan:   understanding.Span{Start: 0, End: 3},
		NormalizedSpan: understanding.Span{Start: 0, End: 3},
		OriginalText:   "销售额", NormalizedText: "销售额", Term: "销售额",
		TermVersionID: askdata.ID(uuid.NewString()), TargetVersionID: metricID,
		TargetObjectType: "METRIC", TargetCode: "sales_total", MatchMode: understanding.DictionaryMatchExact,
		Priority: 100, EvidenceHash: askdata.HashBytes([]byte("term")),
	}
	unsupported := base
	unsupported.TermVersionID, unsupported.TargetObjectType = askdata.ID(uuid.NewString()), "OPERATOR"
	foreign := base
	foreign.TermVersionID, foreign.DomainID = askdata.ID(uuid.NewString()), askdata.ID(uuid.NewString())
	hits, err := ExactHits(scope, "销售额", []understanding.DictionaryHit{base, unsupported, foreign})
	if err != nil || len(hits) != 1 || hits[0].ObjectType != search.ObjectMetric ||
		hits[0].ObjectVersionID != metricID || hits[0].Score != 1 {
		t.Fatalf("ExactHits() = %#v/%v", hits, err)
	}
	none, err := ExactHits(scope, "毛利", []understanding.DictionaryHit{base})
	if err != nil || len(none) != 0 {
		t.Fatalf("different mention = %#v/%v", none, err)
	}
	matches, err := ExactMatches([]understanding.DictionaryHit{base})
	if err != nil || len(matches) != 1 || matches[0].ObjectType != search.ObjectMetric ||
		matches[0].Span != base.OriginalSpan || matches[0].Evidence.SourceID != metricID ||
		matches[0].CanonicalLabel != "sales_total" || matches[0].Evidence.Kind != askdata.EvidenceKindExactAlias {
		t.Fatalf("ExactMatches() = %#v/%v", matches, err)
	}
}
