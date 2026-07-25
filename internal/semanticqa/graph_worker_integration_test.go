package semanticqa

import (
	"context"
	"os"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/platform/database"
)

func TestGraphWorkerProjectsOneFrozenGeneration(t *testing.T) {
	databaseURL := os.Getenv("SEMANTIC_QA_TEST_DATABASE_URL")
	tenantID := os.Getenv("SEMANTIC_QA_TEST_TENANT_ID")
	if databaseURL == "" || tenantID == "" {
		t.Skip("semantic QA integration database is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := database.Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := NewPostgresStore(pool)
	worker := NewGraphWorker(store)
	processed, err := worker.ProcessNext(
		ctx, tenantID, "semanticqa-integration", 30*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !processed {
		t.Fatal("expected a dirty graph generation to be projected")
	}
	status, err := store.GetGraphStatus(ctx, tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != "READY" ||
		status.CurrentGenerationID == "" ||
		status.AppliedEventVersion != status.RequestedEventVersion ||
		status.NodeCount == 0 {
		t.Fatalf("graph status=%#v", status)
	}
}
