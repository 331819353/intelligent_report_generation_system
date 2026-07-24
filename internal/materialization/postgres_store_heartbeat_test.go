package materialization

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestHeartbeatRejectsInvalidLeaseBeforeDatabaseAccess(t *testing.T) {
	store := NewPostgresStore(nil)
	claim := Claim{
		Run: Run{
			ID: testRunID, TenantID: testTenantID,
			DatasetID: testDatasetID, DatasetVersionID: testDatasetVersionID,
			Status: RunRunning, PlanHash: testSchemaHash,
		},
		WorkerID: "worker-1", LeaseToken: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
	}
	if _, err := store.Heartbeat(context.Background(), claim, time.Millisecond); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("heartbeat error=%v", err)
	}
}
