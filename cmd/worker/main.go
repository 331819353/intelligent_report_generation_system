package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"intelligent-report-generation-system/internal/config"
	"intelligent-report-generation-system/internal/observability"
)

// main 启动后台任务进程；当前先完成基础依赖校验并等待任务执行器接入。
func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("load configuration", "error", err)
		os.Exit(1)
	}
	logger := observability.NewLogger(cfg.LogLevel)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger.Info("worker starting", "poll_interval", cfg.WorkerPollInterval.String(), "environment", cfg.Environment)
	<-ctx.Done()
	logger.Info("worker stopped")
}
