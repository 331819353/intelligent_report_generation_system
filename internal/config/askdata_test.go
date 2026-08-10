package config

import (
	"strings"
	"testing"
	"time"
)

func TestParseAskDataBudgetOverrides(t *testing.T) {
	value := `[{"domainId":"11111111-1111-4111-8111-111111111111","budgetClass":"BUNDLE","maxLlmCalls":2,"maxToolCalls":9,"maxPrimaryQueries":5,"maxValidationQueries":2,"maxCandidateCompares":2,"maxJoinHops":3,"hardTimeout":"29s","p95Target":"24s","maxConcurrentPlans":3}]`
	overrides, err := ParseAskDataBudgetOverrides(value)
	if err != nil {
		t.Fatalf("ParseAskDataBudgetOverrides() error = %v", err)
	}
	if len(overrides) != 1 || overrides[0].BudgetClass != "BUNDLE" ||
		overrides[0].HardTimeout != 29*time.Second || overrides[0].MaxConcurrentPlans != 3 {
		t.Fatalf("overrides = %+v", overrides)
	}
}

func TestParseAskDataBudgetOverridesFailsClosed(t *testing.T) {
	base := `{"domainId":"11111111-1111-4111-8111-111111111111","budgetClass":"DEFINITION","maxLlmCalls":1,"maxToolCalls":2,"maxPrimaryQueries":0,"maxValidationQueries":0,"maxCandidateCompares":0,"maxJoinHops":0,"hardTimeout":"10s","p95Target":"3s","maxConcurrentPlans":0}`
	tests := []struct {
		name, value, code string
	}{
		{"unknown field", `[{"unknown":1}]`, "unknown field"},
		{"duplicate", `[` + base + `,` + base + `]`, "duplicates"},
		{"target beyond hard timeout", `[{
			"domainId":"11111111-1111-4111-8111-111111111111","budgetClass":"DEFINITION",
			"maxLlmCalls":1,"maxToolCalls":2,"maxPrimaryQueries":0,"maxValidationQueries":0,
			"maxCandidateCompares":0,"maxJoinHops":0,"hardTimeout":"10s","p95Target":"11s",
			"maxConcurrentPlans":0}]`, "governed bounds"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseAskDataBudgetOverrides(test.value)
			if err == nil || !strings.Contains(err.Error(), test.code) {
				t.Fatalf("ParseAskDataBudgetOverrides() error = %v", err)
			}
		})
	}
}
