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
	"intelligent-report-generation-system/internal/config"
	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/datasource"
	"intelligent-report-generation-system/internal/metadataai"
	"intelligent-report-generation-system/internal/metriccandidate"
	"intelligent-report-generation-system/internal/observability"
	"intelligent-report-generation-system/internal/platform/database"
)

// main 装配持久化元数据任务 worker；HTTP 提交后由该进程完成采样和 LLM 加工。
func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("load configuration", "error", err)
		os.Exit(1)
	}
	logger := observability.NewLogger(cfg.LogLevel)
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
	mysqlConnector := datasource.NewPythonConnector(datasource.TypeMySQL, cfg.ConnectorURL, cfg.ConnectorToken, credentialManager)
	oracleConnector := datasource.NewPythonConnector(datasource.TypeOracle, cfg.ConnectorURL, cfg.ConnectorToken, credentialManager)
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
	datasetStore.SetPublicationCommitSink(metricCandidateStore)
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
	go runMetadataJobWorker(ctx, logger, dataSourceService, workerID, cfg.WorkerPollInterval)
	runMetricExtractionWorker(ctx, logger, metriccandidate.NewWorker(metricCandidateStore), workerID, cfg.WorkerPollInterval)
	logger.Info("worker stopped")
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
