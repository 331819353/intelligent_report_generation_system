package materializationworker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/datasource"
	"intelligent-report-generation-system/internal/materialization"
	"intelligent-report-generation-system/internal/platform/database"
	"intelligent-report-generation-system/internal/querycompiler"
	"intelligent-report-generation-system/internal/warehouse"
)

const odsStageBatchSize = 1000

type databaseStager interface {
	Stage(context.Context, warehouse.StageInput) (warehouse.StageResult, error)
}

type fileStager interface {
	Stage(context.Context, warehouse.FileStageInput) (warehouse.StageResult, error)
}

// ODSResolver reloads a published single-table ODS contract, validates its
// frozen SOURCE input and stages the exact remote/file version into PostgreSQL.
// It never accepts physical names, SQL or connection details from the claim.
type ODSResolver struct {
	pool            *pgxpool.Pool
	databaseStagers map[datasource.Type]databaseStager
	fileStager      fileStager
}

func NewODSResolver(
	pool *pgxpool.Pool,
	mysqlStager databaseStager,
	oracleStager databaseStager,
	excelStager fileStager,
) *ODSResolver {
	stagers := make(map[datasource.Type]databaseStager, 2)
	if mysqlStager != nil {
		stagers[datasource.TypeMySQL] = mysqlStager
	}
	if oracleStager != nil {
		stagers[datasource.TypeOracle] = oracleStager
	}
	return &ODSResolver{
		pool: pool, databaseStagers: stagers, fileStager: excelStager,
	}
}

// CompositeResolver keeps PostgreSQL-only DWD/DWS resolution separate from
// source extraction. Layer identity is loaded again by each concrete resolver.
type CompositeResolver struct {
	ods      Resolver
	postgres Resolver
}

func NewCompositeResolver(ods Resolver, postgres Resolver) *CompositeResolver {
	return &CompositeResolver{ods: ods, postgres: postgres}
}

func (resolver *CompositeResolver) Resolve(
	ctx context.Context,
	claim materialization.Claim,
) (ResolvedBuild, error) {
	if resolver == nil {
		return ResolvedBuild{}, errors.New("materialization resolver is not configured")
	}
	if claim.Layer == materialization.LayerODS {
		if resolver.ods == nil {
			return ResolvedBuild{}, executionError(
				CodeODSSourceStagingNotConfigured,
				"ODS source staging is not configured for this worker",
				nil,
			)
		}
		return resolver.ods.Resolve(ctx, claim)
	}
	if resolver.postgres == nil {
		return ResolvedBuild{}, errors.New("PostgreSQL materialization resolver is not configured")
	}
	return resolver.postgres.Resolve(ctx, claim)
}

type odsSourcePlan struct {
	document         dataset.Document
	schemaHash       string
	versionNo        int
	node             dataset.Node
	input            materialization.InputSnapshot
	source           datasource.Source
	sourceTable      querycompiler.TableRef
	stageColumns     []warehouse.StageColumn
	tableName        string
	fileAssetID      string
	fileSHA256       string
	maxExcelFileSize int64
}

func (resolver *ODSResolver) Resolve(
	ctx context.Context,
	claim materialization.Claim,
) (ResolvedBuild, error) {
	if resolver == nil || resolver.pool == nil {
		return ResolvedBuild{}, errors.New("ODS materialization resolver is not configured")
	}
	if err := validateODSClaim(claim); err != nil {
		return ResolvedBuild{}, err
	}
	plan, err := resolver.loadPlan(ctx, claim)
	if err != nil {
		return ResolvedBuild{}, err
	}

	stageCtx, cancel := context.WithTimeout(
		ctx,
		time.Duration(plan.document.ExecutionPolicy.TimeoutMS)*time.Millisecond,
	)
	defer cancel()
	result, err := resolver.stage(stageCtx, claim, plan)
	if err != nil {
		return ResolvedBuild{}, mapODSStageError(ctx, stageCtx, err)
	}
	if err := stageCtx.Err(); err != nil {
		return ResolvedBuild{}, mapODSStageError(ctx, stageCtx, err)
	}
	if err := resolver.revalidateSource(stageCtx, claim, plan); err != nil {
		return ResolvedBuild{}, mapODSStageError(ctx, stageCtx, err)
	}

	columns := make(map[string]bool, len(plan.stageColumns))
	columnTypes := make(map[string]string, len(plan.stageColumns))
	for _, column := range plan.stageColumns {
		columns[column.Name] = true
		columnTypes[column.Name] = column.CanonicalType
	}
	return ResolvedBuild{
		Document: plan.document,
		Tables: map[string]querycompiler.TableRef{
			plan.node.ID: {
				NodeID:  plan.node.ID,
				Schema:  result.Schema,
				Name:    result.Table,
				Columns: columns, ColumnTypes: columnTypes,
			},
		},
		SchemaHash: plan.schemaHash,
		VersionNo:  plan.versionNo,
		InputRowCount: map[int]int64{
			plan.input.Ordinal: result.RowCount,
		},
	}, nil
}

func validateODSClaim(claim materialization.Claim) error {
	request := materialization.RegisterRequest{
		Plan: claim.Plan, Inputs: claim.Inputs,
		PartitionKey: claim.PartitionKey, MaxAttempts: claim.MaxAttempts,
	}
	if err := request.Validate(); err != nil ||
		claim.Plan.DatasetID != claim.DatasetID ||
		claim.Plan.DatasetVersionID != claim.DatasetVersionID ||
		claim.Plan.Layer != claim.Layer ||
		claim.Plan.Mode != claim.Mode ||
		claim.Layer != materialization.LayerODS {
		return executionError(
			CodeTrustedPlanInvalid,
			"the registered ODS build plan is invalid",
			err,
		)
	}
	if claim.Mode != materialization.RunModeFull {
		return executionError(
			CodeRefreshModeUnsupported,
			"incremental and backfill ODS materialization are not supported",
			nil,
		)
	}
	if claim.Plan.Target.RelationKind != "TABLE" {
		return executionError(
			CodePartitionedTableUnsupported,
			"partitioned ODS materialization is not supported",
			nil,
		)
	}
	if len(claim.Inputs) != 1 ||
		claim.Inputs[0].Ordinal != 1 ||
		(claim.Inputs[0].Type != materialization.InputSourceTable &&
			claim.Inputs[0].Type != materialization.InputFileVersion) {
		return executionError(
			CodeTrustedPlanInvalid,
			"the registered ODS build must contain one frozen source input",
			nil,
		)
	}
	extracts := 0
	for _, node := range claim.Plan.Nodes {
		if node.Kind == materialization.NodeExtract {
			extracts++
			if node.Engine != materialization.EngineSourceDB ||
				len(node.InputOrdinals) != 1 ||
				node.InputOrdinals[0] != 1 {
				return executionError(
					CodeTrustedPlanInvalid,
					"the ODS extraction node does not match its frozen source input",
					nil,
				)
			}
			continue
		}
		if node.Engine != materialization.EnginePostgres {
			return executionError(
				CodePostgresExecutionRequired,
				"ODS transformations after extraction must execute in PostgreSQL",
				nil,
			)
		}
	}
	if extracts != 1 {
		return executionError(
			CodeTrustedPlanInvalid,
			"the ODS build must contain exactly one extraction node",
			nil,
		)
	}
	return nil
}

func (resolver *ODSResolver) loadPlan(
	ctx context.Context,
	claim materialization.Claim,
) (plan odsSourcePlan, err error) {
	err = database.WithTenantTx(ctx, resolver.pool, claim.TenantID, func(tx pgx.Tx) error {
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
			Scan(&dslJSON, &plan.schemaHash, &plan.versionNo, &storedLayer); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return executionError(
					CodeTargetVersionUnavailable,
					"the target published ODS version is unavailable",
					err,
				)
			}
			return err
		}
		prepared, err := dataset.Prepare(dslJSON)
		if err != nil {
			return executionError(
				CodeTargetContractChanged,
				"the target published ODS contract is invalid",
				err,
			)
		}
		if storedLayer != string(materialization.LayerODS) ||
			prepared.DSLHash != plan.schemaHash ||
			prepared.Document.Dataset.Layer != dataset.LayerODS ||
			!prepared.Document.ExecutionPolicy.Materialization.Enabled ||
			len(prepared.Document.Nodes) != 1 ||
			prepared.Document.Nodes[0].Type != "TABLE" {
			return executionError(
				CodeTargetContractChanged,
				"the target published ODS contract no longer matches the registered build",
				nil,
			)
		}
		plan.document = prepared.Document
		plan.node = prepared.Document.Nodes[0]
		plan.input = claim.Inputs[0]
		if err := validateODSNodeInput(plan.node, plan.input); err != nil {
			return err
		}
		if err := loadODSSourceTx(ctx, tx, claim, &plan); err != nil {
			return err
		}
		if err := loadODSMetadataTableTx(ctx, tx, &plan); err != nil {
			return err
		}
		return nil
	})
	return plan, err
}

func validateODSNodeInput(
	node dataset.Node,
	input materialization.InputSnapshot,
) error {
	if node.DataSourceID != input.DataSourceID ||
		input.DataSourceVersionID == "" {
		return executionError(
			CodeODSSourceContractInvalid,
			"the ODS node does not match its frozen published data source",
			nil,
		)
	}
	switch input.Type {
	case materialization.InputSourceTable:
		if node.TableID != input.MetadataTableID || node.FileVersionID != "" {
			return executionError(
				CodeODSSourceContractInvalid,
				"the ODS database table does not match its frozen input",
				nil,
			)
		}
	case materialization.InputFileVersion:
		if node.TableID == "" ||
			node.FileVersionID != input.FileVersionID ||
			input.MetadataTableID != "" {
			return executionError(
				CodeODSSourceContractInvalid,
				"the ODS worksheet does not match its frozen file version",
				nil,
			)
		}
	default:
		return executionError(
			CodeODSSourceContractInvalid,
			"the ODS source input type is unsupported",
			nil,
		)
	}
	return nil
}

func loadODSSourceTx(
	ctx context.Context,
	tx pgx.Tx,
	claim materialization.Claim,
	plan *odsSourcePlan,
) error {
	var configJSON []byte
	err := tx.QueryRow(ctx, `SELECT source.id::text,source.tenant_id::text,
			source.code::text,source.name,COALESCE(source.description,''),
			COALESCE(source.owner_user_id::text,''),source.visibility::text,
			version.source_type::text,source.status::text,version.config,
			COALESCE(version.secret_ref,''),COALESCE(version.file_asset_id::text,''),
			COALESCE(version.file_version_id::text,''),version.id::text,
			version.version_no,version.config_hash,
			COALESCE(quota.max_data_sources,20),
			COALESCE(quota.max_connections_per_source,5),
			COALESCE(quota.max_concurrent_queries,10),
			COALESCE(quota.max_excel_file_bytes,52428800)
		FROM platform.data_sources AS source
		JOIN platform.data_source_versions AS version
		  ON version.id=source.current_published_version_id
		 AND version.data_source_id=source.id
		 AND version.tenant_id=source.tenant_id
		LEFT JOIN platform.tenant_data_source_quotas AS quota
		  ON quota.tenant_id=source.tenant_id
		WHERE source.id=$1
		  AND source.status='ACTIVE'
		  AND source.publication_status='PUBLISHED'
		  AND source.deleted_at IS NULL
		  AND source.current_published_version_id=$2
		FOR SHARE OF source,version`,
		plan.input.DataSourceID,
		plan.input.DataSourceVersionID,
	).Scan(
		&plan.source.ID, &plan.source.TenantID,
		&plan.source.Code, &plan.source.Name, &plan.source.Description,
		&plan.source.OwnerID, &plan.source.Visibility,
		&plan.source.Type, &plan.source.Status, &configJSON,
		&plan.source.SecretRef, &plan.source.FileAssetID,
		&plan.source.FileVersionID, &plan.source.ConfigVersionID,
		&plan.source.ConfigVersion, &plan.source.ConfigHash,
		&plan.source.RuntimeQuota.MaxDataSources,
		&plan.source.RuntimeQuota.MaxConnectionsPerSource,
		&plan.source.RuntimeQuota.MaxConcurrentQueries,
		&plan.source.RuntimeQuota.MaxExcelFileBytes,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return executionError(
			CodeODSSourceContractInvalid,
			"the frozen ODS data source is no longer the active published version",
			err,
		)
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(configJSON, &plan.source.Config); err != nil {
		return executionError(
			CodeODSSourceContractInvalid,
			"the frozen ODS data source configuration is invalid",
			err,
		)
	}
	plan.source.PublishedVersionID = plan.source.ConfigVersionID
	plan.source.PublishedConfigVersion = plan.source.ConfigVersion
	plan.source.PublicationStatus = datasource.PublicationPublished
	if plan.source.TenantID != claim.TenantID ||
		plan.source.ID != plan.input.DataSourceID ||
		plan.source.ConfigVersionID != plan.input.DataSourceVersionID {
		return executionError(
			CodeODSSourceContractInvalid,
			"the frozen ODS data source identity is invalid",
			nil,
		)
	}
	switch plan.input.Type {
	case materialization.InputSourceTable:
		if plan.source.Type != datasource.TypeMySQL &&
			plan.source.Type != datasource.TypeOracle {
			return executionError(
				CodeODSSourceContractInvalid,
				"the ODS database input uses an unsupported source type",
				nil,
			)
		}
		if plan.source.FileAssetID != "" || plan.source.FileVersionID != "" {
			return executionError(
				CodeODSSourceContractInvalid,
				"the ODS database source has an invalid immutable configuration",
				nil,
			)
		}
	case materialization.InputFileVersion:
		if plan.source.Type != datasource.TypeExcel ||
			plan.source.FileAssetID == "" ||
			plan.source.FileVersionID != plan.input.FileVersionID {
			return executionError(
				CodeODSSourceContractInvalid,
				"the ODS file input is not the published immutable file version",
				nil,
			)
		}
	}
	return nil
}

func loadODSMetadataTableTx(
	ctx context.Context,
	tx pgx.Tx,
	plan *odsSourcePlan,
) error {
	var catalogName, structureHash string
	err := tx.QueryRow(ctx, `SELECT table_asset.catalog_name,
			table_asset.schema_name,table_asset.table_name,
			table_asset.structure_hash
		FROM platform.metadata_tables AS table_asset
		WHERE table_asset.id=$1
		  AND table_asset.data_source_id=$2
		  AND table_asset.asset_status='ACTIVE'
		  AND table_asset.management_status='ENABLED'
		FOR SHARE`,
		plan.node.TableID, plan.source.ID,
	).Scan(
		&catalogName,
		&plan.sourceTable.Schema,
		&plan.sourceTable.Name,
		&structureHash,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return executionError(
			CodeODSSourceContractInvalid,
			"the frozen ODS table is no longer active",
			err,
		)
	}
	if err != nil {
		return err
	}
	if structureHash != plan.input.SchemaHash {
		return executionError(
			CodeODSSourceContractInvalid,
			"the ODS source table structure changed after build registration",
			nil,
		)
	}

	rows, err := tx.Query(ctx, `SELECT column_name,canonical_type
		FROM platform.metadata_columns
		WHERE table_id=$1 AND asset_status='ACTIVE'
		ORDER BY ordinal_position,column_name
		FOR SHARE`, plan.node.TableID)
	if err != nil {
		return err
	}
	defer rows.Close()
	available := make(map[string]string)
	for rows.Next() {
		var name, canonicalType string
		if err := rows.Scan(&name, &canonicalType); err != nil {
			return err
		}
		if _, duplicate := available[name]; duplicate {
			return executionError(
				CodeODSSourceContractInvalid,
				"the ODS source table contains duplicate metadata columns",
				nil,
			)
		}
		available[name] = strings.ToUpper(strings.TrimSpace(canonicalType))
	}
	if err := rows.Err(); err != nil {
		return err
	}
	plan.sourceTable.NodeID = plan.node.ID
	plan.sourceTable.Columns = make(map[string]bool, len(plan.node.Projection))
	plan.sourceTable.ColumnTypes = make(map[string]string, len(plan.node.Projection))
	plan.stageColumns = make([]warehouse.StageColumn, len(plan.node.Projection))
	for index, name := range plan.node.Projection {
		canonicalType, exists := available[name]
		if !exists || canonicalType == "" {
			return executionError(
				CodeODSSourceContractInvalid,
				"the ODS source projection is absent from its frozen metadata",
				nil,
			)
		}
		plan.sourceTable.Columns[name] = true
		plan.sourceTable.ColumnTypes[name] = canonicalType
		plan.stageColumns[index] = warehouse.StageColumn{
			Name: name, CanonicalType: canonicalType,
		}
	}
	plan.tableName = plan.sourceTable.Name

	if plan.input.Type == materialization.InputFileVersion {
		err := tx.QueryRow(ctx, `SELECT file_version.file_asset_id::text,
				file_version.sha256
			FROM platform.file_asset_versions AS file_version
			JOIN platform.data_source_versions AS source_version
			  ON source_version.file_version_id=file_version.id
			 AND source_version.file_asset_id=file_version.file_asset_id
			 AND source_version.tenant_id=file_version.tenant_id
			WHERE file_version.id=$1
			  AND source_version.id=$2
			FOR SHARE OF file_version,source_version`,
			plan.input.FileVersionID,
			plan.input.DataSourceVersionID,
		).Scan(&plan.fileAssetID, &plan.fileSHA256)
		if errors.Is(err, pgx.ErrNoRows) {
			return executionError(
				CodeODSSourceContractInvalid,
				"the frozen ODS file version is unavailable",
				err,
			)
		}
		if err != nil {
			return err
		}
		if plan.fileAssetID != plan.source.FileAssetID ||
			plan.fileSHA256 != plan.input.SnapshotHash {
			return executionError(
				CodeODSSourceContractInvalid,
				"the ODS file checksum does not match its frozen input",
				nil,
			)
		}
		plan.maxExcelFileSize = plan.source.RuntimeQuota.MaxExcelFileBytes
	}
	return nil
}

func (resolver *ODSResolver) stage(
	ctx context.Context,
	claim materialization.Claim,
	plan odsSourcePlan,
) (warehouse.StageResult, error) {
	switch plan.input.Type {
	case materialization.InputSourceTable:
		stager := resolver.databaseStagers[plan.source.Type]
		if stager == nil {
			return warehouse.StageResult{}, executionError(
				CodeODSSourceStagingNotConfigured,
				"database ODS staging is not configured for this source type",
				nil,
			)
		}
		dialect := querycompiler.MySQL
		if plan.source.Type == datasource.TypeOracle {
			dialect = querycompiler.Oracle
		}
		return stager.Stage(ctx, warehouse.StageInput{
			TenantID: claim.TenantID,
			RunID:    claim.ID,
			Source:   plan.source,
			Scan: querycompiler.ScanInput{
				Document: plan.document,
				NodeID:   plan.node.ID,
				Dialect:  dialect,
				Table:    plan.sourceTable,
				MaxRows:  warehouse.MaxODSRows,
			},
			BatchSize: odsStageBatchSize,
			Columns:   plan.stageColumns,
		})
	case materialization.InputFileVersion:
		if resolver.fileStager == nil {
			return warehouse.StageResult{}, executionError(
				CodeODSExcelUnsupported,
				"Excel ODS staging is not configured for this worker",
				nil,
			)
		}
		return resolver.fileStager.Stage(ctx, warehouse.FileStageInput{
			TenantID:            claim.TenantID,
			RunID:               claim.ID,
			NodeID:              plan.node.ID,
			Source:              plan.source,
			FileVersionID:       plan.input.FileVersionID,
			ExpectedFileAssetID: plan.fileAssetID,
			ExpectedSHA256:      plan.fileSHA256,
			TableName:           plan.tableName,
			MaxFileBytes:        plan.maxExcelFileSize,
			MaxRows:             warehouse.MaxODSRows,
			BatchSize:           odsStageBatchSize,
			Columns:             plan.stageColumns,
		})
	default:
		return warehouse.StageResult{}, executionError(
			CodeODSUnsupported,
			"the ODS source input type is unsupported",
			nil,
		)
	}
}

func (resolver *ODSResolver) revalidateSource(
	ctx context.Context,
	claim materialization.Claim,
	plan odsSourcePlan,
) error {
	return database.WithTenantTx(ctx, resolver.pool, claim.TenantID, func(tx pgx.Tx) error {
		var valid bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1
			FROM platform.data_sources AS source
			JOIN platform.data_source_versions AS version
			  ON version.id=source.current_published_version_id
			 AND version.data_source_id=source.id
			 AND version.tenant_id=source.tenant_id
			JOIN platform.metadata_tables AS table_asset
			  ON table_asset.id=$3
			 AND table_asset.data_source_id=source.id
			 AND table_asset.tenant_id=source.tenant_id
			WHERE source.id=$1
			  AND version.id=$2
			  AND source.status='ACTIVE'
			  AND source.publication_status='PUBLISHED'
			  AND source.deleted_at IS NULL
			  AND table_asset.asset_status='ACTIVE'
			  AND table_asset.management_status='ENABLED'
			  AND table_asset.structure_hash=$4
			  AND (
				$5::uuid IS NULL
				OR (
					version.file_version_id=$5::uuid
					AND EXISTS(
						SELECT 1
						FROM platform.file_asset_versions AS file_version
						WHERE file_version.id=$5::uuid
						  AND file_version.file_asset_id=version.file_asset_id
						  AND file_version.sha256=$6
					)
				)
			  )
		)`,
			plan.input.DataSourceID,
			plan.input.DataSourceVersionID,
			plan.node.TableID,
			plan.input.SchemaHash,
			nullableUUID(plan.input.FileVersionID),
			plan.input.SnapshotHash,
		).Scan(&valid); err != nil {
			return err
		}
		if !valid {
			return executionError(
				CodeODSSourceContractInvalid,
				"the published ODS source contract changed during staging",
				nil,
			)
		}
		return nil
	})
}

func nullableUUID(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func mapODSStageError(
	parent context.Context,
	stage context.Context,
	err error,
) error {
	var execution *ExecutionError
	if errors.As(err, &execution) {
		return err
	}
	if parent.Err() != nil {
		return parent.Err()
	}
	if errors.Is(stage.Err(), context.DeadlineExceeded) ||
		errors.Is(err, context.DeadlineExceeded) {
		return executionError(
			CodeODSStagingTimeout,
			"the ODS source staging exceeded the published execution timeout",
			err,
		)
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	return executionError(
		CodeODSStagingFailed,
		"the exact published ODS source could not be staged into PostgreSQL",
		err,
	)
}
