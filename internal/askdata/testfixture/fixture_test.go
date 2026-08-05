package testfixture

import "testing"

func TestStandardFixtureIsValidAndContainsHardCases(t *testing.T) {
	fixture := Standard()
	if err := fixture.Validate(); err != nil {
		t.Fatalf("Standard().Validate() error = %v", err)
	}
	scenarios := map[ScenarioCode]bool{}
	for _, question := range fixture.Questions {
		scenarios[question.Scenario] = true
	}
	for _, required := range []ScenarioCode{ScenarioSameNameMetric, ScenarioSameNameMember, ScenarioUnauthorized, ScenarioJoinFanout, ScenarioEmptyResult, ScenarioExpiredMember} {
		if !scenarios[required] {
			t.Fatalf("missing scenario %s", required)
		}
	}
	metricNames := map[string]int{}
	for _, metric := range fixture.Metrics {
		metricNames[metric.Name]++
	}
	if metricNames["销售额"] < 2 {
		t.Fatal("same-name metric fixture is missing")
	}
	memberLabels := map[string]int{}
	for _, member := range fixture.Members {
		memberLabels[member.Label]++
	}
	if memberLabels["华东"] < 2 {
		t.Fatal("same-name member fixture is missing")
	}
}

func TestFixtureCannotBeMistakenForProductionData(t *testing.T) {
	fixture := Standard()
	fixture.Synthetic = false
	if err := fixture.Validate(); err == nil {
		t.Fatal("Validate() accepted fixture without synthetic marker")
	}
}
