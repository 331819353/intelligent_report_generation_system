package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"intelligent-report-generation-system/internal/access"
	aiplatform "intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/asset"
	"intelligent-report-generation-system/internal/auth"
	"intelligent-report-generation-system/internal/config"
	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/datasource"
	"intelligent-report-generation-system/internal/federation"
	"intelligent-report-generation-system/internal/filequery"
	"intelligent-report-generation-system/internal/httpserver"
	"intelligent-report-generation-system/internal/metadataai"
	"intelligent-report-generation-system/internal/metric"
	"intelligent-report-generation-system/internal/observability"
	"intelligent-report-generation-system/internal/platform/database"
	"intelligent-report-generation-system/internal/policy"
	"intelligent-report-generation-system/internal/queryruntime"
	"intelligent-report-generation-system/internal/report"
)

// main 装配 API 服务依赖，并负责启动、信号监听与优雅停机。
func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("load configuration", "error", err)
		os.Exit(1)
	}
	logger := observability.NewLogger(cfg.LogLevel)
	// 数据库和对象存储属于进程级资源，初始化失败时直接终止启动。
	startupCtx, startupCancel := context.WithTimeout(context.Background(), 10*time.Second)
	pool, err := database.Open(startupCtx, cfg.DatabaseURL)
	startupCancel()
	if err != nil {
		logger.Error("connect database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	passwords := auth.NewPasswordManager(cfg.AuthBcryptCost)
	tokens := auth.NewTokenManager(cfg.AuthTokenIssuer, cfg.AuthAccessSecret, cfg.AuthAccessTTL)
	authService := auth.NewService(auth.NewPostgresStore(pool), passwords, tokens, cfg.AuthRefreshTTL)
	accessService := access.NewService(access.NewPostgresStore(pool))
	accessAdminHandler := access.NewAdminHandler(authService, accessService, access.NewAdminStore(pool))
	dataSourceRepo := datasource.NewPostgresRepository(pool)
	objectStorage, err := datasource.NewMinIOStorage(cfg.MinIOEndpoint, cfg.MinIOAccessKey, cfg.MinIOSecretKey, cfg.MinIOUseSSL)
	if err != nil {
		logger.Error("initialize object storage", "error", err)
		os.Exit(1)
	}
	excelManager := datasource.NewExcelManager(dataSourceRepo, objectStorage, cfg.MinIOUploadsBucket)
	credentialManager, err := datasource.NewCredentialManager(cfg.DataSourceCredentialKey, datasource.EnvSecretResolver{})
	if err != nil {
		logger.Error("initialize data source credential manager", "error", err)
		os.Exit(1)
	}
	// 连接器按数据源类型注册：数据库走隔离的 Python 服务，文件走本地解析器。
	mysqlConnector := datasource.NewPythonConnector(datasource.TypeMySQL, cfg.ConnectorURL, cfg.ConnectorToken, credentialManager)
	oracleConnector := datasource.NewPythonConnector(datasource.TypeOracle, cfg.ConnectorURL, cfg.ConnectorToken, credentialManager)
	dataSourceService := datasource.NewService(
		dataSourceRepo,
		mysqlConnector,
		oracleConnector,
		datasource.NewExcelConnector(excelManager),
	)
	dataSourceService.SetMetadataJobRepository(datasource.NewPostgresMetadataJobRepository(pool))
	excelHandler := datasource.NewExcelHandler(authService, accessService, excelManager)
	assetHandler := asset.NewHandler(authService, accessService, asset.NewRepository(pool))
	modelProvider := aiplatform.NewOpenAICompatibleProvider(cfg.AIBaseURL, cfg.AIAPIKey, cfg.AIModel, &http.Client{Timeout: cfg.AIAttemptTimeout})
	aiService, err := aiplatform.NewService(aiplatform.NewPostgresStore(pool), modelProvider, aiplatform.ServiceOptions{
		Timeout: cfg.AIRequestTimeout, AttemptTimeout: cfg.AIAttemptTimeout,
		MaxAttempts: cfg.AIMaxAttempts, BaseRetryDelay: cfg.AIRetryBaseDelay, MaxRetryDelay: cfg.AIRetryMaxDelay,
		MaxInputBytes: cfg.AIMaxInputBytes, InputCostMicrosPerMTokens: cfg.AIInputCostMicrosPerMTokens,
		OutputCostMicrosPerMTokens: cfg.AIOutputCostMicrosPerMTokens,
	})
	if err != nil {
		logger.Error("initialize AI orchestration", "error", err)
		os.Exit(1)
	}
	metadataAIProvider := metadataai.NewOrchestratedProvider(aiService)
	metadataAIService := metadataai.NewService(metadataai.NewPostgresStore(pool), metadataAIProvider, cfg.AIRequestTimeout, cfg.AIConfidenceThreshold)
	dataSourceService.SetTableCompleter(metadataAIService)
	dataSourceHandler := datasource.NewHandler(authService, accessService, dataSourceService, credentialManager)
	metadataAIHandler := metadataai.NewHandler(authService, accessService, metadataAIService)
	datasetStore := dataset.NewPostgresStore(pool)
	datasetService := dataset.NewService(datasetStore)
	queryConnectors := map[datasource.Type]queryruntime.QueryConnector{
		datasource.TypeMySQL:  mysqlConnector,
		datasource.TypeOracle: oracleConnector,
	}
	queryService := queryruntime.NewService(
		datasetStore,
		dataSourceRepo,
		policy.NewPostgresStore(pool),
		queryruntime.NewPostgresStore(pool),
		queryConnectors,
		filequery.NewExecutor(excelManager),
	)
	queryService.SetFederatedExecutor(federation.NewExecutor(queryConnectors, excelManager))
	datasetService.SetPublicationValidator(queryService)
	datasetHandler := dataset.NewHandler(authService, accessService, datasetService, queryService)
	metricService := metric.NewService(metric.NewPostgresStore(pool), queryService)
	metricService.SetPermissionChecker(accessService)
	metricHandler := metric.NewHandler(authService, accessService, metricService)
	reportService := report.NewService(report.NewPostgresStore(pool))
	reportHandler := report.NewHandler(authService, accessService, reportService)
	api := http.NewServeMux()
	api.Handle("/api/v1/auth/", auth.NewHandler(authService))
	api.Handle("POST /api/v1/permissions/evaluate", auth.RequireAccessToken(authService, access.EvaluateHandler(accessService)))
	api.Handle("/api/v1/roles", accessAdminHandler)
	api.Handle("/api/v1/roles/", accessAdminHandler)
	api.Handle("/api/v1/users/", accessAdminHandler)
	api.Handle("/api/v1/object-permissions", accessAdminHandler)
	api.Handle("/api/v1/object-permissions/", accessAdminHandler)
	api.Handle("/api/v1/data-sources", dataSourceHandler)
	api.Handle("/api/v1/data-sources/", dataSourceHandler)
	api.Handle("/api/v1/excel-files", excelHandler)
	api.Handle("/api/v1/excel-files/", excelHandler)
	api.Handle("/api/v1/assets/", assetHandler)
	api.Handle("/api/v1/metadata-diffs", assetHandler)
	api.Handle("/api/v1/metadata-ai/", metadataAIHandler)
	api.Handle("/api/v1/datasets", datasetHandler)
	api.Handle("/api/v1/datasets/", datasetHandler)
	api.Handle("/api/v1/metrics", metricHandler)
	api.Handle("/api/v1/metrics/", metricHandler)
	api.Handle("/api/v1/reports", reportHandler)
	api.Handle("/api/v1/reports/", reportHandler)
	server := httpserver.New(cfg, logger, api)

	serverErrors := make(chan error, 1)
	// HTTP 服务放在独立协程中运行，主协程统一处理退出信号。
	go func() {
		logger.Info("api server starting", "addr", cfg.HTTPAddr, "environment", cfg.Environment)
		serverErrors <- server.ListenAndServe()
	}()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-signals:
		logger.Info("shutdown signal received", "signal", sig.String())
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("api server stopped unexpectedly", "error", err)
			os.Exit(1)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		logger.Error("api server shutdown", "error", err)
		os.Exit(1)
	}
	logger.Info("api server stopped", "timeout", cfg.ShutdownTimeout.String())
}
