package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/google/uuid"
	aiplatform "intelligent-report-generation-system/internal/ai"
	askdatacognition "intelligent-report-generation-system/internal/askdata/cognition"
	askdatacompiler "intelligent-report-generation-system/internal/askdata/compiler"
	askdatadimension "intelligent-report-generation-system/internal/askdata/dimension"
	askdatafeedback "intelligent-report-generation-system/internal/askdata/feedback"
	askdatagraph "intelligent-report-generation-system/internal/askdata/graph"
	askdataobservability "intelligent-report-generation-system/internal/askdata/observability"
	askdataorchestrator "intelligent-report-generation-system/internal/askdata/orchestrator"
	askdataprojection "intelligent-report-generation-system/internal/askdata/projection"
	askdataregistry "intelligent-report-generation-system/internal/askdata/registry"
	registryimport "intelligent-report-generation-system/internal/askdata/registry/import"
	askdatareportasset "intelligent-report-generation-system/internal/askdata/reportasset"
	askdatasearch "intelligent-report-generation-system/internal/askdata/search"
	askdatatools "intelligent-report-generation-system/internal/askdata/tools"
	askdataunderstanding "intelligent-report-generation-system/internal/askdata/understanding"
	dictionarypostgres "intelligent-report-generation-system/internal/askdata/understanding/dictionarypostgres"
	askdatavalidator "intelligent-report-generation-system/internal/askdata/validator"
	"intelligent-report-generation-system/internal/assetembedding"
	"intelligent-report-generation-system/internal/config"
	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/datasettagsuggestion"
	"intelligent-report-generation-system/internal/datasource"
	"intelligent-report-generation-system/internal/decision"
	"intelligent-report-generation-system/internal/embedding"
	"intelligent-report-generation-system/internal/federation"
	"intelligent-report-generation-system/internal/filequery"
	"intelligent-report-generation-system/internal/materialization"
	"intelligent-report-generation-system/internal/materializationworker"
	"intelligent-report-generation-system/internal/metadataai"
	"intelligent-report-generation-system/internal/observability"
	"intelligent-report-generation-system/internal/platform/database"
	platformidempotency "intelligent-report-generation-system/internal/platform/idempotency"
	"intelligent-report-generation-system/internal/policy"
	"intelligent-report-generation-system/internal/queryruntime"
	reportauthorization "intelligent-report-generation-system/internal/report/authorization"
	reportinbound "intelligent-report-generation-system/internal/report/inbound"
	reportpublication "intelligent-report-generation-system/internal/report/publication"
	reportruntime "intelligent-report-generation-system/internal/report/runtime"
	reportschedule "intelligent-report-generation-system/internal/report/schedule"
	reportsharing "intelligent-report-generation-system/internal/report/sharing"
	reportstore "intelligent-report-generation-system/internal/report/store"
	reporttemplate "intelligent-report-generation-system/internal/report/template"
	"intelligent-report-generation-system/internal/runtimeconfig"
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
	selectedTasks, dedicated, err := parseWorkerTaskSelection(os.Getenv("WORKER_TASK_TYPES"))
	if err != nil {
		logger.Error("parse worker task selection", "error", err)
		os.Exit(1)
	}
	if dedicated {
		if err := runSelectedAskDataTasks(ctx, logger, cfg, selectedTasks); err != nil {
			logger.Error("run selected worker tasks", "error", err)
			os.Exit(1)
		}
		return
	}

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

	warehouseQueryStartupCtx, warehouseQueryStartupCancel := context.WithTimeout(ctx, 10*time.Second)
	warehouseQueryPool, err := database.Open(warehouseQueryStartupCtx, cfg.WarehouseQueryDatabaseURL)
	warehouseQueryStartupCancel()
	if err != nil {
		logger.Error("connect warehouse query database", "error", err)
		os.Exit(1)
	}
	defer warehouseQueryPool.Close()

	graphTLSEnabled, err := strconv.ParseBool(envOrDefault("ASKDATA_NEBULA_TLS_ENABLED", "false"))
	if err != nil {
		logger.Error("parse AskData NebulaGraph TLS configuration", "error", err)
		os.Exit(1)
	}
	graphPool, err := askdatagraph.OpenSessionPool(
		os.Getenv("ASKDATA_NEBULA_ADDRESSES"),
		os.Getenv("ASKDATA_NEBULA_USERNAME"),
		os.Getenv("ASKDATA_NEBULA_PASSWORD"),
		os.Getenv("ASKDATA_NEBULA_SPACE"),
		graphTLSEnabled,
	)
	if err != nil {
		logger.Error("initialize AskData NebulaGraph worker session", "error", err)
		os.Exit(1)
	}
	defer graphPool.Close()
	graphWriter, err := askdatagraph.NewNebulaProjector(graphPool)
	if err != nil {
		logger.Error("initialize AskData graph projection writer", "error", err)
		os.Exit(1)
	}
	graphProjector, err := askdatagraph.NewProjector(
		askdatagraph.NewPostgresProjectionStore(pool), graphWriter,
	)
	if err != nil {
		logger.Error("initialize AskData release projector", "error", err)
		os.Exit(1)
	}
	runtimeProjectionWorker, err := askdataprojection.NewWorker(askdataprojection.NewPostgresStore(pool))
	if err != nil {
		logger.Error("initialize AskData runtime projection worker", "error", err)
		os.Exit(1)
	}
	reportGraphWriter, err := askdatareportasset.NewNebulaReportGraphWriter(graphPool)
	if err != nil {
		logger.Error("initialize report semantic asset graph writer", "error", err)
		os.Exit(1)
	}
	reportAssetProjectionWorker, err := askdatareportasset.NewProjectionRuntimeWorker(
		askdatareportasset.NewPostgresProjectionRuntimeStore(pool), reportGraphWriter,
	)
	if err != nil {
		logger.Error("initialize report semantic asset projection worker", "error", err)
		os.Exit(1)
	}

	dataSourceRepo := datasource.NewPostgresRepository(pool)
	objectStorage, err := datasource.NewMinIOStorage(
		cfg.MinIOEndpoint, cfg.MinIOAccessKey, cfg.MinIOSecretKey, cfg.MinIOUseSSL,
	)
	if err != nil {
		logger.Error("initialize object storage", "error", err)
		os.Exit(1)
	}
	reportArtifacts, err := reportpublication.NewMinIOArtifactStoreWithCredentials(
		cfg.MinIOEndpoint, cfg.MinIOAccessKey, cfg.MinIOSecretKey, cfg.MinIOUseSSL, cfg.MinIOUploadsBucket,
	)
	if err != nil {
		logger.Error("initialize report artifact store", "error", err)
		os.Exit(1)
	}
	reportRecoveryWorker, err := reportpublication.NewRecoveryWorker(
		reportstore.NewPostgresStore(pool), reportArtifacts,
	)
	if err != nil {
		logger.Error("initialize report publication recovery worker", "error", err)
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
	databaseConnectors := make(map[datasource.Type]*datasource.PythonConnector)
	serviceConnectors := make([]datasource.Connector, 0, len(datasource.DatabaseDrivers())+1)
	for _, driver := range datasource.DatabaseDrivers() {
		connector := datasource.NewPythonConnectorWithLimits(
			driver.Type, cfg.ConnectorURL, cfg.ConnectorToken,
			credentialManager, connectorLimits,
		)
		databaseConnectors[driver.Type] = connector
		serviceConnectors = append(serviceConnectors, connector)
	}
	serviceConnectors = append(serviceConnectors, datasource.NewExcelConnector(excelManager))
	dataSourceService := datasource.NewService(dataSourceRepo, serviceConnectors...)
	dataSourceService.SetMetadataJobRepository(datasource.NewPostgresMetadataJobRepository(pool))

	providerEndpoints := make([]aiplatform.ProviderEndpoint, 0, len(cfg.AIProviderEndpoints))
	for _, endpoint := range cfg.AIProviderEndpoints {
		providerEndpoints = append(providerEndpoints, aiplatform.ProviderEndpoint{
			Name: endpoint.Name, BaseURL: endpoint.BaseURL,
			APIKey: endpoint.APIKey, Models: endpoint.Models,
			ThinkingEnabled: endpoint.ThinkingEnabled,
			ReasoningEffort: endpoint.ReasoningEffort,
			ResponseFormat:  endpoint.ResponseFormat,
			MaxOutputTokens: endpoint.MaxOutputTokens,
		})
	}
	modelProvider := aiplatform.NewMultiEndpointProviderPool(
		providerEndpoints, cfg.AIProviderSelectionMode,
		&http.Client{Timeout: cfg.AIAttemptTimeout},
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
	queryConnectors := make(map[datasource.Type]queryruntime.QueryConnector, len(databaseConnectors))
	for sourceType, connector := range databaseConnectors {
		queryConnectors[sourceType] = connector
	}
	queryService := queryruntime.NewService(
		datasetStore, dataSourceRepo, policy.NewPostgresStore(pool),
		queryruntime.NewPostgresStore(pool), queryConnectors,
		filequery.NewExecutor(excelManager),
	)
	queryService.SetFederatedExecutor(federation.NewExecutor(queryConnectors, excelManager))
	queryService.SetWarehouseExecutor(
		queryruntime.NewSeparatedPostgresWarehouseExecutor(pool, warehouseQueryPool),
	)
	reportDatasetRunner, err := reportruntime.NewDatasetVersionRunner(queryService)
	if err != nil {
		logger.Error("initialize report export dataset runtime", "error", err)
		os.Exit(1)
	}
	reportSemanticRehydrator, err := askdatacompiler.NewPinnedArtifactRehydrator(
		askdatacompiler.NewPostgresContractStore(pool),
	)
	if err != nil {
		logger.Error("initialize report export semantic compiler", "error", err)
		os.Exit(1)
	}
	reportCoverage, err := askdatavalidator.NewCoverageControl(materializationStore)
	if err != nil {
		logger.Error("initialize report export semantic coverage", "error", err)
		os.Exit(1)
	}
	reportPlanValidator, err := askdatavalidator.NewValidator(
		askdatavalidator.NewPostgresExplainer(warehouseQueryPool), askdatavalidator.DefaultLimits(),
	)
	if err != nil {
		logger.Error("initialize report export semantic validator", "error", err)
		os.Exit(1)
	}
	reportPlanExecutor, err := askdatavalidator.NewExecutor(
		warehouseQueryPool,
		queryruntime.NewPostgresSemanticMaterializationRevalidator(pool),
		queryruntime.NewPostgresSemanticQuestionAuditStore(pool),
	)
	if err != nil {
		logger.Error("initialize report export semantic executor", "error", err)
		os.Exit(1)
	}
	reportSemanticRunner, err := reportruntime.NewSemanticRuntimeRunner(
		reportruntime.NewPostgresSemanticArtifactSource(pool),
		reportruntime.NewPostgresViewerScopeResolver(pool), reportSemanticRehydrator,
		reportCoverage, reportPlanValidator, reportPlanExecutor,
	)
	if err != nil {
		logger.Error("initialize report export semantic runtime", "error", err)
		os.Exit(1)
	}
	reportRuntime := reportruntime.GovernedQueryExecutor{
		Dataset: reportDatasetRunner, Semantic: reportSemanticRunner,
	}
	reportComponentRegistry, err := reporttemplate.NewDefaultRegistry()
	if err != nil {
		logger.Error("initialize report export component registry", "error", err)
		os.Exit(1)
	}
	reportExportSource, err := reportpublication.NewRuntimeExportResultSource(
		pool,
		reportruntime.Loader{
			Versions: reportstore.NewPostgresStore(pool), Artifacts: reportArtifacts,
			Manifests: reportComponentRegistry,
		},
		reportRuntime,
	)
	if err != nil {
		logger.Error("initialize report tabular export source", "error", err)
		os.Exit(1)
	}
	reportDocumentFonts, err := reportpublication.LoadDocumentFontSet(os.Getenv("REPORT_EXPORT_FONT_PATH"))
	if err != nil {
		logger.Error("initialize report export fonts", "error", err)
		os.Exit(1)
	}
	reportDocumentFallback, err := reportpublication.NewRuntimeDocumentExportGenerator(reportExportSource, reportDocumentFonts)
	if err != nil {
		logger.Error("initialize built-in report document renderer", "error", err)
		os.Exit(1)
	}
	var reportDocumentGenerator reportpublication.ExportGenerator = reportDocumentFallback
	if cfg.ReportExportRendererURL != "" {
		reportExportRenderer, rendererErr := reportpublication.NewHTTPDocumentExportGenerator(
			cfg.ReportExportRendererURL, cfg.ReportExportRendererToken,
			&http.Client{Timeout: 15 * time.Second},
		)
		if rendererErr != nil {
			logger.Error("initialize optional report export renderer", "error", rendererErr)
			os.Exit(1)
		}
		reportDocumentGenerator = reportpublication.FallbackExportGenerator{
			Primary: reportExportRenderer, Fallback: reportDocumentFallback,
		}
	}
	reportExportWorker, err := reportpublication.NewExportWorker(
		reportpublication.NewExportJobStore(pool), reportstore.NewPostgresStore(pool),
		reportpublication.CompositeExportGenerator{
			Document: reportDocumentGenerator,
			Tabular:  reportpublication.TabularExportGenerator{Source: reportExportSource},
		},
		objectStorage, cfg.MinIOUploadsBucket,
	)
	if err != nil {
		logger.Error("initialize report export worker", "error", err)
		os.Exit(1)
	}
	reportScheduleWorker, err := reportschedule.NewWorker(
		reportschedule.NewPostgresStore(pool), reportauthorization.NewPostgresAuthorizer(pool),
	)
	if err != nil {
		logger.Error("initialize report schedule worker", "error", err)
		os.Exit(1)
	}
	runtimeConfigWorker, err := runtimeconfig.NewWorker(pool)
	if err != nil {
		logger.Error("initialize runtime configuration worker", "error", err)
		os.Exit(1)
	}
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
	go runAskDataEmbeddingWorker(
		ctx,
		logger,
		askdatasearch.NewEmbeddingWorker(
			askdatasearch.NewPostgresEmbeddingStore(pool), embeddingProvider,
		),
		workerID,
		cfg.WorkerPollInterval,
	)
	recallAuditor, err := askdatasearch.NewRecallAuditor(
		askdatasearch.NewPostgresRecallAuditStore(pool),
		askdatasearch.DefaultRecallAuditOptions(
			cfg.AIEmbeddingModel, cfg.AIEmbeddingDimensions,
		),
		func(_ context.Context, result askdatasearch.RecallAuditResult) {
			logger.Warn(
				"AskData ANN recall below threshold",
				"tenant_id", result.TenantID,
				"domain_id", result.DomainID,
				"doc_type", result.DocumentType,
				"k", result.K,
				"recall", result.Recall,
				"threshold", result.Threshold,
			)
		},
	)
	if err != nil {
		logger.Error("initialize AskData recall auditor", "error", err)
		os.Exit(1)
	}
	go runAskDataSearchRecallAuditWorker(ctx, logger, recallAuditor)
	dimensionProfileOptions := askdatadimension.DefaultWorkerOptions()
	dimensionProfileOptions.Budget.MaxRows = int64(cfg.AskDataProfileScanLimit)
	dimensionProfileWorker, err := askdatadimension.NewWorker(
		askdatadimension.NewPostgresProfileStore(pool),
		askdatadimension.NewPostgresWarehouseScanner(warehouseQueryPool),
		dimensionProfileOptions,
	)
	if err != nil {
		logger.Error("initialize AskData dimension profile worker", "error", err)
		os.Exit(1)
	}
	go runAskDataDimensionProfileWorker(
		ctx, logger, dimensionProfileWorker, workerID, cfg.WorkerPollInterval,
	)
	go runAskDataGraphProjectionWorker(
		ctx, logger, graphProjector, workerID, cfg.WorkerPollInterval, cfg.AskDataProjectionLease,
	)
	go runAskDataRuntimeProjectionWorker(
		ctx, logger, runtimeProjectionWorker, workerID, cfg.WorkerPollInterval, cfg.AskDataProjectionLease,
	)
	go runReportAssetProjectionWorker(
		ctx, logger, reportAssetProjectionWorker, cfg.WorkerPollInterval,
	)
	go runReportPublicationRecoveryWorker(ctx, logger, reportRecoveryWorker, cfg.WorkerPollInterval)
	go runReportExportWorker(ctx, logger, reportExportWorker, workerID, cfg.WorkerPollInterval)
	go runReportScheduleWorker(ctx, logger, reportScheduleWorker, cfg.WorkerPollInterval)
	go runRuntimeConfigWorker(ctx, logger, runtimeConfigWorker, cfg.WorkerPollInterval)
	go runAskDataClarificationExpiryWorker(
		ctx, logger, askdataorchestrator.NewClarificationExpiryWorker(pool),
		cfg.WorkerPollInterval,
	)
	go runIdempotencyCleanupWorker(
		ctx, logger, platformidempotency.NewCleanupWorker(pool), cfg.WorkerPollInterval,
	)
	go runReportShareExpiryWorker(
		ctx, logger, reportsharing.NewExpiryWorker(pool), cfg.WorkerPollInterval,
	)
	decisionDueService, err := decision.NewService(
		decision.NewPostgresStore(pool), decision.NewPostgresEvidenceVerifier(pool), nil,
	)
	if err != nil {
		logger.Error("initialize decision due worker", "error", err)
		os.Exit(1)
	}
	go runDecisionDueWorker(ctx, logger, decisionDueService, cfg.WorkerPollInterval)
	// 问数运行 Worker：领取 RECEIVED 的运行并按 AI-005 阶段协议驱动状态机。
	// 图解析、向量检索与执行器缺失时对应工具报不可用，运行会带明确原因码失败关闭，
	// 而不是永远停在 RECEIVED。
	askDataGraphClient, err := askdatagraph.NewClient(graphPool)
	if err != nil {
		logger.Error("initialize AskData graph client", "error", err)
		os.Exit(1)
	}
	askDataGraphResolver, err := askdatagraph.NewResolver(
		askDataGraphClient,
		askdatagraph.NewPostgresCertifiedPlanCache(pool),
		askdatagraph.NewPostgresFallback(pool),
	)
	if err != nil {
		logger.Error("initialize AskData graph resolver", "error", err)
		os.Exit(1)
	}
	askDataRetriever, err := askdatasearch.NewRetriever(
		askdatasearch.NewPostgresRetrievalStore(pool), askdatasearch.DefaultRankConfig(),
	)
	if err != nil {
		logger.Error("initialize AskData retriever", "error", err)
		os.Exit(1)
	}
	askDataPinnedCompiler, err := askdatacompiler.NewPinnedIRCompiler(
		askdatacompiler.NewPostgresContractStore(pool),
	)
	if err != nil {
		logger.Error("initialize AskData pinned IR compiler", "error", err)
		os.Exit(1)
	}
	// 认证业务词典作为确定性精确命中进入检索：Owner 认证过的企业黑话与同义词
	// 必须压过词法相似度，否则认证本身没有产生任何效果。
	askDataDictionary, err := askdataunderstanding.NewDictionaryMatcher(
		dictionarypostgres.NewLoader(pool), askdataunderstanding.NewDictionaryCache(),
	)
	if err != nil {
		logger.Error("initialize AskData business dictionary", "error", err)
		os.Exit(1)
	}
	askDataCognition, err := askdatatools.NewCognitionRunner(aiService, askdatacognition.ExecutorOptions{})
	if err != nil {
		logger.Error("initialize AskData cognition runner", "error", err)
		os.Exit(1)
	}
	askDataLoopOptions := askdataorchestrator.DefaultLoopOptions()
	askDataCostGovernor, costGovernorErr := askdataobservability.NewQuotaPostgresStore(pool)
	if costGovernorErr != nil {
		logger.Error("initialize AskData cost governor", "error", costGovernorErr)
		os.Exit(1)
	}
	askDataLoopOptions.CostGovernor = askDataCostGovernor
	askDataAssembler, err := askdatatools.NewAssembler(askdatatools.Services{
		Reader:        askdataregistry.NewQueryReader(pool),
		Retriever:     askDataRetriever,
		Embedder:      askdatatools.BatchEmbedder{Provider: embeddingProvider},
		Graph:         askDataGraphResolver,
		Compiler:      askDataPinnedCompiler,
		Validator:     reportPlanValidator,
		Coverage:      reportCoverage,
		Executor:      reportPlanExecutor,
		Dictionary:    askDataDictionary,
		ReportSources: askdatareportasset.NewPostgresProjectionRuntimeStore(pool),
	}, askDataCognition, askDataLoopOptions)
	if err != nil {
		logger.Error("initialize AskData question assembler", "error", err)
		os.Exit(1)
	}
	askDataLeases := askdataorchestrator.NewLeaseStore(pool)
	questionRunWorkerOptions := askdataorchestrator.DefaultRunWorkerOptions()
	questionRunWorkerOptions.WorkerID = workerID
	questionRunWorker, err := askdataorchestrator.NewRunWorker(
		askDataLeases, askdataorchestrator.NewPostgresStore(pool),
		askDataAssembler, questionRunWorkerOptions,
	)
	if err != nil {
		logger.Error("initialize AskData question run worker", "error", err)
		os.Exit(1)
	}
	questionRetention, err := askdataorchestrator.NewRetentionPolicy(askdataorchestrator.RetentionConfig{
		QuestionMode: askdataorchestrator.OriginalQuestionMode(cfg.AskDataQuestionRetentionMode),
		QuestionTTL:  cfg.AskDataQuestionRetentionTTL, RunArtifactTTL: cfg.AskDataRunArtifactTTL,
		QuestionEncryptionKey: cfg.AskDataQuestionEncryptionKey,
	})
	if err != nil {
		logger.Error("initialize AskData question retention", "error", err)
		os.Exit(1)
	}
	questionEnvelopes, err := askdataorchestrator.NewPostgresQuestionEnvelopeStore(pool, questionRetention)
	if err != nil {
		logger.Error("initialize AskData question envelopes", "error", err)
		os.Exit(1)
	}
	questionRunWorker.SetQuestionFactSource(questionEnvelopes)
	shadowDispatcher, err := askdataorchestrator.NewShadowDispatcher(
		askdataorchestrator.NewShadowJobStore(pool), askDataLeases,
		askdataorchestrator.NewPostgresStore(pool), questionEnvelopes, workerID,
	)
	if err != nil {
		logger.Error("initialize AskData release shadow dispatcher", "error", err)
		os.Exit(1)
	}
	go runAskDataReleaseShadowWorker(ctx, logger, shadowDispatcher, cfg.WorkerPollInterval)
	go runAskDataQuestionRunWorker(ctx, logger, questionRunWorker, askDataLeases, cfg.WorkerPollInterval)
	go runAskDataSemanticImportWorker(
		ctx,
		logger,
		registryimport.NewWorker(
			registryimport.NewPostgresStore(pool),
			registryimport.NewFileRowSource(objectStorage),
			registryimport.NewFourLayerValidator(registryimport.NewPostgresValidationCatalog(pool)),
		),
		workerID,
		cfg.WorkerPollInterval,
	)
	go runAskDataSemanticExportWorker(
		ctx,
		logger,
		registryimport.NewExportWorker(
			registryimport.NewPostgresExportJobStore(pool),
			registryimport.NewExportService(registryimport.NewPostgresExportCatalog(pool)),
			objectStorage,
			cfg.MinIOUploadsBucket,
		),
		workerID,
		cfg.WorkerPollInterval,
	)
	intentRepository := askdatareportasset.NewPostgresIntentRepository(pool)
	inboundService, err := reportinbound.NewService(
		reportinbound.NewPostgresAuthorizer(pool), reportstore.NewPostgresStore(pool),
	)
	if err != nil {
		logger.Error("initialize AskData add-to-report inbound service", "error", err)
		os.Exit(1)
	}
	intentWorker, err := askdatareportasset.NewIntentWorker(intentRepository, inboundService)
	if err != nil {
		logger.Error("initialize AskData add-to-report worker", "error", err)
		os.Exit(1)
	}
	go runAskDataAddToReportWorker(
		ctx, logger, intentWorker, cfg.WorkerPollInterval,
	)
	activeLearningWorker, err := askdatafeedback.NewActiveLearningWorker(
		askdatafeedback.NewPostgresSignalSource(pool), askdatafeedback.NewPostgresRepository(pool), 100,
	)
	if err != nil {
		logger.Error("initialize AskData active-learning worker", "error", err)
		os.Exit(1)
	}
	go runAskDataActiveLearningWorker(ctx, logger, activeLearningWorker)

	odsResolver := materializationworker.NewODSResolver(
		pool,
		warehouse.NewFileStagerWithMaxBytes(warehousePool, excelManager, cfg.WarehouseStageMaxBytes),
	)
	for sourceType, connector := range databaseConnectors {
		odsResolver.SetDatabaseStager(
			sourceType,
			warehouse.NewStagerWithMaxBytes(warehousePool, connector, cfg.WarehouseStageMaxBytes),
		)
	}
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
	go runDWSModelingWorker(
		ctx,
		logger,
		dataset.NewDWSModelingWorker(
			datasetStore,
			dataset.NewOrchestratedDWSModelingPlanner(aiService),
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

func runDWSModelingWorker(
	ctx context.Context,
	logger *slog.Logger,
	worker *dataset.DWSModelingWorker,
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
				logger.Error("process DWS theme modeling", "tenant_id", tenantID, "error", runErr)
			}
			processed = processed || didProcess
		}
		return processed, nil
	}, func(err error) { logger.Error("list DWS modeling tenants", "error", err) })
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

func runAskDataEmbeddingWorker(
	ctx context.Context,
	logger *slog.Logger,
	worker *askdatasearch.EmbeddingWorker,
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
				logger.Error("process AskData embeddings", "tenant_id", tenantID, "error", runErr)
			}
			processed = processed || count > 0
		}
		return processed, nil
	}, func(err error) { logger.Error("list AskData embedding tenants", "error", err) })
}

func runAskDataSearchRecallAuditWorker(
	ctx context.Context,
	logger *slog.Logger,
	auditor *askdatasearch.RecallAuditor,
) {
	const auditPollInterval = time.Hour
	runTenantWorkerLoop(ctx, auditPollInterval, func(ctx context.Context) (bool, error) {
		processed := false
		tenantIDs, err := auditor.TenantIDs(ctx)
		if err != nil {
			return false, err
		}
		for _, tenantID := range tenantIDs {
			auditCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
			results, runErr := auditor.RunTenant(auditCtx, tenantID, time.Now().UTC())
			cancel()
			if runErr != nil {
				logger.Error("audit AskData ANN recall", "tenant_id", tenantID, "error", runErr)
			}
			processed = processed || len(results) > 0
		}
		return processed, nil
	}, func(err error) { logger.Error("list AskData recall audit tenants", "error", err) })
}

func runAskDataDimensionProfileWorker(
	ctx context.Context,
	logger *slog.Logger,
	worker *askdatadimension.Worker,
	workerID string,
	pollInterval time.Duration,
) {
	runTenantWorkerLoop(ctx, pollInterval, func(ctx context.Context) (bool, error) {
		processed := false
		tenantIDs, err := worker.TenantIDs(ctx)
		if err != nil {
			return false, err
		}
		for _, tenantID := range tenantIDs {
			didProcess, runErr := worker.ProcessNext(
				ctx, tenantID, workerID, askdatadimension.DefaultDimensionProfileLease,
			)
			if runErr != nil {
				logger.Error("process AskData dimension profile", "tenant_id", tenantID, "error", runErr)
			}
			processed = processed || didProcess
		}
		return processed, nil
	}, func(err error) { logger.Error("list AskData dimension profile tenants", "error", err) })
}

func runAskDataGraphProjectionWorker(
	ctx context.Context,
	logger *slog.Logger,
	projector *askdatagraph.Projector,
	workerID string,
	pollInterval time.Duration,
	lease time.Duration,
) {
	runTenantWorkerLoop(ctx, pollInterval, func(ctx context.Context) (bool, error) {
		processed := false
		tenantIDs, err := projector.TenantIDs(ctx)
		if err != nil {
			return false, err
		}
		for _, tenantID := range tenantIDs {
			didProcess, runErr := projector.ProcessNext(
				ctx, tenantID, workerID, lease,
			)
			if runErr != nil {
				logger.Error("process AskData graph projection", "tenant_id", tenantID, "error", runErr)
			}
			processed = processed || didProcess
		}
		return processed, nil
	}, func(err error) { logger.Error("list AskData graph projection tenants", "error", err) })
}

func runAskDataRuntimeProjectionWorker(
	ctx context.Context,
	logger *slog.Logger,
	worker *askdataprojection.Worker,
	workerID string,
	pollInterval time.Duration,
	lease time.Duration,
) {
	runTenantWorkerLoop(ctx, pollInterval, func(ctx context.Context) (bool, error) {
		processed := false
		for _, target := range askdataprojection.RuntimeTargets {
			tenantIDs, err := worker.TenantIDs(ctx, target)
			if err != nil {
				return processed, err
			}
			for _, tenantID := range tenantIDs {
				didProcess, runErr := worker.ProcessNext(ctx, tenantID, target, workerID, lease)
				if runErr != nil {
					logger.Error("process AskData runtime projection", "tenant_id", tenantID, "target", target, "error", runErr)
				}
				processed = processed || didProcess
			}
		}
		return processed, nil
	}, func(err error) { logger.Error("list AskData runtime projection tenants", "error", err) })
}

func runReportAssetProjectionWorker(
	ctx context.Context,
	logger *slog.Logger,
	worker *askdatareportasset.ProjectionRuntimeWorker,
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
			didProcess, runErr := worker.ProcessNext(ctx, tenantID, lease)
			if runErr != nil {
				logger.Error("project report semantic asset", "tenant_id", tenantID, "error", runErr)
			}
			processed = processed || didProcess
		}
		return processed, nil
	}, func(err error) { logger.Error("list report semantic asset projection tenants", "error", err) })
}

func runReportPublicationRecoveryWorker(
	ctx context.Context,
	logger *slog.Logger,
	worker *reportpublication.RecoveryWorker,
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
			didProcess, runErr := worker.ProcessNext(ctx, tenantID, lease)
			if runErr != nil {
				logger.Error("recover report publication", "tenant_id", tenantID, "error", runErr)
			}
			processed = processed || didProcess
		}
		return processed, nil
	}, func(err error) { logger.Error("list report publication recovery tenants", "error", err) })
}

func runReportExportWorker(
	ctx context.Context,
	logger *slog.Logger,
	worker *reportpublication.ExportWorker,
	workerID string,
	pollInterval time.Duration,
) {
	runTenantWorkerLoop(ctx, pollInterval, func(ctx context.Context) (bool, error) {
		processed := false
		tenantIDs, err := worker.TenantIDs(ctx)
		if err != nil {
			return false, err
		}
		for _, tenantID := range tenantIDs {
			didProcess, runErr := worker.ProcessNext(ctx, tenantID, workerID, reportpublication.DefaultExportLease)
			if runErr != nil {
				logger.Error("process report export", "tenant_id", tenantID, "error", runErr)
			}
			processed = processed || didProcess
		}
		return processed, nil
	}, func(err error) { logger.Error("list report export tenants", "error", err) })
}

func runAskDataClarificationExpiryWorker(
	ctx context.Context,
	logger *slog.Logger,
	worker *askdataorchestrator.ClarificationExpiryWorker,
	pollInterval time.Duration,
) {
	runTenantWorkerLoop(ctx, pollInterval, func(ctx context.Context) (bool, error) {
		processed := false
		tenantIDs, err := worker.TenantIDs(ctx)
		if err != nil {
			return false, err
		}
		for _, tenantID := range tenantIDs {
			count, runErr := worker.ProcessTenant(
				ctx, tenantID, time.Now().UTC(), askdataorchestrator.MaxClarificationExpiryBatch,
			)
			if runErr != nil {
				logger.Error("expire AskData clarifications", "tenant_id", tenantID, "error", runErr)
			}
			processed = processed || count > 0
		}
		return processed, nil
	}, func(err error) { logger.Error("list AskData clarification expiry tenants", "error", err) })
}

func runIdempotencyCleanupWorker(
	ctx context.Context,
	logger *slog.Logger,
	worker *platformidempotency.CleanupWorker,
	pollInterval time.Duration,
) {
	runTenantWorkerLoop(ctx, pollInterval, func(ctx context.Context) (bool, error) {
		processed := false
		tenantIDs, err := worker.TenantIDs(ctx)
		if err != nil {
			return false, err
		}
		for _, tenantID := range tenantIDs {
			count, runErr := worker.ProcessTenant(
				ctx, tenantID, time.Now().UTC(), platformidempotency.MaxExpiredCleanupBatch,
			)
			if runErr != nil {
				logger.Error("clean expired idempotency records", "tenant_id", tenantID, "error", runErr)
			}
			processed = processed || count > 0
		}
		return processed, nil
	}, func(err error) { logger.Error("list idempotency cleanup tenants", "error", err) })
}

func runReportShareExpiryWorker(
	ctx context.Context,
	logger *slog.Logger,
	worker *reportsharing.ExpiryWorker,
	pollInterval time.Duration,
) {
	runTenantWorkerLoop(ctx, pollInterval, func(ctx context.Context) (bool, error) {
		processed := false
		tenantIDs, err := worker.TenantIDs(ctx)
		if err != nil {
			return false, err
		}
		for _, tenantID := range tenantIDs {
			count, runErr := worker.ProcessTenant(
				ctx, tenantID, time.Now().UTC(), reportsharing.MaxShareExpiryBatch,
			)
			if runErr != nil {
				logger.Error("expire report shares", "tenant_id", tenantID, "error", runErr)
			}
			processed = processed || count > 0
		}
		return processed, nil
	}, func(err error) { logger.Error("list report share expiry tenants", "error", err) })
}

func runReportScheduleWorker(ctx context.Context, logger *slog.Logger, worker *reportschedule.Worker, pollInterval time.Duration) {
	runTenantWorkerLoop(ctx, pollInterval, func(ctx context.Context) (bool, error) {
		processed := false
		tenantIDs, err := worker.TenantIDs(ctx)
		if err != nil {
			return false, err
		}
		for _, tenantID := range tenantIDs {
			count, runErr := worker.ProcessTenant(ctx, tenantID, 200)
			if runErr != nil {
				logger.Error("process report schedules", "tenant_id", tenantID, "error", runErr)
			}
			processed = processed || count > 0
		}
		return processed, nil
	}, func(err error) { logger.Error("list report schedule tenants", "error", err) })
}

func runDecisionDueWorker(ctx context.Context, logger *slog.Logger, service *decision.Service, pollInterval time.Duration) {
	runTenantWorkerLoop(ctx, pollInterval, func(ctx context.Context) (bool, error) {
		count, err := service.ProcessDue(ctx, 500)
		if err != nil {
			return false, err
		}
		return count > 0, nil
	}, func(err error) { logger.Error("process decision review and action deadlines", "error", err) })
}

func runRuntimeConfigWorker(ctx context.Context, logger *slog.Logger, worker *runtimeconfig.Worker, pollInterval time.Duration) {
	runTenantWorkerLoop(ctx, pollInterval, func(ctx context.Context) (bool, error) {
		processed := false
		tenantIDs, err := worker.TenantIDs(ctx)
		if err != nil {
			return false, err
		}
		for _, tenantID := range tenantIDs {
			count, runErr := worker.ProcessTenant(ctx, tenantID, 200)
			if runErr != nil {
				logger.Error("process runtime configuration rollout", "tenant_id", tenantID, "error", runErr)
			}
			processed = processed || count > 0
		}
		return processed, nil
	}, func(err error) { logger.Error("list runtime configuration rollout tenants", "error", err) })
}

func runAskDataSemanticImportWorker(
	ctx context.Context,
	logger *slog.Logger,
	worker *registryimport.Worker,
	workerID string,
	pollInterval time.Duration,
) {
	runTenantWorkerLoop(ctx, pollInterval, func(ctx context.Context) (bool, error) {
		processed := false
		tenantIDs, err := worker.TenantIDs(ctx)
		if err != nil {
			return false, err
		}
		for _, tenantID := range tenantIDs {
			didProcess, runErr := worker.ProcessNext(
				ctx, tenantID, workerID, registryimport.DefaultValidationLease,
			)
			if runErr != nil {
				logger.Error(
					"process AskData semantic import",
					"tenant_id", tenantID,
					"error", runErr,
				)
			}
			processed = processed || didProcess
		}
		return processed, nil
	}, func(err error) { logger.Error("list AskData semantic import tenants", "error", err) })
}

func runAskDataSemanticExportWorker(
	ctx context.Context,
	logger *slog.Logger,
	worker *registryimport.ExportWorker,
	workerID string,
	pollInterval time.Duration,
) {
	runTenantWorkerLoop(ctx, pollInterval, func(ctx context.Context) (bool, error) {
		processed := false
		tenantIDs, err := worker.TenantIDs(ctx)
		if err != nil {
			return false, err
		}
		for _, tenantID := range tenantIDs {
			didProcess, runErr := worker.ProcessNext(
				ctx, tenantID, workerID, registryimport.DefaultExportLease,
			)
			if runErr != nil {
				logger.Error(
					"process AskData semantic export",
					"tenant_id", tenantID,
					"error", runErr,
				)
			}
			processed = processed || didProcess
		}
		return processed, nil
	}, func(err error) { logger.Error("list AskData semantic export tenants", "error", err) })
}

func runAskDataAddToReportWorker(
	ctx context.Context,
	logger *slog.Logger,
	worker *askdatareportasset.IntentWorker,
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
			didProcess, runErr := worker.ProcessNext(ctx, tenantID, lease)
			if runErr != nil {
				logger.Error("process AskData add-to-report intent", "tenant_id", tenantID, "error", runErr)
			}
			processed = processed || didProcess
		}
		return processed, nil
	}, func(err error) { logger.Error("list AskData add-to-report tenants", "error", err) })
}

func runAskDataActiveLearningWorker(
	ctx context.Context,
	logger *slog.Logger,
	worker *askdatafeedback.ActiveLearningWorker,
) {
	const interval = 24 * time.Hour
	runTenantWorkerLoop(ctx, interval, func(ctx context.Context) (bool, error) {
		pairs, err := worker.TenantDomains(ctx)
		if err != nil {
			return false, err
		}
		for _, pair := range pairs {
			count, runErr := worker.ProcessDomain(ctx, pair[0], pair[1], time.Now().UTC())
			if runErr != nil {
				logger.Error("mine AskData active-learning candidates", "tenant_id", pair[0], "domain_id", pair[1], "error", runErr)
			}
			if count > 0 {
				logger.Info("mined AskData active-learning candidates", "tenant_id", pair[0], "domain_id", pair[1], "count", count)
			}
		}
		// Mining is a daily snapshot, not a queue drain. Always wait the full interval.
		return false, nil
	}, func(err error) { logger.Error("list AskData active-learning domains", "error", err) })
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
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

func runAskDataQuestionRunWorker(
	ctx context.Context,
	logger *slog.Logger,
	worker *askdataorchestrator.RunWorker,
	leases *askdataorchestrator.LeaseStore,
	pollInterval time.Duration,
) {
	runTenantWorkerLoop(ctx, pollInterval, func(ctx context.Context) (bool, error) {
		processed := false
		tenantIDs, err := leases.ListTenantIDs(ctx)
		if err != nil {
			return false, err
		}
		for _, tenantID := range tenantIDs {
			didProcess, runErr := worker.ProcessNext(ctx, tenantID)
			if runErr != nil {
				logger.Error("process AskData question run", "tenant_id", tenantID, "error", runErr)
			}
			processed = processed || didProcess
		}
		return processed, nil
	}, func(err error) { logger.Error("list AskData question run tenants", "error", err) })
}

func runAskDataReleaseShadowWorker(
	ctx context.Context,
	logger *slog.Logger,
	dispatcher *askdataorchestrator.ShadowDispatcher,
	pollInterval time.Duration,
) {
	runTenantWorkerLoop(ctx, pollInterval, func(ctx context.Context) (bool, error) {
		processed := false
		tenantIDs, err := dispatcher.ListTenantIDs(ctx)
		if err != nil {
			return false, err
		}
		for _, tenantID := range tenantIDs {
			didProcess, dispatchErr := dispatcher.ProcessNext(ctx, tenantID)
			if dispatchErr != nil {
				logger.Error("dispatch AskData release shadow", "tenant_id", tenantID, "error", dispatchErr)
			}
			processed = processed || didProcess
		}
		return processed, nil
	}, func(err error) { logger.Error("list AskData release shadow tenants", "error", err) })
}
