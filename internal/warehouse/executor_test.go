package warehouse

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/materialization"
	"intelligent-report-generation-system/internal/querycompiler"
)

type fakeRow struct {
	value int64
	err   error
}

func (row fakeRow) Scan(destinations ...any) error {
	if row.err != nil {
		return row.err
	}
	*destinations[0].(*int64) = row.value
	return nil
}

type fakeTx struct {
	executed  []string
	rows      []fakeRow
	committed bool
}

func (tx *fakeTx) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	tx.executed = append(tx.executed, sql)
	return pgconn.NewCommandTag("OK"), nil
}

func (tx *fakeTx) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	tx.executed = append(tx.executed, sql)
	row := tx.rows[0]
	tx.rows = tx.rows[1:]
	return row
}

func (tx *fakeTx) Commit(context.Context) error {
	tx.committed = true
	return nil
}

func (tx *fakeTx) Rollback(context.Context) error { return nil }

type fakeFactory struct{ tx *fakeTx }

func (factory fakeFactory) Begin(context.Context) (transaction, error) { return factory.tx, nil }

func warehouseInput(t *testing.T) BuildInput {
	t.Helper()
	raw, err := os.ReadFile("../../api/examples/dataset-dsl-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := dataset.Prepare(raw)
	if err != nil {
		t.Fatal(err)
	}
	prepared.Document.ExecutionPolicy.Materialization.Enabled = true
	prepared.Document.ExecutionPolicy.Materialization.RefreshMode = "MANUAL"
	upstream, err := materialization.GeneratePhysicalIdentifier(
		"cccccccc-cccc-4ccc-8ccc-cccccccccccc",
		"eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee",
		"ffffffff-ffff-4fff-8fff-ffffffffffff",
		materialization.LayerDWD,
	)
	if err != nil {
		t.Fatal(err)
	}
	return BuildInput{
		TenantID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
		RunID:    "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", DatasetID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
		DatasetVersionID: "dddddddd-dddd-4ddd-8ddd-dddddddddddd",
		Layer:            "DWS", Document: prepared.Document,
		Tables: map[string]querycompiler.TableRef{"orders": {
			NodeID: "orders", Schema: upstream.Schema, Name: upstream.Name,
			Columns: map[string]bool{"order_date": true, "order_amount": true, "order_status": true},
		}},
		Parameters: map[string]any{"start_date": "2026-01-01"}, RequireRows: true,
		BusinessKeyCode: []string{"stat_month"},
	}
}

func TestPhysicalTargetIsDeterministicAndLayerScoped(t *testing.T) {
	schema, table, err := PhysicalTarget(
		"cccccccc-cccc-4ccc-8ccc-cccccccccccc",
		"DWD",
		"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
		"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
	)
	if err != nil {
		t.Fatal(err)
	}
	if schema != "warehouse_dwd" || table != "dwd_t14a4982a2b3b_d2c6b2332604d_r3c940003cd4e" {
		t.Fatalf("%s.%s", schema, table)
	}
	if _, _, err := PhysicalTarget("bad", "UNKNOWN", "bad", "bad"); !errors.Is(err, ErrInvalidBuild) {
		t.Fatalf("invalid target error=%v", err)
	}
}

func TestBuildRejectsCrossTenantOrWrongLayerInputRelation(t *testing.T) {
	input := warehouseInput(t)
	foreign, err := materialization.GeneratePhysicalIdentifier(
		"11111111-1111-4111-8111-111111111111",
		"eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee",
		"ffffffff-ffff-4fff-8fff-ffffffffffff",
		materialization.LayerDWD,
	)
	if err != nil {
		t.Fatal(err)
	}
	table := input.Tables["orders"]
	table.Schema, table.Name = foreign.Schema, foreign.Name
	input.Tables["orders"] = table
	if _, err := newExecutor(fakeFactory{tx: &fakeTx{}}).Build(context.Background(), input); !errors.Is(err, ErrInvalidBuild) {
		t.Fatalf("cross-tenant err=%v", err)
	}

	input = warehouseInput(t)
	input.Layer = "DWD"
	if _, err := newExecutor(fakeFactory{tx: &fakeTx{}}).Build(context.Background(), input); !errors.Is(err, ErrInvalidBuild) {
		t.Fatalf("wrong-layer err=%v", err)
	}
}

func TestBuildRejectsBusinessKeysThatDivergeFromDeclaredGrain(t *testing.T) {
	input := warehouseInput(t)
	input.BusinessKeyCode = []string{"revenue"}
	if _, err := newExecutor(fakeFactory{tx: &fakeTx{}}).Build(context.Background(), input); !errors.Is(err, ErrInvalidBuild) {
		t.Fatalf("err=%v", err)
	}
}

func TestBuildCreatesAndChecksShadowTableBeforeCommit(t *testing.T) {
	tx := &fakeTx{rows: []fakeRow{{value: 4}, {value: 0}, {value: 0}, {value: 8192}}}
	result, err := newExecutor(fakeFactory{tx: tx}).Build(context.Background(), warehouseInput(t))
	if err != nil {
		t.Fatal(err)
	}
	if !tx.committed || result.RowCount != 4 || result.SizeBytes != 8192 || result.Schema != "warehouse_dws" {
		t.Fatalf("result=%#v committed=%v", result, tx.committed)
	}
	joined := strings.Join(tx.executed, "\n")
	for _, expected := range []string{
		"DROP TABLE IF EXISTS", "CREATE TABLE", "COUNT(*)::bigint",
		"COUNT(DISTINCT ROW(", "ANALYZE", "pg_total_relation_size",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("execution missing %q:\n%s", expected, joined)
		}
	}
}

func TestBuildFailsClosedOnBusinessKeyViolation(t *testing.T) {
	tx := &fakeTx{rows: []fakeRow{{value: 4}, {value: 1}, {value: 0}}}
	_, err := newExecutor(fakeFactory{tx: tx}).Build(context.Background(), warehouseInput(t))
	if !errors.Is(err, ErrQualityFailed) || tx.committed {
		t.Fatalf("err=%v committed=%v", err, tx.committed)
	}
}
