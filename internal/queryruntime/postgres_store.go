package queryruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/datasource"
	"intelligent-report-generation-system/internal/materialization"
	"intelligent-report-generation-system/internal/metric"
	"intelligent-report-generation-system/internal/platform/database"
	"intelligent-report-generation-system/internal/querycompiler"
)

type PostgresStore struct{ pool *pgxpool.Pool }

// NewPostgresStore 创建查询白名单与运行审计仓储。
func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

// Resolve 以租户 RLS 为边界，把 DSL 资产标识解析成可信物理表和字段白名单。
func (s *PostgresStore) Resolve(ctx context.Context, tenantID string, document dataset.Document) (ResolvedPlan, error) {
	var result ResolvedPlan
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var err error
		result, err = resolveTx(ctx, tx, tenantID, document)
		return err
	})
	return result, err
}

// ResolveVersion 在同一事务内锁定精确版本、复核依赖摘要并解析物理白名单，消除检查与使用之间的元数据窗口。
func (s *PostgresStore) ResolveVersion(ctx context.Context, tenantID, datasetID, versionID string, document dataset.Document) (ResolvedPlan, error) {
	var result ResolvedPlan
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := dataset.ValidateVersionDependenciesInTx(ctx, tx, datasetID, versionID); err != nil {
			return err
		}
		var err error
		result, err = resolveTx(ctx, tx, tenantID, document)
		if errors.Is(err, dataset.ErrInvalidDocument) || errors.Is(err, dataset.ErrPreviewUnsupported) {
			return dataset.ErrVersionUnavailable
		}
		return err
	})
	return result, err
}

// ResolveMaterializedVersion resolves an execution-only one-node document to
// the exact current DWS version's ACTIVE materialization. It is used by metric
// preview/publication so metrics read the governed DWS output instead of
// replaying the DWS DAG against mutable upstream contents.
func (s *PostgresStore) ResolveMaterializedVersion(
	ctx context.Context,
	tenantID, datasetID, versionID string,
	document dataset.Document,
) (result ResolvedPlan, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if len(document.Nodes) != 1 ||
			document.Nodes[0].Type != "DATASET" ||
			document.Nodes[0].DatasetVersionID != versionID {
			return dataset.ErrPreviewUnsupported
		}
		if err := dataset.ValidateVersionDependenciesInTx(
			ctx, tx, datasetID, versionID,
		); err != nil {
			return err
		}
		binding, table, err := resolveActiveMaterializationTx(
			ctx, tx, tenantID, document.Nodes[0],
			string(dataset.LayerDWS), datasetID,
		)
		if err != nil {
			if errors.Is(err, dataset.ErrInvalidDocument) ||
				errors.Is(err, dataset.ErrPreviewUnsupported) {
				return dataset.ErrVersionUnavailable
			}
			return err
		}
		result = ResolvedPlan{
			Engine: ExecutionPostgreSQL,
			Tables: map[string]querycompiler.TableRef{
				document.Nodes[0].ID: table,
			},
			Nodes:            map[string]ResolvedNode{},
			Materializations: []ResolvedMaterialization{binding},
		}
		return nil
	})
	return result, err
}

func resolveTx(
	ctx context.Context,
	tx pgx.Tx,
	tenantID string,
	document dataset.Document,
) (ResolvedPlan, error) {
	hasTable, hasDataset := false, false
	for _, node := range document.Nodes {
		hasTable = hasTable || node.Type == "TABLE"
		hasDataset = hasDataset || node.Type == "DATASET"
	}
	if hasDataset {
		if hasTable {
			return ResolvedPlan{}, dataset.ErrPreviewUnsupported
		}
		return resolveDatasetNodesTx(ctx, tx, tenantID, document)
	}
	result := ResolvedPlan{
		Engine: ExecutionSource,
		Tables: map[string]querycompiler.TableRef{},
		Nodes:  map[string]ResolvedNode{},
	}
	fileVersionsBySource := map[string]string{}
	for _, node := range document.Nodes {
		if node.Type != "TABLE" {
			return ResolvedPlan{}, fmt.Errorf("%w: published dataset nodes are not executable in the single-source compiler", dataset.ErrPreviewUnsupported)
		}
		var sourceID, schemaName, tableName, sourceWatermark string
		var sourceType datasource.Type
		var sourceStatus datasource.Status
		var sourceVersion int64
		err := tx.QueryRow(ctx, `SELECT t.data_source_id::text,t.schema_name,t.table_name,d.source_type,d.status,d.version,COALESCE(d.last_synced_at::text,'')
				FROM platform.metadata_tables t JOIN platform.data_sources d ON d.id=t.data_source_id AND d.tenant_id=t.tenant_id
				WHERE t.id::text=$1 AND t.data_source_id::text=$2 AND (d.source_type='EXCEL' OR (t.asset_status='ACTIVE' AND t.management_status='ENABLED')) AND d.deleted_at IS NULL`, node.TableID, node.DataSourceID).
			Scan(&sourceID, &schemaName, &tableName, &sourceType, &sourceStatus, &sourceVersion, &sourceWatermark)
		if errors.Is(err, pgx.ErrNoRows) {
			return ResolvedPlan{}, dataset.ErrInvalidDocument
		}
		if err != nil {
			return ResolvedPlan{}, err
		}
		if sourceStatus != datasource.StatusActive {
			return ResolvedPlan{}, dataset.ErrPreviewUnsupported
		}
		if document.Dataset.Type == "SINGLE_SOURCE" && result.SourceID != "" && result.SourceID != sourceID {
			return ResolvedPlan{}, dataset.ErrPreviewUnsupported
		}
		if result.SourceID == "" {
			// query_runs 保留首节点数据源作为兼容主来源，完整来源另存 query_run_sources。
			result.SourceID, result.SourceType = sourceID, sourceType
		}
		columns := make(map[string]bool, len(node.Projection))
		columnTypes := make(map[string]string, len(node.Projection))
		fileVersionID, watermark := "", sourceWatermark
		if sourceType == datasource.TypeExcel {
			// 同一文件数据源的全部节点必须固定同一不可变版本；不同 Excel 源可各自固定版本。
			if node.FileVersionID == "" || fileVersionsBySource[sourceID] != "" && fileVersionsBySource[sourceID] != node.FileVersionID {
				return ResolvedPlan{}, dataset.ErrInvalidDocument
			}
			var versionExists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(
					SELECT 1 FROM platform.file_asset_versions fv
					JOIN platform.data_sources ds ON ds.file_asset_id=fv.file_asset_id AND ds.tenant_id=fv.tenant_id
					WHERE fv.id::text=$1 AND ds.id::text=$2 AND ds.deleted_at IS NULL
					),COALESCE((SELECT sha256 FROM platform.file_asset_versions WHERE id::text=$1),'')`, node.FileVersionID, sourceID).Scan(&versionExists, &watermark); err != nil {
				return ResolvedPlan{}, err
			}
			if !versionExists {
				return ResolvedPlan{}, dataset.ErrInvalidDocument
			}
			fileVersionsBySource[sourceID] = node.FileVersionID
			fileVersionID = node.FileVersionID
			// 仓储级调用的旧测试文档可能尚未携带 dataset.type；只有明确的
			// CROSS_SOURCE 才不应把某个文件版本提升为计划级唯一版本。
			if document.Dataset.Type != "CROSS_SOURCE" {
				result.FileVersionID = node.FileVersionID
			}
			for _, name := range node.Projection {
				columns[name] = true
			}
		} else {
			if node.FileVersionID != "" {
				return ResolvedPlan{}, dataset.ErrInvalidDocument
			}
			availableColumns := map[string]string{}
			rows, err := tx.Query(ctx, `SELECT column_name,canonical_type FROM platform.metadata_columns WHERE table_id::text=$1 AND asset_status='ACTIVE' ORDER BY ordinal_position`, node.TableID)
			if err != nil {
				return ResolvedPlan{}, err
			}
			for rows.Next() {
				var name, canonicalType string
				if err := rows.Scan(&name, &canonicalType); err != nil {
					rows.Close()
					return ResolvedPlan{}, err
				}
				availableColumns[name] = canonicalType
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return ResolvedPlan{}, err
			}
			rows.Close()
			for _, name := range node.Projection {
				canonicalType, exists := availableColumns[name]
				if !exists {
					return ResolvedPlan{}, dataset.ErrInvalidDocument
				}
				columns[name] = true
				columnTypes[name] = canonicalType
			}
		}
		table := querycompiler.TableRef{NodeID: node.ID, Schema: schemaName, Name: tableName, Columns: columns, ColumnTypes: columnTypes}
		result.Tables[node.ID] = table
		result.Nodes[node.ID] = ResolvedNode{
			NodeID: node.ID, SourceID: sourceID, SourceType: sourceType, SourceVersion: sourceVersion,
			FileVersionID: fileVersionID, Watermark: watermark, Table: table,
		}
	}
	return result, nil
}

func resolveDatasetNodesTx(
	ctx context.Context,
	tx pgx.Tx,
	tenantID string,
	document dataset.Document,
) (ResolvedPlan, error) {
	expectedLayer := ""
	switch document.Dataset.Layer {
	case dataset.LayerDWD:
		expectedLayer = string(dataset.LayerODS)
	case dataset.LayerDWS:
		expectedLayer = string(dataset.LayerDWD)
	default:
		return ResolvedPlan{}, dataset.ErrPreviewUnsupported
	}
	result := ResolvedPlan{
		Engine: ExecutionPostgreSQL,
		Tables: map[string]querycompiler.TableRef{},
		Nodes:  map[string]ResolvedNode{},
	}
	for _, node := range document.Nodes {
		binding, table, err := resolveActiveMaterializationTx(
			ctx, tx, tenantID, node, expectedLayer, "",
		)
		if err != nil {
			return ResolvedPlan{}, err
		}
		result.Tables[node.ID] = table
		result.Materializations = append(result.Materializations, binding)
	}
	if len(result.Materializations) == 0 {
		return ResolvedPlan{}, dataset.ErrPreviewUnsupported
	}
	return result, nil
}

func resolveActiveMaterializationTx(
	ctx context.Context,
	tx pgx.Tx,
	tenantID string,
	node dataset.Node,
	expectedLayer string,
	expectedDatasetID string,
) (ResolvedMaterialization, querycompiler.TableRef, error) {
	if node.Type != "DATASET" || node.DatasetVersionID == "" ||
		len(node.Projection) == 0 {
		return ResolvedMaterialization{}, querycompiler.TableRef{}, dataset.ErrInvalidDocument
	}
	var binding ResolvedMaterialization
	binding.NodeID = node.ID
	var versionLayer, versionHash, buildRunID, materializationLayer string
	var physicalSchema, physicalName, relationKind string
	err := tx.QueryRow(ctx, `SELECT owner.id::text,version.id::text,
			version.layer,version.schema_hash,
			materialization.id::text,materialization.build_run_id::text,
			materialization.layer,materialization.relation_kind,
			materialization.physical_schema,materialization.physical_name,
			materialization.published_schema,materialization.published_name,
			materialization.schema_hash,materialization.snapshot_hash
		FROM platform.dataset_versions AS version
		JOIN platform.datasets AS owner
		  ON owner.id=version.dataset_id AND owner.tenant_id=version.tenant_id
		JOIN platform.dataset_materializations AS materialization
		  ON materialization.dataset_id=owner.id
		 AND materialization.dataset_version_id=version.id
		 AND materialization.tenant_id=owner.tenant_id
		 AND materialization.status='ACTIVE'
		WHERE version.id::text=$1
		  AND version.status='PUBLISHED'
		  AND owner.status='PUBLISHED'
		  AND owner.current_published_version_id=version.id
		  AND owner.deleted_at IS NULL
		FOR SHARE OF version,owner,materialization`,
		node.DatasetVersionID).
		Scan(
			&binding.DatasetID, &binding.DatasetVersionID,
			&versionLayer, &versionHash,
			&binding.MaterializationID, &buildRunID,
			&materializationLayer, &relationKind,
			&physicalSchema, &physicalName,
			&binding.PublishedSchema, &binding.PublishedName,
			&binding.SchemaHash, &binding.SnapshotHash,
		)
	if errors.Is(err, pgx.ErrNoRows) {
		return ResolvedMaterialization{}, querycompiler.TableRef{}, dataset.ErrPreviewUnsupported
	}
	if err != nil {
		return ResolvedMaterialization{}, querycompiler.TableRef{}, err
	}
	binding.Layer = versionLayer
	if versionLayer != expectedLayer ||
		materializationLayer != expectedLayer ||
		expectedDatasetID != "" && binding.DatasetID != expectedDatasetID ||
		binding.SchemaHash != versionHash ||
		relationKind != "TABLE" && relationKind != "PARTITIONED_TABLE" {
		return ResolvedMaterialization{}, querycompiler.TableRef{}, dataset.ErrPreviewUnsupported
	}
	identifier := materialization.PhysicalIdentifier{
		Schema: physicalSchema, Name: physicalName,
		PublishedSchema: binding.PublishedSchema,
		PublishedName:   binding.PublishedName,
	}
	if err := materialization.ValidatePhysicalIdentifier(
		identifier, tenantID, binding.DatasetID, buildRunID,
		materialization.Layer(binding.Layer),
	); err != nil {
		return ResolvedMaterialization{}, querycompiler.TableRef{}, dataset.ErrPreviewUnsupported
	}
	var publishedKind string
	if err := tx.QueryRow(ctx, `SELECT class.relkind::text
		FROM pg_class AS class
		JOIN pg_namespace AS namespace ON namespace.oid=class.relnamespace
		WHERE namespace.nspname=$1 AND class.relname=$2`,
		binding.PublishedSchema, binding.PublishedName).Scan(&publishedKind); errors.Is(err, pgx.ErrNoRows) {
		return ResolvedMaterialization{}, querycompiler.TableRef{}, dataset.ErrPreviewUnsupported
	} else if err != nil {
		return ResolvedMaterialization{}, querycompiler.TableRef{}, err
	}
	if publishedKind != "v" {
		return ResolvedMaterialization{}, querycompiler.TableRef{}, dataset.ErrPreviewUnsupported
	}

	available, types, err := loadMaterializedColumnsTx(
		ctx, tx, binding.DatasetVersionID,
	)
	if err != nil {
		return ResolvedMaterialization{}, querycompiler.TableRef{}, err
	}
	columns := make(map[string]bool, len(node.Projection))
	columnTypes := make(map[string]string, len(node.Projection))
	for _, projected := range node.Projection {
		canonicalType, found := types[projected]
		if !found || !available[projected] {
			return ResolvedMaterialization{}, querycompiler.TableRef{}, dataset.ErrInvalidDocument
		}
		columns[projected] = true
		columnTypes[projected] = canonicalType
	}
	return binding, querycompiler.TableRef{
		NodeID: node.ID, Schema: binding.PublishedSchema,
		Name: binding.PublishedName, Columns: columns, ColumnTypes: columnTypes,
	}, nil
}

func loadMaterializedColumnsTx(
	ctx context.Context,
	tx pgx.Tx,
	versionID string,
) (map[string]bool, map[string]string, error) {
	rows, err := tx.Query(ctx, `SELECT field_code,canonical_type
		FROM platform.dataset_fields
		WHERE dataset_version_id::text=$1
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
		return nil, nil, dataset.ErrPreviewUnsupported
	}
	return columns, types, nil
}

// Start 创建 RUNNING 审计记录，哈希字段用于关联相同执行计划但不泄露敏感值。
func (s *PostgresStore) Start(ctx context.Context, run RunRecord) error {
	if run.RunType == "" {
		run.RunType = "PREVIEW"
	}
	if run.RunType != "PREVIEW" && run.RunType != "VALIDATION" && run.RunType != "COMPONENT_PREVIEW" {
		return dataset.ErrPreviewInvalid
	}
	if run.ExecutionEngine == "" {
		run.ExecutionEngine = ExecutionSource
	}
	switch run.ExecutionEngine {
	case ExecutionSource:
		if run.SourceID == "" || len(run.Materializations) != 0 {
			return dataset.ErrPreviewInvalid
		}
	case ExecutionPostgreSQL:
		if run.SourceID != "" || len(run.Sources) != 0 ||
			len(run.Materializations) == 0 {
			return dataset.ErrPreviewInvalid
		}
	default:
		return dataset.ErrPreviewInvalid
	}
	if run.CandidateCode != "" {
		if run.DatasetID != "" || run.DatasetVersionID != "" || run.RunType != "COMPONENT_PREVIEW" {
			return dataset.ErrPreviewInvalid
		}
		return s.startCandidate(ctx, run)
	}
	err := database.WithTenantTx(ctx, s.pool, run.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO platform.query_runs(
				id,tenant_id,dataset_id,dataset_version_id,
				metric_id,metric_version_id,actor_user_id,data_source_id,
				execution_engine,run_type,plan_hash,parameter_hash,status
			)
			VALUES(
				$1,$2,$3,$4,NULLIF($5,'')::uuid,NULLIF($6,'')::uuid,
				$7,NULLIF($8,'')::uuid,$9,$10,$11,$12,'RUNNING'
			)`,
			run.ID, run.TenantID, run.DatasetID, run.DatasetVersionID, run.MetricID, run.MetricVersionID,
			run.ActorID, run.SourceID, run.ExecutionEngine, run.RunType,
			run.PlanHash, run.ParameterHash)
		if err != nil {
			return err
		}
		for _, source := range run.Sources {
			if _, err := tx.Exec(ctx, `INSERT INTO platform.query_run_sources(query_run_id,tenant_id,node_id,data_source_id,subquery_id,source_version,source_watermark,file_version_id)
				VALUES($1,$2,$3,$4,$5,$6,$7,NULLIF($8,'')::uuid)`, run.ID, run.TenantID, source.NodeID, source.SourceID, source.SubqueryID, source.SourceVersion, source.Watermark, source.FileVersionID); err != nil {
				return err
			}
		}
		return insertMaterializationSnapshotsTx(
			ctx, tx, "platform.query_run_materializations", run,
		)
	})
	var pgError *pgconn.PgError
	if errors.As(err, &pgError) && pgError.Code == "23505" {
		return dataset.ErrQueryConflict
	}
	if errors.As(err, &pgError) && pgError.Code == "23503" &&
		pgError.ConstraintName == "query_runs_metric_version_dataset_tenant_snapshot_check" {
		return metric.ErrVersionUnavailable
	}
	return err
}

func (s *PostgresStore) startCandidate(ctx context.Context, run RunRecord) error {
	err := database.WithTenantTx(ctx, s.pool, run.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO platform.query_candidate_runs(
				id,tenant_id,candidate_code,actor_user_id,data_source_id,
				execution_engine,run_type,plan_hash,parameter_hash,status
			)
			VALUES($1,$2,$3,$4,NULLIF($5,'')::uuid,$6,$7,$8,$9,'RUNNING')`,
			run.ID, run.TenantID, run.CandidateCode, run.ActorID,
			run.SourceID, run.ExecutionEngine, run.RunType,
			run.PlanHash, run.ParameterHash); err != nil {
			return err
		}
		for _, source := range run.Sources {
			if _, err := tx.Exec(ctx, `INSERT INTO platform.query_candidate_run_sources(query_run_id,tenant_id,node_id,data_source_id,subquery_id,source_version,source_watermark,file_version_id)
				VALUES($1,$2,$3,$4,$5,$6,$7,NULLIF($8,'')::uuid)`, run.ID, run.TenantID, source.NodeID, source.SourceID, source.SubqueryID, source.SourceVersion, source.Watermark, source.FileVersionID); err != nil {
				return err
			}
		}
		return insertMaterializationSnapshotsTx(
			ctx, tx, "platform.query_candidate_run_materializations", run,
		)
	})
	var pgError *pgconn.PgError
	if errors.As(err, &pgError) && pgError.Code == "23505" {
		return dataset.ErrQueryConflict
	}
	return err
}

func insertMaterializationSnapshotsTx(
	ctx context.Context,
	tx pgx.Tx,
	table string,
	run RunRecord,
) error {
	if table != "platform.query_run_materializations" &&
		table != "platform.query_candidate_run_materializations" {
		return dataset.ErrPreviewInvalid
	}
	for _, binding := range run.Materializations {
		tag, err := tx.Exec(ctx, `INSERT INTO `+table+`(
				query_run_id,tenant_id,node_id,dataset_id,dataset_version_id,
				materialization_id,layer,published_schema,published_name,
				schema_hash,snapshot_hash
			)
			SELECT $1,$2,$3,materialization.dataset_id,
				materialization.dataset_version_id,materialization.id,
				materialization.layer,materialization.published_schema,
				materialization.published_name,materialization.schema_hash,
				materialization.snapshot_hash
			FROM platform.dataset_materializations AS materialization
			JOIN platform.dataset_versions AS version
			  ON version.id=materialization.dataset_version_id
			 AND version.dataset_id=materialization.dataset_id
			 AND version.tenant_id=materialization.tenant_id
			JOIN platform.datasets AS owner
			  ON owner.id=version.dataset_id
			 AND owner.tenant_id=version.tenant_id
			WHERE materialization.id=$4
			  AND materialization.dataset_id=$5
			  AND materialization.dataset_version_id=$6
			  AND materialization.layer=$7
			  AND materialization.published_schema=$8
			  AND materialization.published_name=$9
			  AND materialization.schema_hash=$10
			  AND materialization.snapshot_hash=$11
			  AND materialization.status='ACTIVE'
			  AND version.status='PUBLISHED'
			  AND owner.status='PUBLISHED'
			  AND owner.current_published_version_id=version.id
			  AND owner.deleted_at IS NULL`,
			run.ID, run.TenantID, binding.NodeID,
			binding.MaterializationID, binding.DatasetID,
			binding.DatasetVersionID, binding.Layer,
			binding.PublishedSchema, binding.PublishedName,
			binding.SchemaHash, binding.SnapshotHash)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return dataset.ErrVersionUnavailable
		}
	}
	return nil
}

// Finish 只允许结束仍在运行的记录，取消和原请求回写并发时不会互相覆盖。

func (s *PostgresStore) Finish(ctx context.Context, tenantID, queryID, status string, rowCount int, durationMS int64, errorCode string, warnings []datasource.QueryWarning, sourceStats []datasource.QuerySourceStat) error {
	if warnings == nil {
		warnings = []datasource.QueryWarning{}
	}
	warningsJSON, err := json.Marshal(warnings)
	if err != nil {
		return err
	}
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE platform.query_runs SET status=$1,row_count=$2,duration_ms=$3,error_code=$4,warnings_json=$5,completed_at=now() WHERE id::text=$6 AND status='RUNNING'`, status, rowCount, durationMS, errorCode, warningsJSON, queryID)
		if err != nil {
			return err
		}
		candidate := false
		if tag.RowsAffected() != 1 {
			tag, err = tx.Exec(ctx, `UPDATE platform.query_candidate_runs SET status=$1,row_count=$2,duration_ms=$3,error_code=$4,warnings_json=$5,completed_at=now() WHERE id::text=$6 AND status='RUNNING'`, status, rowCount, durationMS, errorCode, warningsJSON, queryID)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return dataset.ErrQueryNotFound
			}
			candidate = true
		}
		sourceTable := "platform.query_run_sources"
		if candidate {
			sourceTable = "platform.query_candidate_run_sources"
		}
		for _, stat := range sourceStats {
			tag, err = tx.Exec(ctx, `UPDATE `+sourceTable+` SET status=$1,row_count=$2,duration_ms=$3
				WHERE query_run_id::text=$4 AND tenant_id::text=$5 AND node_id=$6 AND subquery_id::text=$7 AND status='RUNNING'`,
				stat.Status, stat.RowCount, stat.DurationMS, queryID, tenantID, stat.NodeID, stat.SubqueryID)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return errors.New("query source runtime stat does not match a running source")
			}
		}
		if status == "SUCCEEDED" {
			var missing int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM `+sourceTable+` WHERE query_run_id::text=$1 AND status='RUNNING'`, queryID).Scan(&missing); err != nil {
				return err
			}
			if missing > 0 {
				return errors.New("successful federated query is missing source runtime stats")
			}
			return nil
		}
		// 失败、超时或取消时，尚未返回指标的节点跟随主查询终态收口。
		_, err = tx.Exec(ctx, `UPDATE platform.query_run_sources SET status=$1 WHERE query_run_id::text=$2 AND status='RUNNING'`, status, queryID)
		return err
	})
}

// CancellableSources 确认查询归属，并返回各节点持久化的子查询取消标识。
func (s *PostgresStore) CancellableSources(ctx context.Context, tenantID, actorID, datasetID, queryID string) (out []RunSourceRecord, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var primarySourceID string
		if err := tx.QueryRow(ctx, `SELECT COALESCE(data_source_id::text,'')
			FROM platform.query_runs
			WHERE id::text=$1 AND actor_user_id::text=$2
			  AND dataset_id::text=$3 AND status='RUNNING'`,
			queryID, actorID, datasetID).Scan(&primarySourceID); errors.Is(err, pgx.ErrNoRows) {
			return dataset.ErrQueryNotFound
		} else if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT q.node_id,q.data_source_id::text,d.source_type,q.subquery_id::text,COALESCE(q.file_version_id::text,''),q.source_version,q.source_watermark
			FROM platform.query_run_sources q JOIN platform.data_sources d ON d.id=q.data_source_id AND d.tenant_id=q.tenant_id
			WHERE q.query_run_id::text=$1 ORDER BY q.node_id`, queryID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var item RunSourceRecord
			if err := rows.Scan(&item.NodeID, &item.SourceID, &item.SourceType, &item.SubqueryID, &item.FileVersionID, &item.SourceVersion, &item.Watermark); err != nil {
				rows.Close()
				return err
			}
			out = append(out, item)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if len(out) == 0 && primarySourceID != "" {
			// 兼容迁移前以及直接写入的单源运行记录。
			var item RunSourceRecord
			if err := tx.QueryRow(ctx, `SELECT d.id::text,d.source_type,d.version,COALESCE(d.last_synced_at::text,'') FROM platform.data_sources d WHERE d.id::text=$1`, primarySourceID).
				Scan(&item.SourceID, &item.SourceType, &item.SourceVersion, &item.Watermark); err != nil {
				return err
			}
			item.SubqueryID = queryID
			out = append(out, item)
		}
		return nil
	})
	return out, err
}
