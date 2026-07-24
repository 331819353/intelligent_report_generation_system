package datasource

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
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

const sourceProjection = `source.id::text,source.tenant_id::text,source.code::text,source.name,
	COALESCE(source.description,''),COALESCE(source.owner_user_id::text,''),source.visibility::text,
	config_version.source_type::text,source.status::text,config_version.config,
	COALESCE(config_version.secret_ref,''),COALESCE(config_version.file_asset_id::text,''),
	COALESCE(config_version.file_version_id::text,''),config_version.id::text,
	COALESCE(source.current_published_version_id::text,''),config_version.version_no,
	COALESCE(published_version.version_no,0),config_version.config_hash,
	source.validation_status,source.publication_status,
	source.current_draft_version_id IS DISTINCT FROM source.current_published_version_id,
	source.last_tested_at,source.test_expires_at,
	COALESCE(source.created_by::text,''),COALESCE(source.updated_by::text,''),
	source.created_at,source.updated_at,source.version`

const draftSourceFrom = ` FROM platform.data_sources AS source
	JOIN platform.data_source_versions AS config_version
	  ON config_version.id=source.current_draft_version_id
	LEFT JOIN platform.data_source_versions AS published_version
	  ON published_version.id=source.current_published_version_id`

const runtimeSourceFrom = ` FROM platform.data_sources AS source
	JOIN platform.data_source_versions AS config_version
	  ON config_version.id=COALESCE(source.current_published_version_id,source.current_draft_version_id)
	LEFT JOIN platform.data_source_versions AS published_version
	  ON published_version.id=source.current_published_version_id`

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
	if s.ID == "" {
		s.ID = uuid.NewString()
	}
	if s.Visibility == "" {
		s.Visibility = VisibilityPrivate
	}
	if s.OwnerID == "" {
		s.OwnerID = s.CreatedBy
	}
	configVersionID := uuid.NewString()
	err = database.WithTenantTx(ctx, r.pool, s.TenantID, func(tx pgx.Tx) error {
		if s.Type == TypeExcel {
			if err := tx.QueryRow(ctx, `SELECT version.id::text
				FROM platform.file_assets AS asset
				JOIN platform.file_asset_versions AS version
				  ON version.file_asset_id=asset.id AND version.tenant_id=asset.tenant_id
				 AND version.version=asset.current_version
				WHERE asset.id=$1 AND asset.deleted_at IS NULL`, s.FileAssetID).Scan(&s.FileVersionID); err != nil {
				return err
			}
		}
		s.ConfigHash, err = sourceConfigurationHash(s)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.data_sources(
				id,tenant_id,code,name,description,owner_user_id,visibility,source_type,status,config,
				secret_ref,file_asset_id,created_by,updated_by,validation_status,publication_status,
				current_draft_version_id)
			VALUES($1,$2,$3,$4,$5,NULLIF($6,'')::uuid,$7,$8,$9,$10,NULLIF($11,''),NULLIF($12,'')::uuid,
				NULLIF($13,'')::uuid,NULLIF($14,'')::uuid,'UNTESTED','UNPUBLISHED',$15)`,
			s.ID, s.TenantID, s.Code, s.Name, s.Description, s.OwnerID, s.Visibility, s.Type,
			StatusDraft, config, s.SecretRef, s.FileAssetID, s.CreatedBy, s.UpdatedBy, configVersionID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.data_source_versions(
				id,tenant_id,data_source_id,version_no,source_type,config,secret_ref,file_asset_id,
				file_version_id,config_hash,created_by)
			VALUES($1,$2,$3,1,$4,$5,NULLIF($6,''),NULLIF($7,'')::uuid,NULLIF($8,'')::uuid,$9,NULLIF($10,'')::uuid)`,
			configVersionID, s.TenantID, s.ID, s.Type, config, s.SecretRef, s.FileAssetID,
			s.FileVersionID, s.ConfigHash, s.CreatedBy); err != nil {
			return err
		}
		return r.scanDraftSource(ctx, tx, s.ID, &s)
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
	out = []Source{}
	err = database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+sourceProjection+draftSourceFrom+`
			WHERE source.deleted_at IS NULL ORDER BY source.created_at DESC`)
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
		return scanSource(tx.QueryRow(ctx, `SELECT `+sourceProjection+runtimeSourceFrom+`
			WHERE source.id=$1 AND source.deleted_at IS NULL`, id), &s)
	})
	return
}

// GetDraft 返回管理面当前可编辑版本；Repository.Get 则保留给运行时并优先读取发布版本。
func (r *PostgresRepository) GetDraft(ctx context.Context, tenantID, id string) (s Source, err error) {
	err = database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		return r.scanDraftSource(ctx, tx, id, &s)
	})
	return
}

// Update 使用版本号执行乐观锁更新，防止覆盖并发修改。
func (r *PostgresRepository) Update(ctx context.Context, s Source) (Source, error) {
	config, err := json.Marshal(s.Config)
	if err != nil {
		return Source{}, err
	}
	expectedVersion := s.Version
	if expectedVersion < 1 {
		return Source{}, ErrVersionConflict
	}
	err = database.WithTenantTx(ctx, r.pool, s.TenantID, func(tx pgx.Tx) error {
		var nextVersion int64
		if err := tx.QueryRow(ctx, `SELECT
				(SELECT COALESCE(max(version_no),0)+1 FROM platform.data_source_versions WHERE data_source_id=source.id)
				FROM platform.data_sources AS source
				WHERE source.id=$1 AND source.version=$2 AND source.deleted_at IS NULL FOR UPDATE`, s.ID, expectedVersion).
			Scan(&nextVersion); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrVersionConflict
			}
			return err
		}
		if s.Type == TypeExcel {
			if err := tx.QueryRow(ctx, `SELECT version.id::text
				FROM platform.file_assets AS asset
				JOIN platform.file_asset_versions AS version
				  ON version.file_asset_id=asset.id AND version.tenant_id=asset.tenant_id
				 AND version.version=asset.current_version
				WHERE asset.id=$1 AND asset.deleted_at IS NULL`, s.FileAssetID).Scan(&s.FileVersionID); err != nil {
				return err
			}
		} else {
			s.FileVersionID = ""
		}
		s.ConfigHash, err = sourceConfigurationHash(s)
		if err != nil {
			return err
		}
		configVersionID := uuid.NewString()
		if _, err := tx.Exec(ctx, `INSERT INTO platform.data_source_versions(
				id,tenant_id,data_source_id,version_no,source_type,config,secret_ref,file_asset_id,
				file_version_id,config_hash,created_by)
			VALUES($1,$2,$3,$4,$5,$6,NULLIF($7,''),NULLIF($8,'')::uuid,NULLIF($9,'')::uuid,$10,NULLIF($11,'')::uuid)`,
			configVersionID, s.TenantID, s.ID, nextVersion, s.Type, config, s.SecretRef,
			s.FileAssetID, s.FileVersionID, s.ConfigHash, s.UpdatedBy); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE platform.data_sources SET
				code=$1,name=$2,description=$3,owner_user_id=NULLIF($4,'')::uuid,visibility=$5,
				source_type=CASE WHEN current_published_version_id IS NULL THEN $6 ELSE source_type END,
				config=CASE WHEN current_published_version_id IS NULL THEN $7 ELSE config END,
				secret_ref=CASE WHEN current_published_version_id IS NULL THEN NULLIF($8,'') ELSE secret_ref END,
				file_asset_id=CASE WHEN current_published_version_id IS NULL THEN NULLIF($9,'')::uuid ELSE file_asset_id END,
				current_draft_version_id=$10,validation_status='UNTESTED',
				last_tested_version_id=NULL,last_tested_config_hash=NULL,test_expires_at=NULL,last_tested_at=NULL,
				status=CASE WHEN current_published_version_id IS NULL THEN 'DRAFT'::platform.data_source_status ELSE status END,
				last_error=CASE WHEN current_published_version_id IS NULL THEN NULL ELSE last_error END,
				updated_by=NULLIF($11,'')::uuid,version=version+1
			WHERE id=$12 AND version=$13 AND deleted_at IS NULL`,
			s.Code, s.Name, s.Description, s.OwnerID, s.Visibility, s.Type, config, s.SecretRef,
			s.FileAssetID, configVersionID, s.UpdatedBy, s.ID, expectedVersion)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrVersionConflict
		}
		return r.scanDraftSource(ctx, tx, s.ID, &s)
	})
	if dataSourceCodeConflict(err) {
		return Source{}, ErrCodeConflict
	}
	return s, err
}

// RecordConnectionTest 保存精确版本测试证据。测试期间如果草稿指针或摘要变化，
// 整个事务回滚，旧配置上的成功结果不能污染新草稿。
func (r *PostgresRepository) RecordConnectionTest(ctx context.Context, tenantID, id string, run ConnectionTestRun) (ConnectionTestRun, error) {
	err := database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		if run.ID == "" {
			run.ID = uuid.NewString()
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.data_source_test_runs(
				id,tenant_id,data_source_id,data_source_version_id,config_hash,status,server_version,
				latency_ms,error_message,started_at,completed_at,expires_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
			run.ID, tenantID, id, run.ConfigVersion, run.ConfigHash, run.Status,
			run.ServerVersion, run.LatencyMS, run.ErrorMessage, run.StartedAt, run.CompletedAt, run.ExpiresAt); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE platform.data_sources SET
				validation_status=$1,last_tested_at=$2,last_tested_version_id=$3,
				last_tested_config_hash=$4,test_expires_at=$5,
				status=CASE
					WHEN current_published_version_id IS NULL AND $1='FAILED'
					  THEN 'ERROR'::platform.data_source_status
					WHEN current_published_version_id IS NULL
					  THEN 'DRAFT'::platform.data_source_status
					ELSE status
				END,
				last_error=CASE
					WHEN current_published_version_id IS NULL AND $1='FAILED' THEN 'connection test failed'
					WHEN current_published_version_id IS NULL THEN NULL
					ELSE last_error
				END
			WHERE id=$6 AND deleted_at IS NULL
			  AND current_draft_version_id=$3
			  AND EXISTS(
			    SELECT 1 FROM platform.data_source_versions
			    WHERE id=$3 AND data_source_id=$6 AND config_hash=$4
			  )`,
			run.Status, run.CompletedAt, run.ConfigVersion, run.ConfigHash, run.ExpiresAt, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrSourceVersionChanged
		}
		return nil
	})
	return run, err
}

// Publish 原子校验当前草稿及其未过期测试证据，再切换运行时发布指针。
func (r *PostgresRepository) Publish(ctx context.Context, tenantID, id, actorID, configVersionID, configHash string, _ time.Time) (published Source, err error) {
	err = database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		var currentVersionID, currentHash, currentPublishedVersionID string
		if err := tx.QueryRow(ctx, `SELECT source.current_draft_version_id::text,
				version.config_hash,COALESCE(source.current_published_version_id::text,'')
			FROM platform.data_sources AS source
			JOIN platform.data_source_versions AS version ON version.id=source.current_draft_version_id
			WHERE source.id=$1 AND source.deleted_at IS NULL FOR UPDATE`, id).
			Scan(&currentVersionID, &currentHash, &currentPublishedVersionID); err != nil {
			return err
		}
		if currentVersionID != configVersionID || currentHash != configHash {
			return ErrSourceVersionChanged
		}
		// 已发布的同一精确版本是安全幂等重放，不重新解释已经过期的测试记录。
		if currentPublishedVersionID == currentVersionID {
			return r.scanDraftSource(ctx, tx, id, &published)
		}
		var expiresAt time.Time
		var valid bool
		testErr := tx.QueryRow(ctx, `SELECT attestation.expires_at,
				attestation.expires_at>clock_timestamp()
			FROM platform.data_source_connection_test_attestations AS attestation
			JOIN platform.data_source_connection_test_jobs AS job
			  ON job.id=attestation.connection_test_job_id
			 AND job.tenant_id=attestation.tenant_id
			WHERE attestation.data_source_id=$1
			  AND attestation.data_source_version_id=$2
			  AND attestation.config_hash=$3
			  AND attestation.attestation_version='connection-test-worker-v1'
			  AND job.status='SUCCEEDED'
			  AND job.data_source_id=attestation.data_source_id
			  AND job.data_source_version_id=attestation.data_source_version_id
			  AND job.config_hash=attestation.config_hash
			ORDER BY attestation.completed_at DESC
			LIMIT 1`, id, currentVersionID, currentHash).Scan(&expiresAt, &valid)
		if errors.Is(testErr, pgx.ErrNoRows) {
			return ErrTestRequired
		}
		if testErr != nil {
			return testErr
		}
		if !valid {
			return ErrTestExpired
		}
		tag, err := tx.Exec(ctx, `UPDATE platform.data_sources AS source SET
				current_published_version_id=source.current_draft_version_id,
				publication_status='PUBLISHED',
				source_type=version.source_type,config=version.config,secret_ref=version.secret_ref,
				file_asset_id=version.file_asset_id,
				status=CASE WHEN source.status='DISABLED' THEN source.status ELSE 'ACTIVE'::platform.data_source_status END,
				last_error=NULL,updated_by=NULLIF($1,'')::uuid,version=source.version+1
			FROM platform.data_source_versions AS version
			WHERE source.id=$2 AND source.current_draft_version_id=$3
			  AND version.id=source.current_draft_version_id AND source.deleted_at IS NULL`,
			actorID, id, currentVersionID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrSourceVersionChanged
		}
		return r.scanDraftSource(ctx, tx, id, &published)
	})
	return published, err
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

func (r *PostgresRepository) scanDraftSource(ctx context.Context, tx pgx.Tx, id string, s *Source) error {
	return scanSource(tx.QueryRow(ctx, `SELECT `+sourceProjection+draftSourceFrom+`
		WHERE source.id=$1 AND source.deleted_at IS NULL`, id), s)
}

// scanSource 统一数据库列到数据源模型的映射。
func scanSource(row rowScanner, s *Source) error {
	var config []byte
	if err := row.Scan(
		&s.ID, &s.TenantID, &s.Code, &s.Name, &s.Description, &s.OwnerID, &s.Visibility,
		&s.Type, &s.Status, &config, &s.SecretRef, &s.FileAssetID, &s.FileVersionID,
		&s.ConfigVersionID, &s.PublishedVersionID, &s.ConfigVersion, &s.PublishedConfigVersion,
		&s.ConfigHash, &s.ValidationStatus, &s.PublicationStatus, &s.HasUnpublishedChanges,
		&s.LastTestedAt, &s.TestExpiresAt, &s.CreatedBy, &s.UpdatedBy,
		&s.CreatedAt, &s.UpdatedAt, &s.Version,
	); err != nil {
		return err
	}
	return json.Unmarshal(config, &s.Config)
}
