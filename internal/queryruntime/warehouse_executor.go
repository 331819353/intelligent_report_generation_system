package queryruntime

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/datasource"
	"intelligent-report-generation-system/internal/platform/database"
	"intelligent-report-generation-system/internal/policy"
	"intelligent-report-generation-system/internal/querycompiler"
)

// PostgresWarehouseExecutor is the API role's SELECT-only data-plane adapter.
// Its public contract contains no SQL. SQL is compiled locally from validated
// DSL after every stable-view binding has been revalidated under tenant RLS.
type PostgresWarehouseExecutor struct {
	controlPool   *pgxpool.Pool
	warehousePool *pgxpool.Pool
}

func NewPostgresWarehouseExecutor(pool *pgxpool.Pool) *PostgresWarehouseExecutor {
	return &PostgresWarehouseExecutor{controlPool: pool, warehousePool: pool}
}

func NewSeparatedPostgresWarehouseExecutor(
	controlPool, warehousePool *pgxpool.Pool,
) *PostgresWarehouseExecutor {
	return &PostgresWarehouseExecutor{
		controlPool: controlPool, warehousePool: warehousePool,
	}
}

func (executor *PostgresWarehouseExecutor) Execute(
	ctx context.Context,
	tenantID string,
	_ string,
	document dataset.Document,
	resolved ResolvedPlan,
	parameters map[string]any,
	scope policy.UserScope,
	rowPolicies []policy.RowPolicy,
	columnPolicies []policy.ColumnPolicy,
	maxRows int,
) (result datasource.QueryResult, err error) {
	if executor == nil || executor.controlPool == nil || executor.warehousePool == nil ||
		resolved.Engine != ExecutionPostgreSQL ||
		len(resolved.Materializations) == 0 ||
		len(resolved.Tables) != len(document.Nodes) {
		return datasource.QueryResult{}, dataset.ErrPreviewUnsupported
	}
	compiled, err := querycompiler.Compile(querycompiler.Input{
		Document: document, Dialect: querycompiler.PostgreSQL,
		Tables: resolved.Tables, Parameters: parameters, Scope: scope,
		RowPolicies: rowPolicies, ColumnPolicies: columnPolicies,
		MaxRows: maxRows,
	})
	if err != nil {
		return datasource.QueryResult{},
			fmt.Errorf("%w: %v", dataset.ErrPreviewInvalid, err)
	}

	if executor.controlPool == executor.warehousePool {
		err = database.WithTenantTx(ctx, executor.controlPool, tenantID, func(tx pgx.Tx) error {
			if err := revalidateMaterializationsTx(ctx, tx, resolved.Materializations); err != nil {
				return err
			}
			return executeWarehouseQueryTx(ctx, tx, compiled, maxRows, &result)
		})
		return result, err
	}

	err = database.WithTenantTx(ctx, executor.controlPool, tenantID, func(tx pgx.Tx) error {
		return revalidateMaterializationMetadataTx(ctx, tx, resolved.Materializations)
	})
	if err != nil {
		return datasource.QueryResult{}, err
	}
	warehouseTx, err := executor.warehousePool.BeginTx(
		ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly},
	)
	if err != nil {
		return datasource.QueryResult{}, err
	}
	defer warehouseTx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
	if err := validatePublishedRelationsTx(ctx, warehouseTx, resolved.Materializations); err != nil {
		return datasource.QueryResult{}, err
	}
	if err := executeWarehouseQueryTx(ctx, warehouseTx, compiled, maxRows, &result); err != nil {
		return datasource.QueryResult{}, err
	}
	if err := warehouseTx.Commit(ctx); err != nil {
		return datasource.QueryResult{}, err
	}
	return result, nil
}

func executeWarehouseQueryTx(
	ctx context.Context,
	tx pgx.Tx,
	compiled querycompiler.CompiledQuery,
	maxRows int,
	result *datasource.QueryResult,
) error {
	rows, err := tx.Query(ctx, compiled.SQL, compiled.Args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	fields := rows.FieldDescriptions()
	result.Columns = make([]string, len(fields))
	for index, field := range fields {
		result.Columns[index] = field.Name
	}
	result.Rows = make([][]any, 0, maxRows)
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return err
		}
		if len(result.Rows) >= maxRows {
			return errors.New("warehouse query exceeded its compiled row limit")
		}
		result.Rows = append(result.Rows, values)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	result.RowCount = len(result.Rows)
	return nil
}

// Context cancellation is the authoritative local cancellation mechanism for
// PostgreSQL previews. Returning false keeps the interface honest: unlike
// remote connectors this executor has no independent query registry.
func (*PostgresWarehouseExecutor) Cancel(context.Context, string) (bool, error) {
	return false, nil
}

func revalidateMaterializationsTx(
	ctx context.Context,
	tx pgx.Tx,
	bindings []ResolvedMaterialization,
) error {
	if err := revalidateMaterializationMetadataTx(ctx, tx, bindings); err != nil {
		return err
	}
	return validatePublishedRelationsTx(ctx, tx, bindings)
}

func revalidateMaterializationMetadataTx(
	ctx context.Context,
	tx pgx.Tx,
	bindings []ResolvedMaterialization,
) error {
	seenNodes := make(map[string]bool, len(bindings))
	for _, expected := range bindings {
		if expected.NodeID == "" || seenNodes[expected.NodeID] {
			return dataset.ErrPreviewUnsupported
		}
		seenNodes[expected.NodeID] = true
		var actual ResolvedMaterialization
		actual.NodeID = expected.NodeID
		err := tx.QueryRow(ctx, `SELECT materialization.id::text,
				materialization.dataset_id::text,
				materialization.dataset_version_id::text,
				materialization.layer,
				materialization.published_schema,
				materialization.published_name,
				materialization.schema_hash,
				materialization.snapshot_hash
			FROM platform.dataset_materializations AS materialization
			JOIN platform.dataset_versions AS version
			  ON version.id=materialization.dataset_version_id
			 AND version.dataset_id=materialization.dataset_id
			 AND version.tenant_id=materialization.tenant_id
			JOIN platform.datasets AS owner
			  ON owner.id=version.dataset_id
			 AND owner.tenant_id=version.tenant_id
			WHERE materialization.id=$1
			  AND materialization.status='ACTIVE'
			  AND version.status='PUBLISHED'
			  AND owner.status='PUBLISHED'
			  AND owner.current_published_version_id=version.id
			  AND owner.deleted_at IS NULL
			FOR SHARE OF materialization,version,owner`,
			expected.MaterializationID).
			Scan(
				&actual.MaterializationID, &actual.DatasetID,
				&actual.DatasetVersionID, &actual.Layer,
				&actual.PublishedSchema, &actual.PublishedName,
				&actual.SchemaHash, &actual.SnapshotHash,
			)
		if errors.Is(err, pgx.ErrNoRows) {
			return dataset.ErrVersionUnavailable
		}
		if err != nil {
			return err
		}
		if actual != expected {
			return dataset.ErrVersionUnavailable
		}
	}
	return nil
}

func validatePublishedRelationsTx(
	ctx context.Context,
	tx pgx.Tx,
	bindings []ResolvedMaterialization,
) error {
	for _, expected := range bindings {
		var relationKind string
		var canSelect bool
		err := tx.QueryRow(ctx, `SELECT class.relkind::text,
				has_table_privilege(current_user,class.oid,'SELECT')
			FROM pg_class AS class
			JOIN pg_namespace AS namespace
			  ON namespace.oid=class.relnamespace
			WHERE namespace.nspname=$1 AND class.relname=$2`,
			expected.PublishedSchema, expected.PublishedName).
			Scan(&relationKind, &canSelect)
		if errors.Is(err, pgx.ErrNoRows) {
			return dataset.ErrVersionUnavailable
		}
		if err != nil {
			return err
		}
		if relationKind != "v" || !canSelect {
			return dataset.ErrPreviewUnsupported
		}
	}
	return nil
}
