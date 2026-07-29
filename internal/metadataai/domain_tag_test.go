package metadataai

import "testing"

func TestControlledTagsExcludeBusinessDomain(t *testing.T) {
	if allowedTags["领域:运营"] || allowedTags["领域:企业"] {
		t.Fatal("business domain must not be part of the LLM tag taxonomy")
	}
	if !allowedTags["主题:经营分析"] {
		t.Fatal("non-domain controlled tags must remain available")
	}
}
