package config

import (
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	for _, key := range []string{
		"APP_ENV", "APP_LOG_LEVEL", "API_HTTP_ADDR", "API_READ_HEADER_TIMEOUT",
		"API_READ_TIMEOUT", "API_WRITE_TIMEOUT", "API_IDLE_TIMEOUT",
		"SHUTDOWN_TIMEOUT", "WORKER_POLL_INTERVAL",
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
