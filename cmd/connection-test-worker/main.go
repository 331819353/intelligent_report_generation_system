package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	"intelligent-report-generation-system/internal/config"
	"intelligent-report-generation-system/internal/datasource"
	"intelligent-report-generation-system/internal/observability"
	"intelligent-report-generation-system/internal/platform/database"
)

func main() {
	cfg, err := config.LoadConnectionTestWorker()
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
		logger.Error("connect connection-test database role", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	objectStorage, err := datasource.NewMinIOStorage(
		cfg.MinIOEndpoint, cfg.MinIOAccessKey, cfg.MinIOSecretKey,
		cfg.MinIOUseSSL,
	)
	if err != nil {
		logger.Error("initialize connection-test object storage", "error", err)
		os.Exit(1)
	}
	dataSourceRepository := datasource.NewPostgresRepository(pool)
	excelManager := datasource.NewExcelManager(
		dataSourceRepository, objectStorage, cfg.MinIOUploadsBucket,
	)
	credentialManager, err := datasource.NewCredentialManager(
		cfg.DataSourceCredentialKey, datasource.EnvSecretResolver{},
	)
	if err != nil {
		logger.Error("initialize connection-test credential manager", "error", err)
		os.Exit(1)
	}
	mysqlConnector := datasource.NewPythonConnector(
		datasource.TypeMySQL, cfg.ConnectorURL, cfg.ConnectorToken,
		credentialManager,
	)
	oracleConnector := datasource.NewPythonConnector(
		datasource.TypeOracle, cfg.ConnectorURL, cfg.ConnectorToken,
		credentialManager,
	)
	jobRepository := datasource.NewPostgresConnectionTestRepository(pool)
	worker := datasource.NewConnectionTestWorker(
		jobRepository, cfg.ConnectionTestTimeout,
		mysqlConnector, oracleConnector, datasource.NewExcelConnector(excelManager),
	)

	workerID := uuid.NewString()
	logger.Info(
		"connection-test worker starting",
		"worker_id", workerID,
		"poll_interval", cfg.WorkerPollInterval.String(),
		"timeout", cfg.ConnectionTestTimeout.String(),
		"lease", cfg.ConnectionTestLease.String(),
	)
	run(ctx, logger, worker, workerID, cfg.WorkerPollInterval, cfg.ConnectionTestLease)
	logger.Info("connection-test worker stopped")
}

func run(
	ctx context.Context,
	logger *slog.Logger,
	worker *datasource.ConnectionTestWorker,
	workerID string,
	pollInterval, lease time.Duration,
) {
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
			logger.Error("list connection-test tenants", "error", err)
		} else {
			for _, tenantID := range tenantIDs {
				didProcess, processErr := worker.ProcessNext(
					ctx, tenantID, workerID, lease,
				)
				if processErr != nil &&
					!errors.Is(processErr, context.Canceled) &&
					!errors.Is(processErr, datasource.ErrConnectionTestLeaseLost) {
					logger.Error(
						"process connection-test job",
						"tenant_id", tenantID,
						"error", processErr,
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
