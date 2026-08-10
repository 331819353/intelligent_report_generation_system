package feedback

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresSignalSourceDataRequestClusterQuery(t *testing.T) {
	databaseURL := os.Getenv("ASKDATA_INTEGRATION_ADMIN_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set ASKDATA_INTEGRATION_ADMIN_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	source := NewPostgresSignalSource(pool)
	signals, err := source.Mine(
		ctx, uuid.NewString(), uuid.NewString(), TaskDataRequestCluster, 10,
	)
	if err != nil || len(signals) != 0 {
		t.Fatalf("signals=%#v err=%v", signals, err)
	}
}
