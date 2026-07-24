// Package warehouse executes trusted dataset materialization plans inside the
// PostgreSQL data plane. It never accepts client-authored SQL or physical names.
package warehouse

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/materialization"
	"intelligent-report-generation-system/internal/querycompiler"
)

var (
	outputIdentifier = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,62}$`)
)

var (
	ErrInvalidBuild  = errors.New("warehouse build is invalid")
	ErrQualityFailed = errors.New("warehouse quality gate failed")
)

type BuildInput struct {
	TenantID         string
	RunID            string
	DatasetID        string
	DatasetVersionID string
	Layer            string
	Document         dataset.Document
	Tables           map[string]querycompiler.TableRef
	Parameters       map[string]any
	RequireRows      bool
	BusinessKeyCode  []string
}

type BuildResult struct {
	Schema        string
	Table         string
	QualifiedName string
	RowCount      int64
	SizeBytes     int64
}

type transaction interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
	Commit(context.Context) error
	Rollback(context.Context) error
}

type transactionFactory interface {
	Begin(context.Context) (transaction, error)
}

type poolFactory struct{ pool *pgxpool.Pool }

func (value poolFactory) Begin(ctx context.Context) (transaction, error) {
	return value.pool.Begin(ctx)
}

type Executor struct{ transactions transactionFactory }

func NewExecutor(pool *pgxpool.Pool) *Executor {
	return &Executor{transactions: poolFactory{pool: pool}}
}

func newExecutor(transactions transactionFactory) *Executor {
	return &Executor{transactions: transactions}
}

// Build creates an immutable shadow table, validates its row/key contract and
// commits it as one transaction. Publication is a separate metadata-pointer
// transaction, so a failed build never replaces the active materialization.
func (executor *Executor) Build(ctx context.Context, input BuildInput) (BuildResult, error) {
	if executor == nil || executor.transactions == nil {
		return BuildResult{}, fmt.Errorf("%w: executor is not configured", ErrInvalidBuild)
	}
	if _, err := uuid.Parse(input.DatasetVersionID); err != nil {
		return BuildResult{}, fmt.Errorf("%w: dataset version identity is invalid", ErrInvalidBuild)
	}
	targetLayer := materialization.Layer(strings.ToUpper(strings.TrimSpace(input.Layer)))
	if string(input.Document.Dataset.Layer) != string(targetLayer) {
		return BuildResult{}, fmt.Errorf("%w: target layer does not match the dataset contract", ErrInvalidBuild)
	}
	schema, table, err := PhysicalTarget(input.TenantID, input.Layer, input.DatasetID, input.RunID)
	if err != nil {
		return BuildResult{}, err
	}
	keys, err := businessKeys(input.Document, input.BusinessKeyCode)
	if err != nil {
		return BuildResult{}, err
	}
	if err := validateOutputContract(input.Document, keys); err != nil {
		return BuildResult{}, err
	}
	if err := validateTenantOwnedInputs(input.TenantID, targetLayer, input.Tables); err != nil {
		return BuildResult{}, err
	}
	compiled, err := querycompiler.CompileMaterialization(querycompiler.MaterializationInput{
		Document: input.Document, Tables: input.Tables, Parameters: input.Parameters,
	})
	if err != nil {
		return BuildResult{}, fmt.Errorf("%w: %v", ErrInvalidBuild, err)
	}
	qualified := quoteIdentifier(schema) + "." + quoteIdentifier(table)
	tx, err := executor.transactions.Begin(ctx)
	if err != nil {
		return BuildResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// A lease retry reuses the run-scoped immutable name. PostgreSQL DDL is
	// transactional: replacing an unactivated shadow is safe, while any
	// published view dependency makes DROP fail closed instead of deleting
	// active data.
	if _, err := tx.Exec(ctx, "DROP TABLE IF EXISTS "+qualified); err != nil {
		return BuildResult{}, fmt.Errorf("replace warehouse shadow table: %w", err)
	}
	if _, err := tx.Exec(ctx, "CREATE TABLE "+qualified+" AS "+compiled.SQL, compiled.Args...); err != nil {
		return BuildResult{}, fmt.Errorf("create warehouse shadow table: %w", err)
	}
	var rowCount int64
	if err := tx.QueryRow(ctx, "SELECT COUNT(*)::bigint FROM "+qualified).Scan(&rowCount); err != nil {
		return BuildResult{}, fmt.Errorf("count warehouse rows: %w", err)
	}
	if input.RequireRows && rowCount == 0 {
		return BuildResult{}, fmt.Errorf("%w: materialization produced no rows", ErrQualityFailed)
	}
	if len(keys) > 0 {
		nulls, duplicates, err := inspectBusinessKey(ctx, tx, qualified, keys)
		if err != nil {
			return BuildResult{}, err
		}
		if nulls > 0 || duplicates > 0 {
			return BuildResult{}, fmt.Errorf("%w: business key has %d null rows and %d duplicate rows", ErrQualityFailed, nulls, duplicates)
		}
	}
	if _, err := tx.Exec(ctx, "ANALYZE "+qualified); err != nil {
		return BuildResult{}, fmt.Errorf("analyze warehouse table: %w", err)
	}
	var sizeBytes int64
	if err := tx.QueryRow(
		ctx,
		"SELECT pg_total_relation_size($1::regclass)::bigint",
		schema+"."+table,
	).Scan(&sizeBytes); err != nil {
		return BuildResult{}, fmt.Errorf("measure warehouse table: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return BuildResult{}, err
	}
	return BuildResult{
		Schema: schema, Table: table, QualifiedName: schema + "." + table,
		RowCount: rowCount, SizeBytes: sizeBytes,
	}, nil
}

func PhysicalTarget(tenantID, layer, datasetID, runID string) (string, string, error) {
	identifier, err := materialization.GeneratePhysicalIdentifier(
		tenantID,
		datasetID,
		runID,
		materialization.Layer(strings.ToUpper(strings.TrimSpace(layer))),
	)
	if err != nil {
		return "", "", fmt.Errorf("%w: layer, tenant, dataset or run identity is invalid", ErrInvalidBuild)
	}
	return identifier.Schema, identifier.Name, nil
}

func businessKeys(document dataset.Document, requested []string) ([]string, error) {
	declared := document.OutputGrain.KeyFields
	if len(requested) == 0 {
		return append([]string(nil), declared...), nil
	}
	if len(requested) != len(declared) {
		return nil, fmt.Errorf("%w: business keys do not match the declared output grain", ErrInvalidBuild)
	}
	for index := range requested {
		if requested[index] != declared[index] {
			return nil, fmt.Errorf("%w: business keys do not match the declared output grain", ErrInvalidBuild)
		}
	}
	return append([]string(nil), requested...), nil
}

func validateTenantOwnedInputs(
	tenantID string,
	targetLayer materialization.Layer,
	tables map[string]querycompiler.TableRef,
) error {
	digest := sha256.Sum256([]byte("tenant\x00" + tenantID))
	tenantFragment := hex.EncodeToString(digest[:])[:12]
	for _, table := range tables {
		valid := false
		switch targetLayer {
		case materialization.LayerODS:
			valid = table.Schema == "warehouse_staging" &&
				strings.HasPrefix(table.Name, "stage_t"+tenantFragment+"_") &&
				warehouseStagingName.MatchString(table.Name)
		case materialization.LayerDWD:
			valid = warehouseLayerInput(table, "ods", "warehouse_ods", tenantFragment)
		case materialization.LayerDWS:
			valid = warehouseLayerInput(table, "dwd", "warehouse_dwd", tenantFragment)
		}
		if !valid {
			return fmt.Errorf("%w: input relation is outside the tenant and layer boundary", ErrInvalidBuild)
		}
	}
	return nil
}

var (
	warehousePhysicalName  = regexp.MustCompile(`^(ods|dwd|dws)_t[0-9a-f]{12}_d[0-9a-f]{12}_r[0-9a-f]{12}$`)
	warehousePublishedName = regexp.MustCompile(`^(ods|dwd|dws)_t[0-9a-f]{12}_d[0-9a-f]{12}$`)
	warehouseStagingName   = regexp.MustCompile(`^stage_t[0-9a-f]{12}_r[0-9a-f]{12}_n[0-9a-f]{12}$`)
)

func warehouseLayerInput(
	table querycompiler.TableRef,
	sourceLayer string,
	physicalSchema string,
	tenantFragment string,
) bool {
	prefix := sourceLayer + "_t" + tenantFragment + "_"
	if !strings.HasPrefix(table.Name, prefix) {
		return false
	}
	if table.Schema == physicalSchema {
		return warehousePhysicalName.MatchString(table.Name)
	}
	return table.Schema == "warehouse_published" && warehousePublishedName.MatchString(table.Name)
}

func validateOutputContract(document dataset.Document, keys []string) error {
	fields := make(map[string]bool, len(document.Fields))
	for _, field := range document.Fields {
		if !outputIdentifier.MatchString(field.Code) {
			return fmt.Errorf("%w: output field %q is not a portable PostgreSQL identifier", ErrInvalidBuild, field.Code)
		}
		fields[field.Code] = true
	}
	seen := map[string]bool{}
	for _, key := range keys {
		if !outputIdentifier.MatchString(key) || !fields[key] || seen[key] {
			return fmt.Errorf("%w: business key %q is invalid", ErrInvalidBuild, key)
		}
		seen[key] = true
	}
	return nil
}

func inspectBusinessKey(ctx context.Context, tx transaction, qualified string, keys []string) (int64, int64, error) {
	quoted := make([]string, len(keys))
	notNull := make([]string, len(keys))
	nullPredicates := make([]string, len(keys))
	for index, key := range keys {
		quoted[index] = quoteIdentifier(key)
		notNull[index] = quoted[index] + " IS NOT NULL"
		nullPredicates[index] = quoted[index] + " IS NULL"
	}
	var nulls int64
	if err := tx.QueryRow(ctx, "SELECT COUNT(*)::bigint FROM "+qualified+" WHERE "+strings.Join(nullPredicates, " OR ")).Scan(&nulls); err != nil {
		return 0, 0, fmt.Errorf("inspect warehouse null keys: %w", err)
	}
	var duplicates int64
	distinctRow := "ROW(" + strings.Join(quoted, ",") + ")"
	query := "SELECT (COUNT(*) - COUNT(DISTINCT " + distinctRow + "))::bigint FROM " + qualified +
		" WHERE " + strings.Join(notNull, " AND ")
	if err := tx.QueryRow(ctx, query).Scan(&duplicates); err != nil {
		return 0, 0, fmt.Errorf("inspect warehouse duplicate keys: %w", err)
	}
	return nulls, duplicates, nil
}

func quoteIdentifier(value string) string {
	return `"` + value + `"`
}
