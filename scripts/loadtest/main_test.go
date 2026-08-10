package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/askdata/observability"
)

func TestCapacityLoadIsRepeatableAndDoesNotNeedResponseBodies(t *testing.T) {
	var mutex sync.Mutex
	keys := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer governed-token" ||
			request.Header.Get("X-Business-Domain-Id") != "00000000-0000-0000-0000-000000000001" {
			t.Errorf("governed request headers are missing: %#v", request.Header)
		}
		key := request.Header.Get("Idempotency-Key")
		mutex.Lock()
		if key == "" || keys[key] {
			t.Errorf("POST idempotency key is missing or reused: %q", key)
		}
		keys[key] = true
		mutex.Unlock()
		writer.Header().Set("X-AskData-Recall-At-K", "0.995")
		writer.Header().Set("X-AskData-Degraded", "true")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"sensitive":"this body must never enter the report"}`))
	}))
	defer server.Close()
	config := completeLoadConfig(server.URL)
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	parsed, base, err := parseLoadConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	report, err := executeLoad(context.Background(), server.Client(), parsed, base, raw, time.Now().UTC(), "governed-token")
	if err != nil {
		t.Fatal(err)
	}
	if err := observability.ValidateCapacityReport(report); err != nil {
		t.Fatal(err)
	}
	if len(report.Scenarios) != 7 {
		t.Fatalf("unexpected scenario count: %d", len(report.Scenarios))
	}
	if len(keys) != 28 {
		t.Fatalf("each POST must have a unique idempotency key: %d", len(keys))
	}
	for _, result := range report.Scenarios {
		if result.Requests != 4 || result.Succeeded != 4 || result.Degraded != 4 || len(result.FailureCodeHash) != 64 {
			t.Fatalf("unexpected result: %+v", result)
		}
	}
	encoded, _ := json.Marshal(report)
	if string(encoded) == "" || contains(string(encoded), "sensitive") {
		t.Fatal("capacity report leaked a response body")
	}
}

func TestLoadConfigRejectsSensitiveOrRunnerManagedHeaders(t *testing.T) {
	for _, headers := range []map[string]string{
		{"Authorization": "Bearer leaked"},
		{"Cookie": "session=leaked"},
		{"Idempotency-Key": "caller-forged"},
		{"X-Bad": "line\nbreak"},
	} {
		config := completeLoadConfig("http://127.0.0.1:8080")
		config.Scenarios[0].Headers = headers
		raw, _ := json.Marshal(config)
		if _, _, err := parseLoadConfig(raw); err == nil {
			t.Fatalf("unsafe headers were accepted: %#v", headers)
		}
	}
}

func TestLoadConfigRejectsMissingScenario(t *testing.T) {
	config := completeLoadConfig("http://127.0.0.1:8080")
	config.Scenarios = config.Scenarios[:6]
	raw, _ := json.Marshal(config)
	if _, _, err := parseLoadConfig(raw); err == nil {
		t.Fatal("missing governed scenario must be rejected")
	}
}

func completeLoadConfig(baseURL string) loadConfig {
	config := loadConfig{
		BaseURL: baseURL, Seed: 42, DatabaseLabel: "postgres", GraphLabel: "nebula", LLMLabel: "fault-proxy",
	}
	for _, scenario := range []observability.CapacityScenario{
		observability.CapacityFastPath, observability.CapacityComplexLoop,
		observability.CapacityWarehousePool, observability.CapacityVectorRecall,
		observability.CapacityGraphThreeHops, observability.CapacityLLMDegradation,
		observability.CapacityKPIBundle,
	} {
		thresholds := observability.CapacityThresholds{MaxP95MS: 1000, MinSuccessRate: .99}
		if scenario == observability.CapacityVectorRecall {
			thresholds.MinRecallAtK = .99
		}
		config.Scenarios = append(config.Scenarios, scenarioConfig{
			Scenario: scenario, Method: http.MethodPost, Path: "/test",
			Headers: map[string]string{"X-Business-Domain-Id": "00000000-0000-0000-0000-000000000001"},
			Body:    json.RawMessage(`{"fixtureMode":true}`), Concurrency: 2,
			Requests: 4, TimeoutMS: 1000, Thresholds: thresholds,
		})
	}
	return config
}

func contains(value, substring string) bool {
	for index := 0; index+len(substring) <= len(value); index++ {
		if value[index:index+len(substring)] == substring {
			return true
		}
	}
	return false
}
