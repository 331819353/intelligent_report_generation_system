package savedquestion

import (
	"testing"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/ircontract"
)

func TestDependenciesAreStableAndComplete(t *testing.T) {
	ir := ircontract.SemanticIR{
		SemanticReleaseID: "11111111-1111-4111-8111-111111111111",
		Metrics:           []ircontract.Metric{{MetricVersionID: "22222222-2222-4222-8222-222222222222"}},
		GroupBy:           []ircontract.GroupBy{{DimensionVersionID: "33333333-3333-4333-8333-333333333333"}},
		Filters:           []ircontract.Filter{{DimensionVersionID: "33333333-3333-4333-8333-333333333333", MemberVersionIDs: []askdata.ID{"44444444-4444-4444-8444-444444444444"}}},
	}
	dependencies := Dependencies(ir)
	if len(dependencies) != 4 {
		t.Fatalf("Dependencies() = %#v", dependencies)
	}
	for index := 1; index < len(dependencies); index++ {
		if dependencies[index-1].Type+dependencies[index-1].ID > dependencies[index].Type+dependencies[index].ID {
			t.Fatal("dependencies are not stable")
		}
	}
}
