package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"intelligent-report-generation-system/internal/assetembedding"
	"intelligent-report-generation-system/internal/config"
	"intelligent-report-generation-system/internal/embedding"
	"intelligent-report-generation-system/internal/platform/database"
)

type benchmarkFile struct {
	Queries []benchmarkQuery `json:"queries"`
}

type benchmarkQuery struct {
	Query          string   `json:"query"`
	ExpectedTables []string `json:"expectedTables"`
}

type report struct {
	QueryCount      int     `json:"queryCount"`
	ExpectedCount   int     `json:"expectedCount"`
	HitCount        int     `json:"hitCount"`
	RecallAt10      float64 `json:"recallAt10"`
	ANNP95Millis    int64   `json:"annP95Milliseconds"`
	P95Milliseconds int64   `json:"p95Milliseconds"`
	Model           string  `json:"model"`
	Passed          bool    `json:"passed"`
}

func main() {
	benchmarkPath := flag.String("benchmark", "testdata/asset-retrieval-zh.json", "benchmark JSON path")
	tenantCode := flag.String("tenant", "demo", "tenant code")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		fatal(err)
	}
	content, err := os.ReadFile(*benchmarkPath)
	if err != nil {
		fatal(err)
	}
	var benchmark benchmarkFile
	if err := json.Unmarshal(content, &benchmark); err != nil {
		fatal(err)
	}
	if len(benchmark.Queries) < 50 {
		fatal(fmt.Errorf("benchmark needs at least 50 queries, got %d", len(benchmark.Queries)))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		fatal(err)
	}
	defer pool.Close()
	var tenantID string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM platform.tenants
		WHERE code=$1 AND status='ACTIVE' AND deleted_at IS NULL`, *tenantCode).Scan(&tenantID); err != nil {
		fatal(fmt.Errorf("resolve tenant %q: %w", *tenantCode, err))
	}
	tableIDs := map[string]string{}
	if err := database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT upper(table_name),id::text FROM platform.metadata_tables
			WHERE asset_status='ACTIVE' AND management_status='ENABLED'`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var name, id string
			if err := rows.Scan(&name, &id); err != nil {
				return err
			}
			tableIDs[name] = id
		}
		return rows.Err()
	}); err != nil {
		fatal(err)
	}

	provider := embedding.NewOpenAICompatibleProvider(
		cfg.AIEmbeddingBaseURL, cfg.AIEmbeddingAPIKey, cfg.AIEmbeddingModel, cfg.AIEmbeddingDimensions,
		&http.Client{Timeout: cfg.AIEmbeddingTimeout},
	)
	if !provider.Configured() {
		fatal(fmt.Errorf("embedding provider is not configured"))
	}
	store := assetembedding.NewPostgresStore(pool)
	retriever := assetembedding.NewRetriever(store, provider)
	probeVectors, err := provider.Embed(ctx, []string{benchmark.Queries[0].Query})
	if err != nil || len(probeVectors) != 1 {
		fatal(fmt.Errorf("prepare ANN probe: %w", err))
	}
	annDurations := make([]time.Duration, 100)
	for index := range annDurations {
		startedAt := time.Now()
		if _, err := store.VectorRanks(ctx, tenantID, "TABLE", nil, probeVectors[0], 10); err != nil {
			fatal(fmt.Errorf("ANN probe %d: %w", index+1, err))
		}
		annDurations[index] = time.Since(startedAt)
	}
	sort.Slice(annDurations, func(i, j int) bool { return annDurations[i] < annDurations[j] })
	durations := make([]time.Duration, 0, len(benchmark.Queries))
	hits, expected := 0, 0
	for index, item := range benchmark.Queries {
		wanted := make(map[string]bool, len(item.ExpectedTables))
		for _, tableName := range item.ExpectedTables {
			id := tableIDs[strings.ToUpper(strings.TrimSpace(tableName))]
			if id == "" {
				fatal(fmt.Errorf("query %d references unknown table %q", index+1, tableName))
			}
			wanted[id] = true
		}
		startedAt := time.Now()
		result, err := retriever.Retrieve(ctx, tenantID, item.Query, nil, 10, 160)
		durations = append(durations, time.Since(startedAt))
		if err != nil {
			fatal(fmt.Errorf("query %d retrieval failed: %w", index+1, err))
		}
		for _, id := range result.TableIDs {
			if wanted[id] {
				hits++
				delete(wanted, id)
			}
		}
		expected += len(item.ExpectedTables)
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	p95Index := (95*len(durations)+99)/100 - 1
	recall := float64(hits) / float64(expected)
	result := report{
		QueryCount: len(benchmark.Queries), ExpectedCount: expected, HitCount: hits,
		RecallAt10: recall, ANNP95Millis: annDurations[94].Milliseconds(),
		P95Milliseconds: durations[p95Index].Milliseconds(), Model: provider.Model(),
	}
	result.Passed = result.RecallAt10 >= 0.95 && result.ANNP95Millis <= 100 && result.P95Milliseconds <= 1500
	encoded, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println(string(encoded))
	if !result.Passed {
		os.Exit(1)
	}
}

func fatal(err error) {
	slog.Error("asset retrieval evaluation failed", "error", err)
	os.Exit(1)
}
