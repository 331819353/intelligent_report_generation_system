package warehouse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/datasource"
	"intelligent-report-generation-system/internal/materialization"
	"intelligent-report-generation-system/internal/querycompiler"
)

var stageIdentifier = regexp.MustCompile(`^[\p{L}][\p{L}\p{N}_$#]{0,127}$`)

const DefaultStageMaxBytes int64 = 512 << 20

var ErrStageBytesExceeded = errors.New("warehouse staging byte limit exceeded")

type StageColumn struct {
	Name          string
	CanonicalType string
}

type StageInput struct {
	TenantID  string
	RunID     string
	Source    datasource.Source
	Scan      querycompiler.ScanInput
	BatchSize int
	Columns   []StageColumn
}

type StageResult struct {
	Schema           string
	Table            string
	QualifiedName    string
	RowCount         int64
	SourceDurationMS int64
	SourceBytes      int64
	StagedBytes      int64
}

type stagingTransaction interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	CopyFrom(context.Context, pgx.Identifier, []string, pgx.CopyFromSource) (int64, error)
	Commit(context.Context) error
	Rollback(context.Context) error
}

type stagingTransactionFactory interface {
	Begin(context.Context) (stagingTransaction, error)
}

type pgxStagingFactory struct{ pool *pgxpool.Pool }

func (factory pgxStagingFactory) Begin(ctx context.Context) (stagingTransaction, error) {
	return factory.pool.Begin(ctx)
}

// Stager 把一个已安全编译的远端节点扫描直接 COPY 到 PostgreSQL。它不接受
// 客户端物理表名，且完整流和 staging 写入共享一个事务成功边界。
type Stager struct {
	transactions stagingTransactionFactory
	reader       datasource.StreamQuerier
	maxBytes     int64
}

func NewStager(pool *pgxpool.Pool, reader datasource.StreamQuerier) *Stager {
	return NewStagerWithMaxBytes(pool, reader, DefaultStageMaxBytes)
}

// NewStagerWithMaxBytes 为每个租户物化任务应用独立的落库逻辑字节预算。
func NewStagerWithMaxBytes(
	pool *pgxpool.Pool,
	reader datasource.StreamQuerier,
	maxBytes int64,
) *Stager {
	return &Stager{
		transactions: pgxStagingFactory{pool: pool},
		reader:       reader,
		maxBytes:     maxBytes,
	}
}

func newStager(transactions stagingTransactionFactory, reader datasource.StreamQuerier) *Stager {
	return newStagerWithMaxBytes(
		transactions, reader, DefaultStageMaxBytes,
	)
}

func newStagerWithMaxBytes(
	transactions stagingTransactionFactory,
	reader datasource.StreamQuerier,
	maxBytes int64,
) *Stager {
	return &Stager{
		transactions: transactions,
		reader:       reader,
		maxBytes:     maxBytes,
	}
}

func (stager *Stager) Stage(ctx context.Context, input StageInput) (StageResult, error) {
	if stager == nil || stager.transactions == nil || stager.reader == nil {
		return StageResult{}, fmt.Errorf("%w: stager is not configured", ErrInvalidBuild)
	}
	if stager.maxBytes < 1 {
		return StageResult{}, fmt.Errorf("%w: staging byte limit is invalid", ErrInvalidBuild)
	}
	if input.Source.Status != datasource.StatusActive ||
		input.Source.PublicationStatus != datasource.PublicationPublished ||
		input.Source.ConfigVersionID == "" ||
		input.Source.ConfigVersionID != input.Source.PublishedVersionID ||
		(input.Source.Type != datasource.TypeMySQL && input.Source.Type != datasource.TypeOracle) {
		return StageResult{}, fmt.Errorf("%w: source is not an active published database version", ErrInvalidBuild)
	}
	if input.Source.TenantID != input.TenantID {
		return StageResult{}, fmt.Errorf("%w: source tenant does not match the build tenant", ErrInvalidBuild)
	}
	if input.BatchSize < 1 || input.BatchSize > 5000 {
		return StageResult{}, fmt.Errorf("%w: staging batch size is invalid", ErrInvalidBuild)
	}
	expectedDialect := querycompiler.MySQL
	if input.Source.Type == datasource.TypeOracle {
		expectedDialect = querycompiler.Oracle
	}
	if input.Scan.Dialect != expectedDialect {
		return StageResult{}, fmt.Errorf("%w: scan dialect does not match the source", ErrInvalidBuild)
	}
	var scanNode *dataset.Node
	for index := range input.Scan.Document.Nodes {
		if input.Scan.Document.Nodes[index].ID == input.Scan.NodeID {
			scanNode = &input.Scan.Document.Nodes[index]
			break
		}
	}
	if scanNode == nil || scanNode.DataSourceID != input.Source.ID {
		return StageResult{}, fmt.Errorf("%w: scan node does not belong to the source", ErrInvalidBuild)
	}
	compiled, err := querycompiler.CompileExtractionScan(input.Scan)
	if err != nil {
		return StageResult{}, fmt.Errorf("%w: compile source scan: %v", ErrInvalidBuild, err)
	}
	schema, table, err := stagingTarget(input.TenantID, input.RunID, input.Scan.NodeID)
	if err != nil {
		return StageResult{}, err
	}
	columnNames, definitions, canonicalTypes, err := validateStageColumns(input.Columns)
	if err != nil {
		return StageResult{}, err
	}
	if !sameColumnNames(columnNames, scanNode.Projection) {
		return StageResult{}, fmt.Errorf("%w: staging columns do not match the scan projection", ErrInvalidBuild)
	}

	tx, err := stager.transactions.Begin(ctx)
	if err != nil {
		return StageResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qualified := quoteIdentifier(schema) + "." + quoteIdentifier(table)
	// Run/node names are stable across lease retries. Transactional replacement
	// avoids a committed staging artifact making every later attempt fail; if
	// the new stream fails, rollback restores the previous artifact.
	if _, err := tx.Exec(ctx, "DROP TABLE IF EXISTS "+qualified); err != nil {
		return StageResult{}, fmt.Errorf("replace warehouse staging table: %w", err)
	}
	if _, err := tx.Exec(ctx, "CREATE UNLOGGED TABLE "+qualified+" ("+strings.Join(definitions, ", ")+")"); err != nil {
		return StageResult{}, fmt.Errorf("create warehouse staging table: %w", err)
	}

	var copied, stagedBytes int64
	summary, err := stager.reader.StreamQuery(
		ctx, input.Source, "stage:"+table, compiled.SQL, compiled.Args, input.BatchSize, compiled.MaxRows,
		func(batch datasource.StreamBatch) error {
			if !sameColumns(columnNames, batch.Columns) {
				return errors.New("stream schema does not match the trusted staging plan")
			}
			if int64(len(batch.Rows)) > int64(compiled.MaxRows)-copied {
				return errors.New("warehouse staging stream exceeded the trusted row limit")
			}
			rows := make([][]any, len(batch.Rows))
			for rowIndex, row := range batch.Rows {
				if len(row) != len(canonicalTypes) {
					return errors.New("stream row width does not match the trusted staging plan")
				}
				rows[rowIndex] = make([]any, len(row))
				for columnIndex, value := range row {
					normalized, normalizeErr := normalizeStageValue(value, canonicalTypes[columnIndex])
					if normalizeErr != nil {
						return fmt.Errorf("normalize staging column %s: %w", columnNames[columnIndex], normalizeErr)
					}
					rows[rowIndex][columnIndex] = normalized
				}
				encodedRow, encodeErr := json.Marshal(row)
				if encodeErr != nil {
					return errors.New("warehouse staging row is not serializable")
				}
				rowBytes := int64(len(encodedRow) + 1)
				if rowBytes > stager.maxBytes-stagedBytes {
					return ErrStageBytesExceeded
				}
				stagedBytes += rowBytes
			}
			count, copyErr := tx.CopyFrom(
				ctx,
				pgx.Identifier{schema, table},
				columnNames,
				pgx.CopyFromRows(rows),
			)
			if copyErr != nil {
				return fmt.Errorf("copy warehouse staging batch: %w", copyErr)
			}
			if count != int64(len(rows)) {
				return errors.New("warehouse staging copy count is inconsistent")
			}
			copied += count
			return nil
		},
	)
	if err != nil {
		return StageResult{}, err
	}
	if int64(summary.RowCount) != copied {
		return StageResult{}, errors.New("warehouse staging row count is inconsistent")
	}
	if summary.DurationMS < 0 {
		return StageResult{}, errors.New("warehouse staging source duration is invalid")
	}
	if _, err := tx.Exec(ctx, "ANALYZE "+qualified); err != nil {
		return StageResult{}, fmt.Errorf("analyze warehouse staging table: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return StageResult{}, err
	}
	return StageResult{
		Schema: schema, Table: table, QualifiedName: schema + "." + table,
		RowCount: copied, SourceDurationMS: summary.DurationMS,
		SourceBytes: summary.SourceBytes, StagedBytes: stagedBytes,
	}, nil
}

func stagingTarget(tenantID, runID, nodeID string) (string, string, error) {
	schema, table, err := materialization.GenerateStagingIdentifier(tenantID, runID, nodeID)
	if err != nil {
		return "", "", fmt.Errorf("%w: staging tenant, run or node identity is invalid", ErrInvalidBuild)
	}
	return schema, table, nil
}

func validateStageColumns(columns []StageColumn) ([]string, []string, []string, error) {
	if len(columns) == 0 {
		return nil, nil, nil, fmt.Errorf("%w: staging columns are required", ErrInvalidBuild)
	}
	if len(columns) > 1600 {
		return nil, nil, nil, fmt.Errorf("%w: PostgreSQL staging supports at most 1600 columns", ErrInvalidBuild)
	}
	names := make([]string, len(columns))
	definitions := make([]string, len(columns))
	types := make([]string, len(columns))
	seen := map[string]bool{}
	for index, column := range columns {
		key := strings.ToLower(column.Name)
		if !stageIdentifier.MatchString(column.Name) ||
			len(column.Name) > 63 ||
			seen[key] {
			return nil, nil, nil, fmt.Errorf("%w: staging column %q is invalid", ErrInvalidBuild, column.Name)
		}
		canonical := strings.ToUpper(strings.TrimSpace(column.CanonicalType))
		pgType := map[string]string{
			"STRING": "text", "TEXT": "text",
			"INTEGER": "bigint",
			"NUMBER":  "numeric", "DECIMAL": "numeric", "NUMERIC": "numeric",
			"BOOLEAN":  "boolean",
			"DATE":     "date",
			"DATETIME": "timestamp", "TIMESTAMP": "timestamp",
			"TIME": "time",
		}[canonical]
		if pgType == "" {
			return nil, nil, nil, fmt.Errorf("%w: staging type %q is unsupported", ErrInvalidBuild, canonical)
		}
		seen[key] = true
		names[index] = column.Name
		types[index] = canonical
		definitions[index] = quoteIdentifier(column.Name) + " " + pgType
	}
	return names, definitions, types, nil
}

func sameColumnNames(expected, actual []string) bool {
	if len(expected) != len(actual) {
		return false
	}
	for index := range expected {
		if expected[index] != actual[index] {
			return false
		}
	}
	return true
}

func sameColumns(expected, actual []string) bool {
	if len(expected) != len(actual) {
		return false
	}
	for index := range expected {
		if !strings.EqualFold(expected[index], actual[index]) {
			return false
		}
	}
	return true
}

func normalizeStageValue(value any, canonicalType string) (any, error) {
	if value == nil {
		return nil, nil
	}
	switch canonicalType {
	case "STRING", "TEXT":
		text, ok := value.(string)
		if !ok {
			return nil, errors.New("expected text")
		}
		return text, nil
	case "INTEGER":
		switch number := value.(type) {
		case json.Number:
			return number.Int64()
		case float64:
			if math.Trunc(number) != number || number > math.MaxInt64 || number < math.MinInt64 {
				return nil, errors.New("expected 64-bit integer")
			}
			return int64(number), nil
		case int64:
			return number, nil
		case int:
			return int64(number), nil
		case string:
			return strconv.ParseInt(number, 10, 64)
		default:
			return nil, errors.New("expected integer")
		}
	case "NUMBER", "DECIMAL", "NUMERIC":
		var number pgtype.Numeric
		text, err := numericText(value)
		if err != nil {
			return nil, err
		}
		if err := number.Scan(text); err != nil || !number.Valid || number.NaN || number.InfinityModifier != pgtype.Finite {
			return nil, errors.New("expected finite decimal")
		}
		return number, nil
	case "BOOLEAN":
		switch boolean := value.(type) {
		case bool:
			return boolean, nil
		case string:
			parsed, err := strconv.ParseBool(boolean)
			if err != nil {
				if boolean == "1" {
					return true, nil
				}
				if boolean == "0" {
					return false, nil
				}
				return nil, errors.New("expected boolean")
			}
			return parsed, nil
		case json.Number:
			if boolean.String() == "1" {
				return true, nil
			}
			if boolean.String() == "0" {
				return false, nil
			}
			return nil, errors.New("expected boolean")
		default:
			return nil, errors.New("expected boolean")
		}
	case "DATE":
		var date pgtype.Date
		if err := date.Scan(value); err != nil || !date.Valid || date.InfinityModifier != pgtype.Finite {
			return nil, errors.New("expected finite date")
		}
		return date, nil
	case "DATETIME", "TIMESTAMP":
		var timestamp pgtype.Timestamp
		if err := timestamp.Scan(value); err != nil || !timestamp.Valid || timestamp.InfinityModifier != pgtype.Finite {
			return nil, errors.New("expected finite timestamp")
		}
		return timestamp, nil
	case "TIME":
		if temporal, ok := value.(time.Time); ok {
			return temporal.Format("15:04:05.999999"), nil
		}
		var clock pgtype.Time
		if err := clock.Scan(value); err != nil || !clock.Valid {
			return nil, errors.New("expected time")
		}
		return clock, nil
	default:
		return nil, errors.New("unsupported canonical type")
	}
}

func numericText(value any) (string, error) {
	switch number := value.(type) {
	case json.Number:
		return number.String(), nil
	case float64:
		if math.IsNaN(number) || math.IsInf(number, 0) {
			return "", errors.New("expected finite decimal")
		}
		return strconv.FormatFloat(number, 'g', -1, 64), nil
	case float32:
		if math.IsNaN(float64(number)) || math.IsInf(float64(number), 0) {
			return "", errors.New("expected finite decimal")
		}
		return strconv.FormatFloat(float64(number), 'g', -1, 32), nil
	case int:
		return strconv.Itoa(number), nil
	case int64:
		return strconv.FormatInt(number, 10), nil
	case string:
		return number, nil
	default:
		return "", errors.New("expected decimal")
	}
}
