package compiler

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"intelligent-report-generation-system/internal/askdata/registry"
	"intelligent-report-generation-system/internal/platform/database"
)

func TestPostgresPreaggregateAndBridgeSQLMatchHandwrittenCorrectResults(t *testing.T) {
	databaseURL := os.Getenv("ASKDATA_INTEGRATION_ADMIN_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set ASKDATA_INTEGRATION_ADMIN_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := database.Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background()) //nolint:errcheck
	statements := []string{
		`CREATE TEMP TABLE query008_left(entity_id integer,quantity integer) ON COMMIT DROP`,
		`CREATE TEMP TABLE query008_right(entity_id integer,amount integer) ON COMMIT DROP`,
		`CREATE TEMP TABLE query008_bridge(left_id integer,right_id integer) ON COMMIT DROP`,
		`INSERT INTO query008_left VALUES(1,2),(1,3),(2,5)`,
		`INSERT INTO query008_right VALUES(1,10),(1,20),(2,7)`,
		`INSERT INTO query008_bridge VALUES(1,1),(1,1),(2,2)`,
	}
	for _, statement := range statements {
		if _, err := tx.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}

	preaggregate := joinCompileFixture(
		registry.CardinalityOneToMany, registry.FanoutPreAggregateRequired, false,
	)
	preaggregate.Left.Schema, preaggregate.Left.Relation = "pg_temp", "query008_left"
	preaggregate.Right.Schema, preaggregate.Right.Relation = "pg_temp", "query008_right"
	compiled, err := CompileJoin(preaggregate)
	if err != nil {
		t.Fatal(err)
	}
	assertAggregateResult(t, ctx, tx, compiled.SQL,
		`SELECT * FROM pg_temp.query008_left AS l LEFT JOIN (
			SELECT entity_id,SUM(amount) AS amount FROM pg_temp.query008_right GROUP BY entity_id
		) AS r USING(entity_id)`, 3, 77)

	bridge := joinCompileFixture(
		registry.CardinalityManyToMany, registry.FanoutBridgeRequired, true,
	)
	bridge.Left.Schema, bridge.Left.Relation = "pg_temp", "query008_left"
	bridge.Right.Schema, bridge.Right.Relation = "pg_temp", "query008_right"
	bridge.Bridge.Source.Schema, bridge.Bridge.Source.Relation = "pg_temp", "query008_bridge"
	compiled, err = CompileJoin(bridge)
	if err != nil {
		t.Fatal(err)
	}
	manual := `WITH l AS (
		SELECT entity_id,SUM(quantity) AS quantity FROM pg_temp.query008_left GROUP BY entity_id
	),r AS (
		SELECT entity_id,SUM(amount) AS amount FROM pg_temp.query008_right GROUP BY entity_id
	),b AS (
		SELECT DISTINCT left_id,right_id FROM pg_temp.query008_bridge
	) SELECT * FROM l JOIN b ON l.entity_id=b.left_id JOIN r ON b.right_id=r.entity_id`
	assertAggregateResult(t, ctx, tx, compiled.SQL, manual, 2, 47)
}

func assertAggregateResult(
	t *testing.T,
	ctx context.Context,
	tx pgx.Tx,
	generated, manual string,
	wantRows int64,
	wantTotal int64,
) {
	t.Helper()
	query := func(sql string) (int64, int64) {
		var rows, total int64
		if err := tx.QueryRow(ctx, `SELECT count(*)::bigint,
			COALESCE(sum(COALESCE(quantity,0)+COALESCE(amount,0)),0)::bigint
			FROM (`+sql+`) AS result`).Scan(&rows, &total); err != nil {
			t.Fatal(err)
		}
		return rows, total
	}
	generatedRows, generatedTotal := query(generated)
	manualRows, manualTotal := query(manual)
	if generatedRows != manualRows || generatedTotal != manualTotal ||
		generatedRows != wantRows || generatedTotal != wantTotal {
		t.Fatalf("generated/manual = %d/%d vs %d/%d, want %d/%d\nSQL: %s",
			generatedRows, generatedTotal, manualRows, manualTotal, wantRows, wantTotal, generated)
	}
}
