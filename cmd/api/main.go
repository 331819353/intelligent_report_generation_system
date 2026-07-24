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
	"intelligent-report-generation-system/internal/assetembedding"
	"intelligent-report-generation-system/internal/auth"
	"intelligent-report-generation-system/internal/config"
	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/datasetai"
	"intelligent-report-generation-system/internal/datasource"
	"intelligent-report-generation-system/internal/embedding"
	"intelligent-report-generation-system/internal/federation"
	"intelligent-report-generation-system/internal/filequery"
	"intelligent-report-generation-system/internal/httpserver"
	"intelligent-report-generation-system/internal/materialization"
	"intelligent-report-generation-system/internal/metadataai"
	"intelligent-report-generation-system/internal/metric"
	"intelligent-report-generation-system/internal/metricai"
	"intelligent-report-generation-system/internal/metriccandidate"
	"intelligent-report-generation-system/internal/metricsemantic"
	"intelligent-report-generation-system/internal/observability"
	"intelligent-report-generation-system/internal/platform/database"
	"intelligent-report-generation-system/internal/policy"
	"intelligent-report-generation-system/internal/queryruntime"
	"intelligent-report-generation-system/internal/report"
	"intelligent-report-generation-system/internal/semanticmanagement"
)

// main 装配 API 服务依赖，并负责启动、信号监听与优雅停机。
func main() {
	cfg, err := config.LoadAPI()
	if err != nil {
		slog.Error("load configuration", "error", err)
		os.Exit(1)
	}
	logger := observability.NewLogger(cfg.LogLevel)
	slog.SetDefault(logger)
	// 数据库和对象存储属于进程级资源，初始化失败时直接终止启动。
	startupCtx, startupCancel := context.WithTimeout(context.Background(), 10*time.Second)
	pool, err := database.Open(startupCtx, cfg.DatabaseURL)
	startupCancel()
	if err != nil {
		logger.Error("connect database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	warehouseStartupCtx, warehouseStartupCancel := context.WithTimeout(context.Background(), 10*time.Second)
	warehousePool, err := database.Open(warehouseStartupCtx, cfg.WarehouseDatabaseURL)
	warehouseStartupCancel()
	if err != nil {
		logger.Error("connect warehouse database", "error", err)
		os.Exit(1)
	}
	defer warehousePool.Close()
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
	connectorLimits := datasource.ConnectorLimits{
		MaxRequestBytes:        cfg.ConnectorHTTPMaxRequestBytes,
		MaxJSONResponseBytes:   cfg.ConnectorJSONMaxResponseBytes,
		MaxSampleResponseBytes: cfg.ConnectorSampleMaxResponseBytes,
		MaxSampleCellBytes:     cfg.ConnectorSampleMaxCellBytes,
		MaxSampleRowBytes:      cfg.ConnectorSampleMaxRowBytes,
		MaxStreamBytes:         cfg.ConnectorStreamMaxBytes,
		MaxStreamCellBytes:     cfg.ConnectorStreamMaxCellBytes,
		MaxStreamRowBytes:      cfg.ConnectorStreamMaxRowBytes,
	}
	mysqlConnector := datasource.NewPythonConnectorWithLimits(
		datasource.TypeMySQL, cfg.ConnectorURL, cfg.ConnectorToken,
		credentialManager, connectorLimits,
	)
	oracleConnector := datasource.NewPythonConnectorWithLimits(
		datasource.TypeOracle, cfg.ConnectorURL, cfg.ConnectorToken,
		credentialManager, connectorLimits,
	)
	dataSourceService := datasource.NewService(
		dataSourceRepo,
		mysqlConnector,
		oracleConnector,
		datasource.NewExcelConnector(excelManager),
	)
	dataSourceService.SetMetadataJobRepository(datasource.NewPostgresMetadataJobRepository(pool))
	dataSourceService.SetConnectionTestJobRepository(datasource.NewPostgresConnectionTestRepository(pool))
	excelHandler := datasource.NewExcelHandler(authService, accessService, excelManager)
	assetRepository := asset.NewRepository(pool)
	assetHandler := asset.NewHandler(authService, accessService, assetRepository, dataSourceService)
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
	datasetStore := dataset.NewPostgresStore(pool)
	metricCandidateStore := metriccandidate.NewPostgresStore(pool)
	materializationStore := materialization.NewPostgresStoreWithWarehouse(pool, warehousePool)
	datasetStore.SetPublicationCommitSink(metricCandidateStore)
	datasetStore.SetMappedPublicationCommitSink(materializationStore)
	metadataAIStore := metadataai.NewPostgresStore(pool)
	metadataAIStore.SetEnrichmentCommitSink(datasetStore)
	reconcileCtx, reconcileCancel := context.WithTimeout(context.Background(), 30*time.Second)
	reconciledDatasets, err := datasetStore.ReconcileMappedDatasets(reconcileCtx)
	reconcileCancel()
	if err != nil {
		logger.Error("reconcile mapped table datasets", "error", err)
		os.Exit(1)
	}
	if reconciledDatasets > 0 {
		logger.Info("mapped table datasets reconciled", "count", reconciledDatasets)
	}
	metadataAIProvider := metadataai.NewOrchestratedProvider(aiService)
	metadataAIService := metadataai.NewService(metadataAIStore, metadataAIProvider, cfg.AIRequestTimeout, cfg.AIConfidenceThreshold)
	dataSourceService.SetTableCompleter(metadataAIService)
	dataSourceHandler := datasource.NewHandler(authService, accessService, dataSourceService, credentialManager)
	dataSourcePublicationApprovalService := datasource.NewPublicationApprovalService(dataSourceRepo, dataSourceService)
	dataSourcePublicationApprovalHandler := datasource.NewPublicationApprovalHandler(
		authService, accessService, dataSourcePublicationApprovalService, credentialManager,
	)
	metadataAIHandler := metadataai.NewHandler(authService, accessService, metadataAIService)
	embeddingProvider := embedding.NewOpenAICompatibleProvider(
		cfg.AIEmbeddingBaseURL, cfg.AIEmbeddingAPIKey, cfg.AIEmbeddingModel, cfg.AIEmbeddingDimensions,
		&http.Client{Timeout: cfg.AIEmbeddingTimeout},
	)
	assetEmbeddingStore := assetembedding.NewPostgresStore(pool)
	datasetAIService := datasetai.NewService(assetRepository, aiService, datasetai.ServiceOptions{
		Timeout: cfg.AIRequestTimeout, MaxProviderInputBytes: cfg.AIMaxInputBytes,
		Retriever:     assetembedding.NewRetriever(assetEmbeddingStore, embeddingProvider),
		RetrievalMode: cfg.DatasetAIRetrievalMode,
	})
	datasetAIHandler := datasetai.NewHandler(authService, accessService, datasetAIService)
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
	queryService.SetWarehouseExecutor(queryruntime.NewSeparatedPostgresWarehouseExecutor(pool, warehousePool))
	datasetService.SetPublicationValidator(queryService)
	datasetHandler := dataset.NewHandler(authService, accessService, datasetService, queryService)
	materializationHandler := materialization.NewControlHandler(
		authService,
		accessService,
		materialization.NewControlService(materializationStore),
	)
	datasetPublicationApprovalService := dataset.NewPublicationApprovalService(datasetStore, datasetService)
	datasetPublicationApprovalHandler := dataset.NewPublicationApprovalHandler(authService, accessService, datasetPublicationApprovalService)
	metricStore := metric.NewPostgresStore(pool)
	metricService := metric.NewService(metricStore, queryService)
	metricService.SetPermissionChecker(accessService)
	metricHandler := metric.NewHandler(authService, accessService, metricService)
	metricCandidateHandler := metriccandidate.NewHandler(
		authService, accessService, metriccandidate.NewService(metricCandidateStore, metricService),
	)
	metricAIService := metricai.NewService(metricai.NewPostgresRetriever(pool), aiService, metricai.ServiceOptions{Timeout: cfg.AIRequestTimeout})
	metricAIHandler := metricai.NewHandler(authService, accessService, metricAIService)
	metricSemanticService := metricsemantic.NewService(metricsemantic.NewPostgresStore(pool), embeddingProvider)
	metricSemanticHandler := metricsemantic.NewHandler(authService, accessService, metricSemanticService)
	semanticManagementStore := semanticmanagement.NewPostgresStoreWithWarehouse(pool, warehousePool)
	semanticManagementService := semanticmanagement.NewService(semanticManagementStore)
	semanticDimensionService := semanticmanagement.NewDimensionService(semanticManagementStore)
	semanticManagementHandler := semanticmanagement.NewHandler(
		authService, accessService, semanticManagementService, semanticDimensionService,
	)
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
	// Exact review routes take precedence over the general data-source subtree. The legacy
	// /publish path now submits a review request instead of switching the runtime pointer.
	api.Handle("POST /api/v1/data-sources/{id}/publish", dataSourcePublicationApprovalHandler)
	api.Handle("POST /api/v1/data-sources/{id}/publish-requests", dataSourcePublicationApprovalHandler)
	api.Handle("GET /api/v1/data-sources/{id}/publish-requests", dataSourcePublicationApprovalHandler)
	api.Handle("POST /api/v1/data-sources/{id}/publish-requests/{requestId}/withdraw", dataSourcePublicationApprovalHandler)
	api.Handle("POST /api/v1/data-sources/{id}/publish-requests/{requestId}/approve", dataSourcePublicationApprovalHandler)
	api.Handle("POST /api/v1/data-sources/{id}/publish-requests/{requestId}/reject", dataSourcePublicationApprovalHandler)
	api.Handle("/api/v1/data-sources", dataSourceHandler)
	api.Handle("/api/v1/data-sources/", dataSourceHandler)
	api.Handle("/api/v1/excel-files", excelHandler)
	api.Handle("/api/v1/excel-files/", excelHandler)
	api.Handle("/api/v1/assets/", assetHandler)
	api.Handle("/api/v1/metadata-diffs", assetHandler)
	api.Handle("/api/v1/metadata-ai/", metadataAIHandler)
	api.Handle("POST /api/v1/datasets/ai/proposals", datasetAIHandler)
	api.Handle("POST /api/v1/datasets/{id}/ai/proposals", datasetAIHandler)
	// Exact approval routes take precedence over the legacy dataset subtree. In particular,
	// /publish now submits an approval request and cannot directly move the published pointer.
	api.Handle("POST /api/v1/datasets/{id}/publish", datasetPublicationApprovalHandler)
	api.Handle("POST /api/v1/datasets/{id}/publish-requests", datasetPublicationApprovalHandler)
	api.Handle("GET /api/v1/datasets/{id}/publish-requests", datasetPublicationApprovalHandler)
	api.Handle("POST /api/v1/datasets/{id}/publish-requests/{requestId}/approve", datasetPublicationApprovalHandler)
	api.Handle("POST /api/v1/datasets/{id}/publish-requests/{requestId}/reject", datasetPublicationApprovalHandler)
	api.Handle("/api/v1/datasets/{id}/materializations/builds", materializationHandler)
	api.Handle("/api/v1/datasets/{id}/materializations/builds/", materializationHandler)
	api.Handle("/api/v1/datasets", datasetHandler)
	api.Handle("/api/v1/datasets/", datasetHandler)
	api.Handle("POST /api/v1/metrics/ai/proposals", metricAIHandler)
	api.Handle("GET /api/v1/metrics/semantic-search", metricSemanticHandler)
	api.Handle("/api/v1/semantic/", semanticManagementHandler)
	api.Handle("/api/v1/metric-candidates", metricCandidateHandler)
	api.Handle("/api/v1/metric-candidates/", metricCandidateHandler)
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
