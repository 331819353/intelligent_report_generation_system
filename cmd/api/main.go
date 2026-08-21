package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"

	"intelligent-report-generation-system/internal/access"
	aiplatform "intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/answer"
	askdatacompiler "intelligent-report-generation-system/internal/askdata/compiler"
	askdatafeedback "intelligent-report-generation-system/internal/askdata/feedback"
	askdatahttp "intelligent-report-generation-system/internal/askdata/http"
	"intelligent-report-generation-system/internal/askdata/ircontract"
	"intelligent-report-generation-system/internal/askdata/lineage"
	askdataorchestrator "intelligent-report-generation-system/internal/askdata/orchestrator"
	"intelligent-report-generation-system/internal/askdata/registry"
	registryimport "intelligent-report-generation-system/internal/askdata/registry/import"
	askdatareportasset "intelligent-report-generation-system/internal/askdata/reportasset"
	"intelligent-report-generation-system/internal/askdata/retrieval"
	"intelligent-report-generation-system/internal/askdata/savedquestion"
	askdatasearch "intelligent-report-generation-system/internal/askdata/search"
	askdatatools "intelligent-report-generation-system/internal/askdata/tools"
	askdataunderstanding "intelligent-report-generation-system/internal/askdata/understanding"
	dictionarypostgres "intelligent-report-generation-system/internal/askdata/understanding/dictionarypostgres"
	askdatavalidator "intelligent-report-generation-system/internal/askdata/validator"
	"intelligent-report-generation-system/internal/asset"
	"intelligent-report-generation-system/internal/assetembedding"
	"intelligent-report-generation-system/internal/auth"
	"intelligent-report-generation-system/internal/backgroundtask"
	"intelligent-report-generation-system/internal/config"
	"intelligent-report-generation-system/internal/datarequest"
	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/datasetai"
	datasetaiknowledge "intelligent-report-generation-system/internal/datasetai/knowledge"
	datasetaisampling "intelligent-report-generation-system/internal/datasetai/sampling"
	"intelligent-report-generation-system/internal/datasetsemanticnaming"
	"intelligent-report-generation-system/internal/datasource"
	"intelligent-report-generation-system/internal/datasourceai"
	"intelligent-report-generation-system/internal/decision"
	"intelligent-report-generation-system/internal/embedding"
	"intelligent-report-generation-system/internal/federation"
	"intelligent-report-generation-system/internal/filequery"
	"intelligent-report-generation-system/internal/httpserver"
	"intelligent-report-generation-system/internal/materialization"
	"intelligent-report-generation-system/internal/metadataai"
	"intelligent-report-generation-system/internal/observability"
	"intelligent-report-generation-system/internal/platform/database"
	platformidempotency "intelligent-report-generation-system/internal/platform/idempotency"
	"intelligent-report-generation-system/internal/policy"
	"intelligent-report-generation-system/internal/queryruntime"
	reportasset "intelligent-report-generation-system/internal/report/asset"
	reportauthorization "intelligent-report-generation-system/internal/report/authorization"
	"intelligent-report-generation-system/internal/report/cardkind"
	reportfollow "intelligent-report-generation-system/internal/report/follow"
	reporthttp "intelligent-report-generation-system/internal/report/http"
	"intelligent-report-generation-system/internal/report/insight"
	reportpublication "intelligent-report-generation-system/internal/report/publication"
	reportai "intelligent-report-generation-system/internal/report/reportai"
	"intelligent-report-generation-system/internal/report/reportinsight"
	reportruntime "intelligent-report-generation-system/internal/report/runtime"
	reportschedule "intelligent-report-generation-system/internal/report/schedule"
	reportsharing "intelligent-report-generation-system/internal/report/sharing"
	reportstore "intelligent-report-generation-system/internal/report/store"
	reporttemplate "intelligent-report-generation-system/internal/report/template"
	"intelligent-report-generation-system/internal/runtimeconfig"
	"intelligent-report-generation-system/internal/support"
	"intelligent-report-generation-system/internal/userlifecycle"
	"intelligent-report-generation-system/internal/workitem"
)

type savedQuestionLauncher struct{ service *askdatahttp.PostgresService }

type savedQuestionRunResolver struct{ service *askdatahttp.PostgresService }

func (resolver savedQuestionRunResolver) ResolveSavedQuestionIR(
	ctx context.Context, identity savedquestion.Identity, runID askdata.ID,
) (ircontract.SemanticIR, error) {
	snapshot, err := resolver.service.GetQuestion(ctx, askdatahttp.RequestIdentity{
		TenantID: identity.TenantID, DomainID: identity.DomainID, ActorID: identity.ActorID,
	}, runID)
	if err != nil {
		return ircontract.SemanticIR{}, err
	}
	semanticIR, err := askdatahttp.SavedQuestionSemanticIR(snapshot)
	if err != nil {
		return ircontract.SemanticIR{}, savedquestion.ErrInvalid
	}
	return semanticIR, nil
}

func (launcher savedQuestionLauncher) LaunchSavedQuestion(
	ctx context.Context, input savedquestion.LaunchInput,
) (savedquestion.LaunchResult, error) {
	questionHash := askdata.HashBytes([]byte("askdata-saved-question-open-v1\x00" + strings.TrimSpace(input.Question.QuestionText)))
	idempotencyHash := askdata.HashBytes([]byte("askdata-saved-question-idempotency-v1\x00" + input.IdempotencyKey))
	conversationID := askdata.ID(uuid.NewSHA1(uuid.NameSpaceOID, []byte(
		string(input.Identity.TenantID)+":"+string(input.Identity.ActorID)+":"+input.IdempotencyKey,
	)).String())
	result, err := launcher.service.CreateQuestion(ctx, askdatahttp.RequestIdentity{
		TenantID: input.Identity.TenantID, DomainID: input.Identity.DomainID, ActorID: input.Identity.ActorID,
	}, askdatahttp.CreateQuestionInput{
		Question: input.Question.QuestionText, QuestionHash: questionHash,
		IdempotencyKeyHash: idempotencyHash, ConversationID: conversationID,
		SavedQuestionID: input.Question.ID,
	})
	if err != nil {
		return savedquestion.LaunchResult{}, err
	}
	return savedquestion.LaunchResult{
		RunID: result.Snapshot.Run.ID, ConversationID: result.Snapshot.Run.ConversationID, Replayed: result.Replayed,
	}, nil
}

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
	manifestSeedCtx, manifestSeedCancel := context.WithTimeout(context.Background(), 10*time.Second)
	err = reporttemplate.SeedBundledComponents(manifestSeedCtx, pool)
	manifestSeedCancel()
	if err != nil {
		logger.Error("seed bundled report component manifests", "error", err)
		os.Exit(1)
	}

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
	accessAdminStore := access.NewAdminStore(pool)
	accessAdminHandler := access.NewAdminHandler(authService, accessAdminStore)
	operationalObservabilityHandler := observability.NewOperationalHandler(
		authService, accessAdminStore, observability.NewOperationalStore(pool),
	)
	userLifecycleService, err := userlifecycle.NewService(pool, accessAdminStore)
	if err != nil {
		logger.Error("initialize user lifecycle service", "error", err)
		os.Exit(1)
	}
	userLifecycleHandler := userlifecycle.NewHandler(
		authService, platformidempotency.NewPostgresRepository(pool), userLifecycleService,
	)
	runtimeConfigService, err := runtimeconfig.NewService(pool, accessAdminStore)
	if err != nil {
		logger.Error("initialize runtime configuration service", "error", err)
		os.Exit(1)
	}
	runtimeConfigHandler := runtimeconfig.NewHandler(
		authService, platformidempotency.NewPostgresRepository(pool), runtimeConfigService,
	)
	supportHandler := support.NewHandler(authService, accessService, support.NewRepository(pool))
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
	dataSourceService.SetConnectionTestJobRepository(datasource.NewPostgresConnectionTestRepository(pool))

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
	queryConnectors := make(map[datasource.Type]queryruntime.QueryConnector, len(databaseConnectors))
	for sourceType, connector := range databaseConnectors {
		queryConnectors[sourceType] = connector
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
	// Business knowledge for the modeling blueprint reuses the AskData question
	// stack: certified dictionary exact hits, the hybrid semantic-object retriever
	// and release-pinned contracts. Failures here only degrade the blueprint.
	askDataKnowledgeRetriever, err := askdatasearch.NewRetriever(
		askdatasearch.NewPostgresRetrievalStore(pool), askdatasearch.DefaultRankConfig(),
	)
	if err != nil {
		logger.Error("initialize AskData retriever for dataset AI knowledge", "error", err)
		os.Exit(1)
	}
	askDataKnowledgeDictionary, err := askdataunderstanding.NewDictionaryMatcher(
		dictionarypostgres.NewLoader(pool), askdataunderstanding.NewDictionaryCache(),
	)
	if err != nil {
		logger.Error("initialize AskData dictionary for dataset AI knowledge", "error", err)
		os.Exit(1)
	}
	datasetAIKnowledge, err := datasetaiknowledge.NewProvider(
		pool, registry.NewQueryReader(pool), askDataKnowledgeRetriever, askDataKnowledgeDictionary,
		askdatatools.BatchEmbedder{Provider: embeddingProvider},
	)
	if err != nil {
		logger.Error("initialize dataset AI knowledge provider", "error", err)
		os.Exit(1)
	}
	// Sample rows for source screening and blueprint grounding go through the same
	// governed sampling path as the asset preview (masked, ≤5 rows, no client SQL).
	datasetAISampler, err := datasetaisampling.New(assetRepository, dataSourceService)
	if err != nil {
		logger.Error("initialize dataset AI sampler", "error", err)
		os.Exit(1)
	}
	datasetAIService := datasetai.NewService(
		datasetai.NewVersionAwareAssetCatalog(pool, assetRepository),
		aiService,
		datasetai.ServiceOptions{
			Timeout: cfg.AIRequestTimeout, MaxProviderInputBytes: cfg.AIMaxInputBytes,
			Retriever: assetembedding.NewRetriever(
				assetembedding.NewPostgresStore(pool), embeddingProvider,
			),
			RetrievalMode: cfg.DatasetAIRetrievalMode,
			Sessions:      datasetai.NewPostgresSessionStore(pool),
			Knowledge:     datasetAIKnowledge,
			Sampler:       datasetAISampler,
		},
	)
	questionService := askdatahttp.NewPostgresServiceWithClarificationTimeout(
		pool, cfg.AskDataClarificationTimeout,
	)
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
	questionService.SetQuestionEnvelopeStore(questionEnvelopes)
	questionHandler := askdatahttp.NewHandler(authService, questionService)
	savedQuestionHandler := savedquestion.NewHandler(
		authService, savedquestion.NewPostgresRepository(pool),
		savedQuestionLauncher{service: questionService},
		savedQuestionRunResolver{service: questionService},
	)
	feedbackTicketHandler := askdatafeedback.NewHandler(
		authService, askdatafeedback.NewPostgresRepository(pool),
	)
	reportAssetHandler := askdatareportasset.NewCertificationHandler(
		authService, askdatareportasset.NewPostgresProjectionRuntimeStore(pool),
	)
	dataRequestHandler := datarequest.NewHandlerWithIdempotency(
		authService, datarequest.NewService(datarequest.NewPostgresStore(pool)),
		platformidempotency.NewPostgresRepository(pool),
	)
	semanticImportStore := registryimport.NewPostgresStore(pool)
	semanticExportService := registryimport.NewExportService(
		registryimport.NewPostgresExportCatalog(pool),
	)
	semanticRegistryStore := registry.NewPostgresStore(pool)
	releaseReviewer, err := registry.NewReleaseReviewer(aiService)
	if err != nil {
		logger.Error("initialize semantic release reviewer", "error", err)
		os.Exit(1)
	}
	releaseReviewService, err := registry.NewReleaseReviewService(releaseReviewer, semanticRegistryStore)
	if err != nil {
		logger.Error("initialize semantic release review service", "error", err)
		os.Exit(1)
	}
	// 四分区发现检索：确定性目录巷道必备；向量巷道与血缘扩展按可用性组装，
	// 缺席时门面自动降级为确定性结果。
	semanticDiscoveryFacade, err := retrieval.NewFacade(
		retrieval.NewPostgresCatalogSearcher(pool),
		retrieval.NewPostgresVectorSearcher(pool, embeddingProvider),
		retrieval.NewLineageGraphExpander(lineage.NewPostgresStore(pool)),
	)
	if err != nil {
		logger.Error("initialize semantic discovery facade", "error", err)
		os.Exit(1)
	}
	semanticAdminHandler := askdatahttp.NewAdminHandlerWithImportServices(
		authService,
		semanticRegistryStore,
		registryimport.NewTemplateService(registryimport.NewPostgresTemplateCatalog(pool)),
		registryimport.NewUploadService(objectStorage, semanticImportStore, cfg.MinIOUploadsBucket),
		registryimport.NewReportService(semanticImportStore),
		askdatahttp.ImportMutationServices{
			Reads: semanticImportStore,
			Commit: registryimport.NewCommitService(
				semanticImportStore, registryimport.NewPostgresDraftCreator(),
			),
			Withdraw: registryimport.NewWithdrawService(
				semanticImportStore, registryimport.NewPostgresDraftWithdrawer(),
			),
			Certify:         registry.NewCertificationService(pool),
			Export:          semanticExportService,
			ExportJobs:      registryimport.NewPostgresExportJobStore(pool),
			ExportArtifacts: objectStorage,
			ReleaseReview:   releaseReviewService,
			JSONReport: registryimport.NewJSONReportService(
				semanticImportStore, registryimport.NewPostgresIndexReadinessStore(pool),
			),
			Lineage:   lineage.NewPostgresStore(pool),
			Discovery: semanticDiscoveryFacade,
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
	reportArtifacts, err := reportpublication.NewMinIOArtifactStoreWithCredentials(
		cfg.MinIOEndpoint, cfg.MinIOAccessKey, cfg.MinIOSecretKey, cfg.MinIOUseSSL, cfg.MinIOUploadsBucket,
	)
	if err != nil {
		logger.Error("initialize report artifact store", "error", err)
		os.Exit(1)
	}
	reportRepository := reportstore.NewPostgresStore(pool)
	reportAuthorizer := reportauthorization.NewPostgresAuthorizer(pool)
	reportComponentRegistry, err := reporttemplate.NewDefaultRegistry()
	if err != nil {
		logger.Error("initialize report component registry", "error", err)
		os.Exit(1)
	}
	// The verifier only accepts the wordlist version it actually loaded.
	const reportPolicyWordlistVersion = "1.0.0"
	reportInsightArtifacts := insight.NewPostgresStore(pool)

	// Report conclusions are written by the model but may only be stored if they
	// pass the same fact verifier Ask Data uses. A nil verifier leaves the
	// generate endpoint unavailable rather than storing unchecked prose.
	var reportNarrativeService *reportinsight.NarrativeService
	if reportNarrativeVerifier, verifierErr := answer.NewVerifier(answer.ReleaseVerifierPolicy{
		VerifierVersion:       answer.VerifierVersion,
		PolicyWordlistVersion: reportPolicyWordlistVersion,
	}); verifierErr != nil {
		slog.Warn("report conclusion generation disabled", "error", verifierErr)
	} else {
		reportNarrativeService = &reportinsight.NarrativeService{
			Model: reportinsight.AINarrativeModel{
				AI: aiService,
				Identity: func(ctx context.Context) (askdata.ID, askdata.ID, askdata.ID) {
					identity, _ := reportai.InvocationIdentityFrom(ctx)
					return identity.TenantID, identity.ActorID, identity.ReportID
				},
			},
			Verifier: reportNarrativeVerifier, Artifacts: reportInsightArtifacts,
			VerifierVersion:       answer.VerifierVersion,
			PolicyWordlistVersion: reportPolicyWordlistVersion,
			NewID:                 func() askdata.ID { return askdata.ID(uuid.NewString()) },
		}
	}

	reportAIGenerator, err := reportai.NewOrchestratedGenerator(aiService)
	if err != nil {
		logger.Error("initialize report AI generator", "error", err)
		os.Exit(1)
	}
	reportInsightRegistry := insight.NewRegistry()
	reportCardKindRegistry, err := cardkind.NewDefaultRegistry(reportComponentRegistry, reportInsightRegistry)
	if err != nil {
		logger.Error("initialize report card kind registry", "error", err)
		os.Exit(1)
	}
	reportDatasetRunner, err := reportruntime.NewDatasetVersionRunner(queryService)
	if err != nil {
		logger.Error("initialize report dataset runtime", "error", err)
		os.Exit(1)
	}
	reportSemanticContractStore := askdatacompiler.NewPostgresContractStore(pool)
	reportSemanticRehydrator, err := askdatacompiler.NewPinnedArtifactRehydrator(reportSemanticContractStore)
	if err != nil {
		logger.Error("initialize report semantic compiler", "error", err)
		os.Exit(1)
	}
	reportCoverage, err := askdatavalidator.NewCoverageControl(materializationStore)
	if err != nil {
		logger.Error("initialize report semantic coverage", "error", err)
		os.Exit(1)
	}
	reportPlanValidator, err := askdatavalidator.NewValidator(
		askdatavalidator.NewPostgresExplainer(warehousePool), askdatavalidator.DefaultLimits(),
	)
	if err != nil {
		logger.Error("initialize report semantic validator", "error", err)
		os.Exit(1)
	}
	reportPlanExecutor, err := askdatavalidator.NewExecutor(
		warehousePool,
		queryruntime.NewPostgresSemanticMaterializationRevalidator(pool),
		queryruntime.NewPostgresSemanticQuestionAuditStore(pool),
	)
	if err != nil {
		logger.Error("initialize report semantic executor", "error", err)
		os.Exit(1)
	}
	reportSemanticRunner, err := reportruntime.NewSemanticRuntimeRunner(
		reportruntime.NewPostgresSemanticArtifactSource(pool),
		reportruntime.NewPostgresViewerScopeResolver(pool),
		reportSemanticRehydrator, reportCoverage, reportPlanValidator, reportPlanExecutor,
	)
	if err != nil {
		logger.Error("initialize report semantic runtime", "error", err)
		os.Exit(1)
	}
	reportUpgradeCompiler, err := askdatacompiler.NewPinnedIRCompiler(reportSemanticContractStore)
	if err != nil {
		logger.Error("initialize report semantic upgrade compiler", "error", err)
		os.Exit(1)
	}
	decisionOutcomeRunner, err := decision.NewGovernedOutcomeRunner(
		pool, reportruntime.NewPostgresViewerScopeResolver(pool), reportUpgradeCompiler,
		reportCoverage, reportPlanValidator, reportPlanExecutor,
	)
	if err != nil {
		logger.Error("initialize decision outcome runner", "error", err)
		os.Exit(1)
	}
	decisionService, err := decision.NewService(
		decision.NewPostgresStore(pool), decision.NewPostgresEvidenceVerifier(pool), decisionOutcomeRunner,
	)
	if err != nil {
		logger.Error("initialize decision service", "error", err)
		os.Exit(1)
	}
	decisionHandler := decision.NewHandler(
		authService, platformidempotency.NewPostgresRepository(pool), decisionService,
	)
	workItemHandler := workitem.NewHandler(
		authService, platformidempotency.NewPostgresRepository(pool), workitem.NewStore(pool),
	)
	reportRuntime := reportruntime.GovernedQueryExecutor{
		Dataset: reportDatasetRunner, Semantic: reportSemanticRunner,
	}
	reportDependencyValidator := reportpublication.NewPostgresDependencyValidator(pool)
	reportUpgradeService := &reportpublication.UpgradeService{
		Repository: reportRepository, Dependencies: reportDependencyValidator,
		Recompiler: reportpublication.GovernedComponentRecompiler{
			Scopes: reportruntime.NewPostgresViewerScopeResolver(pool), Compiler: reportUpgradeCompiler,
		},
		Comparator:   reportpublication.RuntimeSampleComparator{Runtime: reportSemanticRunner, Limit: 100},
		Components:   reportComponentRegistry,
		Compilations: reportpublication.NewPostgresCompilationStore(pool),
	}
	reportPublisher := &reportpublication.Publisher{
		Repository: reportRepository, Artifacts: reportArtifacts, Authorizer: reportAuthorizer,
		Dependencies:      reportDependencyValidator,
		Insights:          reportpublication.NewPostgresInsightValidator(pool),
		ArtifactURIPrefix: reportArtifacts.URI("report-v2"),
	}
	reportAssetRepository := reportasset.NewPostgresRepository(pool)
	reportAssetService := reportasset.Service{
		Repository: reportAssetRepository, Artifacts: reportArtifacts,
		Manifests: reportComponentRegistry, Dependencies: reportDependencyValidator,
	}
	reportScheduleService, err := reportschedule.NewService(
		reportschedule.NewPostgresStore(pool), reportAuthorizer,
	)
	if err != nil {
		logger.Error("initialize report schedule service", "error", err)
		os.Exit(1)
	}
	reportScheduleHandler := reportschedule.NewHandler(
		authService, platformidempotency.NewPostgresRepository(pool), reportScheduleService,
	)
	reportFollowService, err := reportfollow.NewService(
		reportfollow.NewStore(pool), reportauthorization.NewPostgresAuthorizer(pool),
	)
	if err != nil {
		logger.Error("initialize report follow service", "error", err)
		os.Exit(1)
	}
	reportFollowHandler := reportfollow.NewHandler(
		authService, platformidempotency.NewPostgresRepository(pool), reportFollowService,
	)
	reportHandler := reporthttp.NewHandler(
		authService, platformidempotency.NewPostgresRepository(pool), reportRepository, reportPublisher,
		reportruntime.Loader{Versions: reportRepository, Artifacts: reportArtifacts, Manifests: reportComponentRegistry},
		reportsharing.Service{Repository: reportsharing.NewPostgresRepository(pool),
			Authorizer: reportAuthorizer, Versions: reportRepository},
		reportpublication.NewExportJobStore(pool),
		objectStorage, cfg.MinIOUploadsBucket,
		reportInsightArtifacts,
		reportai.NewPostgresStore(pool),
		reportAssetService,
		reporthttp.AIOptions{
			PlanGenerator: reportAIGenerator, BlueprintGenerator: reportAIGenerator, EditGenerator: reportAIGenerator,
			BindingSuggester:  reportAIGenerator,
			SectionSummarizer: reportAIGenerator,
			Reviewer:          reportAIGenerator,
			Selector:          reportAIGenerator, Contexts: reportai.NewPostgresFieldCatalog(pool),
			Fields: reportai.NewPostgresFieldCatalog(pool), Components: reportComponentRegistry, Kinds: reportCardKindRegistry,
			Methods: reportInsightRegistry, Runtime: reportRuntime, FilterOptions: reportDatasetRunner, Measures: queryService,
			Narrative: reportNarrativeService, Upgrade: reportUpgradeService,
		},
	)

	backgroundTaskHandler := backgroundtask.NewHandler(
		authService, accessService,
		backgroundtask.NewService(backgroundtask.NewPostgresStore(pool)),
	)
	excelHandler := datasource.NewExcelHandler(authService, accessService, excelManager)
	assetHandler := asset.NewHandler(authService, accessService, assetRepository, dataSourceService)
	metadataAIHandler := metadataai.NewHandler(authService, accessService, metadataAIService)
	datasetAIHandler := datasetai.NewHandler(authService, accessService, datasetAIService)
	materializationControlHandler := materialization.NewControlHandler(
		authService, accessService, materialization.NewControlService(materializationStore),
	)
	api := newAPIMux(apiHandlers{
		auth:                     auth.NewHandler(authService),
		question:                 questionHandler,
		savedQuestion:            savedQuestionHandler,
		feedbackTicket:           feedbackTicketHandler,
		askDataReportAsset:       reportAssetHandler,
		report:                   reportHandler,
		reportSchedule:           reportScheduleHandler,
		reportFollow:             reportFollowHandler,
		decision:                 decisionHandler,
		workItem:                 workItemHandler,
		runtimeConfig:            runtimeConfigHandler,
		support:                  supportHandler,
		dataRequest:              dataRequestHandler,
		semanticAdmin:            semanticAdminHandler,
		permissionEvaluate:       auth.RequireAccessToken(authService, access.EvaluateHandler(accessService)),
		accessAdmin:              accessAdminHandler,
		operationalObservability: operationalObservabilityHandler,
		userLifecycle:            userLifecycleHandler,
		assetScope:               assetScopeHandler,
		backgroundTask:           backgroundTaskHandler,
		dataSourceApproval:       dataSourceApprovalHandler,
		dataSourceAI:             dataSourceAIHandler,
		dataSource:               dataSourceHandler,
		excel:                    excelHandler,
		asset:                    assetHandler,
		metadataAI:               metadataAIHandler,
		datasetAI:                datasetAIHandler,
		datasetApproval:          datasetApprovalHandler,
		materializationControl:   materializationControlHandler,
		dataset:                  datasetHandler,
	})

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
