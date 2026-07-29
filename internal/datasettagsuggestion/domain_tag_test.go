package datasettagsuggestion

import "testing"

func TestSuggestedCategoryExcludesBusinessDomain(t *testing.T) {
	if suggestedCategory("BUSINESS_DOMAIN") {
		t.Fatal("business domain must be inherited from user context, not suggested")
	}
	if !suggestedCategory("BUSINESS_ENTITY") {
		t.Fatal("business entity suggestions must remain supported")
	}
}
