package compiler

import (
	"context"
	"os"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/askdata/ir"
	"intelligent-report-generation-system/internal/platform/database"
)

func TestPostgresTopNTiesAndRemainderMatchPolicies(t *testing.T) {
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
	for _, statement := range []string{
		`CREATE TEMP TABLE rank_input(region text,sales numeric,inventory numeric,margin numeric) ON COMMIT DROP`,
		`INSERT INTO rank_input VALUES('a',100,10,.5),('b',90,20,.4),('c',90,30,.3),('d',30,15,.2),('e',10,10,.3)`,
		`CREATE TEMP TABLE recomputed_remainder(inventory numeric,margin numeric) ON COMMIT DROP`,
		`INSERT INTO recomputed_remainder VALUES(25,.25)`,
	} {
		if _, err := tx.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}

	query := remainderQueryFixture()
	includeLimit := remainderLimitFixture(t, query)
	var included, cut bool
	var rows int
	if err := tx.QueryRow(ctx, includeLimit.MetadataSQL).Scan(&included, &cut, &rows); err != nil {
		t.Fatal(err)
	}
	if !included || cut || rows != 3 {
		t.Fatalf("INCLUDE_ALL metadata = included:%v cut:%v rows:%d", included, cut, rows)
	}
	other, err := CompileOther(OtherCompileRequest{
		Query: query, Limit: includeLimit, GroupColumns: []string{"region"},
		Metrics: remainderMetricsFixture(), RecomputedRemainderRelation: "recomputed_remainder",
	})
	if err != nil {
		t.Fatal(err)
	}
	var otherRows, remainderMembers int
	var remainderSales, remainderInventory, remainderMargin float64
	if err := tx.QueryRow(ctx, `SELECT count(*)::integer,
		max(sales) FILTER (WHERE is_remainder)::float8,
		max(inventory) FILTER (WHERE is_remainder)::float8,
		max(margin) FILTER (WHERE is_remainder)::float8,
		max(remainder_member_count) FILTER (WHERE is_remainder)::integer
		FROM (`+other.SQL+`) AS result`).Scan(
		&otherRows, &remainderSales, &remainderInventory, &remainderMargin, &remainderMembers,
	); err != nil {
		t.Fatal(err)
	}
	if otherRows != 4 || remainderSales != 40 || remainderInventory != 25 ||
		remainderMargin != .25 || remainderMembers != 2 {
		t.Fatalf("Other = rows:%d sales:%v inventory:%v margin:%v members:%d",
			otherRows, remainderSales, remainderInventory, remainderMargin, remainderMembers)
	}

	query.TieBreaking = ir.TieDeterministicCut
	cutLimit := remainderLimitFixture(t, query)
	if err := tx.QueryRow(ctx, cutLimit.MetadataSQL).Scan(&included, &cut, &rows); err != nil {
		t.Fatal(err)
	}
	if included || !cut || rows != 2 {
		t.Fatalf("DETERMINISTIC_CUT metadata = included:%v cut:%v rows:%d", included, cut, rows)
	}
}
