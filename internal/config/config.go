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

	"github.com/jackc/pgx/v5"
)

const (
	defaultDataSourceCredentialKey = "bG9jYWxfZGF0YV9zb3VyY2VfY3JlZGVudGlhbF9rZXk="
	defaultConnectorToken          = "local_connector_token_change_me"
	defaultAskDataQuestionKey      = "bG9jYWxfYXNrZGF0YV9xdWVzdGlvbl9rZXlfMzJiISE="
)

type Config struct {
	Environment                     string
	LogLevel                        string
	HTTPAddr                        string
	ReadHeaderTimeout               time.Duration
	ReadTimeout                     time.Duration
	WriteTimeout                    time.Duration
	IdleTimeout                     time.Duration
	ShutdownTimeout                 time.Duration
	WorkerPollInterval              time.Duration
	AIBaseURL                       string
	AIModel                         string
	AIModels                        []string
	AIAPIKey                        string
	AIProviderEndpoints             []AIProviderEndpoint
	AIProviderSelectionMode         string
	AIRequestTimeout                time.Duration
	AIAttemptTimeout                time.Duration
	AIPrimaryFailoverTimeout        time.Duration
	AIRetryBaseDelay                time.Duration
	AIRetryMaxDelay                 time.Duration
	AIMaxAttempts                   int
	AIMaxInputBytes                 int
	AIInputCostMicrosPerMTokens     int64
	AIOutputCostMicrosPerMTokens    int64
	AIConfidenceThreshold           float64
	AIEmbeddingBaseURL              string
	AIEmbeddingModel                string
	AIEmbeddingAPIKey               string
	AIEmbeddingDimensions           int
	AIEmbeddingTimeout              time.Duration
	DatasetAIRetrievalMode          string
	DatabaseURL                     string
	WarehouseDatabaseURL            string
	WarehouseQueryDatabaseURL       string
	MinIOEndpoint                   string
	MinIOAccessKey                  string
	MinIOSecretKey                  string
	MinIOUseSSL                     bool
	MinIOUploadsBucket              string
	ReportExportRendererURL         string
	ReportExportRendererToken       string
	AuthTokenIssuer                 string
	AuthAccessSecret                string
	AuthAccessTTL                   time.Duration
	AuthRefreshTTL                  time.Duration
	AuthBcryptCost                  int
	AskDataQuestionRetentionMode    string
	AskDataQuestionRetentionTTL     time.Duration
	AskDataRunArtifactTTL           time.Duration
	AskDataClarificationTimeout     time.Duration
	AskDataQuestionEncryptionKey    string
	AskDataBudgetOverrides          []AskDataBudgetOverride
	AskDataNebulaAddresses          []string
	AskDataNebulaSpace              string
	AskDataNebulaUsername           string
	AskDataNebulaPassword           string
	AskDataNebulaTLSEnabled         bool
	AskDataGraphWriteEnabled        bool
	AskDataRetrievalThreshold       float64
	AskDataBindingThreshold         float64
	AskDataProjectionLease          time.Duration
	AskDataProfileScanLimit         int
	AskDataReleaseRetentionCount    int
	AskDataEvaluationMinimumCases   int
	AskDataEvaluationStrictMinimum  float64
	AskDataEvaluationWilsonMinimum  float64
	AskDataShadowCanaryMode         string
	AskDataCanaryPercent            int
	ConnectorURL                    string
	ConnectorToken                  string
	ConnectorHTTPMaxRequestBytes    int64
	ConnectorJSONMaxResponseBytes   int64
	ConnectorSampleMaxCellBytes     int
	ConnectorSampleMaxRowBytes      int
	ConnectorSampleMaxResponseBytes int64
	ConnectorStreamMaxCellBytes     int
	ConnectorStreamMaxRowBytes      int
	ConnectorStreamMaxBytes         int64
	WarehouseStageMaxBytes          int64
	DataSourceCredentialKey         string
}

type AIProviderEndpoint struct {
	Name            string
	BaseURL         string
	APIKey          string
	Models          []string
	ThinkingEnabled bool
	ReasoningEffort string
	ResponseFormat  string
	MaxOutputTokens int
}

type databaseProcess struct {
	urlKey                   string
	developmentURL           string
	roleKey                  string
	developmentRole          string
	warehouseURLKey          string
	developmentWarehouseURL  string
	warehouseRoleKey         string
	developmentWarehouseRole string
	forbiddenKeys            []string
}

var (
	apiDatabaseProcess = databaseProcess{
		urlKey:                   "DATABASE_URL",
		developmentURL:           "postgres://report_app:local_report_password@127.0.0.1:5432/intelligent_report_control?sslmode=disable",
		roleKey:                  "POSTGRES_APP_USER",
		developmentRole:          "report_app",
		warehouseURLKey:          "WAREHOUSE_DATABASE_URL",
		developmentWarehouseURL:  "postgres://report_warehouse_reader:local_warehouse_reader_password@127.0.0.1:5433/intelligent_report_warehouse?sslmode=disable",
		warehouseRoleKey:         "WAREHOUSE_READER_USER",
		developmentWarehouseRole: "report_warehouse_reader",
		forbiddenKeys: []string{
			"WORKER_DATABASE_URL", "CONNECTION_TEST_DATABASE_URL", "WAREHOUSE_WORKER_DATABASE_URL",
			"WAREHOUSE_QUERY_DATABASE_URL",
			"POSTGRES_USER", "POSTGRES_PASSWORD", "POSTGRES_APP_PASSWORD",
			"POSTGRES_WORKER_USER", "POSTGRES_WORKER_PASSWORD",
			"POSTGRES_CONNECTION_TEST_USER", "POSTGRES_CONNECTION_TEST_PASSWORD",
			"PGPASSWORD", "PGPASSFILE", "PGSERVICE", "PGSERVICEFILE",
			"CONNECTOR_CONNECTION_TEST_TOKEN",
			"ASKDATA_NEBULA_ROOT_PASSWORD", "ASKDATA_NEBULA_BOOTSTRAP_ROOT_PASSWORD",
			"ASKDATA_NEBULA_WORKER_USER", "ASKDATA_NEBULA_WORKER_PASSWORD",
			"CONNECTION_TEST_MINIO_ACCESS_KEY",
			"CONNECTION_TEST_MINIO_SECRET_KEY",
		},
	}
	workerDatabaseProcess = databaseProcess{
		urlKey:                   "WORKER_DATABASE_URL",
		developmentURL:           "postgres://report_worker:local_worker_password@127.0.0.1:5432/intelligent_report_control?sslmode=disable",
		roleKey:                  "POSTGRES_WORKER_USER",
		developmentRole:          "report_worker",
		warehouseURLKey:          "WAREHOUSE_WORKER_DATABASE_URL",
		developmentWarehouseURL:  "postgres://report_warehouse_worker:local_warehouse_worker_password@127.0.0.1:5433/intelligent_report_warehouse?sslmode=disable",
		warehouseRoleKey:         "WAREHOUSE_WORKER_USER",
		developmentWarehouseRole: "report_warehouse_worker",
		forbiddenKeys: []string{
			"DATABASE_URL", "CONNECTION_TEST_DATABASE_URL", "WAREHOUSE_DATABASE_URL",
			"POSTGRES_USER", "POSTGRES_PASSWORD",
			"POSTGRES_APP_USER", "POSTGRES_APP_PASSWORD",
			"POSTGRES_WORKER_PASSWORD",
			"POSTGRES_CONNECTION_TEST_USER", "POSTGRES_CONNECTION_TEST_PASSWORD",
			"PGPASSWORD", "PGPASSFILE", "PGSERVICE", "PGSERVICEFILE",
			"CONNECTOR_CONNECTION_TEST_TOKEN",
			"ASKDATA_NEBULA_ROOT_PASSWORD", "ASKDATA_NEBULA_BOOTSTRAP_ROOT_PASSWORD",
			"ASKDATA_NEBULA_API_USER", "ASKDATA_NEBULA_API_PASSWORD",
			"CONNECTION_TEST_MINIO_ACCESS_KEY",
			"CONNECTION_TEST_MINIO_SECRET_KEY",
		},
	}
)

// Load is retained for API-oriented utilities such as seed and evaluation.
func Load() (Config, error) { return LoadAPI() }

// LoadAPI reads only the API database credential and removes worker credentials
// from the process environment before any long-lived service is constructed.
func LoadAPI() (Config, error) {
	return loadApplication(apiDatabaseProcess)
}

// LoadWorker reads only the generic background-worker credential. It never
// exposes either the API or connection-test role credential to that process.
func LoadWorker() (Config, error) {
	return loadApplication(workerDatabaseProcess)
}

func loadApplication(process databaseProcess) (Config, error) {
	environment := envOrDefault("APP_ENV", "development")
	databaseURL, err := loadProcessDatabaseURL(environment, process)
	if err != nil {
		return Config{}, err
	}
	warehouseDatabaseURL, err := loadProcessDatabaseURL(environment, databaseProcess{
		urlKey:          process.warehouseURLKey,
		developmentURL:  process.developmentWarehouseURL,
		roleKey:         process.warehouseRoleKey,
		developmentRole: process.developmentWarehouseRole,
	})
	if err != nil {
		return Config{}, err
	}
	warehouseQueryDatabaseURL := warehouseDatabaseURL
	if process.urlKey == workerDatabaseProcess.urlKey {
		warehouseQueryDatabaseURL, err = loadProcessDatabaseURL(environment, databaseProcess{
			urlKey:          "WAREHOUSE_QUERY_DATABASE_URL",
			developmentURL:  "postgres://report_warehouse_reader:local_warehouse_reader_password@127.0.0.1:5433/intelligent_report_warehouse?sslmode=disable",
			roleKey:         "WAREHOUSE_READER_USER",
			developmentRole: "report_warehouse_reader",
		})
		if err != nil {
			return Config{}, err
		}
	}
	askDataBudgetOverrides, err := ParseAskDataBudgetOverrides(os.Getenv("ASKDATA_RUN_BUDGET_OVERRIDES"))
	if err != nil {
		return Config{}, err
	}
	aiBaseURL := envOrDefault("AI_BASE_URL", "https://mgallery.haier.net/v1/")
	aiAPIKey := os.Getenv("AI_API_KEY")
	aiModel := envOrDefault("AI_MODEL", "MiniMax-M2")
	aiModels := parseUniqueCSV(os.Getenv("AI_MODELS"))
	if len(aiModels) == 0 {
		aiModels = []string{aiModel}
	}
	providerThinkingEnabled, err := envBool("AI_THINKING_ENABLED", false)
	if err != nil {
		return Config{}, err
	}
	askDataNebulaTLS, err := envBool("ASKDATA_NEBULA_TLS_ENABLED", false)
	if err != nil {
		return Config{}, err
	}
	isWorkerProcess := process.urlKey == workerDatabaseProcess.urlKey
	defaultNebulaUser, defaultNebulaPassword := "askdata_api", "local_nebula_read_1"
	if isWorkerProcess {
		defaultNebulaUser, defaultNebulaPassword = "askdata_worker", "local_nebula_write_1"
	}
	providerMaxOutputTokens, err := envOptionalInt("AI_MAX_OUTPUT_TOKENS_OVERRIDE")
	if err != nil {
		return Config{}, err
	}
	aiProviderEndpoints := []AIProviderEndpoint{}
	if strings.TrimSpace(aiAPIKey) != "" {
		aiProviderEndpoints = append(aiProviderEndpoints, AIProviderEndpoint{
			Name: "gateway", BaseURL: aiBaseURL, APIKey: aiAPIKey,
			Models:          append([]string(nil), aiModels...),
			ThinkingEnabled: providerThinkingEnabled,
			ReasoningEffort: envOrDefault("AI_REASONING_EFFORT", ""),
			ResponseFormat:  envOrDefault("AI_RESPONSE_FORMAT", ""),
			MaxOutputTokens: providerMaxOutputTokens,
		})
	}
	for _, endpoint := range []struct {
		name, baseURLKey, defaultBaseURL, apiKeyKey, modelsKey string
		thinkingKey, reasoningEffortKey, responseFormatKey     string
		maxOutputTokensKey                                     string
	}{
		{
			"deepseek", "AI_DEEPSEEK_BASE_URL",
			"https://api.deepseek.com", "AI_DEEPSEEK_API_KEY",
			"AI_DEEPSEEK_MODELS",
			"AI_DEEPSEEK_THINKING_ENABLED", "AI_DEEPSEEK_REASONING_EFFORT",
			"AI_DEEPSEEK_RESPONSE_FORMAT",
			"AI_DEEPSEEK_MAX_OUTPUT_TOKENS",
		},
		{
			"glm", "AI_GLM_BASE_URL",
			"https://open.bigmodel.cn/api/paas/v4", "AI_GLM_API_KEY",
			"AI_GLM_MODELS",
			"AI_GLM_THINKING_ENABLED", "AI_GLM_REASONING_EFFORT",
			"AI_GLM_RESPONSE_FORMAT",
			"AI_GLM_MAX_OUTPUT_TOKENS",
		},
		{
			"minimax", "AI_MINIMAX_BASE_URL",
			"https://api.minimaxi.com/v1", "AI_MINIMAX_API_KEY",
			"AI_MINIMAX_MODELS",
			"AI_MINIMAX_THINKING_ENABLED", "AI_MINIMAX_REASONING_EFFORT",
			"AI_MINIMAX_RESPONSE_FORMAT",
			"AI_MINIMAX_MAX_OUTPUT_TOKENS",
		},
	} {
		apiKey := strings.TrimSpace(os.Getenv(endpoint.apiKeyKey))
		if apiKey == "" {
			continue
		}
		thinkingEnabled, err := envBool(endpoint.thinkingKey, false)
		if err != nil {
			return Config{}, err
		}
		maxOutputTokens, err := envOptionalInt(endpoint.maxOutputTokensKey)
		if err != nil {
			return Config{}, err
		}
		aiProviderEndpoints = append(
			aiProviderEndpoints,
			AIProviderEndpoint{
				Name:            endpoint.name,
				BaseURL:         envOrDefault(endpoint.baseURLKey, endpoint.defaultBaseURL),
				APIKey:          apiKey,
				Models:          parseUniqueCSV(os.Getenv(endpoint.modelsKey)),
				ThinkingEnabled: thinkingEnabled,
				ReasoningEffort: envOrDefault(endpoint.reasoningEffortKey, ""),
				ResponseFormat:  envOrDefault(endpoint.responseFormatKey, ""),
				MaxOutputTokens: maxOutputTokens,
			},
		)
	}
	embeddingBaseURL := envOrDefault("AI_EMBEDDING_BASE_URL", aiBaseURL)
	embeddingAPIKey := os.Getenv("AI_EMBEDDING_API_KEY")
	if strings.TrimSpace(embeddingAPIKey) == "" {
		embeddingAPIKey = aiAPIKey
	}
	cfg := Config{
		Environment:                     environment,
		LogLevel:                        envOrDefault("APP_LOG_LEVEL", "info"),
		HTTPAddr:                        envOrDefault("API_HTTP_ADDR", ":8080"),
		ReadHeaderTimeout:               5 * time.Second,
		ReadTimeout:                     15 * time.Second,
		WriteTimeout:                    60 * time.Second,
		IdleTimeout:                     60 * time.Second,
		ShutdownTimeout:                 10 * time.Second,
		WorkerPollInterval:              2 * time.Second,
		AIBaseURL:                       aiBaseURL,
		AIModel:                         aiModel,
		AIModels:                        aiModels,
		AIAPIKey:                        aiAPIKey,
		AIProviderEndpoints:             aiProviderEndpoints,
		AIProviderSelectionMode:         strings.ToLower(envOrDefault("AI_PROVIDER_SELECTION_MODE", "primary")),
		AIRequestTimeout:                25 * time.Second,
		AIAttemptTimeout:                8 * time.Second,
		AIPrimaryFailoverTimeout:        8 * time.Second,
		AIRetryBaseDelay:                200 * time.Millisecond,
		AIRetryMaxDelay:                 2 * time.Second,
		AIMaxAttempts:                   3,
		AIMaxInputBytes:                 256 << 10,
		AIConfidenceThreshold:           0.8,
		AIEmbeddingBaseURL:              embeddingBaseURL,
		AIEmbeddingModel:                envOrDefault("AI_EMBEDDING_MODEL", "Qwen3-Embedding-4B"),
		AIEmbeddingAPIKey:               embeddingAPIKey,
		AIEmbeddingDimensions:           2560,
		AIEmbeddingTimeout:              15 * time.Second,
		DatasetAIRetrievalMode:          strings.ToUpper(envOrDefault("DATASET_AI_RETRIEVAL_MODE", "HYBRID")),
		DatabaseURL:                     databaseURL,
		WarehouseDatabaseURL:            warehouseDatabaseURL,
		WarehouseQueryDatabaseURL:       warehouseQueryDatabaseURL,
		MinIOEndpoint:                   envOrDefault("MINIO_ENDPOINT", "127.0.0.1:9000"),
		MinIOAccessKey:                  envOrDefault("MINIO_ACCESS_KEY", "report_minio"),
		MinIOSecretKey:                  envOrDefault("MINIO_SECRET_KEY", "local_minio_password"),
		MinIOUseSSL:                     strings.EqualFold(os.Getenv("MINIO_USE_SSL"), "true"),
		MinIOUploadsBucket:              envOrDefault("MINIO_BUCKET_UPLOADS", "uploads"),
		ReportExportRendererURL:         envOrDefault("REPORT_EXPORT_RENDERER_URL", ""),
		ReportExportRendererToken:       envOrDefault("REPORT_EXPORT_RENDERER_TOKEN", "local_report_export_renderer_token"),
		AuthTokenIssuer:                 envOrDefault("AUTH_TOKEN_ISSUER", "intelligent-report-system"),
		AuthAccessSecret:                envOrDefault("AUTH_ACCESS_TOKEN_SECRET", "local_access_token_secret_change_me"),
		AuthAccessTTL:                   15 * time.Minute,
		AuthRefreshTTL:                  7 * 24 * time.Hour,
		AuthBcryptCost:                  12,
		AskDataQuestionRetentionMode:    questionRetentionMode(),
		AskDataQuestionRetentionTTL:     24 * time.Hour,
		AskDataRunArtifactTTL:           30 * 24 * time.Hour,
		AskDataClarificationTimeout:     30 * time.Minute,
		AskDataQuestionEncryptionKey:    questionEncryptionKey(environment),
		AskDataBudgetOverrides:          askDataBudgetOverrides,
		AskDataNebulaAddresses:          parseUniqueCSV(envOrDefault("ASKDATA_NEBULA_ADDRESSES", "127.0.0.1:9669")),
		AskDataNebulaSpace:              envOrDefault("ASKDATA_NEBULA_SPACE", "askdata_semantic"),
		AskDataNebulaUsername:           envOrDefault("ASKDATA_NEBULA_USERNAME", defaultNebulaUser),
		AskDataNebulaPassword:           envOrDefault("ASKDATA_NEBULA_PASSWORD", defaultNebulaPassword),
		AskDataNebulaTLSEnabled:         askDataNebulaTLS,
		AskDataGraphWriteEnabled:        isWorkerProcess,
		AskDataRetrievalThreshold:       0.70,
		AskDataBindingThreshold:         0.80,
		AskDataProjectionLease:          2 * time.Minute,
		AskDataProfileScanLimit:         100_000,
		AskDataReleaseRetentionCount:    10,
		AskDataEvaluationMinimumCases:   2_000,
		AskDataEvaluationStrictMinimum:  0.96,
		AskDataEvaluationWilsonMinimum:  0.95,
		AskDataShadowCanaryMode:         strings.ToUpper(envOrDefault("ASKDATA_SHADOW_CANARY_MODE", "OFF")),
		AskDataCanaryPercent:            0,
		ConnectorURL:                    envOrDefault("CONNECTOR_SERVICE_URL", "http://127.0.0.1:8090"),
		ConnectorToken:                  envOrDefault("CONNECTOR_INTERNAL_TOKEN", defaultConnectorToken),
		ConnectorHTTPMaxRequestBytes:    1 << 20,
		ConnectorJSONMaxResponseBytes:   64 << 20,
		ConnectorSampleMaxCellBytes:     16 << 10,
		ConnectorSampleMaxRowBytes:      64 << 10,
		ConnectorSampleMaxResponseBytes: 512 << 10,
		ConnectorStreamMaxCellBytes:     1 << 20,
		ConnectorStreamMaxRowBytes:      4 << 20,
		ConnectorStreamMaxBytes:         1 << 30,
		WarehouseStageMaxBytes:          1 << 30,
		DataSourceCredentialKey:         envOrDefault("DATA_SOURCE_CREDENTIAL_KEY", defaultDataSourceCredentialKey),
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
		{"AI_PRIMARY_FAILOVER_TIMEOUT", &cfg.AIPrimaryFailoverTimeout},
		{"AI_RETRY_BASE_DELAY", &cfg.AIRetryBaseDelay},
		{"AI_RETRY_MAX_DELAY", &cfg.AIRetryMaxDelay},
		{"AI_EMBEDDING_TIMEOUT", &cfg.AIEmbeddingTimeout},
		{"AUTH_ACCESS_TOKEN_TTL", &cfg.AuthAccessTTL},
		{"AUTH_REFRESH_TOKEN_TTL", &cfg.AuthRefreshTTL},
		{"ASKDATA_QUESTION_RETENTION_TTL", &cfg.AskDataQuestionRetentionTTL},
		{"ASKDATA_RUN_ARTIFACT_TTL", &cfg.AskDataRunArtifactTTL},
		{"ASKDATA_CLARIFICATION_TIMEOUT", &cfg.AskDataClarificationTimeout},
		{"ASKDATA_PROJECTION_LEASE", &cfg.AskDataProjectionLease},
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
		{"AI_EMBEDDING_DIMENSIONS", &cfg.AIEmbeddingDimensions},
		{"ASKDATA_PROFILE_SCAN_LIMIT", &cfg.AskDataProfileScanLimit},
		{"ASKDATA_RELEASE_RETENTION_COUNT", &cfg.AskDataReleaseRetentionCount},
		{"ASKDATA_EVALUATION_MINIMUM_CASES", &cfg.AskDataEvaluationMinimumCases},
		{"ASKDATA_CANARY_PERCENT", &cfg.AskDataCanaryPercent},
		{"CONNECTOR_METADATA_SAMPLE_MAX_CELL_BYTES", &cfg.ConnectorSampleMaxCellBytes},
		{"CONNECTOR_METADATA_SAMPLE_MAX_ROW_BYTES", &cfg.ConnectorSampleMaxRowBytes},
		{"CONNECTOR_STREAM_MAX_CELL_BYTES", &cfg.ConnectorStreamMaxCellBytes},
		{"CONNECTOR_STREAM_MAX_ROW_BYTES", &cfg.ConnectorStreamMaxRowBytes},
	}
	byteOptions := []struct {
		key    string
		target *int64
	}{
		{"CONNECTOR_HTTP_MAX_REQUEST_BYTES", &cfg.ConnectorHTTPMaxRequestBytes},
		{"CONNECTOR_JSON_MAX_RESPONSE_BYTES", &cfg.ConnectorJSONMaxResponseBytes},
		{"CONNECTOR_METADATA_SAMPLE_MAX_RESPONSE_BYTES", &cfg.ConnectorSampleMaxResponseBytes},
		{"CONNECTOR_STREAM_MAX_BYTES", &cfg.ConnectorStreamMaxBytes},
		{"WAREHOUSE_STAGE_MAX_BYTES", &cfg.WarehouseStageMaxBytes},
	}
	for _, item := range byteOptions {
		if value := os.Getenv(item.key); value != "" {
			parsed, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return Config{}, fmt.Errorf("parse %s: %w", item.key, err)
			}
			*item.target = parsed
		}
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
	for _, item := range []struct {
		key    string
		target *float64
	}{
		{"ASKDATA_RETRIEVAL_THRESHOLD", &cfg.AskDataRetrievalThreshold},
		{"ASKDATA_BINDING_THRESHOLD", &cfg.AskDataBindingThreshold},
		{"ASKDATA_EVALUATION_STRICT_MINIMUM", &cfg.AskDataEvaluationStrictMinimum},
		{"ASKDATA_EVALUATION_WILSON_MINIMUM", &cfg.AskDataEvaluationWilsonMinimum},
	} {
		if value := os.Getenv(item.key); value != "" {
			parsed, err := strconv.ParseFloat(value, 64)
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

func questionRetentionMode() string {
	return strings.ToUpper(envOrDefault("ASKDATA_QUESTION_RETENTION_MODE", "ENCRYPTED_SHORT_TERM"))
}

func questionEncryptionKey(environment string) string {
	if value := strings.TrimSpace(os.Getenv("ASKDATA_QUESTION_ENCRYPTION_KEY")); value != "" {
		return value
	}
	if questionRetentionMode() != "ENCRYPTED_SHORT_TERM" {
		return ""
	}
	if strings.EqualFold(environment, "development") || strings.EqualFold(environment, "test") {
		return defaultAskDataQuestionKey
	}
	return ""
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
	if c.AIRequestTimeout <= 0 || c.AIRequestTimeout*2 >= c.WriteTimeout {
		return errors.New("AI_REQUEST_TIMEOUT must be greater than zero and twice its value must be less than API_WRITE_TIMEOUT")
	}
	if c.AIAttemptTimeout <= 0 || c.AIAttemptTimeout > c.AIRequestTimeout {
		return errors.New("AI_ATTEMPT_TIMEOUT must be greater than zero and at most AI_REQUEST_TIMEOUT")
	}
	if c.AIPrimaryFailoverTimeout <= 0 || c.AIPrimaryFailoverTimeout > c.AIRequestTimeout {
		return errors.New("AI_PRIMARY_FAILOVER_TIMEOUT must be greater than zero and at most AI_REQUEST_TIMEOUT")
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
		if !validAIBaseURL(c.AIBaseURL) || len(c.AIModels) == 0 {
			return errors.New("AI_BASE_URL or AI_MODELS is invalid")
		}
		for _, model := range c.AIModels {
			if strings.TrimSpace(model) == "" || len(model) > 256 || strings.ContainsAny(model, "\r\n\t") {
				return errors.New("AI_MODELS contains an invalid model name")
			}
		}
	}
	for _, endpoint := range c.AIProviderEndpoints {
		if strings.TrimSpace(endpoint.Name) == "" ||
			!validAIBaseURL(endpoint.BaseURL) ||
			strings.TrimSpace(endpoint.APIKey) == "" ||
			len(endpoint.Models) == 0 {
			return errors.New("configured AI provider endpoint is invalid")
		}
		for _, model := range endpoint.Models {
			if strings.TrimSpace(model) == "" || len(model) > 256 ||
				strings.ContainsAny(model, "\r\n\t") {
				return errors.New(
					"configured AI provider model name is invalid",
				)
			}
		}
		if effort := strings.ToLower(strings.TrimSpace(endpoint.ReasoningEffort)); effort != "" && effort != "low" && effort != "medium" && effort != "high" {
			return errors.New(
				"configured AI provider reasoning effort must be low, medium, or high",
			)
		}
		if format := strings.ToLower(strings.TrimSpace(endpoint.ResponseFormat)); format != "" && format != "json_schema" && format != "json_object" && format != "prompt" {
			return errors.New(
				"configured AI provider response format must be json_schema, json_object, or prompt",
			)
		}
		if endpoint.MaxOutputTokens < 0 || endpoint.MaxOutputTokens > 1_000_000 {
			return errors.New(
				"configured AI provider max output tokens must be between 1 and 1000000",
			)
		}
	}
	if c.AIProviderSelectionMode != "primary" &&
		c.AIProviderSelectionMode != "round_robin" {
		return errors.New(
			"AI_PROVIDER_SELECTION_MODE must be primary or round_robin",
		)
	}
	if c.AIEmbeddingTimeout <= 0 || c.AIEmbeddingTimeout > 2*time.Minute {
		return errors.New("AI_EMBEDDING_TIMEOUT must be greater than zero and at most 2 minutes")
	}
	// The current pgvector migration uses halfvec(2560). A model dimension change needs a
	// versioned column/index migration and a full backfill; accepting another value here would
	// defer the mismatch to an asynchronous SQL cast failure.
	if c.AIEmbeddingDimensions != 2560 {
		return errors.New("AI_EMBEDDING_DIMENSIONS must be 2560 for the current semantic vector schemas")
	}
	if strings.TrimSpace(c.AIEmbeddingAPIKey) != "" {
		if !validAIBaseURL(c.AIEmbeddingBaseURL) || strings.TrimSpace(c.AIEmbeddingModel) == "" {
			return errors.New("AI_EMBEDDING_BASE_URL or AI_EMBEDDING_MODEL is invalid")
		}
	}
	if c.DatasetAIRetrievalMode != "LEXICAL" && c.DatasetAIRetrievalMode != "SHADOW" && c.DatasetAIRetrievalMode != "HYBRID" {
		return errors.New("DATASET_AI_RETRIEVAL_MODE must be LEXICAL, SHADOW or HYBRID")
	}
	if strings.TrimSpace(c.DatabaseURL) == "" {
		return errors.New("process database URL must not be empty")
	}
	if strings.TrimSpace(c.WarehouseDatabaseURL) == "" {
		return errors.New("warehouse database URL must not be empty")
	}
	if strings.TrimSpace(c.WarehouseQueryDatabaseURL) == "" {
		return errors.New("warehouse query database URL must not be empty")
	}
	if samePostgresDatabase(c.DatabaseURL, c.WarehouseDatabaseURL) {
		return errors.New("control and warehouse PostgreSQL must use different physical endpoints")
	}
	if samePostgresDatabase(c.DatabaseURL, c.WarehouseQueryDatabaseURL) {
		return errors.New("control and warehouse query PostgreSQL must use different physical endpoints")
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
	if err := validateAskDataRetentionConfig(c); err != nil {
		return err
	}
	if c.AskDataClarificationTimeout < time.Minute || c.AskDataClarificationTimeout > 24*time.Hour {
		return errors.New("ASKDATA_CLARIFICATION_TIMEOUT must be between 1m and 24h")
	}
	if err := validateAskDataBudgetOverrides(c.AskDataBudgetOverrides); err != nil {
		return err
	}
	if err := validateAskDataOperationalConfig(c); err != nil {
		return err
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
	if strings.EqualFold(c.Environment, "production") &&
		!c.MinIOUseSSL && !loopbackEndpoint(c.MinIOEndpoint) {
		return errors.New(
			"production MinIO must use TLS unless it is loopback",
		)
	}
	if strings.TrimSpace(c.MinIOUploadsBucket) == "" {
		return errors.New("MinIO uploads bucket name must not be empty")
	}
	if c.ReportExportRendererURL != "" && !validAIBaseURL(c.ReportExportRendererURL) {
		return errors.New("REPORT_EXPORT_RENDERER_URL must use HTTPS or loopback HTTP")
	}
	if strings.EqualFold(c.Environment, "production") && c.ReportExportRendererURL != "" &&
		len(strings.TrimSpace(c.ReportExportRendererToken)) < 24 {
		return errors.New("production report export renderer token must contain at least 24 characters")
	}
	credentialKey, err := base64.StdEncoding.DecodeString(strings.TrimSpace(c.DataSourceCredentialKey))
	if err != nil || len(credentialKey) != 32 {
		return errors.New("DATA_SOURCE_CREDENTIAL_KEY must be base64-encoded 32 bytes")
	}
	if strings.EqualFold(c.Environment, "production") && c.DataSourceCredentialKey == defaultDataSourceCredentialKey {
		return errors.New("production must override DATA_SOURCE_CREDENTIAL_KEY")
	}
	if strings.EqualFold(c.Environment, "production") &&
		c.ConnectorToken == defaultConnectorToken {
		return errors.New("production must override CONNECTOR_INTERNAL_TOKEN")
	}
	if c.ConnectorHTTPMaxRequestBytes < 4096 ||
		c.ConnectorHTTPMaxRequestBytes > 16<<20 ||
		c.ConnectorJSONMaxResponseBytes < 64<<10 ||
		c.ConnectorJSONMaxResponseBytes > 256<<20 ||
		c.ConnectorSampleMaxCellBytes < 256 ||
		c.ConnectorSampleMaxCellBytes > 1<<20 ||
		c.ConnectorSampleMaxRowBytes < c.ConnectorSampleMaxCellBytes ||
		c.ConnectorSampleMaxRowBytes > 4<<20 ||
		c.ConnectorSampleMaxResponseBytes < int64(c.ConnectorSampleMaxRowBytes) ||
		c.ConnectorSampleMaxResponseBytes > 8<<20 ||
		c.ConnectorStreamMaxCellBytes < 1024 ||
		c.ConnectorStreamMaxCellBytes > 16<<20 ||
		c.ConnectorStreamMaxRowBytes < c.ConnectorStreamMaxCellBytes ||
		c.ConnectorStreamMaxRowBytes > 64<<20 ||
		c.ConnectorStreamMaxBytes < int64(c.ConnectorStreamMaxRowBytes) ||
		c.ConnectorStreamMaxBytes > 16<<30 ||
		c.WarehouseStageMaxBytes < 1<<20 ||
		c.WarehouseStageMaxBytes > c.ConnectorStreamMaxBytes {
		return errors.New("connector or warehouse byte limit configuration is invalid")
	}
	if strings.EqualFold(c.Environment, "production") {
		for _, key := range []string{
			"CONNECTOR_HTTP_MAX_REQUEST_BYTES",
			"CONNECTOR_JSON_MAX_RESPONSE_BYTES",
			"CONNECTOR_METADATA_SAMPLE_MAX_CELL_BYTES",
			"CONNECTOR_METADATA_SAMPLE_MAX_ROW_BYTES",
			"CONNECTOR_METADATA_SAMPLE_MAX_RESPONSE_BYTES",
			"CONNECTOR_STREAM_MAX_CELL_BYTES",
			"CONNECTOR_STREAM_MAX_ROW_BYTES",
			"CONNECTOR_STREAM_MAX_BYTES",
			"WAREHOUSE_STAGE_MAX_BYTES",
		} {
			if _, configured := os.LookupEnv(key); !configured {
				return fmt.Errorf("%s must be explicitly configured in production", key)
			}
		}
	}
	return nil
}

func validateAskDataOperationalConfig(c Config) error {
	if len(c.AskDataNebulaAddresses) < 1 || len(c.AskDataNebulaAddresses) > 16 {
		return errors.New("ASKDATA_NEBULA_ADDRESSES must contain between 1 and 16 endpoints")
	}
	for _, endpoint := range c.AskDataNebulaAddresses {
		host, port, err := net.SplitHostPort(strings.TrimSpace(endpoint))
		if err != nil || strings.TrimSpace(host) == "" || strings.TrimSpace(port) == "" {
			return errors.New("ASKDATA_NEBULA_ADDRESSES contains an invalid endpoint")
		}
	}
	if !stableConfigCode(c.AskDataNebulaSpace, false) || !stableConfigCode(c.AskDataNebulaUsername, true) ||
		len(strings.TrimSpace(c.AskDataNebulaPassword)) < 12 {
		return errors.New("AskData Nebula space or process credential is invalid")
	}
	if c.AskDataRetrievalThreshold <= 0 || c.AskDataRetrievalThreshold > 1 ||
		c.AskDataBindingThreshold <= 0 || c.AskDataBindingThreshold > 1 ||
		c.AskDataBindingThreshold < c.AskDataRetrievalThreshold {
		return errors.New("AskData retrieval and binding thresholds are invalid")
	}
	if c.AskDataProjectionLease < 10*time.Second || c.AskDataProjectionLease > 30*time.Minute ||
		c.AskDataProfileScanLimit < 1_000 || c.AskDataProfileScanLimit > 10_000_000 ||
		c.AskDataReleaseRetentionCount < 2 || c.AskDataReleaseRetentionCount > 100 {
		return errors.New("AskData projection, profile, or release retention configuration is invalid")
	}
	if c.AskDataEvaluationMinimumCases < 2_000 || c.AskDataEvaluationMinimumCases > 100_000 ||
		c.AskDataEvaluationStrictMinimum < .96 || c.AskDataEvaluationStrictMinimum > 1 ||
		c.AskDataEvaluationWilsonMinimum < .95 || c.AskDataEvaluationWilsonMinimum > c.AskDataEvaluationStrictMinimum {
		return errors.New("AskData evaluation gate configuration is below the governed minimum")
	}
	switch strings.ToUpper(strings.TrimSpace(c.AskDataShadowCanaryMode)) {
	case "OFF", "SHADOW":
		if c.AskDataCanaryPercent != 0 {
			return errors.New("ASKDATA_CANARY_PERCENT must be zero outside CANARY mode")
		}
	case "CANARY":
		if c.AskDataCanaryPercent != 5 && c.AskDataCanaryPercent != 20 && c.AskDataCanaryPercent != 50 {
			return errors.New("ASKDATA_CANARY_PERCENT must be 5, 20, or 50")
		}
	default:
		return errors.New("ASKDATA_SHADOW_CANARY_MODE must be OFF, SHADOW, or CANARY")
	}
	if strings.EqualFold(c.Environment, "production") {
		if !c.AskDataNebulaTLSEnabled {
			return errors.New("production AskData Nebula TLS must be enabled")
		}
		for _, key := range []string{
			"ASKDATA_NEBULA_ADDRESSES", "ASKDATA_NEBULA_SPACE", "ASKDATA_NEBULA_USERNAME",
			"ASKDATA_NEBULA_PASSWORD", "ASKDATA_NEBULA_TLS_ENABLED", "ASKDATA_RETRIEVAL_THRESHOLD",
			"ASKDATA_BINDING_THRESHOLD", "ASKDATA_PROJECTION_LEASE", "ASKDATA_PROFILE_SCAN_LIMIT",
			"ASKDATA_RELEASE_RETENTION_COUNT", "ASKDATA_EVALUATION_MINIMUM_CASES",
			"ASKDATA_EVALUATION_STRICT_MINIMUM", "ASKDATA_EVALUATION_WILSON_MINIMUM",
			"ASKDATA_SHADOW_CANARY_MODE", "ASKDATA_CANARY_PERCENT",
		} {
			if _, configured := os.LookupEnv(key); !configured {
				return fmt.Errorf("%s must be explicitly configured in production", key)
			}
		}
	}
	return nil
}

func stableConfigCode(value string, allowDash bool) bool {
	value = strings.TrimSpace(value)
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if (character < 'A' || character > 'Z') && (character < 'a' || character > 'z') &&
			(character < '0' || character > '9') && character != '_' && (!allowDash || character != '-') {
			return false
		}
	}
	return true
}

func validateAskDataRetentionConfig(c Config) error {
	mode := strings.ToUpper(strings.TrimSpace(c.AskDataQuestionRetentionMode))
	if mode != "HASH_ONLY" && mode != "ENCRYPTED_SHORT_TERM" {
		return errors.New("ASKDATA_QUESTION_RETENTION_MODE must be HASH_ONLY or ENCRYPTED_SHORT_TERM")
	}
	if c.AskDataQuestionRetentionTTL <= 0 || c.AskDataQuestionRetentionTTL > 7*24*time.Hour {
		return errors.New("ASKDATA_QUESTION_RETENTION_TTL must be greater than zero and at most 168h")
	}
	if c.AskDataRunArtifactTTL < c.AskDataQuestionRetentionTTL ||
		c.AskDataRunArtifactTTL > 365*24*time.Hour {
		return errors.New("ASKDATA_RUN_ARTIFACT_TTL must be at least the question TTL and at most 8760h")
	}
	keyText := strings.TrimSpace(c.AskDataQuestionEncryptionKey)
	if mode == "HASH_ONLY" {
		if keyText != "" {
			return errors.New("ASKDATA_QUESTION_ENCRYPTION_KEY must be unset in HASH_ONLY mode")
		}
		return nil
	}
	key, err := base64.StdEncoding.DecodeString(keyText)
	if err != nil || len(key) != 32 {
		return errors.New("ASKDATA_QUESTION_ENCRYPTION_KEY must be base64-encoded 32 bytes in ENCRYPTED_SHORT_TERM mode")
	}
	return nil
}

func parseUniqueCSV(value string) []string {
	values := make([]string, 0)
	seen := make(map[string]struct{})
	for _, part := range strings.Split(value, ",") {
		item := strings.TrimSpace(part)
		if item == "" {
			continue
		}
		key := strings.ToLower(item)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		values = append(values, item)
	}
	return values
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

func loopbackEndpoint(value string) bool {
	parsed, err := url.Parse("//" + strings.TrimSpace(value))
	if err != nil || parsed.Host == "" {
		return false
	}
	host := strings.TrimSpace(parsed.Hostname())
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func databasePrincipal(value string) (string, error) {
	parsed, err := pgx.ParseConfig(strings.TrimSpace(value))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(parsed.User), nil
}

func samePostgresDatabase(left, right string) bool {
	leftConfig, leftErr := pgx.ParseConfig(strings.TrimSpace(left))
	rightConfig, rightErr := pgx.ParseConfig(strings.TrimSpace(right))
	if leftErr != nil || rightErr != nil {
		return false
	}
	return strings.EqualFold(leftConfig.Host, rightConfig.Host) &&
		leftConfig.Port == rightConfig.Port &&
		leftConfig.Database == rightConfig.Database
}

func loadProcessDatabaseURL(
	environment string,
	process databaseProcess,
) (string, error) {
	for _, key := range process.forbiddenKeys {
		if err := os.Unsetenv(key); err != nil {
			return "", fmt.Errorf("remove forbidden process credential %s: %w", key, err)
		}
	}
	databaseURL := strings.TrimSpace(os.Getenv(process.urlKey))
	if databaseURL == "" {
		if strings.EqualFold(environment, "production") {
			return "", fmt.Errorf("%s must be explicitly configured in production", process.urlKey)
		}
		databaseURL = process.developmentURL
	}
	principal, err := databasePrincipal(databaseURL)
	if err != nil {
		return "", fmt.Errorf("parse %s: %w", process.urlKey, err)
	}
	expectedRole := envOrDefault(process.roleKey, process.developmentRole)
	if principal == "" || principal != expectedRole {
		return "", fmt.Errorf(
			"%s must authenticate as the dedicated %s role",
			process.urlKey,
			expectedRole,
		)
	}
	return databaseURL, nil
}

// envOrDefault 返回去除首尾空白的环境变量值或指定默认值。
func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envBool(key string, fallback bool) (bool, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("parse %s: %w", key, err)
	}
	return parsed, nil
}

func envOptionalInt(key string) (int, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return 0, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 {
		if err == nil {
			err = errors.New("value must be greater than zero")
		}
		return 0, fmt.Errorf("parse %s: %w", key, err)
	}
	return parsed, nil
}
