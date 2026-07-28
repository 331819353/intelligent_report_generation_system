package warehouse

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"intelligent-report-generation-system/internal/datasource"
)

const (
	fileStageTenantID = "11111111-1111-4111-8111-111111111111"
	fileStageRunID    = "22222222-2222-4222-8222-222222222222"
	fileStageSourceID = "33333333-3333-4333-8333-333333333333"
	fileStageConfigID = "44444444-4444-4444-8444-444444444444"
	fileStageAssetID  = "55555555-5555-4555-8555-555555555555"
	fileStageVersion  = "66666666-6666-4666-8666-666666666666"
	fileStageSHA      = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

type fakeFileVersionReader struct {
	version datasource.FileVersion
	tables  []datasource.FileTableData
	err     error
}

func (reader fakeFileVersionReader) StreamVersionTable(
	ctx context.Context,
	tenantID string,
	versionID string,
	expectedFileAssetID string,
	expectedSHA256 string,
	_ int64,
	tableName string,
	maxRows int,
	batchSize int,
	allowTruncate bool,
	consume func(datasource.FileTableStreamBatch) error,
) (datasource.FileVersion, datasource.FileTableStreamSummary, error) {
	if reader.err != nil {
		return datasource.FileVersion{}, datasource.FileTableStreamSummary{}, reader.err
	}
	if reader.version.TenantID != tenantID ||
		reader.version.VersionID != versionID ||
		reader.version.ID != expectedFileAssetID ||
		reader.version.SHA256 != expectedSHA256 {
		return datasource.FileVersion{}, datasource.FileTableStreamSummary{},
			fmt.Errorf("%w: immutable file identity changed", ErrInvalidBuild)
	}
	for _, table := range reader.tables {
		if table.Name != tableName {
			continue
		}
		if len(table.Rows) > maxRows && !allowTruncate {
			return datasource.FileVersion{}, datasource.FileTableStreamSummary{},
				fmt.Errorf("%w: file table exceeds row limit", ErrInvalidBuild)
		}
		rows := table.Rows
		if len(rows) > maxRows {
			rows = rows[:maxRows]
		}
		if len(rows) == 0 {
			if err := consume(datasource.FileTableStreamBatch{
				Columns: table.Columns, Rows: [][]string{},
			}); err != nil {
				return datasource.FileVersion{}, datasource.FileTableStreamSummary{}, err
			}
		}
		for start := 0; start < len(rows); start += batchSize {
			if err := ctx.Err(); err != nil {
				return datasource.FileVersion{}, datasource.FileTableStreamSummary{}, err
			}
			end := start + batchSize
			if end > len(rows) {
				end = len(rows)
			}
			if err := consume(datasource.FileTableStreamBatch{
				Columns: table.Columns, Rows: rows[start:end],
			}); err != nil {
				return datasource.FileVersion{}, datasource.FileTableStreamSummary{}, err
			}
		}
		return reader.version, datasource.FileTableStreamSummary{
			RowCount: len(rows),
		}, nil
	}
	return datasource.FileVersion{}, datasource.FileTableStreamSummary{},
		fmt.Errorf("%w: worksheet was not found", ErrInvalidBuild)
}

func fileStageInput(t *testing.T) FileStageInput {
	t.Helper()
	return FileStageInput{
		TenantID: fileStageTenantID,
		RunID:    fileStageRunID,
		NodeID:   "orders",
		Source: datasource.Source{
			ID: fileStageSourceID, TenantID: fileStageTenantID,
			Type: datasource.TypeExcel, Status: datasource.StatusActive,
			PublicationStatus:  datasource.PublicationPublished,
			ConfigVersionID:    fileStageConfigID,
			PublishedVersionID: fileStageConfigID,
			FileAssetID:        fileStageAssetID,
			FileVersionID:      fileStageVersion,
		},
		FileVersionID:       fileStageVersion,
		ExpectedFileAssetID: fileStageAssetID,
		ExpectedSHA256:      fileStageSHA,
		TableName:           "Orders",
		MaxFileBytes:        1 << 20,
		MaxRows:             100,
		BatchSize:           2,
		Columns: []StageColumn{
			{Name: "id", CanonicalType: "INTEGER"},
			{Name: "amount", CanonicalType: "DECIMAL"},
			{Name: "created_at", CanonicalType: "DATETIME"},
		},
	}
}

func fileReader() fakeFileVersionReader {
	return fakeFileVersionReader{
		version: datasource.FileVersion{FileAsset: datasource.FileAsset{
			ID: fileStageAssetID, TenantID: fileStageTenantID,
			VersionID: fileStageVersion, SizeBytes: 128, SHA256: fileStageSHA,
		}},
		tables: []datasource.FileTableData{{
			Name:    "Orders",
			Columns: []string{"id", "amount", "created_at"},
			Types: map[string]string{
				"id": "INTEGER", "amount": "DECIMAL", "created_at": "DATETIME",
			},
			Rows: [][]string{
				{"1", "12.3400", "2026-07-24 10:11:12"},
				{"2", "", "2026/07/25 09:00:00"},
			},
		}},
	}
}

func TestFileStagerCopiesExactImmutableVersionInTypedBatches(t *testing.T) {
	input := fileStageInput(t)
	schema, table, err := stagingTarget(input.TenantID, input.RunID, input.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	tx := &fakeStagingTx{target: quoteIdentifier(schema) + "." + quoteIdentifier(table)}
	// fakeStagingTx compares the pgx.Identifier sanitized representation.
	tx.target = `"` + schema + `"."` + table + `"`
	result, err := newFileStager(
		fakeStagingFactory{tx: tx},
		fileReader(),
	).Stage(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if !tx.committed || result.RowCount != 2 || result.Schema != schema ||
		result.Table != table || len(tx.copiedRows) != 2 ||
		result.SourceBytes != 128 || result.StagedBytes <= 0 {
		t.Fatalf("result=%#v tx=%#v", result, tx)
	}
	if tx.copiedRows[1][1] != nil {
		t.Fatalf("blank spreadsheet cell must be staged as NULL: %#v", tx.copiedRows[1])
	}
}

func TestFileStagerEnforcesLogicalBytes(t *testing.T) {
	reader := fileReader()
	tx := fileStageTx(t)
	_, err := newFileStagerWithMaxBytes(
		fakeStagingFactory{tx: tx},
		reader,
		32,
	).Stage(context.Background(), fileStageInput(t))
	if !errors.Is(err, ErrStageBytesExceeded) || tx.committed ||
		len(tx.copiedRows) != 0 {
		t.Fatalf(
			"err=%v committed=%v rows=%d",
			err, tx.committed, len(tx.copiedRows),
		)
	}
}

func TestFileStagerBudgetsMissingTrailingCellsAsBlanks(t *testing.T) {
	reader := fileReader()
	reader.tables[0].Rows = [][]string{{"3"}}
	tx := fileStageTx(t)
	result, err := newFileStagerWithMaxBytes(
		fakeStagingFactory{tx: tx},
		reader,
		64,
	).Stage(context.Background(), fileStageInput(t))
	if err != nil {
		t.Fatal(err)
	}
	if !tx.committed || len(tx.copiedRows) != 1 ||
		tx.copiedRows[0][1] != nil || tx.copiedRows[0][2] != nil {
		t.Fatalf("result=%#v rows=%#v", result, tx.copiedRows)
	}
	const expectedBytes = int64(len(`["3","",""]`) + 1)
	if result.StagedBytes != expectedBytes {
		t.Fatalf(
			"staged bytes=%d want=%d",
			result.StagedBytes, expectedBytes,
		)
	}
}

func TestFileStagerRejectsWrongVersionChecksumAndCanonicalType(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*fakeFileVersionReader)
	}{
		{
			name: "wrong version",
			mutate: func(reader *fakeFileVersionReader) {
				reader.version.VersionID = "77777777-7777-4777-8777-777777777777"
			},
		},
		{
			name: "wrong checksum",
			mutate: func(reader *fakeFileVersionReader) {
				reader.version.SHA256 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader := fileReader()
			test.mutate(&reader)
			tx := &fakeStagingTx{}
			_, err := newFileStager(
				fakeStagingFactory{tx: tx},
				reader,
			).Stage(context.Background(), fileStageInput(t))
			if !errors.Is(err, ErrInvalidBuild) || tx.committed {
				t.Fatalf("err=%v committed=%v", err, tx.committed)
			}
		})
	}
}

func TestFileStagerRejectsRowLimitShapeAndValueDriftBeforeCommit(t *testing.T) {
	t.Run("row cap", func(t *testing.T) {
		input := fileStageInput(t)
		input.MaxRows = 1
		tx := &fakeStagingTx{}
		_, err := newFileStager(
			fakeStagingFactory{tx: tx},
			fileReader(),
		).Stage(context.Background(), input)
		if !errors.Is(err, ErrInvalidBuild) || tx.committed {
			t.Fatalf("err=%v committed=%v", err, tx.committed)
		}
	})

	t.Run("row shape", func(t *testing.T) {
		reader := fileReader()
		reader.tables[0].Rows[0] = append(
			reader.tables[0].Rows[0],
			"unexpected",
		)
		tx := fileStageTx(t)
		_, err := newFileStager(
			fakeStagingFactory{tx: tx},
			reader,
		).Stage(context.Background(), fileStageInput(t))
		if err == nil || tx.committed {
			t.Fatalf("err=%v committed=%v", err, tx.committed)
		}
	})

	t.Run("typed value", func(t *testing.T) {
		reader := fileReader()
		reader.tables[0].Rows[0][0] = "1.5"
		tx := fileStageTx(t)
		_, err := newFileStager(
			fakeStagingFactory{tx: tx},
			reader,
		).Stage(context.Background(), fileStageInput(t))
		if err == nil || tx.committed {
			t.Fatalf("err=%v committed=%v", err, tx.committed)
		}
	})
}

func TestFileStagerRollsBackWhenBuildLeaseCancelsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tx := fileStageTx(t)
	_, err := newFileStager(
		fakeStagingFactory{tx: tx},
		fileReader(),
	).Stage(ctx, fileStageInput(t))
	if !errors.Is(err, context.Canceled) || tx.committed {
		t.Fatalf("err=%v committed=%v", err, tx.committed)
	}
}

func TestNormalizeSpreadsheetDecimalPreservesFormattedValuesWithoutFloatRoundTrip(t *testing.T) {
	tests := map[string]string{
		"$1,234.5600":        "1234.5600",
		"(￥9，876.5)":         "-9876.5",
		"12.5%":              "0.125",
		"9007199254740993.1": "9007199254740993.1",
	}
	for input, want := range tests {
		got, err := normalizeSpreadsheetDecimal(input)
		if err != nil || got != want {
			t.Fatalf("input=%q got=%q want=%q err=%v", input, got, want, err)
		}
	}
	if _, err := normalizeSpreadsheetDecimal("not-a-number"); err == nil {
		t.Fatal("invalid decimal must fail")
	}
}

func fileStageTx(t *testing.T) *fakeStagingTx {
	t.Helper()
	input := fileStageInput(t)
	schema, table, err := stagingTarget(input.TenantID, input.RunID, input.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	return &fakeStagingTx{target: `"` + schema + `"."` + table + `"`}
}
