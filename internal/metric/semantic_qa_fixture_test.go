package metric

import (
	"os"
	"testing"
)

func TestWorkforceHeadcountMetricFixtureIsGovernedAndExecutable(t *testing.T) {
	raw, err := os.ReadFile(
		"../../testdata/semantic-qa/metric-active-employee-count.json",
	)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := Prepare(raw)
	if err != nil {
		t.Fatal(err)
	}
	definition := prepared.Definition
	if definition.Metric.Code !=
		"metric_dws_employee_profile_regenerated_20260727_em_904c04ae2441" ||
		definition.Metric.Type != "DERIVED" ||
		definition.Aggregation != "NONE" ||
		len(definition.AllowedDimensions) != 3 {
		t.Fatalf("definition=%#v", definition)
	}
}
