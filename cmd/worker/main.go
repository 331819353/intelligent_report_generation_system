package main

import (
	"context"
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
	"intelligent-report-generation-system/internal/metriccandidate"
	"intelligent-report-generation-system/internal/metricsemantic"
	"intelligent-report-generation-system/internal/observability"
	"intelligent-report-generation-system/internal/platform/database"
	"intelligent-report-generation-system/internal/semanticcatalog"
	"intelligent-report-generation-system/internal/semanticmanagement"
	"intelligent-report-generation-system/internal/warehouse"
)

// main 装配持久化元数据任务 worker；HTTP 提交后由该进程完成采样和 LLM 加工。
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
	jobRepo := datasource.NewPostgresMetadataJobRepository(pool)
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
	dataSourceService := datasource.NewService(dataSourceRepo, mysqlConnector, oracleConnector, datasource.NewExcelConnector(excelManager))
	dataSourceService.SetMetadataJobRepository(jobRepo)

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
	metadataAIService := metadataai.NewService(metadataAIStore, metadataai.NewOrchestratedProvider(aiService), cfg.AIRequestTimeout, cfg.AIConfidenceThreshold)
	dataSourceService.SetTableCompleter(metadataAIService)

	workerID := uuid.NewString()
	logger.Info("worker starting", "worker_id", workerID, "poll_interval", cfg.WorkerPollInterval.String(), "environment", cfg.Environment)
	embeddingProvider := embedding.NewOpenAICompatibleProvider(
		cfg.AIEmbeddingBaseURL, cfg.AIEmbeddingAPIKey, cfg.AIEmbeddingModel, cfg.AIEmbeddingDimensions,
		&http.Client{Timeout: cfg.AIEmbeddingTimeout},
	)
	go runMetadataJobWorker(ctx, logger, dataSourceService, workerID, cfg.WorkerPollInterval)
	go runAssetEmbeddingWorker(ctx, logger, assetembedding.NewWorker(assetembedding.NewPostgresStore(pool), embeddingProvider), workerID, cfg.WorkerPollInterval)
	go runMetricEmbeddingWorker(ctx, logger, metricsemantic.NewWorker(metricsemantic.NewPostgresStore(pool), embeddingProvider), workerID, cfg.WorkerPollInterval)
	go runSemanticCatalogWorker(ctx, logger, semanticcatalog.NewWorker(
		semanticcatalog.NewPostgresStore(pool), embeddingProvider,
	), workerID, cfg.WorkerPollInterval)
	go runPublicationPreparationWorker(ctx, logger, metriccandidate.NewPublicationPreparationWorker(
		metricCandidateStore,
		metriccandidate.NewPublicationGenerator(metriccandidate.NewEnricher(aiService, cfg.AIRequestTimeout, metricCandidateStore)),
	), workerID, cfg.WorkerPollInterval)
	go runMaterializationWorker(ctx, logger, materializationworker.NewWorker(
		materializationStore,
		materializationworker.NewCompositeResolver(
			materializationworker.NewODSResolver(
				pool,
				warehouse.NewStagerWithMaxBytes(
					warehousePool, mysqlConnector, cfg.WarehouseStageMaxBytes,
				),
				warehouse.NewStagerWithMaxBytes(
					warehousePool, oracleConnector, cfg.WarehouseStageMaxBytes,
				),
				warehouse.NewFileStagerWithMaxBytes(
					warehousePool, excelManager, cfg.WarehouseStageMaxBytes,
				),
			),
			materializationworker.NewSeparatedPostgresResolver(pool, warehousePool),
		),
		warehouse.NewExecutor(warehousePool),
	), workerID, cfg.WorkerPollInterval)
	go runDimensionMemberRefreshWorker(ctx, logger, semanticmanagement.NewDimensionRefreshWorker(
		semanticmanagement.NewPostgresStoreWithWarehouse(pool, warehousePool),
	), workerID, cfg.WorkerPollInterval)
	go runDimensionProfileWorker(ctx, logger, semanticmanagement.NewDimensionProfileWorker(
		semanticmanagement.NewPostgresStoreWithWarehouse(pool, warehousePool),
	), workerID, cfg.WorkerPollInterval)
	go runDatasetTagSuggestionWorker(ctx, logger, datasettagsuggestion.NewWorker(
		datasettagsuggestion.NewPostgresStore(pool),
		datasettagsuggestion.NewGenerator(aiService, cfg.AIRequestTimeout),
	), workerID, cfg.WorkerPollInterval)
	runMetricExtractionWorker(ctx, logger, metriccandidate.NewWorker(
		metricCandidateStore, metriccandidate.NewEnricher(aiService, cfg.AIRequestTimeout, metricCandidateStore),
	), workerID, cfg.WorkerPollInterval)
	logger.Info("worker stopped")
}

func runDimensionProfileWorker(
	ctx context.Context,
	logger *slog.Logger,
	worker *semanticmanagement.DimensionProfileWorker,
	workerID string,
	pollInterval time.Duration,
) {
	// Profile work is bounded to 60 seconds by the frozen job policy. The
	// heartbeat keeps its owner/token/attempt fence alive and cancels the
	// aggregate query immediately if that fence is lost.
	const lease = 2 * time.Minute
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		processed := false
		tenantIDs, err := worker.TenantIDs(ctx)
		if err != nil {
			logger.Error("list dimension profile tenants", "error", err)
		} else {
			for _, tenantID := range tenantIDs {
				didProcess, runErr := worker.ProcessNext(
					ctx, tenantID, workerID, lease,
				)
				if runErr != nil {
					logger.Error(
						"process dimension profile",
						"tenant_id", tenantID,
						"error", runErr,
					)
				}
				if didProcess {
					processed = true
				}
			}
		}
		if processed {
			timer.Reset(10 * time.Millisecond)
		} else {
			timer.Reset(pollInterval)
		}
	}
}

func runDatasetTagSuggestionWorker(
	ctx context.Context,
	logger *slog.Logger,
	worker *datasettagsuggestion.Worker,
	workerID string,
	pollInterval time.Duration,
) {
	// The worker renews this lease at lease/3 while the external LLM call is
	// running and cancels that call if the fenced heartbeat fails.
	const lease = 2 * time.Minute
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		processed := false
		tenantIDs, err := worker.TenantIDs(ctx)
		if err != nil {
			logger.Error("list dataset tag suggestion tenants", "error", err)
		} else {
			for _, tenantID := range tenantIDs {
				didProcess, runErr := worker.ProcessNext(
					ctx, tenantID, workerID, lease,
				)
				if runErr != nil {
					logger.Error(
						"process dataset tag suggestion",
						"tenant_id", tenantID,
						"error", runErr,
					)
				}
				if didProcess {
					processed = true
				}
			}
		}
		if processed {
			timer.Reset(10 * time.Millisecond)
		} else {
			timer.Reset(pollInterval)
		}
	}
}

func runDimensionMemberRefreshWorker(
	ctx context.Context,
	logger *slog.Logger,
	worker *semanticmanagement.DimensionRefreshWorker,
	workerID string,
	pollInterval time.Duration,
) {
	// The largest accepted refresh timeout is five minutes. A longer lease
	// prevents healthy work from being reclaimed while still allowing recovery.
	const lease = 10 * time.Minute
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		processed := false
		tenantIDs, err := worker.TenantIDs(ctx)
		if err != nil {
			logger.Error("list dimension member refresh tenants", "error", err)
		} else {
			for _, tenantID := range tenantIDs {
				didProcess, runErr := worker.ProcessNext(ctx, tenantID, workerID, lease)
				if runErr != nil {
					logger.Error(
						"process dimension member refresh",
						"tenant_id", tenantID,
						"error", runErr,
					)
				}
				if didProcess {
					processed = true
				}
			}
		}
		if processed {
			timer.Reset(10 * time.Millisecond)
		} else {
			timer.Reset(pollInterval)
		}
	}
}

func runMaterializationWorker(
	ctx context.Context,
	logger *slog.Logger,
	worker *materializationworker.Worker,
	workerID string,
	pollInterval time.Duration,
) {
	const lease = 5 * time.Minute
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		processed := false
		tenantIDs, err := worker.TenantIDs(ctx)
		if err != nil {
			logger.Error("list materialization tenants", "error", err)
		} else {
			for _, tenantID := range tenantIDs {
				didProcess, runErr := worker.ProcessNext(ctx, tenantID, workerID, lease)
				if runErr != nil {
					logger.Error(
						"process dataset materialization",
						"tenant_id", tenantID,
						"error", runErr,
					)
				}
				if didProcess {
					processed = true
				}
			}
		}
		if processed {
			timer.Reset(10 * time.Millisecond)
		} else {
			timer.Reset(pollInterval)
		}
	}
}

func runSemanticCatalogWorker(
	ctx context.Context,
	logger *slog.Logger,
	worker *semanticcatalog.Worker,
	workerID string,
	pollInterval time.Duration,
) {
	// The worker renews each embedding event at lease/3 while its provider
	// batch is in flight and suppresses completion after a lost fence.
	const lease = 2 * time.Minute
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		processed := 0
		tenantIDs, err := worker.TenantIDs(ctx)
		if err != nil {
			logger.Error("list semantic catalog tenants", "error", err)
		} else {
			for _, tenantID := range tenantIDs {
				count, runErr := worker.ProcessNext(ctx, tenantID, workerID, lease)
				if runErr != nil {
					logger.Error(
						"process semantic catalog events",
						"tenant_id", tenantID,
						"error", runErr,
					)
				}
				processed += count
			}
		}
		if processed > 0 {
			timer.Reset(10 * time.Millisecond)
		} else {
			timer.Reset(pollInterval)
		}
	}
}

func runPublicationPreparationWorker(
	ctx context.Context,
	logger *slog.Logger,
	worker *metriccandidate.PublicationPreparationWorker,
	workerID string,
	pollInterval time.Duration,
) {
	const lease = 5 * time.Minute
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		processed := false
		tenantIDs, err := worker.TenantIDs(ctx)
		if err != nil {
			logger.Error("list publication metric preparation tenants", "error", err)
		} else {
			for _, tenantID := range tenantIDs {
				didProcess, runErr := worker.ProcessNext(ctx, tenantID, workerID, lease)
				if runErr != nil {
					logger.Error("prepare publication metric candidates", "tenant_id", tenantID, "error", runErr)
				}
				if didProcess {
					processed = true
				}
			}
		}
		if processed {
			timer.Reset(10 * time.Millisecond)
		} else {
			timer.Reset(pollInterval)
		}
	}
}

func runAssetEmbeddingWorker(ctx context.Context, logger *slog.Logger, worker *assetembedding.Worker, workerID string, pollInterval time.Duration) {
	const lease = 2 * time.Minute
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		processed := 0
		tenantIDs, err := worker.TenantIDs(ctx)
		if err != nil {
			logger.Error("list asset embedding tenants", "error", err)
		} else {
			for _, tenantID := range tenantIDs {
				count, runErr := worker.ProcessNext(ctx, tenantID, workerID, lease)
				if runErr != nil {
					logger.Error("process asset embeddings", "tenant_id", tenantID, "error", runErr)
				}
				processed += count
			}
		}
		if processed > 0 {
			timer.Reset(10 * time.Millisecond)
		} else {
			timer.Reset(pollInterval)
		}
	}
}

func runMetadataJobWorker(ctx context.Context, logger *slog.Logger, service *datasource.Service, workerID string, pollInterval time.Duration) {
	const lease = 5 * time.Minute
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		processed := false
		tenantIDs, err := service.MetadataJobTenantIDs(ctx)
		if err != nil {
			logger.Error("list metadata job tenants", "error", err)
		} else {
			for _, tenantID := range tenantIDs {
				didProcess, runErr := service.ProcessNextMetadataJob(ctx, tenantID, workerID, lease)
				if runErr != nil {
					logger.Error("process metadata job", "tenant_id", tenantID, "error", runErr)
				}
				if didProcess {
					processed = true
				}
			}
		}
		if processed {
			timer.Reset(10 * time.Millisecond)
		} else {
			timer.Reset(pollInterval)
		}
	}
}

func runMetricExtractionWorker(ctx context.Context, logger *slog.Logger, worker *metriccandidate.Worker, workerID string, pollInterval time.Duration) {
	const lease = 5 * time.Minute
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		processed := false
		tenantIDs, err := worker.TenantIDs(ctx)
		if err != nil {
			logger.Error("list metric extraction job tenants", "error", err)
		} else {
			for _, tenantID := range tenantIDs {
				didProcess, runErr := worker.ProcessNext(ctx, tenantID, workerID, lease)
				if runErr != nil {
					logger.Error("process metric extraction job", "tenant_id", tenantID, "error", runErr)
				}
				if didProcess {
					processed = true
				}
			}
		}
		if processed {
			timer.Reset(10 * time.Millisecond)
		} else {
			timer.Reset(pollInterval)
		}
	}
}

func runMetricEmbeddingWorker(ctx context.Context, logger *slog.Logger, worker *metricsemantic.Worker, workerID string, pollInterval time.Duration) {
	const lease = 2 * time.Minute
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		processed := false
		tenantIDs, err := worker.TenantIDs(ctx)
		if err != nil {
			logger.Error("list metric embedding tenants", "error", err)
		} else {
			for _, tenantID := range tenantIDs {
				didProcess, runErr := worker.ProcessNext(ctx, tenantID, workerID, lease)
				if runErr != nil {
					logger.Error("process metric embedding", "tenant_id", tenantID, "error", runErr)
				}
				if didProcess {
					processed = true
				}
			}
		}
		if processed {
			timer.Reset(10 * time.Millisecond)
		} else {
			timer.Reset(pollInterval)
		}
	}
}
