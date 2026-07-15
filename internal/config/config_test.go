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
