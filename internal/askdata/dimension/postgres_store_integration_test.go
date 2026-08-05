package dimension

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"intelligent-report-generation-system/internal/platform/database"
)

func TestPostgresProfileStoreSynchronizesWithTypedBudgets(t *testing.T) {
	workerURL := os.Getenv("ASKDATA_INTEGRATION_WORKER_DATABASE_URL")
	if workerURL == "" {
		t.Skip("set ASKDATA_INTEGRATION_WORKER_DATABASE_URL")
	}
	ctx := context.Background()
	pool, err := database.Open(ctx, workerURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := NewPostgresProfileStore(pool)
	tenantIDs, err := store.ListTenantIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tenantIDs) == 0 {
		t.Skip("control database has no active tenant")
	}
	rollback := errors.New("rollback dimension profile synchronization fixture")
	err = database.WithTenantTx(ctx, pool, tenantIDs[0], func(tx pgx.Tx) error {
		if err := synchronizeProfileJobs(ctx, tx, tenantIDs[0], DefaultWorkerOptions()); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("synchronize profile jobs: %v", err)
	}
}

func TestPostgresWarehouseScannerUsesPublishedViewAndBounds(t *testing.T) {
	adminURL := os.Getenv("ASKDATA_INTEGRATION_WAREHOUSE_ADMIN_DATABASE_URL")
	readerURL := os.Getenv("ASKDATA_INTEGRATION_WAREHOUSE_DATABASE_URL")
	if adminURL == "" || readerURL == "" {
		t.Skip("set AskData warehouse integration database URLs")
	}
	ctx := context.Background()
	admin, err := database.Open(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	reader, err := database.Open(ctx, readerURL)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	readerConfig, err := pgx.ParseConfig(readerURL)
	if err != nil {
		t.Fatal(err)
	}
	relation := "askdata_dim_scan_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	qualified := pgx.Identifier{"warehouse_published", relation}.Sanitize()
	readerRole := pgx.Identifier{readerConfig.User}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE TABLE "+qualified+` (region text)`); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = admin.Exec(context.Background(), "DROP TABLE IF EXISTS "+qualified) }()
	if _, err := admin.Exec(ctx, "INSERT INTO "+qualified+`(region) VALUES
		('华东'),('华东'),('华南'),('UNKNOWN'),(NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "GRANT SELECT ON "+qualified+" TO "+readerRole); err != nil {
		t.Fatal(err)
	}

	claim := validScanClaim()
	claim.PublishedName = relation
	claim.ExpectedRowCount = 5
	claim.Budget.MaxRows = 5
	claim.Budget.MaxDistinctValues = 2
	result, err := NewPostgresWarehouseScanner(reader).Scan(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	if result.RowCount != 5 || result.NullCount != 1 || result.RawDistinct != 3 ||
		len(result.Members) != 2 || !result.Truncated {
		t.Fatalf("unexpected bounded result: %#v", result)
	}
	claim.ExpectedRowCount--
	if _, err := NewPostgresWarehouseScanner(reader).Scan(ctx, claim); !errors.Is(err, ErrWarehouseDrift) {
		t.Fatalf("drift error=%v", err)
	}
}
