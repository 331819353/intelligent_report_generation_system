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

	"github.com/google/uuid"
	aiplatform "intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/assetembedding"
	"intelligent-report-generation-system/internal/config"
	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/datasettagsuggestion"
	"intelligent-report-generation-system/internal/datasource"
	"intelligent-report-generation-system/internal/embedding"
	"intelligent-report-generation-system/internal/materialization"
	"intelligent-report-generation-system/internal/materializationworker"
	"intelligent-report-generation-system/internal/metadataai"
	"intelligent-report-generation-system/internal/observability"
	"intelligent-report-generation-system/internal/platform/database"
	"intelligent-report-generation-system/internal/warehouse"
)

// main runs only background work required by data-source and dataset configuration.
func main() {
	cfg, err := config.LoadWorker()
	if err != nil {
		slog.Error("load configuration", "error", err)
		os.Exit(1)
	}
	logger := observability.NewLogger(cfg.LogLevel)
	slog.SetDefault(logger)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	startupCtx, startupCancel := context.WithTimeout(ctx, 10*time.Second)
	pool, err := database.Open(startupCtx, cfg.DatabaseURL)
	startupCancel()
	if err != nil {
		logger.Error("connect database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	warehouseStartupCtx, warehouseStartupCancel := context.WithTimeout(ctx, 10*time.Second)
	warehousePool, err := database.Open(warehouseStartupCtx, cfg.WarehouseDatabaseURL)
	warehouseStartupCancel()
	if err != nil {
		logger.Error("connect warehouse database", "error", err)
		os.Exit(1)
	}
	defer warehousePool.Close()

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

	reconcileCtx, reconcileCancel := context.WithTimeout(ctx, 30*time.Second)
	reconciledDatasets, err := datasetStore.ReconcileMappedDatasets(reconcileCtx)
	reconcileCancel()
	if err != nil {
		logger.Error("reconcile mapped table datasets", "error", err)
		os.Exit(1)
	}
	if reconciledDatasets > 0 {
		logger.Info("mapped table datasets reconciled", "count", reconciledDatasets)
	}

	workerID := uuid.NewString()
	logger.Info(
		"worker starting", "worker_id", workerID,
		"poll_interval", cfg.WorkerPollInterval.String(), "environment", cfg.Environment,
	)
	embeddingProvider := embedding.NewOpenAICompatibleProvider(
		cfg.AIEmbeddingBaseURL, cfg.AIEmbeddingAPIKey, cfg.AIEmbeddingModel,
		cfg.AIEmbeddingDimensions, &http.Client{Timeout: cfg.AIEmbeddingTimeout},
	)
	go runMetadataJobWorker(ctx, logger, dataSourceService, workerID, cfg.WorkerPollInterval)
	go runAssetEmbeddingWorker(
		ctx,
		logger,
		assetembedding.NewWorker(assetembedding.NewPostgresStore(pool), embeddingProvider),
		workerID,
		cfg.WorkerPollInterval,
	)

	odsResolver := materializationworker.NewODSResolver(
		pool,
		warehouse.NewStagerWithMaxBytes(warehousePool, mysqlConnector, cfg.WarehouseStageMaxBytes),
		warehouse.NewStagerWithMaxBytes(warehousePool, oracleConnector, cfg.WarehouseStageMaxBytes),
		warehouse.NewFileStagerWithMaxBytes(warehousePool, excelManager, cfg.WarehouseStageMaxBytes),
	)
	odsResolver.SetFullProjector(warehouse.NewODSProjector(warehousePool))
	postgresResolver := materializationworker.NewSeparatedPostgresResolver(pool, warehousePool)
	postgresResolver.SetODSRehydrator(odsResolver)
	go runMaterializationWorker(
		ctx,
		logger,
		materializationworker.NewWorker(
			materializationStore,
			materializationworker.NewCompositeResolver(odsResolver, postgresResolver),
			warehouse.NewExecutor(warehousePool),
		),
		workerID,
		cfg.WorkerPollInterval,
	)
	go runDatasetMaterializationCleanupWorker(
		ctx, logger, materializationStore, workerID, cfg.WorkerPollInterval,
	)
	go runDatasetTagSuggestionWorker(
		ctx,
		logger,
		datasettagsuggestion.NewWorker(
			datasettagsuggestion.NewPostgresStore(pool),
			datasettagsuggestion.NewGenerator(aiService, cfg.AIRequestTimeout),
		),
		workerID,
		cfg.WorkerPollInterval,
	)
	go runDWDModelingWorker(
		ctx,
		logger,
		dataset.NewDWDModelingWorker(
			datasetStore,
			dataset.NewOrchestratedDWDModelingPlanner(aiService, cfg.AIRequestTimeout),
		),
		workerID,
		cfg.WorkerPollInterval,
	)

	<-ctx.Done()
	logger.Info("worker stopped")
}

func runDatasetTagSuggestionWorker(
	ctx context.Context,
	logger *slog.Logger,
	worker *datasettagsuggestion.Worker,
	workerID string,
	pollInterval time.Duration,
) {
	const lease = 2 * time.Minute
	runTenantWorkerLoop(ctx, pollInterval, func(ctx context.Context) (bool, error) {
		processed := false
		tenantIDs, err := worker.TenantIDs(ctx)
		if err != nil {
			return false, err
		}
		for _, tenantID := range tenantIDs {
			didProcess, runErr := worker.ProcessNext(ctx, tenantID, workerID, lease)
			if runErr != nil {
				logger.Error("process dataset tag suggestion", "tenant_id", tenantID, "error", runErr)
			}
			processed = processed || didProcess
		}
		return processed, nil
	}, func(err error) { logger.Error("list dataset tag suggestion tenants", "error", err) })
}

func runDWDModelingWorker(
	ctx context.Context,
	logger *slog.Logger,
	worker *dataset.DWDModelingWorker,
	workerID string,
	pollInterval time.Duration,
) {
	const lease = 2 * time.Minute
	runTenantWorkerLoop(ctx, pollInterval, func(ctx context.Context) (bool, error) {
		processed := false
		tenantIDs, err := worker.TenantIDs(ctx)
		if err != nil {
			return false, err
		}
		for _, tenantID := range tenantIDs {
			didProcess, runErr := worker.ProcessNext(ctx, tenantID, workerID, lease)
			if runErr != nil {
				var providerError *aiplatform.ProviderError
				errors.As(runErr, &providerError)
				providerStatus := 0
				providerCode := ""
				if providerError != nil {
					providerStatus = providerError.StatusCode
					providerCode = string(providerError.Code)
				}
				logger.Error(
					"process dataset modeling", "tenant_id", tenantID,
					"provider_status", providerStatus, "provider_code", providerCode,
					"error", runErr,
				)
			}
			processed = processed || didProcess
		}
		return processed, nil
	}, func(err error) { logger.Error("list dataset modeling tenants", "error", err) })
}

func runMaterializationWorker(
	ctx context.Context,
	logger *slog.Logger,
	worker *materializationworker.Worker,
	workerID string,
	pollInterval time.Duration,
) {
	const lease = 5 * time.Minute
	runTenantWorkerLoop(ctx, pollInterval, func(ctx context.Context) (bool, error) {
		processed := false
		tenantIDs, err := worker.TenantIDs(ctx)
		if err != nil {
			return false, err
		}
		for _, tenantID := range tenantIDs {
			didProcess, runErr := worker.ProcessNext(ctx, tenantID, workerID, lease)
			if runErr != nil {
				logger.Error("process dataset materialization", "tenant_id", tenantID, "error", runErr)
			}
			processed = processed || didProcess
		}
		return processed, nil
	}, func(err error) { logger.Error("list materialization tenants", "error", err) })
}

func runDatasetMaterializationCleanupWorker(
	ctx context.Context,
	logger *slog.Logger,
	store *materialization.PostgresStore,
	workerID string,
	pollInterval time.Duration,
) {
	const lease = 8 * time.Minute
	runTenantWorkerLoop(ctx, pollInterval, func(ctx context.Context) (bool, error) {
		processed := false
		tenantIDs, err := store.ListTenantIDs(ctx)
		if err != nil {
			return false, err
		}
		for _, tenantID := range tenantIDs {
			didProcess, runErr := store.ProcessNextDatasetMaterializationCleanup(
				ctx, tenantID, workerID, lease,
			)
			if runErr != nil {
				logger.Error(
					"cleanup dataset warehouse materializations",
					"tenant_id", tenantID, "error", runErr,
				)
			}
			processed = processed || didProcess
		}
		return processed, nil
	}, func(err error) { logger.Error("list materialization cleanup tenants", "error", err) })
}

func runAssetEmbeddingWorker(
	ctx context.Context,
	logger *slog.Logger,
	worker *assetembedding.Worker,
	workerID string,
	pollInterval time.Duration,
) {
	const lease = 2 * time.Minute
	runTenantWorkerLoop(ctx, pollInterval, func(ctx context.Context) (bool, error) {
		processed := false
		tenantIDs, err := worker.TenantIDs(ctx)
		if err != nil {
			return false, err
		}
		for _, tenantID := range tenantIDs {
			count, runErr := worker.ProcessNext(ctx, tenantID, workerID, lease)
			if runErr != nil {
				logger.Error("process asset embeddings", "tenant_id", tenantID, "error", runErr)
			}
			processed = processed || count > 0
		}
		return processed, nil
	}, func(err error) { logger.Error("list asset embedding tenants", "error", err) })
}

func runMetadataJobWorker(
	ctx context.Context,
	logger *slog.Logger,
	service *datasource.Service,
	workerID string,
	pollInterval time.Duration,
) {
	const lease = 5 * time.Minute
	runTenantWorkerLoop(ctx, pollInterval, func(ctx context.Context) (bool, error) {
		processed := false
		tenantIDs, err := service.MetadataJobTenantIDs(ctx)
		if err != nil {
			return false, err
		}
		for _, tenantID := range tenantIDs {
			didProcess, runErr := service.ProcessNextMetadataJob(ctx, tenantID, workerID, lease)
			if runErr != nil {
				logger.Error("process metadata job", "tenant_id", tenantID, "error", runErr)
			}
			processed = processed || didProcess
		}
		return processed, nil
	}, func(err error) { logger.Error("list metadata job tenants", "error", err) })
}

func runTenantWorkerLoop(
	ctx context.Context,
	pollInterval time.Duration,
	process func(context.Context) (bool, error),
	onListError func(error),
) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		processed, err := process(ctx)
		if err != nil {
			onListError(err)
		}
		if processed {
			timer.Reset(10 * time.Millisecond)
		} else {
			timer.Reset(pollInterval)
		}
	}
}
