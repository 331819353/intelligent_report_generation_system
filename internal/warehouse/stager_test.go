package warehouse

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/datasource"
	"intelligent-report-generation-system/internal/querycompiler"
)

type fakeStagingTx struct {
	executed   []string
	copiedRows [][]any
	committed  bool
	target     string
}

func (tx *fakeStagingTx) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	tx.executed = append(tx.executed, sql)
	return pgconn.NewCommandTag("OK"), nil
}

func (tx *fakeStagingTx) CopyFrom(_ context.Context, table pgx.Identifier, columns []string, source pgx.CopyFromSource) (int64, error) {
	if table.Sanitize() != tx.target {
		return 0, errors.New("unexpected target")
	}
	if strings.Join(columns, ",") != "id,amount,created_at" {
		return 0, errors.New("unexpected columns")
	}
	var count int64
	for source.Next() {
		values, err := source.Values()
		if err != nil {
			return count, err
		}
		tx.copiedRows = append(tx.copiedRows, values)
		count++
	}
	return count, source.Err()
}

func (tx *fakeStagingTx) Commit(context.Context) error {
	tx.committed = true
	return nil
}

func (tx *fakeStagingTx) Rollback(context.Context) error { return nil }

type fakeStagingFactory struct{ tx *fakeStagingTx }

func (factory fakeStagingFactory) Begin(context.Context) (stagingTransaction, error) {
	return factory.tx, nil
}

type fakeStreamReader struct {
	columns []string
	rows    [][]any
	err     error
}

func (reader fakeStreamReader) StreamQuery(
	_ context.Context,
	_ datasource.Source,
	_, _ string,
	_ []any,
	_, _ int,
	consume datasource.StreamConsumer,
) (datasource.StreamSummary, error) {
	if reader.err != nil {
		return datasource.StreamSummary{}, reader.err
	}
	if len(reader.rows) > 0 {
		if err := consume(datasource.StreamBatch{Columns: reader.columns, Rows: reader.rows}); err != nil {
			return datasource.StreamSummary{}, err
		}
	}
	return datasource.StreamSummary{RowCount: len(reader.rows), DurationMS: 9}, nil
}

func stageInput(t *testing.T) StageInput {
	t.Helper()
	raw, err := os.ReadFile("../../api/examples/dataset-dsl-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := dataset.Prepare(raw)
	if err != nil {
		t.Fatal(err)
	}
	document := prepared.Document
	document.Dataset.Layer = dataset.LayerODS
	document.Nodes[0].Projection = []string{"id", "amount", "created_at"}
	document.Nodes[0].SourceFilters = nil
	document.Fields = []dataset.Field{
		{ID: "field_id", Code: "id", Name: "ID", Role: "IDENTIFIER", CanonicalType: "INTEGER", Expression: dataset.Expression{Type: "FIELD_REF", NodeID: "orders", Field: "id"}},
		{ID: "field_amount", Code: "amount", Name: "Amount", Role: "MEASURE", CanonicalType: "DECIMAL", Expression: dataset.Expression{Type: "FIELD_REF", NodeID: "orders", Field: "amount"}, Nullable: true},
		{ID: "field_created_at", Code: "created_at", Name: "Created At", Role: "TIME", CanonicalType: "DATETIME", Expression: dataset.Expression{Type: "FIELD_REF", NodeID: "orders", Field: "created_at"}},
	}
	document.Filters = nil
	document.GroupBy = nil
	document.Having = nil
	document.Sorts = nil
	document.Parameters = nil
	document.OutputGrain = dataset.OutputGrain{Description: "one row per id", KeyFields: []string{"id"}, TimeField: "created_at"}
	return StageInput{
		TenantID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
		RunID:    "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		Source: datasource.Source{
			ID: document.Nodes[0].DataSourceID, TenantID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
			Type: datasource.TypeMySQL, Status: datasource.StatusActive,
			PublicationStatus:  datasource.PublicationPublished,
			ConfigVersionID:    "dddddddd-dddd-4ddd-8ddd-dddddddddddd",
			PublishedVersionID: "dddddddd-dddd-4ddd-8ddd-dddddddddddd",
		},
		Scan: querycompiler.ScanInput{
			Document: document, NodeID: "orders", Dialect: querycompiler.MySQL,
			Table: querycompiler.TableRef{
				NodeID: "orders", Schema: "sales", Name: "orders",
				Columns: map[string]bool{"id": true, "amount": true, "created_at": true},
			},
			MaxRows: 100_000,
		},
		BatchSize: 1000,
		Columns: []StageColumn{
			{Name: "id", CanonicalType: "INTEGER"},
			{Name: "amount", CanonicalType: "DECIMAL"},
			{Name: "created_at", CanonicalType: "DATETIME"},
		},
	}
}

func TestStageStreamsTypedRowsIntoOnePostgreSQLTransaction(t *testing.T) {
	schema, table, err := stagingTarget(
		"cccccccc-cccc-4ccc-8ccc-cccccccccccc",
		"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		"orders",
	)
	if err != nil {
		t.Fatal(err)
	}
	tx := &fakeStagingTx{target: pgx.Identifier{schema, table}.Sanitize()}
	reader := fakeStreamReader{
		columns: []string{"ID", "AMOUNT", "CREATED_AT"},
		rows: [][]any{{
			json.Number("9007199254740993"),
			json.Number("1234.5600"),
			"2026-07-24 10:11:12",
		}},
	}
	result, err := newStager(fakeStagingFactory{tx: tx}, reader).Stage(context.Background(), stageInput(t))
	if err != nil {
		t.Fatal(err)
	}
	if !tx.committed || result.RowCount != 1 || result.SourceDurationMS != 9 || len(tx.copiedRows) != 1 {
		t.Fatalf("result=%#v tx=%#v", result, tx)
	}
	if tx.copiedRows[0][0] != int64(9007199254740993) {
		t.Fatalf("integer was not normalized: %#v", tx.copiedRows[0][0])
	}
	if number, ok := tx.copiedRows[0][1].(pgtype.Numeric); !ok || !number.Valid {
		t.Fatalf("decimal was not normalized: %#v", tx.copiedRows[0][1])
	}
	if timestamp, ok := tx.copiedRows[0][2].(pgtype.Timestamp); !ok || !timestamp.Valid {
		t.Fatalf("timestamp was not normalized: %#v", tx.copiedRows[0][2])
	}
	if len(tx.executed) != 3 ||
		!strings.HasPrefix(tx.executed[0], "DROP TABLE IF EXISTS") ||
		!strings.HasPrefix(tx.executed[1], "CREATE UNLOGGED TABLE") ||
		!strings.HasPrefix(tx.executed[2], "ANALYZE") {
		t.Fatalf("executed=%#v", tx.executed)
	}
}

func TestStageFailsClosedBeforeCommitOnShapeOrTypeDrift(t *testing.T) {
	tests := []fakeStreamReader{
		{columns: []string{"id", "wrong", "created_at"}, rows: [][]any{{1, 2, "2026-07-24 10:11:12"}}},
		{columns: []string{"id", "amount", "created_at"}, rows: [][]any{{json.Number("1.5"), 2, "2026-07-24 10:11:12"}}},
		{err: errors.New("source failed")},
	}
	for _, reader := range tests {
		schema, table, err := stagingTarget(
			"cccccccc-cccc-4ccc-8ccc-cccccccccccc",
			"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
			"orders",
		)
		if err != nil {
			t.Fatal(err)
		}
		tx := &fakeStagingTx{target: pgx.Identifier{schema, table}.Sanitize()}
		if _, err := newStager(fakeStagingFactory{tx: tx}, reader).Stage(context.Background(), stageInput(t)); err == nil || tx.committed {
			t.Fatalf("err=%v committed=%v", err, tx.committed)
		}
	}
}

func TestStageByteBudgetRollsBackBeforeAnyMaterializationCanActivate(t *testing.T) {
	schema, table, err := stagingTarget(
		"cccccccc-cccc-4ccc-8ccc-cccccccccccc",
		"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		"orders",
	)
	if err != nil {
		t.Fatal(err)
	}
	tx := &fakeStagingTx{target: pgx.Identifier{schema, table}.Sanitize()}
	reader := fakeStreamReader{
		columns: []string{"id", "amount", "created_at"},
		rows: [][]any{{
			json.Number("1"),
			json.Number("123456789012345678901234567890.1234"),
			"2026-07-24 10:11:12",
		}},
	}
	_, err = newStagerWithMaxBytes(
		fakeStagingFactory{tx: tx}, reader, 32,
	).Stage(context.Background(), stageInput(t))
	if !errors.Is(err, ErrStageBytesExceeded) {
		t.Fatalf("err=%v", err)
	}
	if tx.committed || len(tx.copiedRows) != 0 {
		t.Fatalf(
			"over-budget staging escaped transaction: committed=%v rows=%d",
			tx.committed, len(tx.copiedRows),
		)
	}
}

func TestStageRejectsInactiveOrUnsupportedSources(t *testing.T) {
	input := stageInput(t)
	input.Source.Status = datasource.StatusDraft
	if _, err := newStager(fakeStagingFactory{tx: &fakeStagingTx{}}, fakeStreamReader{}).Stage(context.Background(), input); !errors.Is(err, ErrInvalidBuild) {
		t.Fatalf("err=%v", err)
	}
	input = stageInput(t)
	input.Source.Type = datasource.TypeExcel
	if _, err := newStager(fakeStagingFactory{tx: &fakeStagingTx{}}, fakeStreamReader{}).Stage(context.Background(), input); !errors.Is(err, ErrInvalidBuild) {
		t.Fatalf("err=%v", err)
	}
}

func TestStageRejectsCrossTenantAndWrongSourcePlans(t *testing.T) {
	input := stageInput(t)
	input.Source.TenantID = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
	if _, err := newStager(fakeStagingFactory{tx: &fakeStagingTx{}}, fakeStreamReader{}).Stage(context.Background(), input); !errors.Is(err, ErrInvalidBuild) {
		t.Fatalf("cross-tenant err=%v", err)
	}

	input = stageInput(t)
	input.Source.ID = "ffffffff-ffff-4fff-8fff-ffffffffffff"
	if _, err := newStager(fakeStagingFactory{tx: &fakeStagingTx{}}, fakeStreamReader{}).Stage(context.Background(), input); !errors.Is(err, ErrInvalidBuild) {
		t.Fatalf("wrong-source err=%v", err)
	}
}

func TestStageRejectsUnpublishedOrUnpinnedSourceVersion(t *testing.T) {
	input := stageInput(t)
	input.Source.PublicationStatus = datasource.PublicationUnpublished
	if _, err := newStager(fakeStagingFactory{tx: &fakeStagingTx{}}, fakeStreamReader{}).Stage(context.Background(), input); !errors.Is(err, ErrInvalidBuild) {
		t.Fatalf("unpublished err=%v", err)
	}

	input = stageInput(t)
	input.Source.PublishedVersionID = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
	if _, err := newStager(fakeStagingFactory{tx: &fakeStagingTx{}}, fakeStreamReader{}).Stage(context.Background(), input); !errors.Is(err, ErrInvalidBuild) {
		t.Fatalf("unpinned err=%v", err)
	}
}

func TestValidateStageColumnsSupportsQuotedUnicodePhysicalNames(t *testing.T) {
	names, definitions, types, err := validateStageColumns([]StageColumn{
		{Name: "订单编号", CanonicalType: "INTEGER"},
		{Name: "区域", CanonicalType: "STRING"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(names, ",") != "订单编号,区域" ||
		!strings.Contains(definitions[0], `"订单编号" bigint`) ||
		strings.Join(types, ",") != "INTEGER,STRING" {
		t.Fatalf(
			"names=%#v definitions=%#v types=%#v",
			names, definitions, types,
		)
	}
}
