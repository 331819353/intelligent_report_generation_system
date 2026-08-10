package orchestrator

import (
	"testing"

	"intelligent-report-generation-system/internal/askdata"
)

func TestNarrativeSampleIDsRejectInvalidAndDoNotTransformIDs(t *testing.T) {
	values := []askdata.ID{"metric:revenue@v1", "dimension:region@v2"}
	result, err := narrativeSampleIDs(values)
	if err != nil || len(result) != 2 || result[0] != string(values[0]) || result[1] != string(values[1]) {
		t.Fatalf("result=%v err=%v", result, err)
	}
	if _, err := narrativeSampleIDs([]askdata.ID{"bad id with spaces"}); err == nil {
		t.Fatal("invalid semantic ID accepted")
	}
}
