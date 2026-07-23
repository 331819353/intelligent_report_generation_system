package config

import (
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	for _, key := range []string{
		"APP_ENV", "APP_LOG_LEVEL", "API_HTTP_ADDR", "API_READ_HEADER_TIMEOUT",
		"API_READ_TIMEOUT", "API_WRITE_TIMEOUT", "API_IDLE_TIMEOUT",
		"SHUTDOWN_TIMEOUT", "WORKER_POLL_INTERVAL", "AI_REQUEST_TIMEOUT", "AI_ATTEMPT_TIMEOUT",
	} {
		t.Setenv(key, "")
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Fatalf("HTTPAddr = %q, want :8080", cfg.HTTPAddr)
	}
	if cfg.ShutdownTimeout != 10*time.Second {
		t.Fatalf("ShutdownTimeout = %s, want 10s", cfg.ShutdownTimeout)
	}
	if cfg.WriteTimeout != 60*time.Second {
		t.Fatalf("WriteTimeout = %s, want 60s", cfg.WriteTimeout)
	}
}

func TestLoadRequiresHTTPWindowForBothDatasetAIPhases(t *testing.T) {
	t.Setenv("AI_API_KEY", "")
	t.Setenv("AI_REQUEST_TIMEOUT", "25s")
	t.Setenv("API_WRITE_TIMEOUT", "50s")
	if _, err := Load(); err == nil {
		t.Fatal("HTTP window equal to two AI phases was accepted")
	}
	t.Setenv("API_WRITE_TIMEOUT", "51s")
	if _, err := Load(); err != nil {
		t.Fatalf("HTTP window covering both AI phases was rejected: %v", err)
	}
}

func TestLoadRejectsInvalidDuration(t *testing.T) {
	t.Setenv("API_READ_TIMEOUT", "not-a-duration")
	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want duration parse error")
	}
}

func TestLoadAllowsMissingAIKeyAndRejectsInvalidThreshold(t *testing.T) {
	t.Setenv("AI_API_KEY", "")
	t.Setenv("AI_CONFIDENCE_THRESHOLD", "0.75")
	cfg, err := Load()
	if err != nil || cfg.AIConfidenceThreshold != 0.75 {
		t.Fatalf("cfg=%#v err=%v", cfg, err)
	}
	t.Setenv("AI_CONFIDENCE_THRESHOLD", "1.1")
	if _, err := Load(); err == nil {
		t.Fatal("invalid AI confidence threshold was accepted")
	}
}

func TestLoadParsesAIOrchestrationLimits(t *testing.T) {
	t.Setenv("AI_API_KEY", "")
	t.Setenv("AI_ATTEMPT_TIMEOUT", "7s")
	t.Setenv("AI_MAX_ATTEMPTS", "4")
	t.Setenv("AI_MAX_INPUT_BYTES", "131072")
	t.Setenv("AI_INPUT_COST_MICROS_PER_MILLION_TOKENS", "125000")
	t.Setenv("AI_OUTPUT_COST_MICROS_PER_MILLION_TOKENS", "500000")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AIAttemptTimeout != 7*time.Second || cfg.AIMaxAttempts != 4 || cfg.AIMaxInputBytes != 131072 {
		t.Fatalf("AI 编排边界未正确加载: %#v", cfg)
	}
	if cfg.AIInputCostMicrosPerMTokens != 125000 || cfg.AIOutputCostMicrosPerMTokens != 500000 {
		t.Fatalf("AI 成本配置未正确加载: %#v", cfg)
	}
}

func TestLoadRejectsUnsafeAIOrchestrationLimits(t *testing.T) {
	t.Setenv("AI_MAX_ATTEMPTS", "6")
	if _, err := Load(); err == nil {
		t.Fatal("超过上限的 AI 重试次数未被拒绝")
	}
	t.Setenv("AI_MAX_ATTEMPTS", "3")
	t.Setenv("AI_INPUT_COST_MICROS_PER_MILLION_TOKENS", "-1")
	if _, err := Load(); err == nil {
		t.Fatal("负数 AI 成本配置未被拒绝")
	}
}

func TestLoadLocksEmbeddingDimensionsToVectorSchema(t *testing.T) {
	t.Setenv("AI_EMBEDDING_DIMENSIONS", "1536")
	if _, err := Load(); err == nil {
		t.Fatal("embedding dimensions incompatible with halfvec schema were accepted")
	}
	t.Setenv("AI_EMBEDDING_DIMENSIONS", "2560")
	if _, err := Load(); err != nil {
		t.Fatalf("current metric vector dimensions were rejected: %v", err)
	}
}

func TestLoadValidatesDatasetAIRetrievalMode(t *testing.T) {
	t.Setenv("DATASET_AI_RETRIEVAL_MODE", "shadow")
	cfg, err := Load()
	if err != nil || cfg.DatasetAIRetrievalMode != "SHADOW" {
		t.Fatalf("cfg=%#v err=%v", cfg, err)
	}
	t.Setenv("DATASET_AI_RETRIEVAL_MODE", "vector-only")
	if _, err := Load(); err == nil {
		t.Fatal("invalid dataset AI retrieval mode was accepted")
	}
}

func TestLoadRejectsRemotePlaintextAIEndpoint(t *testing.T) {
	t.Setenv("AI_API_KEY", "configured-secret")
	t.Setenv("AI_BASE_URL", "http://provider.example.test/v1")
	if _, err := Load(); err == nil {
		t.Fatal("远程明文 HTTP Provider 地址未被拒绝")
	}
	t.Setenv("AI_BASE_URL", "http://127.0.0.1:11434/v1")
	if _, err := Load(); err != nil {
		t.Fatalf("本机开发 Provider 地址应被允许: %v", err)
	}
}

func TestLoadRejectsInvalidDataSourceCredentialKey(t *testing.T) {
	t.Setenv("DATA_SOURCE_CREDENTIAL_KEY", "short")
	if _, err := Load(); err == nil {
		t.Fatal("invalid data source credential key was accepted")
	}
}

func TestLoadRejectsDevelopmentCredentialKeyInProduction(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("DATA_SOURCE_CREDENTIAL_KEY", defaultDataSourceCredentialKey)
	if _, err := Load(); err == nil {
		t.Fatal("production accepted the development data source credential key")
	}
}
