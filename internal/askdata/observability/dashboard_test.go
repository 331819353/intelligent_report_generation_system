package observability

import (
	"encoding/json"
	"testing"
)

func TestQualityCostDashboardIsValidAndCoversGovernedDimensions(t *testing.T) {
	payload, err := QualityCostDashboard()
	if err != nil {
		t.Fatal(err)
	}
	var dashboard struct {
		Panels []struct {
			Title   string `json:"title"`
			Targets []struct {
				Expression string `json:"expr"`
			} `json:"targets"`
		} `json:"panels"`
		Templating struct {
			List []struct {
				Name string `json:"name"`
			} `json:"list"`
		} `json:"templating"`
	}
	if err := json.Unmarshal(payload, &dashboard); err != nil {
		t.Fatal(err)
	}
	if len(dashboard.Panels) != 12 || len(dashboard.Templating.List) != 3 {
		t.Fatalf("dashboard shape = panels:%d variables:%d", len(dashboard.Panels), len(dashboard.Templating.List))
	}
	variables := map[string]bool{}
	for _, variable := range dashboard.Templating.List {
		variables[variable.Name] = true
	}
	for _, required := range []string{"domain", "release", "model"} {
		if !variables[required] {
			t.Fatalf("missing variable %s", required)
		}
	}
}
