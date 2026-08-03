package queryruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/datasource"
	"intelligent-report-generation-system/internal/metric"
	"intelligent-report-generation-system/internal/platform/database"
	"intelligent-report-generation-system/internal/policy"
	"intelligent-report-generation-system/internal/querycompiler"
)

const (
	semanticExplainTimeout      = 5 * time.Second
	maximumSemanticExplainRows  = int64(10_000_000)
	maximumSemanticExplainCost  = float64(5_000_000)
	maximumSemanticExplainNodes = 256
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

// Preflight compiles from the trusted dataset DSL, asks PostgreSQL to parse the
// statement through EXPLAIN (never EXPLAIN ANALYZE), validates the read-only
// plan shape and enforces a bounded optimizer-cost budget. The generated SQL
// and argument values intentionally never cross this boundary.
func (executor *PostgresWarehouseExecutor) Preflight(
	ctx context.Context,
	tenantID string,
	document dataset.Document,
	resolved ResolvedPlan,
	parameters map[string]any,
	scope policy.UserScope,
	rowPolicies []policy.RowPolicy,
	columnPolicies []policy.ColumnPolicy,
	maxRows int,
) (proof metric.QueryPreflightProof, err error) {
	if executor == nil || executor.controlPool == nil || executor.warehousePool == nil ||
		resolved.Engine != ExecutionPostgreSQL || len(resolved.Materializations) == 0 ||
		len(resolved.Tables) != len(document.Nodes) {
		return metric.QueryPreflightProof{}, dataset.ErrPreviewUnsupported
	}
	compiled, err := querycompiler.Compile(querycompiler.Input{
		Document: document, Dialect: querycompiler.PostgreSQL,
		Tables: resolved.Tables, Parameters: parameters, Scope: scope,
		RowPolicies: rowPolicies, ColumnPolicies: columnPolicies,
		MaxRows: maxRows,
	})
	if err != nil {
		return metric.QueryPreflightProof{},
			fmt.Errorf("%w: %v", dataset.ErrPreviewInvalid, err)
	}
	argumentJSON, err := json.Marshal(compiled.Args)
	if err != nil {
		return metric.QueryPreflightProof{}, dataset.ErrPreviewInvalid
	}
	proof = metric.QueryPreflightProof{
		Dialect: "POSTGRESQL", QueryHash: hash([]byte(compiled.SQL)),
		ParameterHash: hash(argumentJSON), ArgumentCount: len(compiled.Args),
		MaximumRows: maxRows, ParserDecision: "POSTGRESQL_EXPLAIN_PARSED",
		MaximumEstimatedRows: maximumSemanticExplainRows,
		MaximumEstimatedCost: maximumSemanticExplainCost,
	}
	for _, item := range resolved.Materializations {
		proof.MaterializationIDs = append(proof.MaterializationIDs, item.MaterializationID)
	}
	for _, field := range document.Fields {
		proof.ReferencedFieldIDs = append(proof.ReferencedFieldIDs, field.ID)
	}
	sort.Strings(proof.MaterializationIDs)
	sort.Strings(proof.ReferencedFieldIDs)

	preflight := func(tx pgx.Tx) error {
		rows, cost, explainErr := explainWarehouseQueryTx(
			ctx, tx, compiled, resolved.Materializations,
		)
		if explainErr != nil {
			return explainErr
		}
		proof.EstimatedRows, proof.EstimatedTotalCost = rows, cost
		if rows < 0 || rows > maximumSemanticExplainRows ||
			cost < 0 || cost > maximumSemanticExplainCost {
			return fmt.Errorf("%w: semantic query EXPLAIN budget exceeded", dataset.ErrPreviewUnsupported)
		}
		proof.AllowlistDecision = "POSTGRESQL_AST_RELATION_ALLOWLIST"
		proof.ExplainDecision = "COST_WITHIN_BUDGET"
		return nil
	}
	if executor.controlPool == executor.warehousePool {
		err = database.WithTenantTx(ctx, executor.controlPool, tenantID, func(tx pgx.Tx) error {
			if err := revalidateMaterializationsTx(ctx, tx, resolved.Materializations); err != nil {
				return err
			}
			return preflight(tx)
		})
		return proof, err
	}
	if err := database.WithTenantTx(ctx, executor.controlPool, tenantID, func(tx pgx.Tx) error {
		return revalidateMaterializationMetadataTx(ctx, tx, resolved.Materializations)
	}); err != nil {
		return metric.QueryPreflightProof{}, err
	}
	warehouseTx, err := executor.warehousePool.BeginTx(
		ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly},
	)
	if err != nil {
		return metric.QueryPreflightProof{}, err
	}
	defer warehouseTx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
	if err := validatePublishedRelationsTx(ctx, warehouseTx, resolved.Materializations); err != nil {
		return metric.QueryPreflightProof{}, err
	}
	if err := preflight(warehouseTx); err != nil {
		return metric.QueryPreflightProof{}, err
	}
	if err := warehouseTx.Commit(ctx); err != nil {
		return metric.QueryPreflightProof{}, err
	}
	return proof, nil
}

func explainWarehouseQueryTx(
	ctx context.Context,
	tx pgx.Tx,
	compiled querycompiler.CompiledQuery,
	bindings []ResolvedMaterialization,
) (int64, float64, error) {
	explainContext, cancel := context.WithTimeout(ctx, semanticExplainTimeout)
	defer cancel()
	if _, err := tx.Exec(explainContext,
		"SELECT set_config('statement_timeout','5000',true)"); err != nil {
		return 0, 0, err
	}
	var raw []byte
	if err := tx.QueryRow(
		explainContext, "EXPLAIN (FORMAT JSON, VERBOSE TRUE) "+compiled.SQL, compiled.Args...,
	).Scan(&raw); err != nil {
		return 0, 0, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var envelope []map[string]any
	if err := decoder.Decode(&envelope); err != nil || len(envelope) != 1 {
		return 0, 0, dataset.ErrPreviewUnsupported
	}
	root, ok := envelope[0]["Plan"].(map[string]any)
	if !ok {
		return 0, 0, dataset.ErrPreviewUnsupported
	}
	nodes := 0
	if err := validateReadOnlyExplainNode(root, &nodes); err != nil {
		return 0, 0, err
	}
	allowedRelations, err := allowedWarehouseRelationsTx(
		explainContext, tx, bindings,
	)
	if err != nil {
		return 0, 0, err
	}
	observedRelations := map[string]bool{}
	if err := collectExplainRelations(root, observedRelations); err != nil {
		return 0, 0, err
	}
	if len(observedRelations) == 0 {
		return 0, 0, dataset.ErrPreviewUnsupported
	}
	for relation := range observedRelations {
		if !allowedRelations[relation] {
			return 0, 0, dataset.ErrPreviewUnsupported
		}
	}
	rows, rowOK := explainInt64(root["Plan Rows"])
	cost, costOK := explainFloat64(root["Total Cost"])
	if !rowOK || !costOK {
		return 0, 0, dataset.ErrPreviewUnsupported
	}
	return rows, cost, nil
}

func allowedWarehouseRelationsTx(
	ctx context.Context,
	tx pgx.Tx,
	bindings []ResolvedMaterialization,
) (map[string]bool, error) {
	result := map[string]bool{}
	for _, binding := range bindings {
		rows, err := tx.Query(ctx, `WITH RECURSIVE allowed_relation(oid) AS (
				SELECT class.oid
				FROM pg_class AS class
				JOIN pg_namespace AS namespace ON namespace.oid=class.relnamespace
				WHERE namespace.nspname=$1 AND class.relname=$2
				UNION
				SELECT dependency.refobjid
				FROM allowed_relation AS allowed
				JOIN pg_rewrite AS rewrite ON rewrite.ev_class=allowed.oid
				JOIN pg_depend AS dependency
				  ON dependency.classid='pg_rewrite'::regclass
				 AND dependency.objid=rewrite.oid
				 AND dependency.refclassid='pg_class'::regclass
				 AND dependency.deptype='n'
				JOIN pg_class AS referenced ON referenced.oid=dependency.refobjid
				WHERE referenced.relkind IN ('r','p','v','m')
			)
			SELECT namespace.nspname,class.relname
			FROM allowed_relation AS allowed
			JOIN pg_class AS class ON class.oid=allowed.oid
			JOIN pg_namespace AS namespace ON namespace.oid=class.relnamespace`,
			binding.PublishedSchema, binding.PublishedName,
		)
		if err != nil {
			return nil, err
		}
		count := 0
		for rows.Next() {
			var schema, relation string
			if err := rows.Scan(&schema, &relation); err != nil {
				rows.Close()
				return nil, err
			}
			result[schema+"\x00"+relation] = true
			count++
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
		if count == 0 {
			return nil, dataset.ErrPreviewUnsupported
		}
	}
	return result, nil
}

func collectExplainRelations(node map[string]any, result map[string]bool) error {
	relation, hasRelation := node["Relation Name"].(string)
	schema, hasSchema := node["Schema"].(string)
	if hasRelation != hasSchema || hasRelation && (relation == "" || schema == "") {
		return dataset.ErrPreviewUnsupported
	}
	if hasRelation {
		result[schema+"\x00"+relation] = true
	}
	children, _ := node["Plans"].([]any)
	for _, child := range children {
		childNode, ok := child.(map[string]any)
		if !ok {
			return dataset.ErrPreviewUnsupported
		}
		if err := collectExplainRelations(childNode, result); err != nil {
			return err
		}
	}
	return nil
}

func validateReadOnlyExplainNode(node map[string]any, nodes *int) error {
	*nodes++
	if *nodes > maximumSemanticExplainNodes {
		return dataset.ErrPreviewUnsupported
	}
	nodeType, ok := node["Node Type"].(string)
	if !ok || nodeType == "" {
		return dataset.ErrPreviewUnsupported
	}
	for _, forbidden := range []string{
		"Modify", "Insert", "Update", "Delete", "Foreign Scan",
		"Function Scan", "Custom Scan", "Table Function",
	} {
		if strings.Contains(nodeType, forbidden) {
			return dataset.ErrPreviewUnsupported
		}
	}
	children, _ := node["Plans"].([]any)
	for _, child := range children {
		childNode, ok := child.(map[string]any)
		if !ok {
			return dataset.ErrPreviewUnsupported
		}
		if err := validateReadOnlyExplainNode(childNode, nodes); err != nil {
			return err
		}
	}
	return nil
}

func explainInt64(value any) (int64, bool) {
	switch typed := value.(type) {
	case json.Number:
		result, err := typed.Int64()
		return result, err == nil
	case float64:
		return int64(typed), typed >= 0
	default:
		return 0, false
	}
}

func explainFloat64(value any) (float64, bool) {
	switch typed := value.(type) {
	case json.Number:
		result, err := typed.Float64()
		return result, err == nil
	case float64:
		return typed, typed >= 0
	default:
		return 0, false
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
