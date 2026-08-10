package config

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestValidateAskDataRetentionConfig(t *testing.T) {
	base := Config{
		AskDataQuestionRetentionMode: "HASH_ONLY",
		AskDataQuestionRetentionTTL:  24 * time.Hour,
		AskDataRunArtifactTTL:        30 * 24 * time.Hour,
	}
	if err := validateAskDataRetentionConfig(base); err != nil {
		t.Fatalf("hash-only config error = %v", err)
	}

	encrypted := base
	encrypted.AskDataQuestionRetentionMode = "ENCRYPTED_SHORT_TERM"
	encrypted.AskDataQuestionEncryptionKey = base64.StdEncoding.EncodeToString(make([]byte, 32))
	if err := validateAskDataRetentionConfig(encrypted); err != nil {
		t.Fatalf("encrypted config error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Config)
		code   string
	}{
		{"unknown mode", func(config *Config) { config.AskDataQuestionRetentionMode = "PLAINTEXT" }, "RETENTION_MODE"},
		{"question ttl", func(config *Config) { config.AskDataQuestionRetentionTTL = 8 * 24 * time.Hour }, "RETENTION_TTL"},
		{"artifact ttl", func(config *Config) { config.AskDataRunArtifactTTL = time.Hour }, "ARTIFACT_TTL"},
		{"key in hash mode", func(config *Config) { config.AskDataQuestionEncryptionKey = "unexpected" }, "ENCRYPTION_KEY"},
		{"missing encrypted key", func(config *Config) { config.AskDataQuestionRetentionMode = "ENCRYPTED_SHORT_TERM" }, "ENCRYPTION_KEY"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := base
			test.mutate(&config)
			err := validateAskDataRetentionConfig(config)
			if err == nil || !strings.Contains(err.Error(), test.code) {
				t.Fatalf("validateAskDataRetentionConfig() error = %v", err)
			}
		})
	}
}
