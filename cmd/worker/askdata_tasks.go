package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	askdatadimension "intelligent-report-generation-system/internal/askdata/dimension"
	askdatagraph "intelligent-report-generation-system/internal/askdata/graph"
	askdatareportasset "intelligent-report-generation-system/internal/askdata/reportasset"
	askdatasearch "intelligent-report-generation-system/internal/askdata/search"
	"intelligent-report-generation-system/internal/config"
	"intelligent-report-generation-system/internal/embedding"
	"intelligent-report-generation-system/internal/platform/database"
)

type workerTask string

const (
	workerTaskEmbedding workerTask = "EMBEDDING"
	workerTaskProfile   workerTask = "PROFILE"
	workerTaskProjector workerTask = "PROJECTOR"
	workerTaskEvaluator workerTask = "EVALUATOR"
)

type workerTaskSelection map[workerTask]struct{}

// parseWorkerTaskSelection keeps the legacy all-in-one worker when the value
// is empty. Any explicit value enters the independently scalable AskData lane
// and is fail-closed on unknown or duplicate task names.
func parseWorkerTaskSelection(raw string) (workerTaskSelection, bool, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, false, nil
	}
	selection := workerTaskSelection{}
	for index, value := range strings.Split(raw, ",") {
		task := workerTask(strings.ToUpper(strings.TrimSpace(value)))
		switch task {
		case workerTaskEmbedding, workerTaskProfile, workerTaskProjector, workerTaskEvaluator:
		default:
			return nil, false, fmt.Errorf("WORKER_TASK_TYPES[%d] is not a supported task", index)
		}
		if _, duplicate := selection[task]; duplicate {
			return nil, false, fmt.Errorf("WORKER_TASK_TYPES[%d] duplicates %s", index, task)
		}
		selection[task] = struct{}{}
	}
	if len(selection) == 0 {
		return nil, false, errors.New("WORKER_TASK_TYPES did not select a task")
	}
	return selection, true, nil
}

func (selection workerTaskSelection) has(task workerTask) bool {
	_, exists := selection[task]
	return exists
}

func (selection workerTaskSelection) names() []string {
	result := make([]string, 0, len(selection))
	for task := range selection {
		result = append(result, string(task))
	}
	sort.Strings(result)
	return result
}

// runSelectedAskDataTasks initializes only the resources selected by the
// process. EVALUATOR owns the continuous ANN/exact recall audit; sealed E2E
// batches remain an explicit /app/askdata-eval invocation because their
// release and warehouse pins must be supplied by the release operator.
func runSelectedAskDataTasks(
	ctx context.Context,
	logger *slog.Logger,
	cfg config.Config,
	selection workerTaskSelection,
) error {
	if ctx == nil || logger == nil || len(selection) == 0 {
		return errors.New("selected worker task runtime is invalid")
	}
	startupCtx, startupCancel := context.WithTimeout(ctx, 10*time.Second)
	pool, err := database.Open(startupCtx, cfg.DatabaseURL)
	startupCancel()
	if err != nil {
		return fmt.Errorf("connect selected-task database: %w", err)
	}
	defer pool.Close()

	workerID := uuid.NewString()
	var workers sync.WaitGroup
	start := func(run func()) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			run()
		}()
	}

	if selection.has(workerTaskEmbedding) {
		provider := embedding.NewOpenAICompatibleProvider(
			cfg.AIEmbeddingBaseURL, cfg.AIEmbeddingAPIKey, cfg.AIEmbeddingModel,
			cfg.AIEmbeddingDimensions, &http.Client{Timeout: cfg.AIEmbeddingTimeout},
		)
		worker := askdatasearch.NewEmbeddingWorker(askdatasearch.NewPostgresEmbeddingStore(pool), provider)
		start(func() {
			runAskDataEmbeddingWorker(ctx, logger, worker, workerID, cfg.WorkerPollInterval)
		})
	}

	if selection.has(workerTaskEvaluator) {
		auditor, createErr := askdatasearch.NewRecallAuditor(
			askdatasearch.NewPostgresRecallAuditStore(pool),
			askdatasearch.DefaultRecallAuditOptions(cfg.AIEmbeddingModel, cfg.AIEmbeddingDimensions),
			func(_ context.Context, result askdatasearch.RecallAuditResult) {
				logger.Warn("AskData ANN recall below threshold",
					"tenant_id", result.TenantID, "domain_id", result.DomainID,
					"doc_type", result.DocumentType, "k", result.K,
					"recall", result.Recall, "threshold", result.Threshold)
			},
		)
		if createErr != nil {
			return fmt.Errorf("initialize selected evaluator: %w", createErr)
		}
		start(func() { runAskDataSearchRecallAuditWorker(ctx, logger, auditor) })
	}

	if selection.has(workerTaskProfile) {
		warehouseCtx, warehouseCancel := context.WithTimeout(ctx, 10*time.Second)
		warehousePool, openErr := database.Open(warehouseCtx, cfg.WarehouseDatabaseURL)
		warehouseCancel()
		if openErr != nil {
			return fmt.Errorf("connect selected profile warehouse: %w", openErr)
		}
		defer warehousePool.Close()
		options := askdatadimension.DefaultWorkerOptions()
		options.Budget.MaxRows = int64(cfg.AskDataProfileScanLimit)
		worker, createErr := askdatadimension.NewWorker(
			askdatadimension.NewPostgresProfileStore(pool),
			askdatadimension.NewPostgresWarehouseScanner(warehousePool), options,
		)
		if createErr != nil {
			return fmt.Errorf("initialize selected profile worker: %w", createErr)
		}
		start(func() {
			runAskDataDimensionProfileWorker(ctx, logger, worker, workerID, cfg.WorkerPollInterval)
		})
	}

	if selection.has(workerTaskProjector) {
		graphPool, openErr := askdatagraph.OpenSessionPool(
			strings.Join(cfg.AskDataNebulaAddresses, ","), cfg.AskDataNebulaUsername,
			cfg.AskDataNebulaPassword, cfg.AskDataNebulaSpace, cfg.AskDataNebulaTLSEnabled,
		)
		if openErr != nil {
			return fmt.Errorf("connect selected projector graph: %w", openErr)
		}
		defer graphPool.Close()
		graphWriter, createErr := askdatagraph.NewNebulaProjector(graphPool)
		if createErr != nil {
			return fmt.Errorf("initialize selected semantic projector writer: %w", createErr)
		}
		projector, createErr := askdatagraph.NewProjector(
			askdatagraph.NewPostgresProjectionStore(pool), graphWriter,
		)
		if createErr != nil {
			return fmt.Errorf("initialize selected semantic projector: %w", createErr)
		}
		reportWriter, createErr := askdatareportasset.NewNebulaReportGraphWriter(graphPool)
		if createErr != nil {
			return fmt.Errorf("initialize selected report projector writer: %w", createErr)
		}
		reportWorker, createErr := askdatareportasset.NewProjectionRuntimeWorker(
			askdatareportasset.NewPostgresProjectionRuntimeStore(pool), reportWriter,
		)
		if createErr != nil {
			return fmt.Errorf("initialize selected report projector: %w", createErr)
		}
		start(func() {
			runAskDataGraphProjectionWorker(
				ctx, logger, projector, workerID, cfg.WorkerPollInterval, cfg.AskDataProjectionLease,
			)
		})
		start(func() {
			runReportAssetProjectionWorker(ctx, logger, reportWorker, cfg.WorkerPollInterval)
		})
	}

	logger.Info("selected worker tasks starting", "worker_id", workerID, "task_types", selection.names())
	<-ctx.Done()
	finished := make(chan struct{})
	go func() {
		workers.Wait()
		close(finished)
	}()
	select {
	case <-finished:
		logger.Info("selected worker tasks stopped", "worker_id", workerID, "task_types", selection.names())
		return nil
	case <-time.After(cfg.ShutdownTimeout):
		return errors.New("selected worker tasks did not stop before SHUTDOWN_TIMEOUT")
	}
}
