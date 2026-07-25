package materialization

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCleanupDropsOnlyGeneratedPhysicalTableAndItsGovernedViews(t *testing.T) {
	databaseURL := os.Getenv("WAREHOUSE_CLEANUP_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("WAREHOUSE_CLEANUP_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	tenantID, datasetID, runID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	physical, err := GeneratePhysicalIdentifier(
		tenantID, datasetID, runID, LayerDWD,
	)
	if err != nil {
		t.Fatal(err)
	}
	retiredName := physical.PublishedName + "_r123456789abc"
	qualifiedPhysical := quoteWarehouseIdentifier(physical.Schema) + "." +
		quoteWarehouseIdentifier(physical.Name)
	if _, err := tx.Exec(ctx, "CREATE TABLE "+qualifiedPhysical+"(id bigint)"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(
		ctx,
		"CREATE VIEW "+quoteWarehouseIdentifier(physical.PublishedSchema)+"."+
			quoteWarehouseIdentifier(physical.PublishedName)+
			" AS SELECT * FROM "+qualifiedPhysical,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(
		ctx,
		"CREATE VIEW "+quoteWarehouseIdentifier(physical.Schema)+"."+
			quoteWarehouseIdentifier(retiredName)+
			" AS SELECT * FROM "+qualifiedPhysical,
	); err != nil {
		t.Fatal(err)
	}
	if err := dropCleanupTargetTx(ctx, tx, cleanupTarget{
		BuildRunID: runID, Status: "ACTIVE",
		RelationKind: "TABLE", Physical: physical,
	}); err != nil {
		t.Fatal(err)
	}
	for _, relation := range [][2]string{
		{physical.Schema, physical.Name},
		{physical.PublishedSchema, physical.PublishedName},
		{physical.Schema, retiredName},
	} {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`,
			relation[0]+"."+relation[1]).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists {
			t.Fatalf("relation still exists: %s.%s", relation[0], relation[1])
		}
	}

	activeRunID, retiredRunID := uuid.NewString(), uuid.NewString()
	activePhysical, err := GeneratePhysicalIdentifier(
		tenantID, datasetID, activeRunID, LayerDWD,
	)
	if err != nil {
		t.Fatal(err)
	}
	retiredPhysical, err := GeneratePhysicalIdentifier(
		tenantID, datasetID, retiredRunID, LayerDWD,
	)
	if err != nil {
		t.Fatal(err)
	}
	qualifiedActive := quoteWarehouseIdentifier(activePhysical.Schema) + "." +
		quoteWarehouseIdentifier(activePhysical.Name)
	if _, err := tx.Exec(ctx, "CREATE TABLE "+qualifiedActive+"(id bigint)"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(
		ctx,
		"CREATE VIEW "+quoteWarehouseIdentifier(activePhysical.PublishedSchema)+"."+
			quoteWarehouseIdentifier(activePhysical.PublishedName)+
			" AS SELECT * FROM "+qualifiedActive,
	); err != nil {
		t.Fatal(err)
	}
	if err := dropCleanupTargetTx(ctx, tx, cleanupTarget{
		BuildRunID: retiredRunID, Status: "RETIRED",
		RelationKind: "TABLE", Physical: retiredPhysical,
	}); err != nil {
		t.Fatalf("already-absent retired materialization: %v", err)
	}
	for _, relation := range [][2]string{
		{activePhysical.Schema, activePhysical.Name},
		{activePhysical.PublishedSchema, activePhysical.PublishedName},
	} {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`,
			relation[0]+"."+relation[1]).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatalf("current relation was removed with retired target: %s.%s", relation[0], relation[1])
		}
	}
}
