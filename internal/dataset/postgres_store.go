package dataset

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/platform/database"
)

// PostgresStore 使用事务和 RLS 保存数据集草稿及全部派生索引。
type PostgresStore struct{ pool *pgxpool.Pool }

// NewPostgresStore 创建数据集 PostgreSQL 仓储。
func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

// Create 原子创建数据集、首个草稿版本、字段参数索引和审计记录。
func (s *PostgresStore) Create(ctx context.Context, tenantID, actorID string, input CreateInput, prepared Prepared) (Record, error) {
	var datasetID, versionID string
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO platform.datasets(tenant_id,code,name,description,dataset_type,created_by,updated_by) VALUES($1,$2,$3,$4,$5,$6,$6) RETURNING id::text`, tenantID, input.Code, input.Name, input.Description, input.Type, actorID).Scan(&datasetID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO platform.dataset_versions(tenant_id,dataset_id,version_no,dsl_version,dsl_json,schema_hash,logical_plan_json,plan_hash,created_by,updated_by) VALUES($1,$2,1,$3,$4,$5,$6,$7,$8,$8) RETURNING id::text`, tenantID, datasetID, DSLVersion, prepared.DSLJSON, prepared.DSLHash, prepared.LogicalPlanJSON, prepared.PlanHash, actorID).Scan(&versionID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.datasets SET current_draft_version_id=$1 WHERE id=$2`, versionID, datasetID); err != nil {
			return err
		}
		if err := replaceDerived(ctx, tx, tenantID, datasetID, versionID, prepared.Document, true); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO platform.audit_logs(tenant_id,actor_user_id,action,resource_type,resource_id,detail) VALUES($1,$2,'CREATE','DATASET',$3,jsonb_build_object('dslHash',$4::text,'planHash',$5::text))`, tenantID, actorID, datasetID, prepared.DSLHash, prepared.PlanHash)
		return err
	})
	if err != nil {
		var pgError *pgconn.PgError
		if errors.As(err, &pgError) && pgError.Code == "23505" {
			return Record{}, ErrAlreadyExists
		}
		return Record{}, err
	}
	return s.Get(ctx, tenantID, datasetID)
}

// Get 读取租户内数据集和 current_draft_version_id 指向的规范草稿。
func (s *PostgresStore) Get(ctx context.Context, tenantID, id string) (record Record, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT d.id::text,d.code::text,d.name,d.description,d.dataset_type,d.status,d.version,
			v.id::text,v.version_no,v.record_version,COALESCE(d.current_published_version_id::text,''),
			v.dsl_version,v.schema_hash,v.plan_hash,v.dsl_json,v.logical_plan_json,d.created_at::text,d.updated_at::text
			FROM platform.datasets d JOIN platform.dataset_versions v
			ON v.id=d.current_draft_version_id AND v.tenant_id=d.tenant_id AND v.dataset_id=d.id
			WHERE d.id::text=$1 AND d.deleted_at IS NULL`, id).Scan(
			&record.ID, &record.Code, &record.Name, &record.Description, &record.Type, &record.Status, &record.Version,
			&record.DraftVersionID, &record.DraftVersionNo, &record.DraftRecordVersion, &record.CurrentPublishedVersionID,
			&record.DSLVersion, &record.DSLHash, &record.PlanHash, &record.DSL, &record.LogicalPlan,
			&record.CreatedAt, &record.UpdatedAt,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	return record, err
}

// List 按更新时间倒序返回数据集摘要和租户内总数。
func (s *PostgresStore) List(ctx context.Context, tenantID string, limit, offset int) (items []Summary, total int, err error) {
	items = []Summary{}
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM platform.datasets WHERE deleted_at IS NULL`).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT d.id::text,d.code::text,d.name,d.description,d.dataset_type,d.status,d.version,v.schema_hash,
			COALESCE(d.current_published_version_id::text,''),d.updated_at::text
			FROM platform.datasets d JOIN platform.dataset_versions v
			ON v.id=d.current_draft_version_id AND v.tenant_id=d.tenant_id AND v.dataset_id=d.id
			WHERE d.deleted_at IS NULL ORDER BY d.updated_at DESC,d.id LIMIT $1 OFFSET $2`, limit, offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item Summary
			if err := rows.Scan(&item.ID, &item.Code, &item.Name, &item.Description, &item.Type, &item.Status, &item.Version, &item.DSLHash, &item.CurrentPublishedVersionID, &item.UpdatedAt); err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, total, err
}

// Update 在行锁内校验版本并更新当前草稿，已发布版本不会被此路径修改。
func (s *PostgresStore) Update(ctx context.Context, tenantID, actorID, id string, input UpdateInput, prepared Prepared) (Record, error) {
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var version int64
		var versionID string
		// 行锁把版本检查、草稿覆盖和派生索引重建串成一个原子操作；expectedVersion
		// 防止后保存的浏览器静默覆盖另一位用户已经提交的草稿。
		err := tx.QueryRow(ctx, `SELECT version,current_draft_version_id::text FROM platform.datasets WHERE id::text=$1 AND deleted_at IS NULL FOR UPDATE`, id).Scan(&version, &versionID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if version != input.ExpectedVersion {
			return ErrConflict
		}
		result, err := tx.Exec(ctx, `UPDATE platform.dataset_versions SET dsl_json=$1,schema_hash=$2,logical_plan_json=$3,plan_hash=$4,record_version=record_version+1,updated_by=$5 WHERE id=$6 AND status='DRAFT'`, prepared.DSLJSON, prepared.DSLHash, prepared.LogicalPlanJSON, prepared.PlanHash, actorID, versionID)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return ErrConflict
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.datasets SET name=$1,description=$2,version=version+1,updated_by=$3 WHERE id::text=$4`, input.Name, input.Description, actorID, id); err != nil {
			return err
		}
		if err := replaceDerived(ctx, tx, tenantID, id, versionID, prepared.Document, true); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO platform.audit_logs(tenant_id,actor_user_id,action,resource_type,resource_id,detail) VALUES($1,$2,'UPDATE_DRAFT','DATASET',$3,jsonb_build_object('fromVersion',$4::bigint,'dslHash',$5::text,'planHash',$6::text))`, tenantID, actorID, id, version, prepared.DSLHash, prepared.PlanHash)
		return err
	})
	if err != nil {
		return Record{}, err
	}
	return s.Get(ctx, tenantID, id)
}

// ReplayPublication 在当前发布权限仍有效时精确重放首次成功响应。
func (s *PostgresStore) ReplayPublication(ctx context.Context, tenantID, actorID, datasetID, key, requestHash string) (record VersionRecord, found bool, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		allowed, err := datasetActionAllowedTx(ctx, tx, tenantID, actorID, datasetID, "PUBLISH")
		if err != nil {
			return err
		}
		if !allowed {
			return ErrForbidden
		}
		return replayPublicationTx(ctx, tx, actorID, datasetID, key, requestHash, &record, &found)
	})
	return record, found, err
}

// Publish 在同一数据集行锁内分配发布序号、复制不可变快照并移动当前发布指针。
func (s *PostgresStore) Publish(ctx context.Context, tenantID, actorID, datasetID string, plan PublishPlan) (record VersionRecord, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		allowed, err := datasetActionAllowedTx(ctx, tx, tenantID, actorID, datasetID, "PUBLISH")
		if err != nil {
			return err
		}
		if !allowed {
			return ErrForbidden
		}

		var datasetVersion, draftRecordVersion int64
		var draftVersionNo int
		var draftVersionID, draftDSLHash, draftPlanHash string
		err = tx.QueryRow(ctx, `SELECT d.version,v.id::text,v.version_no,v.record_version,v.schema_hash,v.plan_hash
			FROM platform.datasets d JOIN platform.dataset_versions v
			ON v.id=d.current_draft_version_id AND v.dataset_id=d.id AND v.tenant_id=d.tenant_id
			WHERE d.id::text=$1 AND d.deleted_at IS NULL FOR UPDATE OF d,v`, datasetID).
			Scan(&datasetVersion, &draftVersionID, &draftVersionNo, &draftRecordVersion, &draftDSLHash, &draftPlanHash)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		// 数据集行锁同时串行化相同幂等键和不同键的发布请求；锁后必须再次查重。
		var found bool
		if err := replayPublicationTx(ctx, tx, actorID, datasetID, plan.IdempotencyKey, plan.RequestHash, &record, &found); err != nil || found {
			return err
		}
		if datasetVersion != plan.ExpectedVersion || draftVersionID != plan.DraftVersionID ||
			draftRecordVersion != plan.ExpectedDraftRecordVersion || draftDSLHash != plan.ExpectedDSLHash ||
			plan.Prepared.DSLHash != draftDSLHash || plan.Prepared.PlanHash != draftPlanHash {
			return ErrConflict
		}
		if err := validateDependencySnapshotsTx(ctx, tx, draftVersionID); err != nil {
			return ErrPublishValidation
		}

		// 草稿的 version_no 表示下一待发布序号。先在事务内前移草稿序号，首个发布版本因此为 V1。
		if tag, err := tx.Exec(ctx, `UPDATE platform.dataset_versions SET version_no=version_no+1,updated_by=$1 WHERE id=$2 AND status='DRAFT' AND record_version=$3`, actorID, draftVersionID, draftRecordVersion); err != nil {
			return err
		} else if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		var publishedVersionID string
		if err := tx.QueryRow(ctx, `INSERT INTO platform.dataset_versions(
			tenant_id,dataset_id,version_no,status,dsl_version,dsl_json,schema_hash,logical_plan_json,plan_hash,
			record_version,created_by,updated_by,published_at,published_by,source_draft_version_id,source_draft_record_version
		) VALUES($1,$2,$3,'PUBLISHING',$4,$5,$6,$7,$8,1,$9,$9,now(),$9,$10,$11) RETURNING id::text`,
			tenantID, datasetID, draftVersionNo, DSLVersion, plan.Prepared.DSLJSON, plan.Prepared.DSLHash,
			plan.Prepared.LogicalPlanJSON, plan.Prepared.PlanHash, actorID, draftVersionID, draftRecordVersion).
			Scan(&publishedVersionID); err != nil {
			return err
		}
		if err := replaceDerived(ctx, tx, tenantID, datasetID, publishedVersionID, plan.Prepared.Document, false); err != nil {
			return err
		}
		if tag, err := tx.Exec(ctx, `UPDATE platform.dataset_versions SET status='PUBLISHED' WHERE id=$1 AND status='PUBLISHING'`, publishedVersionID); err != nil {
			return err
		} else if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.datasets SET current_published_version_id=$1,status='PUBLISHED',version=version+1,updated_by=$2 WHERE id=$3`, publishedVersionID, actorID, datasetID); err != nil {
			return err
		}
		if err := scanVersionTx(ctx, tx, datasetID, publishedVersionID, &record); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.audit_logs(tenant_id,actor_user_id,action,resource_type,resource_id,detail)
			VALUES($1,$2,'PUBLISH','DATASET',$3,jsonb_build_object('datasetVersionId',$4::text,'versionNo',$5::int,'draftVersionId',$6::text,'draftRecordVersion',$7::bigint,'dslHash',$8::text,'planHash',$9::text))`,
			tenantID, actorID, datasetID, publishedVersionID, draftVersionNo, draftVersionID, draftRecordVersion, plan.Prepared.DSLHash, plan.Prepared.PlanHash); err != nil {
			return err
		}
		responseJSON, err := json.Marshal(record)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO platform.dataset_publish_idempotency(
			tenant_id,dataset_id,actor_user_id,idempotency_key,request_hash,published_version_id,response_json
		) VALUES($1,$2,$3,$4,$5,$6,$7)`, tenantID, datasetID, actorID, plan.IdempotencyKey, plan.RequestHash, publishedVersionID, responseJSON)
		return err
	})
	if err != nil {
		return VersionRecord{}, mapPublicationPostgresError(err)
	}
	return record, nil
}

// GetVersion 按父数据集和版本 ID 精确读取发布快照。
func (s *PostgresStore) GetVersion(ctx context.Context, tenantID, datasetID, versionID string) (record VersionRecord, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		return scanVersionTx(ctx, tx, datasetID, versionID, &record)
	})
	return record, err
}

// ListVersions 仅列出不可变发布版本，不把 PUBLISHING 或可变草稿暴露给版本目录。
func (s *PostgresStore) ListVersions(ctx context.Context, tenantID, datasetID string, limit, offset int) (items []VersionSummary, total int, err error) {
	items = []VersionSummary{}
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform.datasets WHERE id::text=$1 AND deleted_at IS NULL)`, datasetID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM platform.dataset_versions WHERE dataset_id::text=$1 AND status IN ('PUBLISHED','STALE','DEPRECATED')`, datasetID).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id::text,dataset_id::text,version_no,status,dsl_version,schema_hash,plan_hash,
			source_draft_record_version,published_at::text,published_by::text
			FROM platform.dataset_versions WHERE dataset_id::text=$1 AND status IN ('PUBLISHED','STALE','DEPRECATED')
			ORDER BY version_no DESC,id LIMIT $2 OFFSET $3`, datasetID, limit, offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item VersionSummary
			if err := rows.Scan(&item.ID, &item.DatasetID, &item.VersionNo, &item.Status, &item.DSLVersion, &item.DSLHash,
				&item.PlanHash, &item.DraftRecordVersion, &item.PublishedAt, &item.PublishedBy); err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, total, err
}

// GetVersionUsage 汇总精确发布版本的报告草稿、下游数据集和运行中查询引用。
func (s *PostgresStore) GetVersionUsage(ctx context.Context, tenantID, datasetID, versionID string) (usage VersionUsage, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		// target 同时校验父数据集、精确版本及可见发布状态；不存在和跨租户版本统一按未找到处理。
		err := tx.QueryRow(ctx, `WITH target AS (
			SELECT version.id
			FROM platform.dataset_versions AS version
			JOIN platform.datasets AS dataset
			  ON dataset.id=version.dataset_id AND dataset.tenant_id=version.tenant_id AND dataset.deleted_at IS NULL
			WHERE dataset.id::text=$1 AND version.id::text=$2
			  AND version.status IN ('PUBLISHED','STALE','DEPRECATED')
		)
		SELECT
			(SELECT count(DISTINCT dependency.report_id)::int
			 FROM platform.report_draft_dependencies AS dependency
			 JOIN platform.reports AS report
			   ON report.id=dependency.report_id AND report.tenant_id=dependency.tenant_id AND report.deleted_at IS NULL
			 CROSS JOIN target
			 WHERE dependency.dependency_type='DATASET_VERSION' AND dependency.dependency_id=target.id::text),
			(SELECT count(DISTINCT dependency.dataset_version_id)::int
			 FROM platform.dataset_dependencies AS dependency
			 JOIN platform.dataset_versions AS downstream
			   ON downstream.id=dependency.dataset_version_id AND downstream.tenant_id=dependency.tenant_id
			 JOIN platform.datasets AS downstream_dataset
			   ON downstream_dataset.id=downstream.dataset_id AND downstream_dataset.tenant_id=downstream.tenant_id
			  AND downstream_dataset.deleted_at IS NULL
			 CROSS JOIN target
			 WHERE dependency.source_type='DATASET_VERSION' AND dependency.source_id=target.id::text
			   AND downstream.status='DRAFT'),
			(SELECT count(DISTINCT dependency.dataset_version_id)::int
			 FROM platform.dataset_dependencies AS dependency
			 JOIN platform.dataset_versions AS downstream
			   ON downstream.id=dependency.dataset_version_id AND downstream.tenant_id=dependency.tenant_id
			 JOIN platform.datasets AS downstream_dataset
			   ON downstream_dataset.id=downstream.dataset_id AND downstream_dataset.tenant_id=downstream.tenant_id
			  AND downstream_dataset.deleted_at IS NULL
			 CROSS JOIN target
			 WHERE dependency.source_type='DATASET_VERSION' AND dependency.source_id=target.id::text
			   AND downstream.status IN ('PUBLISHED','STALE','DEPRECATED')),
			(SELECT count(*)::int
			 FROM platform.query_runs AS run,target
			 WHERE run.dataset_version_id=target.id AND run.status='RUNNING')
		FROM target`, datasetID, versionID).Scan(
			&usage.ReportDraftReferences, &usage.DownstreamDraftReferences,
			&usage.DownstreamPublishedReferences, &usage.ActiveQueryRuns,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrVersionNotFound
		}
		return err
	})
	return usage, err
}

// TransitionVersion 在数据集行锁内执行单向生命周期迁移，并在需要时清除当前发布指针。
func (s *PostgresStore) TransitionVersion(ctx context.Context, tenantID, actorID, datasetID, versionID string, input VersionTransitionInput) (record VersionRecord, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		allowed, err := datasetActionAllowedTx(ctx, tx, tenantID, actorID, datasetID, "PUBLISH")
		if err != nil {
			return err
		}
		if !allowed {
			return ErrForbidden
		}
		var datasetVersion int64
		var currentPublishedID, currentStatus string
		err = tx.QueryRow(ctx, `SELECT d.version,COALESCE(d.current_published_version_id::text,''),v.status
			FROM platform.datasets d JOIN platform.dataset_versions v
			ON v.id::text=$2 AND v.dataset_id=d.id AND v.tenant_id=d.tenant_id
			WHERE d.id::text=$1 AND d.deleted_at IS NULL FOR UPDATE OF d,v`, datasetID, versionID).
			Scan(&datasetVersion, &currentPublishedID, &currentStatus)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrVersionNotFound
		}
		if err != nil {
			return err
		}
		if datasetVersion != input.ExpectedVersion || currentStatus != input.ExpectedStatus {
			return ErrConflict
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.dataset_versions SET status=$1,updated_by=$2 WHERE id::text=$3`, input.TargetStatus, actorID, versionID); err != nil {
			return err
		}
		if currentPublishedID == versionID {
			if _, err := tx.Exec(ctx, `UPDATE platform.datasets SET current_published_version_id=NULL,status=$1,version=version+1,updated_by=$2 WHERE id::text=$3`, input.TargetStatus, actorID, datasetID); err != nil {
				return err
			}
		} else if _, err := tx.Exec(ctx, `UPDATE platform.datasets SET version=version+1,updated_by=$1 WHERE id::text=$2`, actorID, datasetID); err != nil {
			return err
		}
		if err := scanVersionTx(ctx, tx, datasetID, versionID, &record); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO platform.audit_logs(tenant_id,actor_user_id,action,resource_type,resource_id,detail)
			VALUES($1,$2,$3,'DATASET',$4,jsonb_build_object('datasetVersionId',$5::text,'fromStatus',$6::text,'toStatus',$7::text))`,
			tenantID, actorID, "VERSION_"+input.TargetStatus, datasetID, versionID, currentStatus, input.TargetStatus)
		return err
	})
	return record, err
}

// ValidateVersionDependencies 确认精确发布版本仍与发布时固定的上游结构摘要一致。
func (s *PostgresStore) ValidateVersionDependencies(ctx context.Context, tenantID, datasetID, versionID string) error {
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		return ValidateVersionDependenciesInTx(ctx, tx, datasetID, versionID)
	})
}

// ValidateVersionDependenciesInTx 在调用方事务内锁定精确版本及其上游摘要，供查询运行时原子完成物理解析。
func ValidateVersionDependenciesInTx(ctx context.Context, tx pgx.Tx, datasetID, versionID string) error {
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM platform.dataset_versions
		WHERE id::text=$1 AND dataset_id::text=$2 FOR SHARE`, versionID, datasetID).Scan(&status); errors.Is(err, pgx.ErrNoRows) {
		return ErrVersionNotFound
	} else if err != nil {
		return err
	}
	if status != "PUBLISHED" {
		return ErrVersionUnavailable
	}
	if err := validateDependencySnapshotsTx(ctx, tx, versionID); err != nil {
		return ErrVersionUnavailable
	}
	return nil
}

func replayPublicationTx(ctx context.Context, tx pgx.Tx, actorID, datasetID, key, requestHash string, record *VersionRecord, found *bool) error {
	var storedActorID, storedHash string
	var responseJSON []byte
	err := tx.QueryRow(ctx, `SELECT actor_user_id::text,request_hash,response_json
		FROM platform.dataset_publish_idempotency WHERE dataset_id::text=$1 AND idempotency_key=$2`, datasetID, key).
		Scan(&storedActorID, &storedHash, &responseJSON)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if storedActorID != actorID || storedHash != requestHash {
		return ErrIdempotencyConflict
	}
	if err := json.Unmarshal(responseJSON, record); err != nil {
		return err
	}
	*found = true
	return nil
}

func scanVersionTx(ctx context.Context, tx pgx.Tx, datasetID, versionID string, record *VersionRecord) error {
	err := tx.QueryRow(ctx, `SELECT v.id::text,v.dataset_id::text,d.version,v.source_draft_version_id::text,v.source_draft_record_version,
		v.version_no,v.status,v.dsl_version,v.schema_hash,v.plan_hash,v.dsl_json,v.logical_plan_json,
		v.published_at::text,v.published_by::text
		FROM platform.dataset_versions v JOIN platform.datasets d
		ON d.id=v.dataset_id AND d.tenant_id=v.tenant_id AND d.deleted_at IS NULL
		WHERE d.id::text=$1 AND v.id::text=$2 AND v.status IN ('PUBLISHED','STALE','DEPRECATED')`, datasetID, versionID).
		Scan(&record.ID, &record.DatasetID, &record.DatasetRecordVersion, &record.DraftVersionID, &record.DraftRecordVersion,
			&record.VersionNo, &record.Status, &record.DSLVersion, &record.DSLHash, &record.PlanHash, &record.DSL,
			&record.LogicalPlan, &record.PublishedAt, &record.PublishedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrVersionNotFound
	}
	return err
}

func datasetActionAllowedTx(ctx context.Context, tx pgx.Tx, tenantID, actorID, datasetID, action string) (bool, error) {
	var allowed bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM platform.user_roles ur
		JOIN platform.roles r ON r.id=ur.role_id AND r.tenant_id=ur.tenant_id AND r.status='ACTIVE' AND r.deleted_at IS NULL
		JOIN platform.role_permissions rp ON rp.role_id=ur.role_id AND rp.tenant_id=ur.tenant_id
		JOIN platform.permissions p ON p.id=rp.permission_id AND p.tenant_id=rp.tenant_id
		WHERE ur.tenant_id=$1 AND ur.user_id=$2 AND p.resource_type='DATASET' AND p.action=$3
		UNION ALL
		SELECT 1 FROM platform.object_permissions op WHERE op.tenant_id=$1 AND op.object_type='DATASET'
		AND op.object_id::text=$4 AND op.action=$3 AND (
			op.subject_type='USER' AND op.subject_id=$2 OR op.subject_type='ROLE' AND EXISTS(
				SELECT 1 FROM platform.user_roles ur JOIN platform.roles r
				ON r.id=ur.role_id AND r.tenant_id=ur.tenant_id AND r.status='ACTIVE' AND r.deleted_at IS NULL
				WHERE ur.tenant_id=$1 AND ur.user_id=$2 AND ur.role_id=op.subject_id
			)
		)
	)`, tenantID, actorID, action, datasetID).Scan(&allowed)
	return allowed, err
}

type dependencySnapshot struct {
	Version  int64
	Hash     string
	PlanHash string
}

func loadDependencySnapshot(ctx context.Context, tx pgx.Tx, dependency DependencyRef) (snapshot dependencySnapshot, err error) {
	switch dependency.Type {
	case "TABLE":
		err = tx.QueryRow(ctx, `SELECT metadata_version,structure_hash FROM platform.metadata_tables
			WHERE id::text=$1 AND asset_status='ACTIVE' AND management_status='ENABLED' FOR SHARE`, dependency.ID).Scan(&snapshot.Version, &snapshot.Hash)
	case "FILE_VERSION":
		err = tx.QueryRow(ctx, `SELECT version,sha256 FROM platform.file_asset_versions WHERE id::text=$1 FOR SHARE`, dependency.ID).
			Scan(&snapshot.Version, &snapshot.Hash)
	case "DATASET_VERSION":
		err = tx.QueryRow(ctx, `SELECT version_no,schema_hash,plan_hash FROM platform.dataset_versions
			WHERE id::text=$1 AND status='PUBLISHED' FOR SHARE`, dependency.ID).Scan(&snapshot.Version, &snapshot.Hash, &snapshot.PlanHash)
	default:
		return dependencySnapshot{}, fmt.Errorf("%w: unsupported dependency type", ErrInvalidDocument)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return dependencySnapshot{}, fmt.Errorf("%w: dependency is unavailable", ErrInvalidDocument)
	}
	return snapshot, err
}

func validateDependencySnapshotsTx(ctx context.Context, tx pgx.Tx, versionID string) error {
	rows, err := tx.Query(ctx, `SELECT source_type,source_id,source_version,source_hash,source_plan_hash
		FROM platform.dataset_dependencies WHERE dataset_version_id::text=$1 ORDER BY source_type,source_id FOR SHARE`, versionID)
	if err != nil {
		return err
	}
	type storedDependency struct {
		Ref      DependencyRef
		Snapshot dependencySnapshot
	}
	stored := []storedDependency{}
	for rows.Next() {
		var item storedDependency
		if err := rows.Scan(&item.Ref.Type, &item.Ref.ID, &item.Snapshot.Version, &item.Snapshot.Hash, &item.Snapshot.PlanHash); err != nil {
			rows.Close()
			return err
		}
		stored = append(stored, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(stored) == 0 {
		return ErrPublishValidation
	}
	for _, item := range stored {
		current, err := loadDependencySnapshot(ctx, tx, item.Ref)
		if err != nil || item.Snapshot.Version <= 0 || item.Snapshot.Hash == "" || current != item.Snapshot {
			return ErrPublishValidation
		}
	}
	return nil
}

func mapPublicationPostgresError(err error) error {
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrForbidden) || errors.Is(err, ErrConflict) ||
		errors.Is(err, ErrIdempotencyConflict) || errors.Is(err, ErrPublishValidation) {
		return err
	}
	var pgError *pgconn.PgError
	if !errors.As(err, &pgError) {
		return err
	}
	if pgError.Code == "23505" {
		if pgError.ConstraintName == "dataset_publish_idempotency_pkey" || pgError.ConstraintName == "dataset_publish_idempotency_key" {
			return ErrIdempotencyConflict
		}
		return ErrConflict
	}
	if pgError.Code == "23503" || pgError.Code == "23514" {
		return ErrPublishValidation
	}
	return err
}

// replaceDerived 重建可由 DSL 派生的字段、参数和血缘索引，并校验上游租户归属。
func replaceDerived(ctx context.Context, tx pgx.Tx, tenantID, datasetID, versionID string, document Document, replaceAssetLineage bool) error {
	// 字段、参数和血缘都能从规范 DSL 完整再生，因此在同一事务内采用删除后重建。
	// 任一上游或插入失败都会连同 DSL 更新一起回滚，不会暴露半新半旧的索引。
	if err := validateUpstreams(ctx, tx, document); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM platform.dataset_fields WHERE dataset_version_id=$1`, versionID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM platform.dataset_parameters WHERE dataset_version_id=$1`, versionID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM platform.dataset_dependencies WHERE dataset_version_id=$1`, versionID); err != nil {
		return err
	}
	if replaceAssetLineage {
		if _, err := tx.Exec(ctx, `DELETE FROM platform.asset_dependencies WHERE downstream_type='DATASET' AND downstream_id=$1`, datasetID); err != nil {
			return err
		}
	}
	for i, field := range document.Fields {
		expression, err := json.Marshal(field.Expression)
		if err != nil {
			return err
		}
		visible := field.Visible == nil || *field.Visible
		if _, err := tx.Exec(ctx, `INSERT INTO platform.dataset_fields(tenant_id,dataset_version_id,field_id,field_code,field_name,expression_json,canonical_type,semantic_type,field_role,aggregation,nullable,visible,ordinal_position) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, tenantID, versionID, field.ID, field.Code, field.Name, expression, field.CanonicalType, field.SemanticType, field.Role, field.Aggregation, field.Nullable, visible, i+1); err != nil {
			return err
		}
	}
	for i, parameter := range document.Parameters {
		var defaultValue []byte
		if parameter.DefaultValue != nil {
			value, err := json.Marshal(parameter.DefaultValue)
			if err != nil {
				return err
			}
			defaultValue = value
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.dataset_parameters(tenant_id,dataset_version_id,code,name,data_type,multi_value,required,default_value,ordinal_position) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, tenantID, versionID, parameter.Code, parameter.Name, parameter.DataType, parameter.MultiValue, parameter.Required, defaultValue, i+1); err != nil {
			return err
		}
	}
	for _, dependency := range SortedDependencies(document) {
		snapshot, err := loadDependencySnapshot(ctx, tx, dependency)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.dataset_dependencies(tenant_id,dataset_version_id,source_type,source_id,source_version,source_hash,source_plan_hash) VALUES($1,$2,$3,$4,$5,$6,$7)`, tenantID, versionID, dependency.Type, dependency.ID, snapshot.Version, snapshot.Hash, snapshot.PlanHash); err != nil {
			return err
		}
		if replaceAssetLineage && dependency.Type == "TABLE" {
			// 资产影响分析沿用统一依赖表，插入使用 SELECT 避免非 UUID 或跨租户引用。
			if _, err := tx.Exec(ctx, `INSERT INTO platform.asset_dependencies(tenant_id,upstream_type,upstream_id,downstream_type,downstream_id,downstream_name,dependency_kind) SELECT $1,'TABLE',t.id,'DATASET',$2,$3,'USES' FROM platform.metadata_tables t WHERE t.id::text=$4`, tenantID, datasetID, document.Dataset.Name, dependency.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateUpstreams 在保存前确认表、文件版本和上游数据集版本均属于当前 RLS 租户。
func validateUpstreams(ctx context.Context, tx pgx.Tx, document Document) error {
	// 所有查询都运行在 WithTenantTx 设置的 RLS 上下文中；即使攻击者猜中其他租户
	// 的 UUID，EXISTS 也只会返回 false，不会建立跨租户依赖。
	for i, node := range document.Nodes {
		switch node.Type {
		case "TABLE":
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform.metadata_tables WHERE id::text=$1 AND data_source_id::text=$2 AND asset_status='ACTIVE' AND management_status='ENABLED')`, node.TableID, node.DataSourceID).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return fmt.Errorf("%w: nodes[%d] references an unavailable table asset", ErrInvalidDocument, i)
			}
			var projectedColumns int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM platform.metadata_columns WHERE table_id::text=$1 AND asset_status='ACTIVE' AND column_name=ANY($2::text[])`, node.TableID, node.Projection).Scan(&projectedColumns); err != nil {
				return err
			}
			if projectedColumns != len(node.Projection) {
				return fmt.Errorf("%w: nodes[%d] projection contains unavailable columns", ErrInvalidDocument, i)
			}
			if node.FileVersionID != "" {
				if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform.file_asset_versions fv JOIN platform.data_sources ds ON ds.file_asset_id=fv.file_asset_id AND ds.tenant_id=fv.tenant_id WHERE fv.id::text=$1 AND ds.id::text=$2)`, node.FileVersionID, node.DataSourceID).Scan(&exists); err != nil {
					return err
				}
				if !exists {
					return fmt.Errorf("%w: nodes[%d] references an unavailable file version", ErrInvalidDocument, i)
				}
			}
		case "DATASET":
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform.dataset_versions WHERE id::text=$1 AND status='PUBLISHED')`, node.DatasetVersionID).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return fmt.Errorf("%w: nodes[%d] references an unavailable published dataset version", ErrInvalidDocument, i)
			}
			var projectedFields int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM platform.dataset_fields WHERE dataset_version_id::text=$1 AND field_code::text=ANY($2::text[])`, node.DatasetVersionID, node.Projection).Scan(&projectedFields); err != nil {
				return err
			}
			if projectedFields != len(node.Projection) {
				return fmt.Errorf("%w: nodes[%d] projection contains unavailable dataset fields", ErrInvalidDocument, i)
			}
		}
	}
	return nil
}
