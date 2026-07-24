package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

var connectionTestDatabaseProcess = databaseProcess{
	urlKey:          "CONNECTION_TEST_DATABASE_URL",
	developmentURL:  "postgres://report_connection_tester:local_connection_test_password@127.0.0.1:5432/intelligent_report_control?sslmode=disable",
	roleKey:         "POSTGRES_CONNECTION_TEST_USER",
	developmentRole: "report_connection_tester",
	forbiddenKeys: []string{
		"DATABASE_URL", "WORKER_DATABASE_URL", "WAREHOUSE_DATABASE_URL", "WAREHOUSE_WORKER_DATABASE_URL",
		"POSTGRES_USER", "POSTGRES_PASSWORD",
		"POSTGRES_APP_USER", "POSTGRES_APP_PASSWORD",
		"POSTGRES_WORKER_USER", "POSTGRES_WORKER_PASSWORD",
		"POSTGRES_CONNECTION_TEST_PASSWORD",
		"PGPASSWORD", "PGPASSFILE", "PGSERVICE", "PGSERVICEFILE",
	},
}

// ConnectionTestWorkerConfig intentionally contains only values needed by the
// isolated connection-test process. In particular it has no API or generic
// worker database credential field.
type ConnectionTestWorkerConfig struct {
	Environment             string
	LogLevel                string
	DatabaseURL             string
	WorkerPollInterval      time.Duration
	ConnectionTestTimeout   time.Duration
	ConnectionTestLease     time.Duration
	MinIOEndpoint           string
	MinIOAccessKey          string
	MinIOSecretKey          string
	MinIOUseSSL             bool
	MinIOUploadsBucket      string
	ConnectorURL            string
	ConnectorToken          string
	DataSourceCredentialKey string
}

func LoadConnectionTestWorker() (ConnectionTestWorkerConfig, error) {
	environment := envOrDefault("APP_ENV", "development")
	if strings.EqualFold(environment, "production") {
		if _, configured := os.LookupEnv("CONNECTION_TEST_MINIO_USE_SSL"); !configured {
			return ConnectionTestWorkerConfig{}, errors.New(
				"CONNECTION_TEST_MINIO_USE_SSL must be explicitly configured in production",
			)
		}
	}
	minIOEndpoint, err := connectionTestResourceValue(
		environment, "CONNECTION_TEST_MINIO_ENDPOINT",
		"MINIO_ENDPOINT", "127.0.0.1:9000",
	)
	if err != nil {
		return ConnectionTestWorkerConfig{}, err
	}
	minIOAccessKey, err := connectionTestResourceValue(
		environment, "CONNECTION_TEST_MINIO_ACCESS_KEY",
		"MINIO_ACCESS_KEY", "report_minio",
	)
	if err != nil {
		return ConnectionTestWorkerConfig{}, err
	}
	minIOSecretKey, err := connectionTestResourceValue(
		environment, "CONNECTION_TEST_MINIO_SECRET_KEY",
		"MINIO_SECRET_KEY", "local_minio_password",
	)
	if err != nil {
		return ConnectionTestWorkerConfig{}, err
	}
	minIOBucket, err := connectionTestResourceValue(
		environment, "CONNECTION_TEST_MINIO_BUCKET_UPLOADS",
		"MINIO_BUCKET_UPLOADS", "uploads",
	)
	if err != nil {
		return ConnectionTestWorkerConfig{}, err
	}
	connectorToken := strings.TrimSpace(
		os.Getenv("CONNECTOR_CONNECTION_TEST_TOKEN"),
	)
	if connectorToken == "" {
		if strings.EqualFold(environment, "production") {
			return ConnectionTestWorkerConfig{}, errors.New(
				"CONNECTOR_CONNECTION_TEST_TOKEN must be explicitly configured in production",
			)
		}
		connectorToken = "local_connector_connection_test_token_change_me"
	}
	if strings.EqualFold(environment, "production") {
		internalToken := strings.TrimSpace(os.Getenv("CONNECTOR_INTERNAL_TOKEN"))
		if internalToken != "" && connectorToken == internalToken {
			return ConnectionTestWorkerConfig{}, errors.New(
				"connection-test connector token must differ from the general connector token",
			)
		}
		generalAccessKey := strings.TrimSpace(os.Getenv("MINIO_ACCESS_KEY"))
		generalSecretKey := strings.TrimSpace(os.Getenv("MINIO_SECRET_KEY"))
		if generalAccessKey != "" && generalSecretKey != "" &&
			minIOAccessKey == generalAccessKey &&
			minIOSecretKey == generalSecretKey {
			return ConnectionTestWorkerConfig{}, errors.New(
				"production connection-test MinIO credentials must be dedicated",
			)
		}
	}
	databaseURL, err := loadProcessDatabaseURL(
		environment, connectionTestDatabaseProcess,
	)
	if err != nil {
		return ConnectionTestWorkerConfig{}, err
	}
	for _, key := range []string{
		"CONNECTOR_INTERNAL_TOKEN", "MINIO_ACCESS_KEY", "MINIO_SECRET_KEY",
	} {
		if err := os.Unsetenv(key); err != nil {
			return ConnectionTestWorkerConfig{}, fmt.Errorf(
				"remove general process credential %s: %w", key, err,
			)
		}
	}
	cfg := ConnectionTestWorkerConfig{
		Environment:             environment,
		LogLevel:                envOrDefault("APP_LOG_LEVEL", "info"),
		DatabaseURL:             databaseURL,
		WorkerPollInterval:      2 * time.Second,
		ConnectionTestTimeout:   30 * time.Second,
		ConnectionTestLease:     60 * time.Second,
		MinIOEndpoint:           minIOEndpoint,
		MinIOAccessKey:          minIOAccessKey,
		MinIOSecretKey:          minIOSecretKey,
		MinIOUseSSL:             connectionTestMinIOUseSSL(environment),
		MinIOUploadsBucket:      minIOBucket,
		ConnectorURL:            envOrDefault("CONNECTOR_SERVICE_URL", "http://127.0.0.1:8090"),
		ConnectorToken:          connectorToken,
		DataSourceCredentialKey: envOrDefault("DATA_SOURCE_CREDENTIAL_KEY", defaultDataSourceCredentialKey),
	}
	for _, item := range []struct {
		key    string
		target *time.Duration
	}{
		{"WORKER_POLL_INTERVAL", &cfg.WorkerPollInterval},
		{"CONNECTION_TEST_TIMEOUT", &cfg.ConnectionTestTimeout},
		{"CONNECTION_TEST_LEASE", &cfg.ConnectionTestLease},
	} {
		value := os.Getenv(item.key)
		if value == "" {
			continue
		}
		parsed, err := time.ParseDuration(value)
		if err != nil {
			return ConnectionTestWorkerConfig{}, fmt.Errorf(
				"parse %s: %w", item.key, err,
			)
		}
		*item.target = parsed
	}
	if err := cfg.Validate(); err != nil {
		return ConnectionTestWorkerConfig{}, err
	}
	return cfg, nil
}

func (c ConnectionTestWorkerConfig) Validate() error {
	if strings.TrimSpace(c.DatabaseURL) == "" {
		return errors.New("CONNECTION_TEST_DATABASE_URL must not be empty")
	}
	if c.WorkerPollInterval <= 0 {
		return errors.New("WORKER_POLL_INTERVAL must be greater than zero")
	}
	if c.ConnectionTestTimeout <= 0 ||
		c.ConnectionTestTimeout > 2*time.Minute {
		return errors.New(
			"CONNECTION_TEST_TIMEOUT must be greater than zero and at most 2 minutes",
		)
	}
	if c.ConnectionTestLease < c.ConnectionTestTimeout+10*time.Second ||
		c.ConnectionTestLease > 5*time.Minute {
		return errors.New(
			"CONNECTION_TEST_LEASE must exceed CONNECTION_TEST_TIMEOUT by at least 10 seconds and be at most 5 minutes",
		)
	}
	if strings.TrimSpace(c.MinIOEndpoint) == "" ||
		strings.TrimSpace(c.MinIOAccessKey) == "" ||
		strings.TrimSpace(c.MinIOSecretKey) == "" ||
		strings.TrimSpace(c.MinIOUploadsBucket) == "" {
		return errors.New("connection-test object storage configuration is invalid")
	}
	if strings.TrimSpace(c.ConnectorURL) == "" || len(c.ConnectorToken) < 24 {
		return errors.New("connector service URL or internal token is invalid")
	}
	if strings.EqualFold(c.Environment, "production") &&
		!validAIBaseURL(c.ConnectorURL) {
		return errors.New(
			"production CONNECTOR_SERVICE_URL must use HTTPS or loopback HTTP",
		)
	}
	if strings.EqualFold(c.Environment, "production") && !c.MinIOUseSSL {
		return errors.New(
			"production connection-test MinIO must use TLS",
		)
	}
	credentialKey, err := base64.StdEncoding.DecodeString(
		strings.TrimSpace(c.DataSourceCredentialKey),
	)
	if err != nil || len(credentialKey) != 32 {
		return errors.New("DATA_SOURCE_CREDENTIAL_KEY must be base64-encoded 32 bytes")
	}
	if strings.EqualFold(c.Environment, "production") &&
		c.DataSourceCredentialKey == defaultDataSourceCredentialKey {
		return errors.New("production must override DATA_SOURCE_CREDENTIAL_KEY")
	}
	return nil
}

func connectionTestResourceValue(
	environment, dedicatedKey, developmentFallbackKey, developmentDefault string,
) (string, error) {
	value := strings.TrimSpace(os.Getenv(dedicatedKey))
	if value != "" {
		return value, nil
	}
	if strings.EqualFold(environment, "production") {
		return "", fmt.Errorf(
			"%s must be explicitly configured in production", dedicatedKey,
		)
	}
	return envOrDefault(developmentFallbackKey, developmentDefault), nil
}

func connectionTestMinIOUseSSL(environment string) bool {
	value := os.Getenv("CONNECTION_TEST_MINIO_USE_SSL")
	if value == "" && !strings.EqualFold(environment, "production") {
		value = os.Getenv("MINIO_USE_SSL")
	}
	return strings.EqualFold(value, "true")
}
