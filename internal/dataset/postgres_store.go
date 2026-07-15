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
		if err := replaceDerived(ctx, tx, tenantID, datasetID, versionID, prepared.Document); err != nil {
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
		err := tx.QueryRow(ctx, `SELECT d.id::text,d.code::text,d.name,d.description,d.dataset_type,d.status,d.version,v.id::text,v.dsl_version,v.schema_hash,v.plan_hash,v.dsl_json,v.logical_plan_json,d.created_at::text,d.updated_at::text FROM platform.datasets d JOIN platform.dataset_versions v ON v.id=d.current_draft_version_id AND v.tenant_id=d.tenant_id WHERE d.id::text=$1 AND d.deleted_at IS NULL`, id).Scan(
			&record.ID, &record.Code, &record.Name, &record.Description, &record.Type, &record.Status, &record.Version,
			&record.DraftVersionID, &record.DSLVersion, &record.DSLHash, &record.PlanHash, &record.DSL, &record.LogicalPlan,
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
		rows, err := tx.Query(ctx, `SELECT d.id::text,d.code::text,d.name,d.description,d.dataset_type,d.status,d.version,v.schema_hash,d.updated_at::text FROM platform.datasets d JOIN platform.dataset_versions v ON v.id=d.current_draft_version_id AND v.tenant_id=d.tenant_id WHERE d.deleted_at IS NULL ORDER BY d.updated_at DESC,d.id LIMIT $1 OFFSET $2`, limit, offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item Summary
			if err := rows.Scan(&item.ID, &item.Code, &item.Name, &item.Description, &item.Type, &item.Status, &item.Version, &item.DSLHash, &item.UpdatedAt); err != nil {
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
		if err := replaceDerived(ctx, tx, tenantID, id, versionID, prepared.Document); err != nil {
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

// replaceDerived 重建可由 DSL 派生的字段、参数和血缘索引，并校验上游租户归属。
func replaceDerived(ctx context.Context, tx pgx.Tx, tenantID, datasetID, versionID string, document Document) error {
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
	if _, err := tx.Exec(ctx, `DELETE FROM platform.asset_dependencies WHERE downstream_type='DATASET' AND downstream_id=$1`, datasetID); err != nil {
		return err
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
		if _, err := tx.Exec(ctx, `INSERT INTO platform.dataset_dependencies(tenant_id,dataset_version_id,source_type,source_id) VALUES($1,$2,$3,$4)`, tenantID, versionID, dependency.Type, dependency.ID); err != nil {
			return err
		}
		if dependency.Type == "TABLE" {
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
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform.metadata_tables WHERE id::text=$1 AND data_source_id::text=$2 AND asset_status='ACTIVE')`, node.TableID, node.DataSourceID).Scan(&exists); err != nil {
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
