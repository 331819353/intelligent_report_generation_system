package main

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"

	"intelligent-report-generation-system/internal/platform/database"
	reporttemplate "intelligent-report-generation-system/internal/report/template"
)

// main hydrates the immutable bundled Report V2 component manifests without
// starting the API. This keeps migration verification and deployment startup
// checks aligned with the API startup seed path.
func main() {
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if databaseURL == "" {
		slog.Error("seed bundled report component manifests", "error", "DATABASE_URL is required")
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := database.Open(ctx, databaseURL)
	if err != nil {
		fatal("connect database", err)
	}
	defer pool.Close()
	if err := reporttemplate.SeedBundledComponents(ctx, pool); err != nil {
		fatal("seed bundled report component manifests", err)
	}
	slog.Info("seeded bundled report component manifests", "count", reporttemplate.BundledManifestCount)
}

func fatal(message string, err error) {
	slog.Error(message, "error", err)
	os.Exit(1)
}
