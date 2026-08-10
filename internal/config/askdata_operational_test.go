package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestValidateAskDataOperationalConfig(t *testing.T) {
	base := askDataOperationalFixture()
	if err := validateAskDataOperationalConfig(base); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"endpoint", func(config *Config) { config.AskDataNebulaAddresses = []string{"not-an-endpoint"} }},
		{"short secret", func(config *Config) { config.AskDataNebulaPassword = "secret" }},
		{"threshold order", func(config *Config) { config.AskDataBindingThreshold = .6 }},
		{"evaluation cases", func(config *Config) { config.AskDataEvaluationMinimumCases = 1999 }},
		{"canary percent", func(config *Config) { config.AskDataShadowCanaryMode, config.AskDataCanaryPercent = "CANARY", 10 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := base
			test.mutate(&config)
			if err := validateAskDataOperationalConfig(config); err == nil {
				t.Fatal("invalid operational configuration accepted")
			} else if strings.Contains(err.Error(), config.AskDataNebulaPassword) {
				t.Fatal("secret was echoed in configuration error")
			}
		})
	}
}

func TestProductionAskDataConfigFailsClosed(t *testing.T) {
	config := askDataOperationalFixture()
	config.Environment = "production"
	config.AskDataNebulaTLSEnabled = true
	for _, key := range []string{
		"ASKDATA_NEBULA_ADDRESSES", "ASKDATA_NEBULA_SPACE", "ASKDATA_NEBULA_USERNAME", "ASKDATA_NEBULA_PASSWORD",
		"ASKDATA_NEBULA_TLS_ENABLED", "ASKDATA_RETRIEVAL_THRESHOLD", "ASKDATA_BINDING_THRESHOLD",
		"ASKDATA_PROJECTION_LEASE", "ASKDATA_PROFILE_SCAN_LIMIT", "ASKDATA_RELEASE_RETENTION_COUNT",
		"ASKDATA_EVALUATION_MINIMUM_CASES", "ASKDATA_EVALUATION_STRICT_MINIMUM",
		"ASKDATA_EVALUATION_WILSON_MINIMUM", "ASKDATA_SHADOW_CANARY_MODE", "ASKDATA_CANARY_PERCENT",
	} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	if err := validateAskDataOperationalConfig(config); err == nil || !strings.Contains(err.Error(), "must be explicitly configured") {
		t.Fatalf("production missing config error = %v", err)
	}
}

func TestAPIAndWorkerForbiddenNebulaCredentialsAreSeparated(t *testing.T) {
	apiForbidden := strings.Join(apiDatabaseProcess.forbiddenKeys, ",")
	workerForbidden := strings.Join(workerDatabaseProcess.forbiddenKeys, ",")
	if !strings.Contains(apiForbidden, "ASKDATA_NEBULA_WORKER_PASSWORD") || strings.Contains(apiForbidden, "ASKDATA_NEBULA_API_PASSWORD") {
		t.Fatalf("API forbidden keys = %s", apiForbidden)
	}
	if !strings.Contains(workerForbidden, "ASKDATA_NEBULA_API_PASSWORD") || strings.Contains(workerForbidden, "ASKDATA_NEBULA_WORKER_PASSWORD") {
		t.Fatalf("worker forbidden keys = %s", workerForbidden)
	}
}

func askDataOperationalFixture() Config {
	return Config{
		Environment: "development", AskDataNebulaAddresses: []string{"127.0.0.1:9669"},
		AskDataNebulaSpace: "askdata_semantic", AskDataNebulaUsername: "askdata_api",
		AskDataNebulaPassword: "a-long-secret-value", AskDataRetrievalThreshold: .7,
		AskDataBindingThreshold: .8, AskDataProjectionLease: 2 * time.Minute,
		AskDataProfileScanLimit: 100_000, AskDataReleaseRetentionCount: 10,
		AskDataEvaluationMinimumCases: 2000, AskDataEvaluationStrictMinimum: .96,
		AskDataEvaluationWilsonMinimum: .95, AskDataShadowCanaryMode: "OFF",
	}
}
