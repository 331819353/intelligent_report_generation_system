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
		result, err = resolveTx(ctx, tx, document)
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
		result, err = resolveTx(ctx, tx, document)
		if errors.Is(err, dataset.ErrInvalidDocument) || errors.Is(err, dataset.ErrPreviewUnsupported) {
			return dataset.ErrVersionUnavailable
		}
		return err
	})
	return result, err
}

func resolveTx(ctx context.Context, tx pgx.Tx, document dataset.Document) (ResolvedPlan, error) {
	result := ResolvedPlan{Tables: map[string]querycompiler.TableRef{}, Nodes: map[string]ResolvedNode{}}
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
				WHERE t.id::text=$1 AND t.data_source_id::text=$2 AND (d.source_type='EXCEL' OR t.asset_status='ACTIVE') AND d.deleted_at IS NULL`, node.TableID, node.DataSourceID).
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

// Start 创建 RUNNING 审计记录，哈希字段用于关联相同执行计划但不泄露敏感值。
func (s *PostgresStore) Start(ctx context.Context, run RunRecord) error {
	if run.RunType == "" {
		run.RunType = "PREVIEW"
	}
	if run.RunType != "PREVIEW" && run.RunType != "VALIDATION" {
		return dataset.ErrPreviewInvalid
	}
	err := database.WithTenantTx(ctx, s.pool, run.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO platform.query_runs(id,tenant_id,dataset_id,dataset_version_id,actor_user_id,data_source_id,run_type,plan_hash,parameter_hash,status)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'RUNNING')`, run.ID, run.TenantID, run.DatasetID, run.DatasetVersionID, run.ActorID, run.SourceID, run.RunType, run.PlanHash, run.ParameterHash)
		if err != nil {
			return err
		}
		for _, source := range run.Sources {
			if _, err := tx.Exec(ctx, `INSERT INTO platform.query_run_sources(query_run_id,tenant_id,node_id,data_source_id,subquery_id,source_version,source_watermark,file_version_id)
				VALUES($1,$2,$3,$4,$5,$6,$7,NULLIF($8,'')::uuid)`, run.ID, run.TenantID, source.NodeID, source.SourceID, source.SubqueryID, source.SourceVersion, source.Watermark, source.FileVersionID); err != nil {
				return err
			}
		}
		return nil
	})
	var pgError *pgconn.PgError
	if errors.As(err, &pgError) && pgError.Code == "23505" {
		return dataset.ErrQueryConflict
	}
	return err
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
		if tag.RowsAffected() != 1 {
			return dataset.ErrQueryNotFound
		}
		for _, stat := range sourceStats {
			tag, err = tx.Exec(ctx, `UPDATE platform.query_run_sources SET status=$1,row_count=$2,duration_ms=$3
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
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM platform.query_run_sources WHERE query_run_id::text=$1 AND status='RUNNING'`, queryID).Scan(&missing); err != nil {
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
		if err := tx.QueryRow(ctx, `SELECT data_source_id::text FROM platform.query_runs WHERE id::text=$1 AND actor_user_id::text=$2 AND dataset_id::text=$3 AND status='RUNNING'`, queryID, actorID, datasetID).Scan(&primarySourceID); errors.Is(err, pgx.ErrNoRows) {
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
		if len(out) == 0 {
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
