package materializationworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/datasource"
	"intelligent-report-generation-system/internal/fieldtype"
	"intelligent-report-generation-system/internal/materialization"
	"intelligent-report-generation-system/internal/platform/database"
	"intelligent-report-generation-system/internal/querycompiler"
	"intelligent-report-generation-system/internal/warehouse"
)

const odsStageBatchSize = 1000
const odsStageTimeout = 30 * time.Minute

type databaseStager interface {
	Stage(context.Context, warehouse.StageInput) (warehouse.StageResult, error)
}

type fileStager interface {
	Stage(context.Context, warehouse.FileStageInput) (warehouse.StageResult, error)
}

type odsProjector interface {
	Project(
		context.Context,
		warehouse.ODSProjectionInput,
	) (warehouse.StageResult, error)
}

// ODSResolver reloads a published single-table source-backed contract,
// validates its frozen SOURCE input and stages the exact remote/file version
// into PostgreSQL. It serves ODS and PRE_AGGREGATED DWS only, and never accepts
// physical names, SQL or connection details from the claim.
type ODSResolver struct {
	pool            *pgxpool.Pool
	databaseStagers map[datasource.Type]databaseStager
	fileStager      fileStager
	projector       odsProjector
}

func (resolver *ODSResolver) SetFullProjector(projector odsProjector) {
	if resolver != nil {
		resolver.projector = projector
	}
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

// CompositeResolver keeps warehouse-input resolution separate from source
// extraction. Layer identity and the source-backed DWS exception are loaded
// again by each concrete resolver.
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
	usesSource := claim.Layer == materialization.LayerODS
	for _, input := range claim.Inputs {
		usesSource = usesSource || input.Type == materialization.InputSourceTable ||
			input.Type == materialization.InputFileVersion
	}
	if usesSource {
		if resolver.ods == nil {
			return ResolvedBuild{}, errors.New("source materialization resolver is not configured")
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
	if err := validateSourceClaim(claim); err != nil {
		return ResolvedBuild{}, err
	}
	plan, err := resolver.loadPlan(ctx, claim)
	if err != nil {
		return ResolvedBuild{}, err
	}

	// executionPolicy.timeoutMs governs interactive query execution and is
	// intentionally capped at two minutes. ODS extraction is a bounded
	// background build with independent row, byte, lease, and retry guards; a
	// large immutable workbook therefore receives a separate worker deadline.
	stageCtx, cancel := context.WithTimeout(ctx, odsStageTimeout)
	defer cancel()
	result, err := resolver.stage(
		stageCtx, claim, plan, warehouse.MaxODSRows, false,
	)
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

func validateSourceClaim(claim materialization.Claim) error {
	request := materialization.RegisterRequest{
		Plan: claim.Plan, Inputs: claim.Inputs,
		PartitionKey: claim.PartitionKey, MaxAttempts: claim.MaxAttempts,
	}
	if err := request.Validate(); err != nil ||
		claim.Plan.DatasetID != claim.DatasetID ||
		claim.Plan.DatasetVersionID != claim.DatasetVersionID ||
		claim.Plan.Layer != claim.Layer ||
		claim.Plan.Mode != claim.Mode ||
		(claim.Layer != materialization.LayerODS &&
			claim.Layer != materialization.LayerDWS) {
		return executionError(
			CodeTrustedPlanInvalid,
			"the registered source-backed build plan is invalid",
			err,
		)
	}
	if claim.Mode != materialization.RunModeFull {
		return executionError(
			CodeRefreshModeUnsupported,
			"incremental and backfill source materialization are not supported",
			nil,
		)
	}
	if claim.Plan.Target.RelationKind != "TABLE" {
		return executionError(
			CodePartitionedTableUnsupported,
			"partitioned source materialization is not supported",
			nil,
		)
	}
	if len(claim.Inputs) != 1 ||
		claim.Inputs[0].Ordinal != 1 ||
		(claim.Inputs[0].Type != materialization.InputSourceTable &&
			claim.Inputs[0].Type != materialization.InputFileVersion) {
		return executionError(
			CodeTrustedPlanInvalid,
			"the registered source-backed build must contain one frozen source input",
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
					"the source extraction node does not match its frozen input",
					nil,
				)
			}
			continue
		}
		if node.Engine != materialization.EnginePostgres {
			return executionError(
				CodePostgresExecutionRequired,
				"source transformations after extraction must execute in PostgreSQL",
				nil,
			)
		}
	}
	if extracts != 1 {
		return executionError(
			CodeTrustedPlanInvalid,
			"the source-backed build must contain exactly one extraction node",
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
					"the target published source-backed version is unavailable",
					err,
				)
			}
			return err
		}
		prepared, err := dataset.Prepare(dslJSON)
		if err != nil {
			return executionError(
				CodeTargetContractChanged,
				"the target published source-backed contract is invalid",
				err,
			)
		}
		if storedLayer != string(claim.Layer) ||
			prepared.DSLHash != plan.schemaHash ||
			string(prepared.Document.Dataset.Layer) != string(claim.Layer) ||
			!dataset.IsSourceBackedMaterialization(prepared.Document) {
			return executionError(
				CodeTargetContractChanged,
				"the target published source-backed contract no longer matches the registered build",
				nil,
			)
		}
		plan.document, _ = dataset.EnableSourceBackedMaterialization(prepared.Document)
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
			COALESCE(quota.max_excel_file_bytes,$3)
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
		datasource.DefaultMaxExcelFileBytes,
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

	rows, err := tx.Query(ctx, `SELECT column_name,business_name,canonical_type
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
		var name, businessName, canonicalType string
		if err := rows.Scan(&name, &businessName, &canonicalType); err != nil {
			return err
		}
		if _, duplicate := available[name]; duplicate {
			return executionError(
				CodeODSSourceContractInvalid,
				"the ODS source table contains duplicate metadata columns",
				nil,
			)
		}
		available[name] = odsMetadataCanonicalType(
			plan.source.Type, name, businessName, canonicalType,
		)
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

func odsMetadataCanonicalType(
	sourceType datasource.Type,
	columnName, businessName, canonicalType string,
) string {
	if sourceType == datasource.TypeExcel &&
		fieldtype.IsCodeLike(columnName, businessName) {
		return "TEXT"
	}
	return strings.ToUpper(strings.TrimSpace(canonicalType))
}

func (resolver *ODSResolver) stage(
	ctx context.Context,
	claim materialization.Claim,
	plan odsSourcePlan,
	maxRows int,
	allowTruncate bool,
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
				MaxRows:  maxRows,
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
			MaxRows:             maxRows,
			BatchSize:           odsStageBatchSize,
			AllowTruncate:       allowTruncate,
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

// Rehydrate stages the full immutable source behind an active ODS preview and
// reapplies the published ODS projection into a DWD-run-scoped staging table.
// The DWD still freezes and validates the governed ODS materialization; it does
// not read an arbitrary current source version.
func (resolver *ODSResolver) Rehydrate(
	ctx context.Context,
	claim materialization.Claim,
	upstream upstreamMaterialization,
	frozenInput materialization.InputSnapshot,
	targetNodeID string,
) (warehouse.StageResult, error) {
	if resolver == nil || resolver.pool == nil || resolver.projector == nil {
		return warehouse.StageResult{}, executionError(
			CodeODSSourceStagingNotConfigured,
			"full ODS source replay is not configured for warehouse materialization",
			nil,
		)
	}
	if (claim.Layer != materialization.LayerDIM &&
		claim.Layer != materialization.LayerDWD) ||
		strings.TrimSpace(targetNodeID) == "" {
		return warehouse.StageResult{}, executionError(
			CodeTrustedPlanInvalid,
			"the full ODS replay request is invalid",
			nil,
		)
	}
	var sourceInput materialization.InputSnapshot
	var err error
	if upstream.ID == "" {
		var snapshot virtualODSSourceSnapshot
		if unmarshalErr := json.Unmarshal(
			frozenInput.SnapshotJSON, &snapshot,
		); unmarshalErr != nil ||
			snapshot.Contract != "virtual-ods-source-v1" ||
			snapshot.DatasetID != upstream.DatasetID ||
			snapshot.DatasetVersionID != upstream.DatasetVersionID ||
			snapshot.SourceInput.SnapshotHash != frozenInput.SnapshotHash {
			return warehouse.StageResult{}, executionError(
				CodeUpstreamContractInvalid,
				"the virtual ODS source snapshot is invalid",
				unmarshalErr,
			)
		}
		sourceInput = snapshot.SourceInput
	} else {
		sourceInput, err = resolver.loadFrozenSourceInput(
			ctx, claim, upstream,
		)
		if err != nil {
			return warehouse.StageResult{}, err
		}
	}
	sourceRunID := uuid.NewSHA1(
		uuid.NameSpaceOID,
		[]byte(
			strings.ToLower(string(claim.Layer))+"-full-ods\x00"+claim.ID+"\x00"+
				upstream.DatasetVersionID,
		),
	).String()
	sourceClaim := materialization.Claim{
		Run: materialization.Run{
			ID: sourceRunID, TenantID: claim.TenantID,
			DatasetID:        upstream.DatasetID,
			DatasetVersionID: upstream.DatasetVersionID,
			Layer:            materialization.LayerODS,
			Mode:             materialization.RunModeFull,
		},
		Inputs: []materialization.InputSnapshot{sourceInput},
	}
	plan, err := resolver.loadPlan(ctx, sourceClaim)
	if err != nil {
		return warehouse.StageResult{}, err
	}
	stageCtx, cancel := context.WithTimeout(ctx, odsStageTimeout)
	defer cancel()
	staged, err := resolver.stage(
		stageCtx, sourceClaim, plan, warehouse.MaxODSRows, false,
	)
	if err != nil {
		return warehouse.StageResult{}, mapODSStageError(ctx, stageCtx, err)
	}
	if err := resolver.revalidateSource(
		stageCtx, sourceClaim, plan,
	); err != nil {
		return warehouse.StageResult{}, mapODSStageError(ctx, stageCtx, err)
	}
	columns := make(map[string]bool, len(plan.stageColumns))
	columnTypes := make(map[string]string, len(plan.stageColumns))
	for _, column := range plan.stageColumns {
		columns[column.Name] = true
		columnTypes[column.Name] = column.CanonicalType
	}
	projected, err := resolver.projector.Project(
		stageCtx,
		warehouse.ODSProjectionInput{
			TenantID:    claim.TenantID,
			SourceRunID: sourceRunID, TargetRunID: claim.ID,
			TargetNodeID: targetNodeID,
			Document:     plan.document,
			Source: querycompiler.TableRef{
				NodeID: plan.node.ID,
				Schema: staged.Schema, Name: staged.Table,
				Columns: columns, ColumnTypes: columnTypes,
			},
		},
	)
	if err != nil {
		return warehouse.StageResult{}, executionError(
			CodeWarehouseBuildFailed,
			"the full ODS source could not be projected for warehouse materialization",
			err,
		)
	}
	return projected, nil
}

func (resolver *ODSResolver) loadFrozenSourceInput(
	ctx context.Context,
	claim materialization.Claim,
	upstream upstreamMaterialization,
) (materialization.InputSnapshot, error) {
	var (
		input      materialization.InputSnapshot
		sourceType string
		snapshot   []byte
		rowCount   pgtype.Int8
	)
	err := database.WithTenantTx(
		ctx, resolver.pool, claim.TenantID,
		func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT input.ordinal_position,
					input.source_type,input.input_layer,
					COALESCE(input.input_data_source_id::text,''),
					COALESCE(input.input_data_source_version_id::text,''),
					COALESCE(input.metadata_table_id::text,''),
					COALESCE(input.file_version_id::text,''),
					input.source_version,input.schema_hash,input.snapshot_hash,
					input.snapshot_json,input.row_count
				FROM platform.dataset_materializations AS active
				JOIN platform.dataset_versions AS version
				  ON version.id=active.dataset_version_id
				 AND version.dataset_id=active.dataset_id
				 AND version.tenant_id=active.tenant_id
				JOIN platform.datasets AS owner
				  ON owner.id=version.dataset_id
				 AND owner.tenant_id=version.tenant_id
				JOIN platform.build_run_inputs AS input
				  ON input.build_run_id=active.build_run_id
				 AND input.tenant_id=active.tenant_id
				 AND input.ordinal_position=1
				WHERE active.id=$1
				  AND active.dataset_id=$2
				  AND active.dataset_version_id=$3
				  AND active.status='ACTIVE'
				  AND active.layer='ODS'
				  AND version.status='PUBLISHED'
				  AND owner.status='PUBLISHED'
				  AND owner.current_published_version_id=version.id
				  AND owner.deleted_at IS NULL
				  AND input.source_type IN ('SOURCE_TABLE','FILE_VERSION')
				FOR SHARE OF active,version,owner,input`,
				upstream.ID, upstream.DatasetID,
				upstream.DatasetVersionID,
			).Scan(
				&input.Ordinal, &sourceType, &input.Layer,
				&input.DataSourceID, &input.DataSourceVersionID,
				&input.MetadataTableID, &input.FileVersionID,
				&input.SourceVersion, &input.SchemaHash,
				&input.SnapshotHash, &snapshot, &rowCount,
			)
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return materialization.InputSnapshot{}, executionError(
			CodeUpstreamContractInvalid,
			"the active ODS preview has no frozen source lineage",
			err,
		)
	}
	if err != nil {
		return materialization.InputSnapshot{}, err
	}
	input.Type = materialization.InputType(sourceType)
	input.SnapshotJSON = json.RawMessage(snapshot)
	if rowCount.Valid {
		value := rowCount.Int64
		input.RowCount = &value
	}
	if input.Ordinal != 1 {
		return materialization.InputSnapshot{}, fmt.Errorf(
			"active ODS source input ordinal is invalid",
		)
	}
	return input, nil
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
	// 持久化错误只保留稳定代码，避免把源库细节暴露到控制面；内部日志记录
	// 受控执行链的具体失败点，便于区分 SQL、流协议和类型规范化问题。
	slog.ErrorContext(stage, "ODS source staging failed", "error", err)
	return executionError(
		CodeODSStagingFailed,
		"the exact published ODS source could not be staged into PostgreSQL",
		err,
	)
}
