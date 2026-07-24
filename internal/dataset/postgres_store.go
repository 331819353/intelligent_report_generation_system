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

// PublicationCommitSink 在数据集发布事务内登记后续处理任务。实现必须复用传入
// 事务，不能自行提交；这样发布版本和任务入队要么同时成功，要么同时回滚。
type PublicationCommitSink interface {
	EnqueueDatasetMetricExtractionTx(context.Context, pgx.Tx, string, string, VersionRecord) error
}

// MappedPublicationCommitSink 在系统自动发布映射 ODS 的事务内登记首个物化
// build。实现只能写入控制面任务，不能在发布事务内访问源库或执行物化。
type MappedPublicationCommitSink interface {
	EnqueueMappedDatasetMaterializationTx(context.Context, pgx.Tx, string, string, VersionRecord) error
}

// PostgresStore 使用事务和 RLS 保存数据集草稿及全部派生索引。
type PostgresStore struct {
	pool                  *pgxpool.Pool
	publicationSink       PublicationCommitSink
	mappedPublicationSink MappedPublicationCommitSink
}

// NewPostgresStore 创建数据集 PostgreSQL 仓储。
func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

// SetPublicationCommitSink 接入发布后任务 outbox。应只在进程启动装配阶段调用。
func (s *PostgresStore) SetPublicationCommitSink(sink PublicationCommitSink) {
	s.publicationSink = sink
}

// SetMappedPublicationCommitSink 接入自动映射 ODS 的物化控制面登记器。
// 应只在进程启动装配阶段调用。
func (s *PostgresStore) SetMappedPublicationCommitSink(sink MappedPublicationCommitSink) {
	s.mappedPublicationSink = sink
}

// Create 原子创建数据集、首个草稿版本、字段参数索引和审计记录。
func (s *PostgresStore) Create(ctx context.Context, tenantID, actorID string, input CreateInput, prepared Prepared) (Record, error) {
	var datasetID string
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var err error
		datasetID, err = createDatasetTx(ctx, tx, tenantID, actorID, input, prepared, "")
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

// createDatasetTx 是所有真实数据集创建的唯一事务路径。originTableID 仅用于
// LLM 映射表自动产生的数据集；手工创建传空字符串。调用方负责事务提交。
func createDatasetTx(ctx context.Context, tx pgx.Tx, tenantID, actorID string, input CreateInput, prepared Prepared, originTableID string) (string, error) {
	var datasetID, versionID string
	if err := tx.QueryRow(ctx, `INSERT INTO platform.datasets(
		tenant_id,code,name,description,dataset_type,layer,origin_table_id,created_by,updated_by
	) VALUES($1,$2,$3,$4,$5,$6,NULLIF($7,'')::uuid,NULLIF($8,'')::uuid,NULLIF($8,'')::uuid) RETURNING id::text`,
		tenantID, input.Code, input.Name, input.Description, input.Type, prepared.Document.Dataset.Layer,
		originTableID, actorID).Scan(&datasetID); err != nil {
		return "", err
	}
	if err := tx.QueryRow(ctx, `INSERT INTO platform.dataset_versions(
		tenant_id,dataset_id,version_no,layer,dsl_version,dsl_json,schema_hash,logical_plan_json,plan_hash,created_by,updated_by
	) VALUES($1,$2,1,$3,$4,$5,$6,$7,$8,NULLIF($9,'')::uuid,NULLIF($9,'')::uuid) RETURNING id::text`,
		tenantID, datasetID, prepared.Document.Dataset.Layer, DSLVersion, prepared.DSLJSON,
		prepared.DSLHash, prepared.LogicalPlanJSON, prepared.PlanHash, actorID).Scan(&versionID); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE platform.datasets SET current_draft_version_id=$1 WHERE id=$2`, versionID, datasetID); err != nil {
		return "", err
	}
	if err := replaceDerived(ctx, tx, tenantID, datasetID, versionID, prepared.Document, true); err != nil {
		return "", err
	}
	if err := insertDraftRevisionTx(ctx, tx, tenantID, datasetID, actorID, versionID, 1, 1, "CREATE", "", prepared); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.audit_logs(
		tenant_id,actor_user_id,action,resource_type,resource_id,detail
	) VALUES($1,NULLIF($2,'')::uuid,'CREATE','DATASET',$3,jsonb_strip_nulls(jsonb_build_object(
		'dslHash',$4::text,'planHash',$5::text,'originTableId',NULLIF($6,'')::text
	)))`, tenantID, actorID, datasetID, prepared.DSLHash, prepared.PlanHash, originTableID); err != nil {
		return "", err
	}
	return datasetID, nil
}

// Get 读取租户内数据集和 current_draft_version_id 指向的规范草稿。
func (s *PostgresStore) Get(ctx context.Context, tenantID, id string) (record Record, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT d.id::text,COALESCE(d.origin_table_id::text,''),d.code::text,d.name,d.description,d.dataset_type,v.layer,d.status,d.version,
			v.id::text,v.version_no,v.record_version,COALESCE(d.current_published_version_id::text,''),
			v.dsl_version,v.schema_hash,v.plan_hash,v.dsl_json,v.logical_plan_json,d.created_at::text,d.updated_at::text
			FROM platform.datasets d JOIN platform.dataset_versions v
			ON v.id=d.current_draft_version_id AND v.tenant_id=d.tenant_id AND v.dataset_id=d.id
			WHERE d.id::text=$1 AND d.deleted_at IS NULL`, id).Scan(
			&record.ID, &record.OriginTableID, &record.Code, &record.Name, &record.Description, &record.Type, &record.Layer,
			&record.Status, &record.Version,
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
		rows, err := tx.Query(ctx, `SELECT d.id::text,COALESCE(d.origin_table_id::text,''),
			COALESCE(origin_table.table_name,''),COALESCE(origin_source.name,''),
			d.code::text,d.name,d.description,d.dataset_type,d.layer,d.status,d.version,v.schema_hash,
			COALESCE(d.current_published_version_id::text,''),d.updated_at::text
			FROM platform.datasets d JOIN platform.dataset_versions v
			ON v.id=d.current_draft_version_id AND v.tenant_id=d.tenant_id AND v.dataset_id=d.id
			LEFT JOIN platform.metadata_tables origin_table
			  ON origin_table.id=d.origin_table_id AND origin_table.tenant_id=d.tenant_id
			LEFT JOIN platform.data_sources origin_source
			  ON origin_source.id=origin_table.data_source_id AND origin_source.tenant_id=origin_table.tenant_id
			WHERE d.deleted_at IS NULL ORDER BY d.updated_at DESC,d.id LIMIT $1 OFFSET $2`, limit, offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item Summary
			if err := rows.Scan(&item.ID, &item.OriginTableID, &item.OriginTableName, &item.OriginDataSourceName,
				&item.Code, &item.Name, &item.Description, &item.Type, &item.Layer, &item.Status, &item.Version,
				&item.DSLHash, &item.CurrentPublishedVersionID, &item.UpdatedAt); err != nil {
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
		var draftRecordVersion int64
		err = tx.QueryRow(ctx, `UPDATE platform.dataset_versions SET layer=$1,dsl_json=$2,schema_hash=$3,logical_plan_json=$4,plan_hash=$5,record_version=record_version+1,updated_by=$6 WHERE id=$7 AND status='DRAFT' RETURNING record_version`,
			prepared.Document.Dataset.Layer, prepared.DSLJSON, prepared.DSLHash, prepared.LogicalPlanJSON,
			prepared.PlanHash, actorID, versionID).Scan(&draftRecordVersion)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict
		}
		if err != nil {
			return err
		}
		// dataset_type 是当前草稿的派生摘要；增删跨源节点时必须与规范 DSL 同步，
		// 已发布版本仍保留各自不可变 DSL，不会被草稿类型切换改写。
		if _, err := tx.Exec(ctx, `UPDATE platform.datasets SET name=$1,description=$2,dataset_type=$3,layer=$4,version=version+1,updated_by=$5 WHERE id::text=$6`,
			input.Name, input.Description, prepared.Document.Dataset.Type, prepared.Document.Dataset.Layer, actorID, id); err != nil {
			return err
		}
		if err := replaceDerived(ctx, tx, tenantID, id, versionID, prepared.Document, true); err != nil {
			return err
		}
		if err := insertDraftRevisionTx(ctx, tx, tenantID, id, actorID, versionID, version+1, draftRecordVersion, "SAVE", "", prepared); err != nil {
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

// GetRevision 按父数据集和精确修订 ID 读取不可变草稿快照。
func (s *PostgresStore) GetRevision(ctx context.Context, tenantID, datasetID, revisionID string) (record RevisionRecord, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		return scanRevisionTx(ctx, tx, datasetID, revisionID, &record)
	})
	return record, err
}

// ListRevisions 按产生快照时的数据集聚合版本倒序返回草稿历史。
func (s *PostgresStore) ListRevisions(ctx context.Context, tenantID, datasetID string, limit, offset int) (items []RevisionSummary, total int, err error) {
	items = []RevisionSummary{}
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform.datasets WHERE id::text=$1 AND deleted_at IS NULL)`, datasetID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM platform.dataset_draft_revisions WHERE dataset_id::text=$1`, datasetID).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id::text,dataset_id::text,version_no,operation_type,
			COALESCE(source_revision_id::text,''),name,description,dataset_type,draft_version_id::text,
			draft_record_version,dsl_version,schema_hash,plan_hash,created_at::text,COALESCE(created_by::text,'')
			FROM platform.dataset_draft_revisions WHERE dataset_id::text=$1
			ORDER BY version_no DESC,id LIMIT $2 OFFSET $3`, datasetID, limit, offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item RevisionSummary
			if err := rows.Scan(&item.ID, &item.DatasetID, &item.VersionNo, &item.OperationType,
				&item.SourceRevisionID, &item.Name, &item.Description, &item.Type, &item.DraftVersionID,
				&item.DraftRecordVersion, &item.DSLVersion, &item.DSLHash, &item.PlanHash,
				&item.CreatedAt, &item.CreatedBy); err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, total, err
}

// RollbackRevision 将历史快照复制到唯一当前草稿并追加新的 ROLLBACK 修订。
// 目标修订、既有历史以及 current_published_version_id 均不会被修改。
func (s *PostgresStore) RollbackRevision(ctx context.Context, tenantID, actorID, datasetID string, input RollbackRevisionInput, revision RevisionRecord, prepared Prepared) (Record, error) {
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var datasetVersion, currentDraftRecordVersion int64
		var datasetCode, draftVersionID, currentDSLHash string
		err := tx.QueryRow(ctx, `SELECT dataset.version,dataset.code::text,draft.id::text,draft.record_version,draft.schema_hash
			FROM platform.datasets AS dataset
			JOIN platform.dataset_versions AS draft
			  ON draft.id=dataset.current_draft_version_id AND draft.dataset_id=dataset.id AND draft.tenant_id=dataset.tenant_id
			WHERE dataset.id::text=$1 AND dataset.deleted_at IS NULL FOR UPDATE OF dataset,draft`, datasetID).
			Scan(&datasetVersion, &datasetCode, &draftVersionID, &currentDraftRecordVersion, &currentDSLHash)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if datasetVersion != input.ExpectedVersion {
			return ErrConflict
		}
		var storedDSLHash, storedPlanHash string
		if err := tx.QueryRow(ctx, `SELECT schema_hash,plan_hash FROM platform.dataset_draft_revisions
			WHERE id::text=$1 AND dataset_id::text=$2`, revision.ID, datasetID).Scan(&storedDSLHash, &storedPlanHash); errors.Is(err, pgx.ErrNoRows) {
			return ErrRevisionNotFound
		} else if err != nil {
			return err
		}
		if storedDSLHash != revision.DSLHash || storedPlanHash != revision.PlanHash ||
			prepared.DSLHash != revision.DSLHash || prepared.PlanHash != revision.PlanHash ||
			prepared.Document.Dataset.Code != datasetCode {
			return fmt.Errorf("%w: revision snapshot mismatch", ErrInvalidDocument)
		}

		var nextDraftRecordVersion int64
		err = tx.QueryRow(ctx, `UPDATE platform.dataset_versions
				SET layer=$1,dsl_json=$2,schema_hash=$3,logical_plan_json=$4,plan_hash=$5,
				    record_version=record_version+1,updated_by=$6
				WHERE id=$7 AND status='DRAFT' AND record_version=$8
				RETURNING record_version`, prepared.Document.Dataset.Layer, prepared.DSLJSON, prepared.DSLHash,
			prepared.LogicalPlanJSON, prepared.PlanHash, actorID, draftVersionID,
			currentDraftRecordVersion).Scan(&nextDraftRecordVersion)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.datasets
				SET name=$1,description=$2,dataset_type=$3,layer=$4,version=version+1,updated_by=$5
				WHERE id::text=$6`, prepared.Document.Dataset.Name, prepared.Document.Dataset.Description,
			prepared.Document.Dataset.Type, prepared.Document.Dataset.Layer, actorID, datasetID); err != nil {
			return err
		}
		if err := replaceDerived(ctx, tx, tenantID, datasetID, draftVersionID, prepared.Document, true); err != nil {
			return err
		}
		if err := insertDraftRevisionTx(ctx, tx, tenantID, datasetID, actorID, draftVersionID,
			datasetVersion+1, nextDraftRecordVersion, "ROLLBACK", revision.ID, prepared); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO platform.audit_logs(tenant_id,actor_user_id,action,resource_type,resource_id,detail)
			VALUES($1,$2,'ROLLBACK_DRAFT','DATASET',$3,jsonb_build_object(
			  'fromVersion',$4::bigint,'sourceRevisionId',$5::text,'sourceRevisionVersion',$6::bigint,
			  'fromDslHash',$7::text,'dslHash',$8::text,'draftRecordVersion',$9::bigint))`,
			tenantID, actorID, datasetID, datasetVersion, revision.ID, revision.VersionNo,
			currentDSLHash, prepared.DSLHash, nextDraftRecordVersion)
		return err
	})
	if err != nil {
		return Record{}, err
	}
	return s.Get(ctx, tenantID, datasetID)
}

// Disable 在同一行锁内保存停用前状态并清除活动发布指针。不可变发布版本本身
// 不做状态回退或改写；精确版本查询还会检查所属数据集未被停用。
func (s *PostgresStore) Disable(ctx context.Context, tenantID, actorID, id string, input LifecycleInput) (Record, error) {
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var version int64
		var status, publishedVersionID string
		err := tx.QueryRow(ctx, `SELECT version,status,COALESCE(current_published_version_id::text,'')
			FROM platform.datasets WHERE id::text=$1 AND deleted_at IS NULL FOR UPDATE`, id).
			Scan(&version, &status, &publishedVersionID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if version != input.ExpectedVersion {
			return ErrConflict
		}
		if status != "DRAFT" && status != "PUBLISHED" && status != "STALE" {
			return ErrInvalidTransition
		}
		if status == "PUBLISHED" && publishedVersionID == "" {
			return ErrInvalidTransition
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.datasets
			SET status='DISABLED',current_published_version_id=NULL,disabled_from_status=$1,
			    disabled_published_version_id=NULLIF($2,'')::uuid,version=version+1,updated_by=$3
			WHERE id::text=$4`, status, publishedVersionID, actorID, id); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO platform.audit_logs(tenant_id,actor_user_id,action,resource_type,resource_id,detail)
			VALUES($1,$2,'DISABLE','DATASET',$3,jsonb_build_object(
			  'fromVersion',$4::bigint,'fromStatus',$5::text,'publishedVersionId',$6::text))`,
			tenantID, actorID, id, version, status, publishedVersionID)
		return err
	})
	if err != nil {
		return Record{}, err
	}
	return s.Get(ctx, tenantID, id)
}

// Restore 只接受目录级 DISABLED 数据集，并优先恢复停用事务保存的稳定状态。
// 迁移前没有快照的停用记录回到 DRAFT，避免猜测并重新启用已经废弃的发布版本。
func (s *PostgresStore) Restore(ctx context.Context, tenantID, actorID, id string, input LifecycleInput) (Record, error) {
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var version int64
		var status, previousStatus, previousPublishedVersionID string
		err := tx.QueryRow(ctx, `SELECT version,status,COALESCE(disabled_from_status,''),
			COALESCE(disabled_published_version_id::text,'')
			FROM platform.datasets WHERE id::text=$1 AND deleted_at IS NULL FOR UPDATE`, id).
			Scan(&version, &status, &previousStatus, &previousPublishedVersionID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if version != input.ExpectedVersion {
			return ErrConflict
		}
		if status != "DISABLED" {
			return ErrInvalidTransition
		}

		targetStatus, targetPublishedVersionID := "DRAFT", ""
		switch previousStatus {
		case "":
			// 迁移前停用没有可信快照；保留历史版本，恢复为可编辑草稿。
		case "DRAFT":
		case "STALE":
			targetStatus = "STALE"
		case "PUBLISHED":
			if previousPublishedVersionID == "" {
				return ErrInvalidTransition
			}
			var versionStatus string
			if err := tx.QueryRow(ctx, `SELECT status FROM platform.dataset_versions
				WHERE id::text=$1 AND dataset_id::text=$2 FOR SHARE`, previousPublishedVersionID, id).
				Scan(&versionStatus); errors.Is(err, pgx.ErrNoRows) {
				return ErrInvalidTransition
			} else if err != nil {
				return err
			} else if versionStatus != "PUBLISHED" {
				return ErrInvalidTransition
			}
			targetStatus, targetPublishedVersionID = "PUBLISHED", previousPublishedVersionID
		default:
			return ErrInvalidTransition
		}

		if _, err := tx.Exec(ctx, `UPDATE platform.datasets
			SET status=$1,current_published_version_id=NULLIF($2,'')::uuid,
			    disabled_from_status=NULL,disabled_published_version_id=NULL,
			    version=version+1,updated_by=$3
			WHERE id::text=$4`, targetStatus, targetPublishedVersionID, actorID, id); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO platform.audit_logs(tenant_id,actor_user_id,action,resource_type,resource_id,detail)
			VALUES($1,$2,'RESTORE','DATASET',$3,jsonb_build_object(
			  'fromVersion',$4::bigint,'restoredStatus',$5::text,'publishedVersionId',$6::text))`,
			tenantID, actorID, id, version, targetStatus, targetPublishedVersionID)
		return err
	})
	if err != nil {
		return Record{}, err
	}
	return s.Get(ctx, tenantID, id)
}

// Delete 只做软删除；检测全部精确版本的下游占用后再隐藏目录项并废弃发布版本。
func (s *PostgresStore) Delete(ctx context.Context, tenantID, actorID, id string, input LifecycleInput) error {
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var version int64
		err := tx.QueryRow(ctx, `SELECT version FROM platform.datasets
			WHERE id::text=$1 AND deleted_at IS NULL FOR UPDATE`, id).Scan(&version)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if version != input.ExpectedVersion {
			return ErrConflict
		}
		var inUse bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1 FROM platform.metrics m WHERE m.dataset_id::text=$1 AND m.deleted_at IS NULL
			UNION ALL
			SELECT 1 FROM platform.dataset_dependencies dependency
			JOIN platform.dataset_versions source_version ON source_version.id::text=dependency.source_id
			JOIN platform.dataset_versions downstream_version ON downstream_version.id=dependency.dataset_version_id
			JOIN platform.datasets downstream_dataset ON downstream_dataset.id=downstream_version.dataset_id AND downstream_dataset.deleted_at IS NULL
			WHERE dependency.source_type='DATASET_VERSION' AND source_version.dataset_id::text=$1 AND downstream_version.status<>'DEPRECATED'
			UNION ALL
			SELECT 1 FROM platform.report_draft_dependencies dependency
			JOIN platform.dataset_versions source_version ON source_version.id::text=dependency.dependency_id
			JOIN platform.reports report ON report.id=dependency.report_id AND report.deleted_at IS NULL
			WHERE dependency.dependency_type='DATASET_VERSION' AND source_version.dataset_id::text=$1
			UNION ALL
			SELECT 1 FROM platform.query_runs run WHERE run.dataset_id::text=$1 AND run.status='RUNNING'
		)`, id).Scan(&inUse); err != nil {
			return err
		}
		if inUse {
			return ErrInUse
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.dataset_versions SET status='DEPRECATED',updated_by=$1
			WHERE dataset_id::text=$2 AND status IN ('PUBLISHED','STALE')`, actorID, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.datasets SET status='DEPRECATED',current_published_version_id=NULL,
			disabled_from_status=NULL,disabled_published_version_id=NULL,
			deleted_at=now(),version=version+1,updated_by=$1 WHERE id::text=$2`, actorID, id); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO platform.audit_logs(tenant_id,actor_user_id,action,resource_type,resource_id,detail)
			VALUES($1,$2,'DELETE','DATASET',$3,jsonb_build_object('fromVersion',$4::bigint))`, tenantID, actorID, id, version)
		return err
	})
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
		var publishErr error
		publishErr = s.publishTx(
			ctx, tx, tenantID, actorID, datasetID, PublicationOriginDirect, plan, &record,
		)
		return publishErr
	})
	if err != nil {
		return VersionRecord{}, mapPublicationPostgresError(err)
	}
	return record, nil
}

// publishTx is the single immutable publication commit path. The caller owns the tenant
// transaction and must either have checked PUBLISH permission or have passed the stricter
// pristine-mapped-dataset system guard. Approval finalization calls this helper so the decision,
// published pointer, audit, outbox and idempotency record commit atomically.
func (s *PostgresStore) publishTx(
	ctx context.Context,
	tx pgx.Tx,
	tenantID, actorID, datasetID string,
	origin PublicationOrigin,
	plan PublishPlan,
	record *VersionRecord,
) (err error) {
	switch origin {
	case PublicationOriginDirect, PublicationOriginHumanApproval,
		PublicationOriginSystemMappedDefault, PublicationOriginSystemMappedRefresh,
		PublicationOriginSystemMappedRegenerate:
	default:
		return ErrPublishValidation
	}
	var datasetVersion, draftRecordVersion int64
	var draftVersionNo int
	var datasetStatus, draftVersionID, draftDSLHash, draftPlanHash string
	err = tx.QueryRow(ctx, `SELECT d.version,d.status,v.id::text,v.version_no,v.record_version,v.schema_hash,v.plan_hash
			FROM platform.datasets d JOIN platform.dataset_versions v
			ON v.id=d.current_draft_version_id AND v.dataset_id=d.id AND v.tenant_id=d.tenant_id
			WHERE d.id::text=$1 AND d.deleted_at IS NULL FOR UPDATE OF d,v`, datasetID).
		Scan(&datasetVersion, &datasetStatus, &draftVersionID, &draftVersionNo, &draftRecordVersion, &draftDSLHash, &draftPlanHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if datasetStatus == "DISABLED" {
		return ErrInvalidTransition
	}
	// 数据集行锁同时串行化相同幂等键和不同键的发布请求；锁后必须再次查重。
	var found bool
	if err := replayPublicationTx(ctx, tx, actorID, datasetID, plan.IdempotencyKey, plan.RequestHash, record, &found); err != nil || found {
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
	publishedVersionID := plan.ReservedPublishedVersionID
	if publishedVersionID != "" && !canonicalUUID(publishedVersionID) {
		return ErrConflict
	}
	if publishedVersionID == "" {
		publishedVersionID = "00000000-0000-0000-0000-000000000000"
	}
	if err := tx.QueryRow(ctx, `INSERT INTO platform.dataset_versions(
			id,
			tenant_id,dataset_id,version_no,status,layer,dsl_version,dsl_json,schema_hash,logical_plan_json,plan_hash,
			record_version,created_by,updated_by,published_at,published_by,source_draft_version_id,
			source_draft_record_version,publication_origin
		) VALUES(CASE WHEN $1::uuid='00000000-0000-0000-0000-000000000000'::uuid THEN gen_random_uuid() ELSE $1::uuid END,
			$2,$3,$4,'PUBLISHING',$5,$6,$7,$8,$9,$10,1,$11,$11,now(),$11,$12,$13,$14) RETURNING id::text`,
		publishedVersionID, tenantID, datasetID, draftVersionNo, plan.Prepared.Document.Dataset.Layer,
		DSLVersion, plan.Prepared.DSLJSON, plan.Prepared.DSLHash, plan.Prepared.LogicalPlanJSON,
		plan.Prepared.PlanHash, actorID, draftVersionID, draftRecordVersion, origin).
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
	if err := scanVersionTx(ctx, tx, datasetID, publishedVersionID, record); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.audit_logs(tenant_id,actor_user_id,action,resource_type,resource_id,detail)
			VALUES($1,$2,'PUBLISH','DATASET',$3,jsonb_build_object('datasetVersionId',$4::text,'versionNo',$5::int,'draftVersionId',$6::text,'draftRecordVersion',$7::bigint,'dslHash',$8::text,'planHash',$9::text))`,
		tenantID, actorID, datasetID, publishedVersionID, draftVersionNo, draftVersionID, draftRecordVersion, plan.Prepared.DSLHash, plan.Prepared.PlanHash); err != nil {
		return err
	}
	if s.publicationSink != nil {
		if err := s.publicationSink.EnqueueDatasetMetricExtractionTx(ctx, tx, tenantID, actorID, *record); err != nil {
			return err
		}
	}
	responseJSON, err := json.Marshal(*record)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO platform.dataset_publish_idempotency(
			tenant_id,dataset_id,actor_user_id,idempotency_key,request_hash,published_version_id,response_json
		) VALUES($1,$2,$3,$4,$5,$6,$7)`, tenantID, datasetID, actorID, plan.IdempotencyKey, plan.RequestHash, publishedVersionID, responseJSON)
	return err
}

// GetVersion 按父数据集和版本 ID 精确读取发布快照。
func (s *PostgresStore) GetVersion(ctx context.Context, tenantID, datasetID, versionID string) (record VersionRecord, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		return scanVersionTx(ctx, tx, datasetID, versionID, &record)
	})
	return record, err
}

// ResolveVersionSourceRevision 按发布版本冻结的草稿身份和内容摘要解析唯一源修订。
// 遗留缺失或重复数据均失败关闭，绝不按版本号、单独哈希或时间顺序降级匹配。
func (s *PostgresStore) ResolveVersionSourceRevision(ctx context.Context, tenantID, datasetID, versionID string) (record RevisionRecord, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var version VersionRecord
		if err := scanVersionTx(ctx, tx, datasetID, versionID, &version); err != nil {
			return err
		}

		rows, err := tx.Query(ctx, `SELECT revision.id::text,revision.dataset_id::text,revision.version_no,
			revision.operation_type,COALESCE(revision.source_revision_id::text,''),revision.name,
			revision.description,revision.dataset_type,revision.draft_version_id::text,
			revision.draft_record_version,revision.dsl_version,revision.schema_hash,revision.plan_hash,
			revision.created_at::text,COALESCE(revision.created_by::text,''),revision.dsl_json,
			revision.logical_plan_json
			FROM platform.dataset_versions AS version
			JOIN platform.datasets AS dataset
			  ON dataset.id=version.dataset_id AND dataset.tenant_id=version.tenant_id AND dataset.deleted_at IS NULL
			JOIN platform.dataset_draft_revisions AS revision
			  ON revision.tenant_id=version.tenant_id AND revision.dataset_id=version.dataset_id
			 AND revision.draft_version_id=version.source_draft_version_id
			 AND revision.draft_record_version=version.source_draft_record_version
			 AND revision.schema_hash=version.schema_hash AND revision.plan_hash=version.plan_hash
			WHERE dataset.id::text=$1 AND version.id::text=$2
			  AND version.status IN ('PUBLISHED','STALE','DEPRECATED')
			ORDER BY revision.id LIMIT 2`, datasetID, versionID)
		if err != nil {
			return err
		}
		defer rows.Close()

		matches := make([]RevisionRecord, 0, 2)
		for rows.Next() {
			var match RevisionRecord
			if err := scanRevisionRecord(rows, &match); err != nil {
				return err
			}
			matches = append(matches, match)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		record, err = resolveUniqueVersionSourceRevision(matches)
		return err
	})
	return record, err
}

func resolveUniqueVersionSourceRevision(matches []RevisionRecord) (RevisionRecord, error) {
	if len(matches) != 1 {
		return RevisionRecord{}, ErrVersionRollbackUnavailable
	}
	return matches[0], nil
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
		rows, err := tx.Query(ctx, `SELECT id::text,dataset_id::text,version_no,status,publication_origin,layer,dsl_version,schema_hash,plan_hash,
				source_draft_record_version,published_at::text,published_by::text
			FROM platform.dataset_versions WHERE dataset_id::text=$1 AND status IN ('PUBLISHED','STALE','DEPRECATED')
			ORDER BY version_no DESC,id LIMIT $2 OFFSET $3`, datasetID, limit, offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item VersionSummary
			if err := rows.Scan(&item.ID, &item.DatasetID, &item.VersionNo, &item.Status, &item.PublicationOrigin, &item.Layer,
				&item.DSLVersion, &item.DSLHash,
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
		var currentPublishedID, currentStatus, datasetStatus string
		err = tx.QueryRow(ctx, `SELECT d.version,COALESCE(d.current_published_version_id::text,''),v.status,d.status
			FROM platform.datasets d JOIN platform.dataset_versions v
			ON v.id::text=$2 AND v.dataset_id=d.id AND v.tenant_id=d.tenant_id
			WHERE d.id::text=$1 AND d.deleted_at IS NULL FOR UPDATE OF d,v`, datasetID, versionID).
			Scan(&datasetVersion, &currentPublishedID, &currentStatus, &datasetStatus)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrVersionNotFound
		}
		if err != nil {
			return err
		}
		if datasetStatus == "DISABLED" {
			return ErrInvalidTransition
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
	var status, ownerStatus string
	if err := tx.QueryRow(ctx, `SELECT version.status,owner.status FROM platform.dataset_versions AS version
		JOIN platform.datasets AS owner
		  ON owner.id=version.dataset_id AND owner.tenant_id=version.tenant_id
		WHERE version.id::text=$1 AND version.dataset_id::text=$2
		  AND owner.deleted_at IS NULL
		FOR SHARE OF version,owner`, versionID, datasetID).Scan(&status, &ownerStatus); errors.Is(err, pgx.ErrNoRows) {
		return ErrVersionNotFound
	} else if err != nil {
		return err
	}
	if status != "PUBLISHED" || ownerStatus != "PUBLISHED" {
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
	// 迁移前的幂等响应没有 layer；重放时只从同一个不可变版本补齐该新字段，
	// 不改写历史 response_json，也不追随数据集的当前发布指针。
	if record.Layer == "" || record.PublicationOrigin == "" {
		if err := tx.QueryRow(ctx, `SELECT layer,publication_origin FROM platform.dataset_versions
			WHERE id=$1 AND dataset_id::text=$2 AND status IN ('PUBLISHED','STALE','DEPRECATED')`,
			record.ID, datasetID).Scan(&record.Layer, &record.PublicationOrigin); err != nil {
			return err
		}
	}
	*found = true
	return nil
}

func scanVersionTx(ctx context.Context, tx pgx.Tx, datasetID, versionID string, record *VersionRecord) error {
	err := tx.QueryRow(ctx, `SELECT v.id::text,v.dataset_id::text,d.version,v.source_draft_version_id::text,v.source_draft_record_version,
			v.version_no,v.status,v.publication_origin,v.layer,v.dsl_version,v.schema_hash,v.plan_hash,v.dsl_json,v.logical_plan_json,
			v.published_at::text,v.published_by::text
		FROM platform.dataset_versions v JOIN platform.datasets d
		ON d.id=v.dataset_id AND d.tenant_id=v.tenant_id AND d.deleted_at IS NULL
		WHERE d.id::text=$1 AND v.id::text=$2 AND v.status IN ('PUBLISHED','STALE','DEPRECATED')`, datasetID, versionID).
		Scan(&record.ID, &record.DatasetID, &record.DatasetRecordVersion, &record.DraftVersionID, &record.DraftRecordVersion,
			&record.VersionNo, &record.Status, &record.PublicationOrigin, &record.Layer, &record.DSLVersion, &record.DSLHash, &record.PlanHash, &record.DSL,
			&record.LogicalPlan, &record.PublishedAt, &record.PublishedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrVersionNotFound
	}
	return err
}

type revisionRowScanner interface{ Scan(...any) error }

func scanRevisionTx(ctx context.Context, tx pgx.Tx, datasetID, revisionID string, record *RevisionRecord) error {
	err := scanRevisionRecord(tx.QueryRow(ctx, `SELECT revision.id::text,revision.dataset_id::text,revision.version_no,
		revision.operation_type,COALESCE(revision.source_revision_id::text,''),revision.name,
		revision.description,revision.dataset_type,revision.draft_version_id::text,
		revision.draft_record_version,revision.dsl_version,revision.schema_hash,revision.plan_hash,
		revision.created_at::text,COALESCE(revision.created_by::text,''),revision.dsl_json,
		revision.logical_plan_json
		FROM platform.dataset_draft_revisions AS revision
		JOIN platform.datasets AS dataset
		  ON dataset.id=revision.dataset_id AND dataset.tenant_id=revision.tenant_id AND dataset.deleted_at IS NULL
		WHERE dataset.id::text=$1 AND revision.id::text=$2`, datasetID, revisionID), record)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrRevisionNotFound
	}
	return err
}

func scanRevisionRecord(row revisionRowScanner, record *RevisionRecord) error {
	return row.Scan(
		&record.ID, &record.DatasetID, &record.VersionNo, &record.OperationType,
		&record.SourceRevisionID, &record.Name, &record.Description, &record.Type,
		&record.DraftVersionID, &record.DraftRecordVersion, &record.DSLVersion,
		&record.DSLHash, &record.PlanHash, &record.CreatedAt, &record.CreatedBy,
		&record.DSL, &record.LogicalPlan,
	)
}

func insertDraftRevisionTx(ctx context.Context, tx pgx.Tx, tenantID, datasetID, actorID, draftVersionID string,
	versionNo, draftRecordVersion int64, operationType, sourceRevisionID string, prepared Prepared) error {
	var sourceRevision any
	if sourceRevisionID != "" {
		sourceRevision = sourceRevisionID
	}
	_, err := tx.Exec(ctx, `INSERT INTO platform.dataset_draft_revisions(
		tenant_id,dataset_id,version_no,operation_type,source_revision_id,name,description,dataset_type,
		draft_version_id,draft_record_version,dsl_version,dsl_json,schema_hash,logical_plan_json,plan_hash,created_by
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,NULLIF($16,'')::uuid)`,
		tenantID, datasetID, versionNo, operationType, sourceRevision,
		prepared.Document.Dataset.Name, prepared.Document.Dataset.Description, prepared.Document.Dataset.Type,
		draftVersionID, draftRecordVersion, DSLVersion, prepared.DSLJSON, prepared.DSLHash,
		prepared.LogicalPlanJSON, prepared.PlanHash, actorID)
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
		err = tx.QueryRow(ctx, `SELECT version.version_no,version.schema_hash,version.plan_hash
			FROM platform.dataset_versions AS version
			JOIN platform.datasets AS owner
			  ON owner.id=version.dataset_id AND owner.tenant_id=version.tenant_id
			WHERE version.id::text=$1 AND version.status='PUBLISHED'
			  AND owner.status='PUBLISHED' AND owner.deleted_at IS NULL
			FOR SHARE OF version,owner`, dependency.ID).Scan(&snapshot.Version, &snapshot.Hash, &snapshot.PlanHash)
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
		errors.Is(err, ErrIdempotencyConflict) || errors.Is(err, ErrPublishValidation) || errors.Is(err, ErrInvalidTransition) {
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
		if _, err := tx.Exec(ctx, `INSERT INTO platform.dataset_fields(
			tenant_id,dataset_version_id,field_id,field_code,field_name,description,
			expression_json,canonical_type,semantic_type,field_role,aggregation,
			nullable,visible,ordinal_position
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
			tenantID, versionID, field.ID, field.Code, field.Name, field.Description,
			expression, field.CanonicalType, field.SemanticType, field.Role,
			field.Aggregation, field.Nullable, visible, i+1); err != nil {
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
	if err := ValidateLayerDependencies(ctx, document, postgresLayerDependencyResolver{tx: tx}); err != nil {
		return err
	}
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
			if err := tx.QueryRow(ctx, `SELECT EXISTS(
				SELECT 1
				FROM platform.dataset_versions AS version
				JOIN platform.datasets AS owner
				  ON owner.id=version.dataset_id AND owner.tenant_id=version.tenant_id
				WHERE version.id::text=$1 AND version.status='PUBLISHED'
				  AND owner.status='PUBLISHED' AND owner.deleted_at IS NULL
			)`, node.DatasetVersionID).Scan(&exists); err != nil {
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

type postgresLayerDependencyResolver struct{ tx pgx.Tx }

func (resolver postgresLayerDependencyResolver) ResolveDatasetVersionLayer(
	ctx context.Context,
	versionID string,
) (Layer, error) {
	var layer Layer
	err := resolver.tx.QueryRow(ctx, `SELECT version.layer
		FROM platform.dataset_versions AS version
		JOIN platform.datasets AS owner
		  ON owner.id=version.dataset_id AND owner.tenant_id=version.tenant_id
		WHERE version.id::text=$1 AND version.status='PUBLISHED'
		  AND owner.status='PUBLISHED' AND owner.deleted_at IS NULL
		FOR SHARE OF version,owner`, versionID).Scan(&layer)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("%w: nodes references an unavailable published dataset layer: %w",
			ErrInvalidDocument, ErrLayerDependencyUnavailable)
	}
	if err != nil {
		return "", err
	}
	if !layer.Valid() {
		return "", fmt.Errorf("%w: upstream dataset version has an invalid layer: %w",
			ErrInvalidDocument, ErrLayerDependencyUnavailable)
	}
	return layer, nil
}
