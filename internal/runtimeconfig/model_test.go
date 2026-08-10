package runtimeconfig

import (
	"encoding/json"
	"testing"
)

func TestValidateConfigCanonicalizesAndRejectsSecretsOrWrongScopes(t *testing.T) {
	canonical, hash, compatibility, err := ValidateConfig("TENANT", json.RawMessage(`{"degradation.narrativeEnabled":true,"budget.dailyRuns":250}`))
	if err != nil || hash.Validate() != nil || compatibility != "HOT_RELOAD" || string(canonical) != `{"budget.dailyRuns":250,"degradation.narrativeEnabled":true}` {
		t.Fatalf("ValidateConfig() = %s, %s, %s, %v", canonical, hash, compatibility, err)
	}
	for _, raw := range []string{
		`{"database.password":"secret"}`,
		`{"worker.maxConcurrentJobs":2}`,
		`{"budget.dailyRuns":0}`,
		`{"budget.dailyRuns":1.5}`,
		`{"budget.dailyRuns":1,"budget.dailyRuns":2}`,
	} {
		if _, _, _, err = ValidateConfig("TENANT", json.RawMessage(raw)); err == nil {
			t.Fatalf("invalid config accepted: %s", raw)
		}
	}
	_, _, compatibility, err = ValidateConfig("TENANT", json.RawMessage(`{"provider.routingMode":"PRIMARY_FAILOVER"}`))
	if err != nil || compatibility != "NEXT_RESTART" {
		t.Fatalf("restart compatibility = %s, %v", compatibility, err)
	}
}
