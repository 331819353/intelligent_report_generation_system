package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const defaultDataSourceCredentialKey = "bG9jYWxfZGF0YV9zb3VyY2VfY3JlZGVudGlhbF9rZXk="

type Config struct {
	Environment                  string
	LogLevel                     string
	HTTPAddr                     string
	ReadHeaderTimeout            time.Duration
	ReadTimeout                  time.Duration
	WriteTimeout                 time.Duration
	IdleTimeout                  time.Duration
	ShutdownTimeout              time.Duration
	WorkerPollInterval           time.Duration
	AIBaseURL                    string
	AIModel                      string
	AIAPIKey                     string
	AIRequestTimeout             time.Duration
	AIAttemptTimeout             time.Duration
	AIRetryBaseDelay             time.Duration
	AIRetryMaxDelay              time.Duration
	AIMaxAttempts                int
	AIMaxInputBytes              int
	AIInputCostMicrosPerMTokens  int64
	AIOutputCostMicrosPerMTokens int64
	AIConfidenceThreshold        float64
	DatabaseURL                  string
	RedisURL                     string
	MinIOEndpoint                string
	MinIOAccessKey               string
	MinIOSecretKey               string
	MinIOUseSSL                  bool
	MinIOUploadsBucket           string
	AuthTokenIssuer              string
	AuthAccessSecret             string
	AuthAccessTTL                time.Duration
	AuthRefreshTTL               time.Duration
	AuthBcryptCost               int
	ConnectorURL                 string
	ConnectorToken               string
	DataSourceCredentialKey      string
}

// Load 从环境变量构建运行配置，并在返回前完成完整校验。
func Load() (Config, error) {
	cfg := Config{
		Environment:             envOrDefault("APP_ENV", "development"),
		LogLevel:                envOrDefault("APP_LOG_LEVEL", "info"),
		HTTPAddr:                envOrDefault("API_HTTP_ADDR", ":8080"),
		ReadHeaderTimeout:       5 * time.Second,
		ReadTimeout:             15 * time.Second,
		WriteTimeout:            30 * time.Second,
		IdleTimeout:             60 * time.Second,
		ShutdownTimeout:         10 * time.Second,
		WorkerPollInterval:      2 * time.Second,
		AIBaseURL:               envOrDefault("AI_BASE_URL", "https://mgallery.haier.net/v1/"),
		AIModel:                 envOrDefault("AI_MODEL", "deepseek-v3"),
		AIAPIKey:                os.Getenv("AI_API_KEY"),
		AIRequestTimeout:        25 * time.Second,
		AIAttemptTimeout:        8 * time.Second,
		AIRetryBaseDelay:        200 * time.Millisecond,
		AIRetryMaxDelay:         2 * time.Second,
		AIMaxAttempts:           3,
		AIMaxInputBytes:         256 << 10,
		AIConfidenceThreshold:   0.8,
		DatabaseURL:             envOrDefault("DATABASE_URL", "postgres://report_app:local_report_password@127.0.0.1:5432/intelligent_report?sslmode=disable"),
		RedisURL:                envOrDefault("REDIS_URL", "redis://:local_redis_password@127.0.0.1:6379/0"),
		MinIOEndpoint:           envOrDefault("MINIO_ENDPOINT", "127.0.0.1:9000"),
		MinIOAccessKey:          envOrDefault("MINIO_ACCESS_KEY", "report_minio"),
		MinIOSecretKey:          envOrDefault("MINIO_SECRET_KEY", "local_minio_password"),
		MinIOUseSSL:             strings.EqualFold(os.Getenv("MINIO_USE_SSL"), "true"),
		MinIOUploadsBucket:      envOrDefault("MINIO_BUCKET_UPLOADS", "uploads"),
		AuthTokenIssuer:         envOrDefault("AUTH_TOKEN_ISSUER", "intelligent-report-system"),
		AuthAccessSecret:        envOrDefault("AUTH_ACCESS_TOKEN_SECRET", "local_access_token_secret_change_me"),
		AuthAccessTTL:           15 * time.Minute,
		AuthRefreshTTL:          7 * 24 * time.Hour,
		AuthBcryptCost:          12,
		ConnectorURL:            envOrDefault("CONNECTOR_SERVICE_URL", "http://127.0.0.1:8090"),
		ConnectorToken:          envOrDefault("CONNECTOR_INTERNAL_TOKEN", "local_connector_token_change_me"),
		DataSourceCredentialKey: envOrDefault("DATA_SOURCE_CREDENTIAL_KEY", defaultDataSourceCredentialKey),
	}

	durations := []struct {
		key    string
		target *time.Duration
	}{
		{"API_READ_HEADER_TIMEOUT", &cfg.ReadHeaderTimeout},
		{"API_READ_TIMEOUT", &cfg.ReadTimeout},
		{"API_WRITE_TIMEOUT", &cfg.WriteTimeout},
		{"API_IDLE_TIMEOUT", &cfg.IdleTimeout},
		{"SHUTDOWN_TIMEOUT", &cfg.ShutdownTimeout},
		{"WORKER_POLL_INTERVAL", &cfg.WorkerPollInterval},
		{"AI_REQUEST_TIMEOUT", &cfg.AIRequestTimeout},
		{"AI_ATTEMPT_TIMEOUT", &cfg.AIAttemptTimeout},
		{"AI_RETRY_BASE_DELAY", &cfg.AIRetryBaseDelay},
		{"AI_RETRY_MAX_DELAY", &cfg.AIRetryMaxDelay},
		{"AUTH_ACCESS_TOKEN_TTL", &cfg.AuthAccessTTL},
		{"AUTH_REFRESH_TOKEN_TTL", &cfg.AuthRefreshTTL},
	}
	if value := os.Getenv("AUTH_PASSWORD_BCRYPT_COST"); value != "" {
		cost, err := strconv.Atoi(value)
		if err != nil {
			return Config{}, fmt.Errorf("parse AUTH_PASSWORD_BCRYPT_COST: %w", err)
		}
		cfg.AuthBcryptCost = cost
	}
	if value := os.Getenv("AI_CONFIDENCE_THRESHOLD"); value != "" {
		threshold, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return Config{}, fmt.Errorf("parse AI_CONFIDENCE_THRESHOLD: %w", err)
		}
		cfg.AIConfidenceThreshold = threshold
	}
	integerOptions := []struct {
		key    string
		target *int
	}{
		{"AI_MAX_ATTEMPTS", &cfg.AIMaxAttempts},
		{"AI_MAX_INPUT_BYTES", &cfg.AIMaxInputBytes},
	}
	for _, item := range integerOptions {
		if value := os.Getenv(item.key); value != "" {
			parsed, err := strconv.Atoi(value)
			if err != nil {
				return Config{}, fmt.Errorf("parse %s: %w", item.key, err)
			}
			*item.target = parsed
		}
	}
	costOptions := []struct {
		key    string
		target *int64
	}{
		{"AI_INPUT_COST_MICROS_PER_MILLION_TOKENS", &cfg.AIInputCostMicrosPerMTokens},
		{"AI_OUTPUT_COST_MICROS_PER_MILLION_TOKENS", &cfg.AIOutputCostMicrosPerMTokens},
	}
	for _, item := range costOptions {
		if value := os.Getenv(item.key); value != "" {
			parsed, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return Config{}, fmt.Errorf("parse %s: %w", item.key, err)
			}
			*item.target = parsed
		}
	}

	for _, item := range durations {
		value := os.Getenv(item.key)
		if value == "" {
			continue
		}
		parsed, err := time.ParseDuration(value)
		if err != nil {
			return Config{}, fmt.Errorf("parse %s: %w", item.key, err)
		}
		*item.target = parsed
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate 检查服务启动所需的地址、密钥、超时和配额边界。
func (c Config) Validate() error {
	if strings.TrimSpace(c.HTTPAddr) == "" {
		return errors.New("API_HTTP_ADDR must not be empty")
	}
	if c.ReadHeaderTimeout <= 0 || c.ReadTimeout <= 0 || c.WriteTimeout <= 0 || c.IdleTimeout <= 0 {
		return errors.New("HTTP timeouts must be greater than zero")
	}
	if c.ShutdownTimeout <= 0 {
		return errors.New("SHUTDOWN_TIMEOUT must be greater than zero")
	}
	if c.WorkerPollInterval <= 0 {
		return errors.New("WORKER_POLL_INTERVAL must be greater than zero")
	}
	if c.AIRequestTimeout <= 0 || c.AIRequestTimeout >= c.WriteTimeout {
		return errors.New("AI_REQUEST_TIMEOUT must be greater than zero and less than API_WRITE_TIMEOUT")
	}
	if c.AIAttemptTimeout <= 0 || c.AIAttemptTimeout > c.AIRequestTimeout {
		return errors.New("AI_ATTEMPT_TIMEOUT must be greater than zero and at most AI_REQUEST_TIMEOUT")
	}
	if c.AIMaxAttempts < 1 || c.AIMaxAttempts > 5 || c.AIRetryBaseDelay <= 0 || c.AIRetryMaxDelay < c.AIRetryBaseDelay {
		return errors.New("AI retry configuration is invalid")
	}
	if c.AIMaxInputBytes < 1024 || c.AIMaxInputBytes > 4<<20 {
		return errors.New("AI_MAX_INPUT_BYTES must be between 1024 and 4194304")
	}
	if c.AIInputCostMicrosPerMTokens < 0 || c.AIOutputCostMicrosPerMTokens < 0 {
		return errors.New("AI token cost configuration must not be negative")
	}
	if c.AIConfidenceThreshold < 0 || c.AIConfidenceThreshold > 1 {
		return errors.New("AI_CONFIDENCE_THRESHOLD must be between zero and one")
	}
	if strings.TrimSpace(c.AIAPIKey) != "" {
		if !validAIBaseURL(c.AIBaseURL) || strings.TrimSpace(c.AIModel) == "" {
			return errors.New("AI_BASE_URL or AI_MODEL is invalid")
		}
	}
	if len(c.AuthAccessSecret) < 32 {
		return errors.New("AUTH_ACCESS_TOKEN_SECRET must be at least 32 characters")
	}
	if c.AuthAccessTTL <= 0 || c.AuthRefreshTTL <= c.AuthAccessTTL {
		return errors.New("auth token TTLs are invalid")
	}
	if c.AuthBcryptCost < 10 || c.AuthBcryptCost > 14 {
		return errors.New("AUTH_PASSWORD_BCRYPT_COST must be between 10 and 14")
	}
	if strings.TrimSpace(c.ConnectorURL) == "" || len(c.ConnectorToken) < 24 {
		return errors.New("connector service URL or internal token is invalid")
	}
	credentialKey, err := base64.StdEncoding.DecodeString(strings.TrimSpace(c.DataSourceCredentialKey))
	if err != nil || len(credentialKey) != 32 {
		return errors.New("DATA_SOURCE_CREDENTIAL_KEY must be base64-encoded 32 bytes")
	}
	if strings.EqualFold(c.Environment, "production") && c.DataSourceCredentialKey == defaultDataSourceCredentialKey {
		return errors.New("production must override DATA_SOURCE_CREDENTIAL_KEY")
	}
	return nil
}

// validAIBaseURL 禁止携带凭据、查询和片段；明文 HTTP 只允许本机开发端点。
func validAIBaseURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return false
	}
	if parsed.Scheme == "https" {
		return true
	}
	if parsed.Scheme != "http" {
		return false
	}
	host := strings.TrimSpace(parsed.Hostname())
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// envOrDefault 返回去除首尾空白的环境变量值或指定默认值。
func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
