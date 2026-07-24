package warehouse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/datasource"
)

const MaxODSRows = 5_000_000

var spreadsheetDecimalPattern = regexp.MustCompile(
	`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`,
)

// FileVersionReader exposes the immutable Excel/CSV read path. Implementations
// must verify the object size and checksum before returning any rows.
type FileVersionReader interface {
	ReadVersionTablesWithExpansionLimit(
		context.Context,
		string,
		string,
		int64,
		int64,
	) (datasource.FileVersion, []datasource.FileTableData, error)
}

type FileStageInput struct {
	TenantID            string
	RunID               string
	NodeID              string
	Source              datasource.Source
	FileVersionID       string
	ExpectedFileAssetID string
	ExpectedSHA256      string
	TableName           string
	MaxFileBytes        int64
	MaxRows             int
	BatchSize           int
	Columns             []StageColumn
}

// FileStager copies one table from an exact immutable file version into a
// run-scoped PostgreSQL staging table. The table is committed only after every
// row has passed the trusted schema, type and row-limit checks.
type FileStager struct {
	transactions stagingTransactionFactory
	reader       FileVersionReader
	maxBytes     int64
}

func NewFileStager(pool *pgxpool.Pool, reader FileVersionReader) *FileStager {
	return NewFileStagerWithMaxBytes(pool, reader, DefaultStageMaxBytes)
}

// NewFileStagerWithMaxBytes 对文件 ODS 使用与数据库 ODS 相同的逻辑 JSON
// 落库预算；解析器也会收到该上限，避免压缩文件在 COPY 前无界展开。
func NewFileStagerWithMaxBytes(
	pool *pgxpool.Pool,
	reader FileVersionReader,
	maxBytes int64,
) *FileStager {
	return &FileStager{
		transactions: pgxStagingFactory{pool: pool},
		reader:       reader,
		maxBytes:     maxBytes,
	}
}

// newFileStager keeps the transaction seam private to warehouse tests.
func newFileStager(
	transactions stagingTransactionFactory,
	reader FileVersionReader,
) *FileStager {
	return newFileStagerWithMaxBytes(
		transactions, reader, DefaultStageMaxBytes,
	)
}

func newFileStagerWithMaxBytes(
	transactions stagingTransactionFactory,
	reader FileVersionReader,
	maxBytes int64,
) *FileStager {
	return &FileStager{
		transactions: transactions, reader: reader, maxBytes: maxBytes,
	}
}

func (stager *FileStager) Stage(
	ctx context.Context,
	input FileStageInput,
) (StageResult, error) {
	if stager == nil || stager.transactions == nil || stager.reader == nil {
		return StageResult{}, fmt.Errorf("%w: file stager is not configured", ErrInvalidBuild)
	}
	if stager.maxBytes < 1 {
		return StageResult{}, fmt.Errorf(
			"%w: file staging byte limit is invalid", ErrInvalidBuild,
		)
	}
	if input.Source.Type != datasource.TypeExcel ||
		input.Source.Status != datasource.StatusActive ||
		input.Source.PublicationStatus != datasource.PublicationPublished ||
		input.Source.TenantID != input.TenantID ||
		input.Source.ConfigVersionID == "" ||
		input.Source.ConfigVersionID != input.Source.PublishedVersionID ||
		input.Source.FileAssetID == "" ||
		input.Source.FileVersionID == "" ||
		input.Source.FileAssetID != input.ExpectedFileAssetID ||
		input.Source.FileVersionID != input.FileVersionID {
		return StageResult{}, fmt.Errorf(
			"%w: source is not an exact active published file version",
			ErrInvalidBuild,
		)
	}
	if len(input.ExpectedSHA256) != 64 ||
		input.MaxFileBytes <= 0 ||
		input.MaxRows < 1 || input.MaxRows > MaxODSRows ||
		input.BatchSize < 1 || input.BatchSize > 5000 ||
		strings.TrimSpace(input.TableName) == "" {
		return StageResult{}, fmt.Errorf("%w: file staging limits are invalid", ErrInvalidBuild)
	}
	columnNames, definitions, canonicalTypes, err := validateStageColumns(input.Columns)
	if err != nil {
		return StageResult{}, err
	}
	schema, table, err := stagingTarget(input.TenantID, input.RunID, input.NodeID)
	if err != nil {
		return StageResult{}, err
	}

	started := time.Now()
	version, tables, err := stager.reader.ReadVersionTablesWithExpansionLimit(
		ctx,
		input.TenantID,
		input.FileVersionID,
		input.MaxFileBytes,
		stager.maxBytes,
	)
	if err != nil {
		return StageResult{}, fmt.Errorf("read immutable file version: %w", err)
	}
	if version.TenantID != input.TenantID ||
		version.ID != input.ExpectedFileAssetID ||
		version.VersionID != input.FileVersionID ||
		version.SHA256 != input.ExpectedSHA256 ||
		version.SizeBytes <= 0 ||
		version.SizeBytes > input.MaxFileBytes {
		return StageResult{}, fmt.Errorf(
			"%w: immutable file identity or checksum changed",
			ErrInvalidBuild,
		)
	}
	selected, err := selectFileTable(tables, input.TableName)
	if err != nil {
		return StageResult{}, err
	}
	indexes, err := validateFileProjection(
		selected,
		columnNames,
		canonicalTypes,
	)
	if err != nil {
		return StageResult{}, err
	}
	if len(selected.Rows) > input.MaxRows {
		return StageResult{}, fmt.Errorf(
			"%w: file table exceeds the trusted row limit",
			ErrInvalidBuild,
		)
	}

	tx, err := stager.transactions.Begin(ctx)
	if err != nil {
		return StageResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qualified := quoteIdentifier(schema) + "." + quoteIdentifier(table)
	if _, err := tx.Exec(ctx, "DROP TABLE IF EXISTS "+qualified); err != nil {
		return StageResult{}, fmt.Errorf("replace file staging table: %w", err)
	}
	if _, err := tx.Exec(
		ctx,
		"CREATE UNLOGGED TABLE "+qualified+" ("+strings.Join(definitions, ", ")+")",
	); err != nil {
		return StageResult{}, fmt.Errorf("create file staging table: %w", err)
	}

	var copied, stagedBytes int64
	for start := 0; start < len(selected.Rows); start += input.BatchSize {
		if err := ctx.Err(); err != nil {
			return StageResult{}, err
		}
		end := start + input.BatchSize
		if end > len(selected.Rows) {
			end = len(selected.Rows)
		}
		rows := make([][]any, end-start)
		for offset, raw := range selected.Rows[start:end] {
			if err := ctx.Err(); err != nil {
				return StageResult{}, err
			}
			normalized, err := normalizeFileRow(
				raw,
				indexes,
				canonicalTypes,
				len(selected.Columns),
			)
			if err != nil {
				return StageResult{}, fmt.Errorf(
					"normalize worksheet row %d: %w",
					start+offset+1,
					err,
				)
			}
			projected := make([]string, len(indexes))
			for index, sourceIndex := range indexes {
				if sourceIndex < len(raw) {
					projected[index] = raw[sourceIndex]
				}
			}
			encodedRow, err := json.Marshal(projected)
			if err != nil {
				return StageResult{}, errors.New(
					"file staging row is not serializable",
				)
			}
			rowBytes := int64(len(encodedRow) + 1)
			if rowBytes > stager.maxBytes-stagedBytes {
				return StageResult{}, ErrStageBytesExceeded
			}
			stagedBytes += rowBytes
			rows[offset] = normalized
		}
		count, err := tx.CopyFrom(
			ctx,
			pgx.Identifier{schema, table},
			columnNames,
			pgx.CopyFromRows(rows),
		)
		if err != nil {
			return StageResult{}, fmt.Errorf("copy file staging batch: %w", err)
		}
		if count != int64(len(rows)) {
			return StageResult{}, errors.New("file staging copy count is inconsistent")
		}
		copied += count
	}
	if copied != int64(len(selected.Rows)) {
		return StageResult{}, errors.New("file staging row count is inconsistent")
	}
	if _, err := tx.Exec(ctx, "ANALYZE "+qualified); err != nil {
		return StageResult{}, fmt.Errorf("analyze file staging table: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return StageResult{}, err
	}
	return StageResult{
		Schema: schema, Table: table, QualifiedName: schema + "." + table,
		RowCount: copied, SourceDurationMS: time.Since(started).Milliseconds(),
		SourceBytes: version.SizeBytes, StagedBytes: stagedBytes,
	}, nil
}

func selectFileTable(
	tables []datasource.FileTableData,
	name string,
) (datasource.FileTableData, error) {
	var selected datasource.FileTableData
	found := false
	for _, table := range tables {
		if table.Name != name {
			continue
		}
		if found {
			return datasource.FileTableData{}, fmt.Errorf(
				"%w: immutable file contains duplicate table names",
				ErrInvalidBuild,
			)
		}
		selected = table
		found = true
	}
	if !found {
		return datasource.FileTableData{}, fmt.Errorf(
			"%w: published worksheet is unavailable",
			ErrInvalidBuild,
		)
	}
	return selected, nil
}

func validateFileProjection(
	table datasource.FileTableData,
	projected []string,
	canonicalTypes []string,
) ([]int, error) {
	if len(table.Columns) == 0 || len(table.Columns) > 1600 {
		return nil, fmt.Errorf("%w: worksheet schema is invalid", ErrInvalidBuild)
	}
	indexByName := make(map[string]int, len(table.Columns))
	for index, name := range table.Columns {
		if _, duplicate := indexByName[name]; duplicate || strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf(
				"%w: worksheet contains duplicate or empty columns",
				ErrInvalidBuild,
			)
		}
		indexByName[name] = index
	}
	indexes := make([]int, len(projected))
	for index, name := range projected {
		columnIndex, exists := indexByName[name]
		if !exists {
			return nil, fmt.Errorf(
				"%w: worksheet projection changed",
				ErrInvalidBuild,
			)
		}
		actualType := strings.ToUpper(strings.TrimSpace(table.Types[name]))
		if actualType != canonicalTypes[index] {
			return nil, fmt.Errorf(
				"%w: worksheet canonical type changed",
				ErrInvalidBuild,
			)
		}
		indexes[index] = columnIndex
	}
	return indexes, nil
}

func normalizeFileRow(
	raw []string,
	indexes []int,
	canonicalTypes []string,
	sourceWidth int,
) ([]any, error) {
	// Missing trailing cells are ordinary blanks; extra cells indicate a
	// worksheet shape drift that the saved parse plan did not describe.
	if sourceWidth < 1 || len(raw) > sourceWidth {
		return nil, errors.New("worksheet row shape changed")
	}
	values := make([]any, len(indexes))
	for outputIndex, sourceIndex := range indexes {
		value := ""
		if sourceIndex < len(raw) {
			value = raw[sourceIndex]
		}
		parsed, err := parseFileCell(value, canonicalTypes[outputIndex])
		if err != nil {
			return nil, fmt.Errorf("column %d: %w", sourceIndex+1, err)
		}
		normalized, err := normalizeStageValue(parsed, canonicalTypes[outputIndex])
		if err != nil {
			return nil, fmt.Errorf("column %d: %w", sourceIndex+1, err)
		}
		values[outputIndex] = normalized
	}
	return values, nil
}

func parseFileCell(value, canonicalType string) (any, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	switch canonicalType {
	case "STRING", "TEXT":
		return value, nil
	case "NUMBER", "INTEGER":
		return strconv.ParseInt(value, 10, 64)
	case "DECIMAL", "NUMERIC":
		return normalizeSpreadsheetDecimal(value)
	case "BOOLEAN":
		switch strings.ToLower(value) {
		case "true", "yes", "1":
			return true, nil
		case "false", "no", "0":
			return false, nil
		default:
			return nil, errors.New("invalid boolean")
		}
	case "DATE":
		for _, layout := range []string{"2006-01-02", "2006/01/02", "01/02/2006"} {
			if parsed, err := time.Parse(layout, value); err == nil {
				return parsed.Format("2006-01-02"), nil
			}
		}
		return nil, errors.New("invalid date")
	case "DATETIME", "TIMESTAMP":
		for _, layout := range []string{
			time.RFC3339,
			"2006-01-02 15:04:05",
			"01/02/06 15:04",
			"2006/01/02 15:04:05",
		} {
			if parsed, err := time.Parse(layout, value); err == nil {
				return parsed.Format("2006-01-02 15:04:05.999999"), nil
			}
		}
		return nil, errors.New("invalid datetime")
	case "TIME":
		for _, layout := range []string{"15:04:05.999999", "15:04:05", "15:04"} {
			if parsed, err := time.Parse(layout, value); err == nil {
				return parsed.Format("15:04:05.999999"), nil
			}
		}
		return nil, errors.New("invalid time")
	default:
		return nil, errors.New("unsupported canonical type")
	}
}

func normalizeSpreadsheetDecimal(value string) (string, error) {
	value = strings.TrimSpace(value)
	negative := strings.HasPrefix(value, "(") && strings.HasSuffix(value, ")")
	if negative {
		value = strings.TrimSpace(value[1 : len(value)-1])
	}
	percentage := strings.HasSuffix(value, "%")
	if percentage {
		value = strings.TrimSpace(strings.TrimSuffix(value, "%"))
	}
	for len(value) > 0 {
		first := []rune(value)[0]
		if !strings.ContainsRune("¥￥$€£", first) {
			break
		}
		value = strings.TrimSpace(strings.TrimPrefix(value, string(first)))
	}
	value = strings.NewReplacer(",", "", "，", "", " ", "").Replace(value)
	if negative {
		value = "-" + value
	}
	if !spreadsheetDecimalPattern.MatchString(value) {
		return "", errors.New("invalid decimal")
	}
	number, ok := new(big.Rat).SetString(value)
	if !ok {
		return "", errors.New("invalid decimal")
	}
	if percentage {
		number.Quo(number, big.NewRat(100, 1))
		// Excel itself is limited to 15 significant digits. Thirty decimal
		// places retain that precision after division while avoiding a float64
		// round trip before PostgreSQL numeric COPY.
		value = strings.TrimRight(
			strings.TrimRight(number.FloatString(30), "0"),
			".",
		)
		if value == "" || value == "-" {
			value = "0"
		}
	}
	return value, nil
}
