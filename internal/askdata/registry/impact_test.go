package registry

import (
	"strings"
	"testing"
)

func TestImpactQueriesUseGovernedDependencies(t *testing.T) {
	if !strings.Contains(savedQuestionImpactSQL, "saved_question_dependencies") {
		t.Fatal("saved-question impact must use the dependency index")
	}
	for _, query := range []string{certifiedExampleImpactSQL, kpiBundleImpactSQL, evaluationCaseImpactSQL} {
		if !strings.Contains(query, "status='CERTIFIED'") {
			t.Fatal("uncertified semantic asset can leak into impact analysis")
		}
	}
}

func TestImpactChangeSixKinds(t *testing.T) {
	for _, kind := range []string{"METRIC_VERSION", "DIMENSION_VERSION", "MEMBER_VERSION", "DATASET_VERSION", "SEMANTIC_RELEASE"} {
		if err := (ImpactChange{Kind: kind, ObjectID: "11111111-1111-4111-8111-111111111111"}).Validate(); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
	}
	if err := (ImpactChange{Kind: "COMPONENT_TEMPLATE", ObjectID: "chart@1.0.0"}).Validate(); err != nil {
		t.Fatal(err)
	}
}
