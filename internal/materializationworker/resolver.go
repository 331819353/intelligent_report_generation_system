package materializationworker

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/materialization"
	"intelligent-report-generation-system/internal/platform/database"
	"intelligent-report-generation-system/internal/querycompiler"
)

type PostgresResolver struct {
	controlPool   *pgxpool.Pool
	warehousePool *pgxpool.Pool
}

func NewPostgresResolver(pool *pgxpool.Pool) *PostgresResolver {
	return &PostgresResolver{controlPool: pool, warehousePool: pool}
}

func NewSeparatedPostgresResolver(
	controlPool, warehousePool *pgxpool.Pool,
) *PostgresResolver {
	return &PostgresResolver{
		controlPool: controlPool, warehousePool: warehousePool,
	}
}

type upstreamMaterialization struct {
	ID               string
	DatasetID        string
	DatasetVersionID string
	Layer            string
	RelationKind     string
	PhysicalSchema   string
	PhysicalName     string
	PublishedSchema  string
	PublishedName    string
	SchemaHash       string
	SnapshotHash     string
	VersionHash      string
	VersionNo        int
	RowCount         int64
}

func (resolver *PostgresResolver) Resolve(
	ctx context.Context,
	claim materialization.Claim,
) (resolved ResolvedBuild, err error) {
	if resolver == nil {
		return ResolvedBuild{}, fmt.Errorf("materialization resolver is not configured")
	}
	if err := claim.Plan.Validate(); err != nil ||
		claim.Plan.DatasetID != claim.DatasetID ||
		claim.Plan.DatasetVersionID != claim.DatasetVersionID ||
		claim.Plan.Layer != claim.Layer {
		return ResolvedBuild{}, executionError(
			CodeTrustedPlanInvalid,
			"the registered build plan is invalid",
			err,
		)
	}
	if claim.Layer == materialization.LayerODS {
		for _, input := range claim.Inputs {
			if input.Type == materialization.InputFileVersion {
				return ResolvedBuild{}, executionError(
					CodeODSExcelUnsupported,
					"Excel ODS materialization is not supported by this worker",
					nil,
				)
			}
		}
		for _, input := range claim.Inputs {
			if input.Type == materialization.InputSourceTable {
				return ResolvedBuild{}, executionError(
					CodeODSSourceStagingNotConfigured,
					"database ODS staging is not configured for this worker",
					nil,
				)
			}
		}
		return ResolvedBuild{}, executionError(
			CodeODSUnsupported,
			"ODS materialization is not supported by this worker",
			nil,
		)
	}
	if claim.Layer != materialization.LayerDWD && claim.Layer != materialization.LayerDWS {
		return ResolvedBuild{}, executionError(
			CodeTrustedPlanInvalid,
			"the registered build layer is invalid",
			nil,
		)
	}
	if claim.Mode != materialization.RunModeFull {
		return ResolvedBuild{}, executionError(
			CodeRefreshModeUnsupported,
			"incremental and backfill materialization are not supported by this worker",
			nil,
		)
	}
	if resolver.controlPool == nil || resolver.warehousePool == nil {
		return ResolvedBuild{}, fmt.Errorf("materialization resolver is not configured")
	}
	if claim.Plan.Target.RelationKind != "TABLE" {
		return ResolvedBuild{}, executionError(
			CodePartitionedTableUnsupported,
			"partitioned materialization is not supported by this worker",
			nil,
		)
	}
	for _, node := range claim.Plan.Nodes {
		if node.Engine != materialization.EnginePostgres {
			return ResolvedBuild{}, executionError(
				CodePostgresExecutionRequired,
				"DWD and DWS builds must execute entirely in PostgreSQL",
				nil,
			)
		}
	}

	var upstreams []upstreamMaterialization
	err = database.WithTenantTx(ctx, resolver.controlPool, claim.TenantID, func(tx pgx.Tx) error {
		var dslJSON []byte
		var storedLayer string
		if err := tx.QueryRow(ctx, `SELECT version.dsl_json,version.schema_hash,
				version.version_no,version.layer
			FROM platform.dataset_versions AS version
			JOIN platform.datasets AS owner
			  ON owner.id=version.dataset_id AND owner.tenant_id=version.tenant_id
			WHERE version.id=$1 AND version.dataset_id=$2
			  AND version.status='PUBLISHED'
			  AND owner.status='PUBLISHED' AND owner.deleted_at IS NULL
			  AND owner.current_published_version_id=version.id
			FOR SHARE OF version,owner`,
			claim.DatasetVersionID, claim.DatasetID).
			Scan(&dslJSON, &resolved.SchemaHash, &resolved.VersionNo, &storedLayer); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return executionError(
					CodeTargetVersionUnavailable,
					"the target published dataset version is unavailable",
					err,
				)
			}
			return err
		}
		if storedLayer != string(claim.Layer) {
			return executionError(
				CodeTargetContractChanged,
				"the target dataset layer no longer matches the registered build",
				nil,
			)
		}
		prepared, err := dataset.Prepare(dslJSON)
		if err != nil {
			return executionError(
				CodeTargetContractChanged,
				"the target published dataset contract is invalid",
				err,
			)
		}
		if prepared.DSLHash != resolved.SchemaHash ||
			string(prepared.Document.Dataset.Layer) != string(claim.Layer) ||
			!prepared.Document.ExecutionPolicy.Materialization.Enabled {
			return executionError(
				CodeTargetContractChanged,
				"the target published dataset contract does not match its immutable metadata",
				nil,
			)
		}
		for _, node := range prepared.Document.Nodes {
			if node.Type != "DATASET" {
				return executionError(
					CodePostgresExecutionRequired,
					"DWD and DWS materialization requires governed dataset inputs",
					nil,
				)
			}
		}

		upstreams = make([]upstreamMaterialization, len(claim.Inputs))
		resolved.InputRowCount = make(map[int]int64, len(claim.Inputs))
		for index, input := range claim.Inputs {
			upstream, err := resolveFrozenInputTx(ctx, tx, input)
			if err != nil {
				return err
			}
			expectedLayer := string(materialization.LayerODS)
			if claim.Layer == materialization.LayerDWS {
				expectedLayer = string(materialization.LayerDWD)
			}
			if upstream.Layer != expectedLayer || input.Layer != expectedLayer ||
				upstream.VersionHash != upstream.SchemaHash ||
				input.SchemaHash != upstream.SchemaHash {
				return executionError(
					CodeUpstreamContractInvalid,
					"an upstream materialization does not match the required layer or schema",
					nil,
				)
			}
			if input.SnapshotHash != upstream.SnapshotHash ||
				(input.RowCount != nil && *input.RowCount != upstream.RowCount) {
				return executionError(
					CodeUpstreamSnapshotChanged,
					"an upstream materialization no longer matches the frozen input snapshot",
					nil,
				)
			}
			if resolver.controlPool == resolver.warehousePool {
				if err := assertUpstreamRelationsTx(ctx, tx, upstream); err != nil {
					return err
				}
			}
			upstreams[index] = upstream
			resolved.InputRowCount[input.Ordinal] = upstream.RowCount
		}

		resolved.Document = prepared.Document
		resolved.Tables = make(map[string]querycompiler.TableRef, len(prepared.Document.Nodes))
		usedInputs := make([]bool, len(upstreams))
		for _, node := range prepared.Document.Nodes {
			match := -1
			for index := range upstreams {
				if upstreams[index].DatasetVersionID == node.DatasetVersionID {
					if match >= 0 &&
						upstreams[match].ID != upstreams[index].ID {
						return executionError(
							CodeUpstreamContractInvalid,
							"the frozen inputs are ambiguous for a dataset node",
							nil,
						)
					}
					match = index
				}
			}
			if match < 0 {
				return executionError(
					CodeUpstreamUnavailable,
					"a dataset node has no matching frozen active materialization",
					nil,
				)
			}
			columns, columnTypes, err := loadDatasetColumnsTx(
				ctx, tx, upstreams[match].DatasetVersionID,
			)
			if err != nil {
				return err
			}
			for _, projected := range node.Projection {
				if !columns[projected] {
					return executionError(
						CodeUpstreamContractInvalid,
						"a dataset node projection is absent from its frozen upstream contract",
						nil,
					)
				}
			}
			resolved.Tables[node.ID] = querycompiler.TableRef{
				NodeID:      node.ID,
				Schema:      upstreams[match].PhysicalSchema,
				Name:        upstreams[match].PhysicalName,
				Columns:     columns,
				ColumnTypes: columnTypes,
			}
			usedInputs[match] = true
		}
		for _, used := range usedInputs {
			if !used {
				return executionError(
					CodeUpstreamContractInvalid,
					"the registered build contains an unused frozen input",
					nil,
				)
			}
		}
		return nil
	})
	if err != nil {
		return ResolvedBuild{}, err
	}
	if resolver.controlPool != resolver.warehousePool {
		warehouseTx, beginErr := resolver.warehousePool.BeginTx(
			ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly},
		)
		if beginErr != nil {
			return ResolvedBuild{}, beginErr
		}
		defer warehouseTx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
		for _, upstream := range upstreams {
			if err := assertUpstreamRelationsTx(ctx, warehouseTx, upstream); err != nil {
				return ResolvedBuild{}, err
			}
		}
		if err := warehouseTx.Commit(ctx); err != nil {
			return ResolvedBuild{}, err
		}
	}
	return resolved, nil
}

func resolveFrozenInputTx(
	ctx context.Context,
	tx pgx.Tx,
	input materialization.InputSnapshot,
) (upstreamMaterialization, error) {
	var item upstreamMaterialization
	base := `SELECT materialization.id::text,materialization.dataset_id::text,
		materialization.dataset_version_id::text,materialization.layer,
		materialization.relation_kind,materialization.physical_schema,
		materialization.physical_name,
		materialization.published_schema,materialization.published_name,
		materialization.schema_hash,materialization.snapshot_hash,
		version.schema_hash,version.version_no,materialization.row_count
	FROM platform.dataset_materializations AS materialization
	JOIN platform.dataset_versions AS version
	  ON version.id=materialization.dataset_version_id
	 AND version.dataset_id=materialization.dataset_id
	 AND version.tenant_id=materialization.tenant_id
	JOIN platform.datasets AS owner
	  ON owner.id=version.dataset_id AND owner.tenant_id=version.tenant_id
	WHERE materialization.status='ACTIVE'
	  AND version.status='PUBLISHED'
	  AND owner.status='PUBLISHED' AND owner.deleted_at IS NULL
	  AND owner.current_published_version_id=version.id`
	var row pgx.Row
	switch input.Type {
	case materialization.InputDatasetVersion:
		row = tx.QueryRow(ctx, base+`
		  AND materialization.dataset_id=$1
		  AND materialization.dataset_version_id=$2
		FOR SHARE OF materialization,version,owner`,
			input.DatasetID, input.DatasetVersionID)
	case materialization.InputMaterialization:
		row = tx.QueryRow(ctx, base+`
		  AND materialization.id=$1
		FOR SHARE OF materialization,version,owner`,
			input.MaterializationID)
	default:
		return upstreamMaterialization{}, executionError(
			CodeUpstreamContractInvalid,
			"the registered input type is not valid for a PostgreSQL layer build",
			nil,
		)
	}
	if err := row.Scan(
		&item.ID, &item.DatasetID, &item.DatasetVersionID, &item.Layer,
		&item.RelationKind, &item.PhysicalSchema, &item.PhysicalName,
		&item.PublishedSchema, &item.PublishedName, &item.SchemaHash,
		&item.SnapshotHash, &item.VersionHash, &item.VersionNo, &item.RowCount,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return upstreamMaterialization{}, executionError(
				CodeUpstreamUnavailable,
				"a frozen upstream has no active published materialization",
				err,
			)
		}
		return upstreamMaterialization{}, err
	}
	return item, nil
}

func assertUpstreamRelationsTx(
	ctx context.Context,
	tx pgx.Tx,
	upstream upstreamMaterialization,
) error {
	if upstream.PublishedSchema != "warehouse_published" ||
		!strings.HasPrefix(upstream.PublishedName, strings.ToLower(upstream.Layer)+"_") {
		return executionError(
			CodeUpstreamContractInvalid,
			"an upstream stable relation has invalid trusted metadata",
			nil,
		)
	}
	var relationKind string
	err := tx.QueryRow(ctx, `SELECT class.relkind::text
		FROM pg_class AS class
		JOIN pg_namespace AS namespace ON namespace.oid=class.relnamespace
		WHERE namespace.nspname=$1 AND class.relname=$2`,
		upstream.PublishedSchema, upstream.PublishedName).Scan(&relationKind)
	if errors.Is(err, pgx.ErrNoRows) {
		return executionError(
			CodeUpstreamUnavailable,
			"an upstream stable relation is unavailable",
			err,
		)
	}
	if err != nil {
		return err
	}
	if relationKind != "v" {
		return executionError(
			CodeUpstreamContractInvalid,
			"an upstream stable relation is not a governed view",
			nil,
		)
	}
	expectedPhysicalKind := "r"
	if upstream.RelationKind == "PARTITIONED_TABLE" {
		expectedPhysicalKind = "p"
	} else if upstream.RelationKind != "TABLE" {
		return executionError(
			CodeUpstreamContractInvalid,
			"an upstream materialization has an unsupported relation kind",
			nil,
		)
	}
	err = tx.QueryRow(ctx, `SELECT class.relkind::text
		FROM pg_class AS class
		JOIN pg_namespace AS namespace ON namespace.oid=class.relnamespace
		WHERE namespace.nspname=$1 AND class.relname=$2`,
		upstream.PhysicalSchema, upstream.PhysicalName).Scan(&relationKind)
	if errors.Is(err, pgx.ErrNoRows) {
		return executionError(
			CodeUpstreamUnavailable,
			"an upstream immutable relation is unavailable",
			err,
		)
	}
	if err != nil {
		return err
	}
	if relationKind != expectedPhysicalKind {
		return executionError(
			CodeUpstreamContractInvalid,
			"an upstream immutable relation has an invalid relation kind",
			nil,
		)
	}
	return nil
}

func loadDatasetColumnsTx(
	ctx context.Context,
	tx pgx.Tx,
	versionID string,
) (map[string]bool, map[string]string, error) {
	rows, err := tx.Query(ctx, `SELECT field_code::text,canonical_type
		FROM platform.dataset_fields
		WHERE dataset_version_id=$1
		ORDER BY ordinal_position`, versionID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	columns := map[string]bool{}
	types := map[string]string{}
	for rows.Next() {
		var code, canonicalType string
		if err := rows.Scan(&code, &canonicalType); err != nil {
			return nil, nil, err
		}
		columns[code] = true
		types[code] = canonicalType
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if len(columns) == 0 {
		return nil, nil, executionError(
			CodeUpstreamContractInvalid,
			"an upstream published dataset has no indexed fields",
			nil,
		)
	}
	return columns, types, nil
}
