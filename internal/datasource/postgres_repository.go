package datasource

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/platform/database"
)

// Audit 写入数据源操作审计事件。
func (r *PostgresRepository) Audit(ctx context.Context, tenantID, actorID, action, resourceID string, detail any) error {
	payload, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	return database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO platform.audit_logs(tenant_id,actor_user_id,action,resource_type,resource_id,detail) VALUES($1,$2,$3,'DATA_SOURCE',$4,$5)`, tenantID, actorID, action, resourceID, payload)
		return err
	})
}

type PostgresRepository struct{ pool *pgxpool.Pool }

// NewPostgresRepository 创建数据源及元数据的 PostgreSQL 仓储。
func NewPostgresRepository(pool *pgxpool.Pool) *PostgresRepository {
	return &PostgresRepository{pool: pool}
}

const sourceColumns = `id,tenant_id,code,name,source_type,status,config,COALESCE(secret_ref,''),COALESCE(file_asset_id::text,''),version`

// Count 统计租户下占用配额的有效数据源数量。
func (r *PostgresRepository) Count(ctx context.Context, tenantID string) (count int, err error) {
	err = database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM platform.data_sources WHERE deleted_at IS NULL`).Scan(&count)
	})
	return
}

// Create 持久化新数据源并返回数据库生成的标识和版本。
func (r *PostgresRepository) Create(ctx context.Context, s Source) (Source, error) {
	config, err := json.Marshal(s.Config)
	if err != nil {
		return Source{}, err
	}
	err = database.WithTenantTx(ctx, r.pool, s.TenantID, func(tx pgx.Tx) error {
		return scanSource(tx.QueryRow(ctx, `INSERT INTO platform.data_sources(tenant_id,code,name,source_type,status,config,secret_ref,file_asset_id) VALUES($1,$2,$3,$4,$5,$6,NULLIF($7,''),NULLIF($8,'')::uuid) RETURNING `+sourceColumns, s.TenantID, s.Code, s.Name, s.Type, s.Status, config, s.SecretRef, s.FileAssetID), &s)
	})
	if dataSourceCodeConflict(err) {
		return Source{}, ErrCodeConflict
	}
	return s, err
}

func dataSourceCodeConflict(err error) bool {
	var databaseError *pgconn.PgError
	if !errors.As(err, &databaseError) || databaseError.Code != "23505" {
		return false
	}
	return databaseError.ConstraintName == "data_sources_tenant_code_active_key" || databaseError.ConstraintName == "data_sources_tenant_id_code_key"
}

// List 查询租户下未软删除的数据源。
func (r *PostgresRepository) List(ctx context.Context, tenantID string) (out []Source, err error) {
	err = database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+sourceColumns+` FROM platform.data_sources WHERE deleted_at IS NULL ORDER BY created_at DESC`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var s Source
			if err := scanSource(rows, &s); err != nil {
				return err
			}
			out = append(out, s)
		}
		return rows.Err()
	})
	return
}

// Get 在租户边界内加载单个数据源。
func (r *PostgresRepository) Get(ctx context.Context, tenantID, id string) (s Source, err error) {
	err = database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		return scanSource(tx.QueryRow(ctx, `SELECT `+sourceColumns+` FROM platform.data_sources WHERE id=$1 AND deleted_at IS NULL`, id), &s)
	})
	return
}

// Update 使用版本号执行乐观锁更新，防止覆盖并发修改。
func (r *PostgresRepository) Update(ctx context.Context, s Source) (Source, error) {
	config, err := json.Marshal(s.Config)
	if err != nil {
		return Source{}, err
	}
	err = database.WithTenantTx(ctx, r.pool, s.TenantID, func(tx pgx.Tx) error {
		return scanSource(tx.QueryRow(ctx, `UPDATE platform.data_sources SET code=$1,name=$2,source_type=$3,status=$4,config=$5,secret_ref=NULLIF($6,''),file_asset_id=NULLIF($7,'')::uuid,last_error=NULL,version=version+1 WHERE id=$8 AND deleted_at IS NULL RETURNING `+sourceColumns, s.Code, s.Name, s.Type, s.Status, config, s.SecretRef, s.FileAssetID, s.ID), &s)
	})
	return s, err
}

// UpdateStatus 原子更新生命周期状态和探测信息。只有生命周期状态真正变化时才
// 推进版本；对 ACTIVE 数据源重复执行成功的连通性测试只更新 last_tested_at，
// 不应让正在进行的元数据 LLM 任务误判为配置已发生变化。
func (r *PostgresRepository) UpdateStatus(ctx context.Context, tenantID, id string, status Status, message string) error {
	return database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE platform.data_sources SET status=$1::platform.data_source_status,last_error=NULLIF($2,''),last_tested_at=CASE WHEN $1::platform.data_source_status IN ('ACTIVE','ERROR') THEN now() ELSE last_tested_at END,last_synced_at=CASE WHEN $1::platform.data_source_status='ACTIVE' AND status='SYNCING' THEN now() ELSE last_synced_at END,deleted_at=CASE WHEN $1::platform.data_source_status='DELETED' THEN now() ELSE deleted_at END,version=version+CASE WHEN status IS DISTINCT FROM $1::platform.data_source_status THEN 1 ELSE 0 END WHERE id=$3`, status, message, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return pgx.ErrNoRows
		}
		return nil
	})
}

// Quota 加载租户的数据源数量、查询行数和文件大小限制。
func (r *PostgresRepository) Quota(ctx context.Context, tenantID string) (q Quota, err error) {
	err = database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT COALESCE(q.max_data_sources,20),COALESCE(q.max_connections_per_source,5),COALESCE(q.max_concurrent_queries,10),COALESCE(q.max_excel_file_bytes,52428800) FROM (SELECT $1::uuid tenant_id) x LEFT JOIN platform.tenant_data_source_quotas q USING(tenant_id)`, tenantID).Scan(&q.MaxDataSources, &q.MaxConnectionsPerSource, &q.MaxConcurrentQueries, &q.MaxExcelFileBytes)
	})
	return
}

type rowScanner interface{ Scan(...any) error }

// scanSource 统一数据库列到数据源模型的映射。
func scanSource(row rowScanner, s *Source) error {
	var config []byte
	if err := row.Scan(&s.ID, &s.TenantID, &s.Code, &s.Name, &s.Type, &s.Status, &config, &s.SecretRef, &s.FileAssetID, &s.Version); err != nil {
		return err
	}
	return json.Unmarshal(config, &s.Config)
}
