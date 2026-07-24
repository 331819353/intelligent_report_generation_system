package config

import (
	"os"
	"testing"
	"time"
)

func setProductionConnectorByteLimits(t *testing.T) {
	t.Helper()
	t.Setenv(
		"WAREHOUSE_DATABASE_URL",
		"postgres://report_warehouse_reader:secret@warehouse.internal:5432/report_warehouse?sslmode=require",
	)
	for key, value := range map[string]string{
		"CONNECTOR_HTTP_MAX_REQUEST_BYTES":             "1048576",
		"CONNECTOR_JSON_MAX_RESPONSE_BYTES":            "67108864",
		"CONNECTOR_METADATA_SAMPLE_MAX_CELL_BYTES":     "16384",
		"CONNECTOR_METADATA_SAMPLE_MAX_ROW_BYTES":      "65536",
		"CONNECTOR_METADATA_SAMPLE_MAX_RESPONSE_BYTES": "524288",
		"CONNECTOR_STREAM_MAX_CELL_BYTES":              "1048576",
		"CONNECTOR_STREAM_MAX_ROW_BYTES":               "4194304",
		"CONNECTOR_STREAM_MAX_BYTES":                   "1073741824",
		"WAREHOUSE_STAGE_MAX_BYTES":                    "536870912",
	} {
		t.Setenv(key, value)
	}
}

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
	t.Setenv(
		"DATABASE_URL",
		"postgres://report_app:secret@db.internal:5432/report?sslmode=require",
	)
	t.Setenv("DATA_SOURCE_CREDENTIAL_KEY", defaultDataSourceCredentialKey)
	if _, err := Load(); err == nil {
		t.Fatal("production accepted the development data source credential key")
	}
}

func TestLoadRejectsDevelopmentConnectorTokenInProduction(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	setProductionConnectorByteLimits(t)
	t.Setenv(
		"DATABASE_URL",
		"postgres://report_app:secret@db.internal:5432/report?sslmode=require",
	)
	t.Setenv(
		"DATA_SOURCE_CREDENTIAL_KEY",
		"MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
	)
	t.Setenv("CONNECTOR_INTERNAL_TOKEN", defaultConnectorToken)
	if _, err := LoadAPI(); err == nil {
		t.Fatal("production accepted the development connector token")
	}
	t.Setenv("CONNECTOR_INTERNAL_TOKEN", "production-general-connector-token")
	if _, err := LoadAPI(); err != nil {
		t.Fatalf("production rejected an explicit connector token: %v", err)
	}
}

func TestLoadRejectsRemotePlaintextConnectorInProduction(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	setProductionConnectorByteLimits(t)
	t.Setenv(
		"DATABASE_URL",
		"postgres://report_app:secret@db.internal:5432/report?sslmode=require",
	)
	t.Setenv(
		"DATA_SOURCE_CREDENTIAL_KEY",
		"MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
	)
	t.Setenv("CONNECTOR_INTERNAL_TOKEN", "production-general-connector-token")
	t.Setenv("CONNECTOR_SERVICE_URL", "http://connector.internal:8090")
	if _, err := LoadAPI(); err == nil {
		t.Fatal("production accepted a remote plaintext connector endpoint")
	}
	t.Setenv("CONNECTOR_SERVICE_URL", "https://connector.internal")
	if _, err := LoadAPI(); err != nil {
		t.Fatalf("production rejected an HTTPS connector endpoint: %v", err)
	}
}

func TestLoadRejectsRemotePlaintextMinIOInProduction(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	setProductionConnectorByteLimits(t)
	t.Setenv(
		"DATABASE_URL",
		"postgres://report_app:secret@db.internal:5432/report?sslmode=require",
	)
	t.Setenv(
		"DATA_SOURCE_CREDENTIAL_KEY",
		"MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
	)
	t.Setenv("CONNECTOR_INTERNAL_TOKEN", "production-general-connector-token")
	t.Setenv("CONNECTOR_SERVICE_URL", "https://connector.internal")
	t.Setenv("MINIO_ENDPOINT", "minio.internal:9000")
	t.Setenv("MINIO_USE_SSL", "false")
	if _, err := LoadAPI(); err == nil {
		t.Fatal("production accepted a remote plaintext MinIO endpoint")
	}
	t.Setenv("MINIO_USE_SSL", "true")
	if _, err := LoadAPI(); err != nil {
		t.Fatalf("production rejected TLS-enabled MinIO: %v", err)
	}
}

func TestProcessLoadersRemoveForeignDatabaseCredentials(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATABASE_URL", "postgres://report_app:one@127.0.0.1:5432/report?sslmode=disable")
	t.Setenv("WORKER_DATABASE_URL", "postgres://report_worker:two@127.0.0.1:5432/report?sslmode=disable")
	t.Setenv("CONNECTION_TEST_DATABASE_URL", "postgres://report_connection_tester:three@127.0.0.1:5432/report?sslmode=disable")
	t.Setenv("POSTGRES_WORKER_PASSWORD", "worker-secret")
	t.Setenv("POSTGRES_CONNECTION_TEST_PASSWORD", "tester-secret")
	t.Setenv("CONNECTOR_CONNECTION_TEST_TOKEN", "test-only-connector-secret")
	t.Setenv("CONNECTION_TEST_MINIO_SECRET_KEY", "test-only-minio-secret")
	api, err := LoadAPI()
	if err != nil {
		t.Fatal(err)
	}
	if api.DatabaseURL == "" || os.Getenv("WORKER_DATABASE_URL") != "" ||
		os.Getenv("CONNECTION_TEST_DATABASE_URL") != "" ||
		os.Getenv("POSTGRES_WORKER_PASSWORD") != "" ||
		os.Getenv("POSTGRES_CONNECTION_TEST_PASSWORD") != "" ||
		os.Getenv("CONNECTOR_CONNECTION_TEST_TOKEN") != "" ||
		os.Getenv("CONNECTION_TEST_MINIO_SECRET_KEY") != "" {
		t.Fatalf("API process retained foreign credentials: %#v", api)
	}

	t.Setenv("DATABASE_URL", "postgres://report_app:one@127.0.0.1:5432/report?sslmode=disable")
	t.Setenv("WORKER_DATABASE_URL", "postgres://worker:two@127.0.0.1:5432/report?sslmode=disable")
	t.Setenv("POSTGRES_WORKER_USER", "worker")
	t.Setenv("POSTGRES_WORKER_PASSWORD", "worker-secret")
	t.Setenv("CONNECTION_TEST_DATABASE_URL", "postgres://report_connection_tester:three@127.0.0.1:5432/report?sslmode=disable")
	t.Setenv("POSTGRES_APP_PASSWORD", "app-secret")
	t.Setenv("POSTGRES_CONNECTION_TEST_PASSWORD", "tester-secret")
	t.Setenv("CONNECTOR_CONNECTION_TEST_TOKEN", "test-only-connector-secret")
	t.Setenv("CONNECTION_TEST_MINIO_SECRET_KEY", "test-only-minio-secret")
	worker, err := LoadWorker()
	if err != nil {
		t.Fatal(err)
	}
	if worker.DatabaseURL == "" || os.Getenv("DATABASE_URL") != "" ||
		os.Getenv("CONNECTION_TEST_DATABASE_URL") != "" ||
		os.Getenv("POSTGRES_APP_PASSWORD") != "" ||
		os.Getenv("POSTGRES_CONNECTION_TEST_PASSWORD") != "" ||
		os.Getenv("CONNECTOR_CONNECTION_TEST_TOKEN") != "" ||
		os.Getenv("CONNECTION_TEST_MINIO_SECRET_KEY") != "" {
		t.Fatalf("generic worker retained foreign credentials: %#v", worker)
	}

	t.Setenv("DATABASE_URL", "postgres://report_app:one@127.0.0.1:5432/report?sslmode=disable")
	t.Setenv("WORKER_DATABASE_URL", "postgres://worker:two@127.0.0.1:5432/report?sslmode=disable")
	t.Setenv("POSTGRES_APP_PASSWORD", "app-secret")
	t.Setenv("POSTGRES_WORKER_PASSWORD", "worker-secret")
	t.Setenv("CONNECTION_TEST_DATABASE_URL", "postgres://connection_tester:three@127.0.0.1:5432/report?sslmode=disable")
	t.Setenv("POSTGRES_CONNECTION_TEST_USER", "connection_tester")
	tester, err := LoadConnectionTestWorker()
	if err != nil {
		t.Fatal(err)
	}
	if tester.DatabaseURL == "" || os.Getenv("DATABASE_URL") != "" ||
		os.Getenv("WORKER_DATABASE_URL") != "" ||
		os.Getenv("POSTGRES_APP_PASSWORD") != "" ||
		os.Getenv("POSTGRES_WORKER_PASSWORD") != "" {
		t.Fatalf("connection-test worker retained foreign credentials: %#v", tester)
	}
}

func TestProcessDatabaseRoleParsingSupportsKeywordDSN(t *testing.T) {
	t.Setenv(
		"DATABASE_URL",
		"host=127.0.0.1 port=5432 dbname=report user=report_app password=secret sslmode=disable",
	)
	if _, err := LoadAPI(); err != nil {
		t.Fatalf("keyword/value PostgreSQL DSN was rejected: %v", err)
	}
	t.Setenv(
		"DATABASE_URL",
		"host=127.0.0.1 dbname=report user=report_worker password=secret sslmode=disable",
	)
	if _, err := LoadAPI(); err == nil {
		t.Fatal("API loader accepted the worker database principal")
	}
}

func TestProductionProcessDatabaseURLsHaveNoDefaults(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("DATABASE_URL", "")
	if _, err := LoadAPI(); err == nil {
		t.Fatal("production API accepted a missing DATABASE_URL")
	}
	t.Setenv("WORKER_DATABASE_URL", "")
	if _, err := LoadWorker(); err == nil {
		t.Fatal("production worker accepted a missing WORKER_DATABASE_URL")
	}
	t.Setenv("CONNECTION_TEST_DATABASE_URL", "")
	if _, err := LoadConnectionTestWorker(); err == nil {
		t.Fatal("production connection tester accepted a missing database URL")
	}
}

func TestLoadConnectionTestWorkerValidatesTimeoutAndLease(t *testing.T) {
	t.Setenv("CONNECTION_TEST_TIMEOUT", "30s")
	t.Setenv("CONNECTION_TEST_LEASE", "35s")
	if _, err := LoadConnectionTestWorker(); err == nil {
		t.Fatal("connection-test lease without fencing margin was accepted")
	}
	t.Setenv("CONNECTION_TEST_LEASE", "45s")
	if _, err := LoadConnectionTestWorker(); err != nil {
		t.Fatalf("valid connection-test timing was rejected: %v", err)
	}
}

func TestProductionConnectionTestWorkerRequiresScopedResources(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv(
		"CONNECTION_TEST_DATABASE_URL",
		"postgres://report_connection_tester:secret@db.internal:5432/report?sslmode=require",
	)
	t.Setenv(
		"DATA_SOURCE_CREDENTIAL_KEY",
		"MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
	)
	t.Setenv(
		"CONNECTOR_CONNECTION_TEST_TOKEN",
		"production-connection-test-token",
	)
	t.Setenv("CONNECTOR_INTERNAL_TOKEN", "production-general-connector-token")
	t.Setenv("CONNECTION_TEST_MINIO_ENDPOINT", "minio.internal:9000")
	t.Setenv("CONNECTION_TEST_MINIO_ACCESS_KEY", "connection-test-reader")
	t.Setenv("CONNECTION_TEST_MINIO_BUCKET_UPLOADS", "uploads")
	t.Setenv("CONNECTION_TEST_MINIO_USE_SSL", "true")
	t.Setenv("CONNECTION_TEST_MINIO_SECRET_KEY", "")
	if _, err := LoadConnectionTestWorker(); err == nil {
		t.Fatal("production tester accepted missing scoped MinIO secret")
	}
	t.Setenv("CONNECTION_TEST_MINIO_SECRET_KEY", "connection-test-reader-secret")
	t.Setenv("CONNECTION_TEST_MINIO_USE_SSL", "false")
	if _, err := LoadConnectionTestWorker(); err == nil {
		t.Fatal("production tester accepted remote plaintext MinIO")
	}
	t.Setenv("CONNECTION_TEST_MINIO_USE_SSL", "true")
	cfg, err := LoadConnectionTestWorker()
	if err != nil {
		t.Fatalf("production tester rejected scoped resources: %v", err)
	}
	if cfg.ConnectorToken != "production-connection-test-token" ||
		cfg.MinIOAccessKey != "connection-test-reader" {
		t.Fatalf("tester loaded unscoped resources: %#v", cfg)
	}
	if os.Getenv("CONNECTOR_INTERNAL_TOKEN") != "" ||
		os.Getenv("MINIO_ACCESS_KEY") != "" ||
		os.Getenv("MINIO_SECRET_KEY") != "" {
		t.Fatal("tester retained general connector or MinIO credentials")
	}
}
