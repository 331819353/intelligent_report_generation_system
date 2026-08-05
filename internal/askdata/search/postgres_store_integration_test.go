package search

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/platform/database"
)

func TestPostgresEmbeddingStoreEmptyClaimPath(t *testing.T) {
	workerURL := os.Getenv("ASKDATA_INTEGRATION_WORKER_DATABASE_URL")
	if workerURL == "" {
		t.Skip("set ASKDATA_INTEGRATION_WORKER_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, workerURL)
	if err != nil {
		t.Fatalf("open worker pool: %v", err)
	}
	defer pool.Close()
	store := NewPostgresEmbeddingStore(pool)
	tenantIDs, err := store.ListTenantIDs(ctx)
	if err != nil || len(tenantIDs) == 0 {
		t.Fatalf("ListTenantIDs() = %v, %v", tenantIDs, err)
	}
	var pending bool
	if err := database.WithTenantTx(ctx, pool, tenantIDs[0], func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1 FROM askdata.embedding_outbox
			WHERE status IN ('PENDING','RUNNING')
		)`).Scan(&pending)
	}); err != nil {
		t.Fatalf("inspect outbox: %v", err)
	}
	if pending {
		t.Skip("tenant has live embedding work; empty-path fixture would claim it")
	}
	claims, err := store.ClaimBatch(
		ctx, tenantIDs[0], "integration-worker", "Qwen3-Embedding-4B", time.Minute, 16,
	)
	if err != nil {
		t.Fatalf("ClaimBatch() error = %v", err)
	}
	if len(claims) != 0 {
		t.Fatalf("ClaimBatch() = %#v, want no work", claims)
	}
}
