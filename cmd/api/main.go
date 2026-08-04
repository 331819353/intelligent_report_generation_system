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
	"intelligent-report-generation-system/internal/backgroundtask"
	"intelligent-report-generation-system/internal/config"
	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/datasetai"
	"intelligent-report-generation-system/internal/datasetsemanticnaming"
	"intelligent-report-generation-system/internal/datasource"
	"intelligent-report-generation-system/internal/datasourceai"
	"intelligent-report-generation-system/internal/embedding"
	"intelligent-report-generation-system/internal/federation"
	"intelligent-report-generation-system/internal/filequery"
	"intelligent-report-generation-system/internal/httpserver"
	"intelligent-report-generation-system/internal/materialization"
	"intelligent-report-generation-system/internal/metadataai"
	"intelligent-report-generation-system/internal/observability"
	"intelligent-report-generation-system/internal/platform/database"
	"intelligent-report-generation-system/internal/policy"
	"intelligent-report-generation-system/internal/queryruntime"
)

// main assembles the access, data-source, and dataset configuration APIs.
func main() {
	cfg, err := config.LoadAPI()
	if err != nil {
		slog.Error("load configuration", "error", err)
		os.Exit(1)
	}
	logger := observability.NewLogger(cfg.LogLevel)
	slog.SetDefault(logger)

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
	accessAdminHandler := access.NewAdminHandler(authService, access.NewAdminStore(pool))
	assetScopeHandler := access.NewAssetScopeHandler(authService, access.NewAssetScopeStore(pool))

	dataSourceRepo := datasource.NewPostgresRepository(pool)
	objectStorage, err := datasource.NewMinIOStorage(
		cfg.MinIOEndpoint, cfg.MinIOAccessKey, cfg.MinIOSecretKey, cfg.MinIOUseSSL,
	)
	if err != nil {
		logger.Error("initialize object storage", "error", err)
		os.Exit(1)
	}
	excelManager := datasource.NewExcelManager(dataSourceRepo, objectStorage, cfg.MinIOUploadsBucket)
	credentialManager, err := datasource.NewCredentialManager(
		cfg.DataSourceCredentialKey, datasource.EnvSecretResolver{},
	)
	if err != nil {
		logger.Error("initialize data source credential manager", "error", err)
		os.Exit(1)
	}
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

	providerEndpoints := make([]aiplatform.ProviderEndpoint, 0, len(cfg.AIProviderEndpoints))
	for _, endpoint := range cfg.AIProviderEndpoints {
		providerEndpoints = append(providerEndpoints, aiplatform.ProviderEndpoint{
			Name: endpoint.Name, BaseURL: endpoint.BaseURL,
			APIKey: endpoint.APIKey, Models: endpoint.Models,
		})
	}
	modelProvider := aiplatform.NewMultiEndpointProviderPool(
		providerEndpoints, &http.Client{Timeout: cfg.AIAttemptTimeout},
	)
	aiService, err := aiplatform.NewService(
		aiplatform.NewPostgresStore(pool), modelProvider, aiplatform.ServiceOptions{
			Timeout: cfg.AIRequestTimeout, AttemptTimeout: cfg.AIAttemptTimeout,
			MaxAttempts: cfg.AIMaxAttempts, BaseRetryDelay: cfg.AIRetryBaseDelay,
			MaxRetryDelay: cfg.AIRetryMaxDelay, MaxInputBytes: cfg.AIMaxInputBytes,
			InputCostMicrosPerMTokens:  cfg.AIInputCostMicrosPerMTokens,
			OutputCostMicrosPerMTokens: cfg.AIOutputCostMicrosPerMTokens,
		},
	)
	if err != nil {
		logger.Error("initialize dataset AI support", "error", err)
		os.Exit(1)
	}

	datasetStore := dataset.NewPostgresStore(pool)
	materializationStore := materialization.NewPostgresStoreWithWarehouse(pool, warehousePool)
	datasetStore.SetMappedPublicationCommitSink(materializationStore)
	datasetStore.SetGovernedPublicationCommitSink(materializationStore)
	datasetStore.SetMaterializationDeletionSink(materializationStore)

	assetRepository := asset.NewRepository(pool)
	assetRepository.SetManualCompletionSink(datasetStore)
	metadataAIStore := metadataai.NewPostgresStore(pool)
	metadataAIStore.SetEnrichmentCommitSink(datasetStore)
	metadataAIService := metadataai.NewService(
		metadataAIStore,
		metadataai.NewOrchestratedProviderWithPrimaryFailover(aiService, cfg.AIPrimaryFailoverTimeout),
		cfg.AIRequestTimeout,
		cfg.AIConfidenceThreshold,
	)
	dataSourceService.SetTableCompleter(metadataAIService)
	dataSourceService.SetMappedDatasetDraftEnsurer(datasetStore)

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

	datasetService := dataset.NewService(datasetStore)
	datasetService.SetSemanticNamer(datasetsemanticnaming.NewGenerator(
		datasetsemanticnaming.NewPostgresCatalog(pool), aiService, cfg.AIRequestTimeout,
	))
	datasetService.SetLLMTriggerStore(datasetStore)
	queryConnectors := map[datasource.Type]queryruntime.QueryConnector{
		datasource.TypeMySQL: mysqlConnector, datasource.TypeOracle: oracleConnector,
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
	queryService.SetWarehouseExecutor(
		queryruntime.NewSeparatedPostgresWarehouseExecutor(pool, warehousePool),
	)
	datasetService.SetPublicationValidator(queryService)

	embeddingProvider := embedding.NewOpenAICompatibleProvider(
		cfg.AIEmbeddingBaseURL, cfg.AIEmbeddingAPIKey, cfg.AIEmbeddingModel,
		cfg.AIEmbeddingDimensions, &http.Client{Timeout: cfg.AIEmbeddingTimeout},
	)
	datasetAIService := datasetai.NewService(
		datasetai.NewVersionAwareAssetCatalog(pool, assetRepository),
		aiService,
		datasetai.ServiceOptions{
			Timeout: cfg.AIRequestTimeout, MaxProviderInputBytes: cfg.AIMaxInputBytes,
			Retriever: assetembedding.NewRetriever(
				assetembedding.NewPostgresStore(pool), embeddingProvider,
			),
			RetrievalMode: cfg.DatasetAIRetrievalMode,
		},
	)

	dataSourceHandler := datasource.NewHandler(authService, accessService, dataSourceService, credentialManager)
	dataSourceAIHandler := datasourceai.NewHandler(
		authService, accessService,
		datasourceai.NewService(dataSourceService, aiService, cfg.AIRequestTimeout),
	)
	dataSourceApprovalHandler := datasource.NewPublicationApprovalHandler(
		authService,
		accessService,
		datasource.NewPublicationApprovalService(dataSourceRepo, dataSourceService),
		credentialManager,
	)
	datasetHandler := dataset.NewHandler(authService, accessService, datasetService, queryService)
	datasetApprovalHandler := dataset.NewPublicationApprovalHandler(
		authService,
		accessService,
		dataset.NewPublicationApprovalService(datasetStore, datasetService),
	)

	api := http.NewServeMux()
	api.Handle("/api/v1/auth/", auth.NewHandler(authService))
	api.Handle("POST /api/v1/permissions/evaluate", auth.RequireAccessToken(authService, access.EvaluateHandler(accessService)))
	api.Handle("/api/v1/domain-catalog", accessAdminHandler)
	api.Handle("/api/v1/domain-applications", accessAdminHandler)
	api.Handle("/api/v1/domain-applications/", accessAdminHandler)
	api.Handle("/api/v1/managed-domains", accessAdminHandler)
	api.Handle("/api/v1/platform-management/", accessAdminHandler)
	api.Handle("/api/v1/domains", accessAdminHandler)
	api.Handle("/api/v1/domains/", accessAdminHandler)
	api.Handle("/api/v1/users", accessAdminHandler)
	api.Handle("/api/v1/users/", accessAdminHandler)
	api.Handle("/api/v1/asset-access/", assetScopeHandler)
	api.Handle("/api/v1/background-tasks", backgroundtask.NewHandler(
		authService, accessService,
		backgroundtask.NewService(backgroundtask.NewPostgresStore(pool)),
	))
	api.Handle("/api/v1/background-tasks/", backgroundtask.NewHandler(
		authService, accessService,
		backgroundtask.NewService(backgroundtask.NewPostgresStore(pool)),
	))

	api.Handle("POST /api/v1/data-sources/{id}/publish", dataSourceApprovalHandler)
	api.Handle("POST /api/v1/data-sources/{id}/publish-requests", dataSourceApprovalHandler)
	api.Handle("GET /api/v1/data-sources/{id}/publish-requests", dataSourceApprovalHandler)
	api.Handle("POST /api/v1/data-sources/{id}/publish-requests/{requestId}/withdraw", dataSourceApprovalHandler)
	api.Handle("POST /api/v1/data-sources/{id}/publish-requests/{requestId}/approve", dataSourceApprovalHandler)
	api.Handle("POST /api/v1/data-sources/{id}/publish-requests/{requestId}/reject", dataSourceApprovalHandler)
	api.Handle("POST /api/v1/data-sources/ai/turns", dataSourceAIHandler)
	api.Handle("POST /api/v1/data-sources/{id}/ai/turns", dataSourceAIHandler)
	api.Handle("/api/v1/data-sources", dataSourceHandler)
	api.Handle("/api/v1/data-sources/", dataSourceHandler)
	api.Handle("/api/v1/excel-files", datasource.NewExcelHandler(authService, accessService, excelManager))
	api.Handle("/api/v1/excel-files/", datasource.NewExcelHandler(authService, accessService, excelManager))
	api.Handle("/api/v1/assets/", asset.NewHandler(
		authService, accessService, assetRepository, dataSourceService,
	))
	api.Handle("/api/v1/metadata-diffs", asset.NewHandler(
		authService, accessService, assetRepository, dataSourceService,
	))

	api.Handle("POST /api/v1/datasets/ai/proposals", datasetai.NewHandler(authService, accessService, datasetAIService))
	api.Handle("POST /api/v1/datasets/{id}/ai/proposals", datasetai.NewHandler(authService, accessService, datasetAIService))
	api.Handle("POST /api/v1/datasets/{id}/publish", datasetApprovalHandler)
	api.Handle("POST /api/v1/datasets/{id}/publish-requests", datasetApprovalHandler)
	api.Handle("GET /api/v1/datasets/{id}/publish-requests", datasetApprovalHandler)
	api.Handle("POST /api/v1/datasets/{id}/publish-requests/{requestId}/approve", datasetApprovalHandler)
	api.Handle("POST /api/v1/datasets/{id}/publish-requests/{requestId}/reject", datasetApprovalHandler)
	api.Handle("/api/v1/datasets/{id}/materializations/builds", materialization.NewControlHandler(
		authService, accessService, materialization.NewControlService(materializationStore),
	))
	api.Handle("/api/v1/datasets/{id}/materializations/builds/", materialization.NewControlHandler(
		authService, accessService, materialization.NewControlService(materializationStore),
	))
	api.Handle("/api/v1/datasets", datasetHandler)
	api.Handle("/api/v1/datasets/", datasetHandler)

	server := httpserver.New(cfg, logger, api)
	serverErrors := make(chan error, 1)
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
