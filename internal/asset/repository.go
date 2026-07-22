package asset

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/platform/database"
	"intelligent-report-generation-system/internal/semanticquality"
)

type Repository struct{ pool *pgxpool.Pool }

// NewRepository 创建数据资产读写仓储。
func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

const tableSelect = `t.id::text,t.data_source_id::text,d.name,d.source_type::text,COALESCE((SELECT fv.id::text FROM platform.file_assets fa JOIN platform.file_asset_versions fv ON fv.file_asset_id=fa.id AND fv.tenant_id=fa.tenant_id AND fv.version=fa.current_version WHERE fa.id=d.file_asset_id),''),t.catalog_name,t.schema_name,t.table_name,t.table_type,t.source_comment,t.business_name,t.business_description,t.tags,t.sensitivity_level::text,t.visibility::text,t.manual_locked,t.asset_status::text,t.management_status,CASE WHEN t.last_enriched_structure_hash<>'' AND t.last_enriched_structure_hash=t.structure_hash THEN 'SUCCEEDED' ELSE COALESCE((SELECT j.status FROM platform.ai_metadata_jobs j WHERE j.table_id=t.id AND j.metadata_structure_hash=t.structure_hash ORDER BY j.created_at DESC LIMIT 1),'PENDING') END,t.structure_hash,t.metadata_version,t.business_version,(SELECT count(*) FROM platform.metadata_columns c WHERE c.table_id=t.id AND c.asset_status='ACTIVE'),t.last_sync_at::text`

// SearchTables 按租户、关键词和分类条件分页检索表资产。
func (r *Repository) SearchTables(ctx context.Context, tenantID string, search Search) (items []Table, total int, err error) {
	items = []Table{}
	err = database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		args := []any{search.Query, search.DataSourceID, search.SourceType, search.Status, search.Sensitivity, search.Tag, search.Visibility, search.ManagementStatus, search.EnrichedOnly}
		where := ` FROM platform.metadata_tables t JOIN platform.data_sources d ON d.id=t.data_source_id WHERE
			($1='' OR t.table_name ILIKE '%'||$1||'%' OR t.business_name ILIKE '%'||$1||'%' OR t.business_description ILIKE '%'||$1||'%')
			AND ($2='' OR t.data_source_id=$2::uuid) AND ($3='' OR d.source_type::text=$3) AND ($4='' OR t.asset_status::text=$4)
			AND ($5='' OR t.sensitivity_level::text=$5) AND ($6='' OR $6=ANY(t.tags)) AND ($7='' OR t.visibility::text=$7)
			AND ($8='' OR t.management_status=$8)
			AND (NOT $9 OR (t.last_enriched_structure_hash<>'' AND t.last_enriched_structure_hash=t.structure_hash))`
		if err := tx.QueryRow(ctx, `SELECT count(*)`+where, args...).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+tableSelect+where+` ORDER BY COALESCE(NULLIF(t.business_name,''),t.table_name),t.id LIMIT $10 OFFSET $11`, append(args, search.Limit, search.Offset)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item Table
			if err := scanTable(rows, &item); err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return
}

// GetTable 返回单个表资产及其版本、同步状态摘要。
func (r *Repository) GetTable(ctx context.Context, tenantID, id string) (item Table, err error) {
	err = database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		return scanTable(tx.QueryRow(ctx, `SELECT `+tableSelect+` FROM platform.metadata_tables t JOIN platform.data_sources d ON d.id=t.data_source_id WHERE t.id=$1`, id), &item)
	})
	return
}

// scanTable 统一数据库列到表资产模型的映射顺序。
func scanTable(row interface{ Scan(...any) error }, item *Table) error {
	return row.Scan(&item.ID, &item.DataSourceID, &item.DataSourceName, &item.DataSourceType, &item.FileVersionID, &item.CatalogName, &item.SchemaName, &item.TableName, &item.TableType, &item.SourceComment, &item.BusinessName, &item.BusinessDescription, &item.Tags, &item.SensitivityLevel, &item.Visibility, &item.ManualLocked, &item.AssetStatus, &item.ManagementStatus, &item.EnrichmentStatus, &item.StructureHash, &item.MetadataVersion, &item.BusinessVersion, &item.ColumnCount, &item.LastSyncAt)
}

// ListColumns 返回表下按序排列的字段资产。
func (r *Repository) ListColumns(ctx context.Context, tenantID, tableID string) (items []Column, err error) {
	items = []Column{}
	err = database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text,table_id::text,column_name,ordinal_position,source_comment,native_type,canonical_type,nullable,business_name,business_description,tags,sensitivity_level::text,semantic_type,manual_locked,asset_status::text,business_version FROM platform.metadata_columns WHERE table_id=$1 ORDER BY ordinal_position`, tableID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var i Column
			if err := rows.Scan(&i.ID, &i.TableID, &i.ColumnName, &i.OrdinalPosition, &i.SourceComment, &i.NativeType, &i.CanonicalType, &i.Nullable, &i.BusinessName, &i.BusinessDescription, &i.Tags, &i.SensitivityLevel, &i.SemanticType, &i.ManualLocked, &i.AssetStatus, &i.BusinessVersion); err != nil {
				return err
			}
			items = append(items, i)
		}
		return rows.Err()
	})
	return
}

// UpdateTable 更新人工业务元数据、锁定字段并记录审计事件。
func (r *Repository) UpdateTable(ctx context.Context, tenantID, actorID, id string, m BusinessMetadata) (Table, error) {
	m.Tags = normalizeTags(m.Tags)
	if err := m.Validate(false); err != nil {
		return Table{}, err
	}
	err := database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE platform.metadata_tables SET business_name=$1,business_description=$2,tags=$3,sensitivity_level=$4,visibility=$5,manual_locked=$6,business_version=business_version+1 WHERE id=$7 AND business_version=$8`, strings.TrimSpace(m.BusinessName), strings.TrimSpace(m.BusinessDescription), m.Tags, m.SensitivityLevel, m.Visibility, m.ManualLocked, id, m.ExpectedVersion)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("asset version conflict or asset not found")
		}
		return audit(ctx, tx, tenantID, actorID, "UPDATE_BUSINESS_METADATA", "TABLE_ASSET", id, m)
	})
	if err != nil {
		return Table{}, err
	}
	return r.GetTable(ctx, tenantID, id)
}

// UpdateColumn 更新字段业务元数据，并递增业务版本用于并发判断。
func (r *Repository) UpdateColumn(ctx context.Context, tenantID, actorID, id string, m BusinessMetadata) (Column, error) {
	m.Tags = normalizeTags(m.Tags)
	if err := m.Validate(true); err != nil {
		return Column{}, err
	}
	var item Column
	err := database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		var canonicalType string
		if err := tx.QueryRow(ctx, `SELECT canonical_type FROM platform.metadata_columns
			WHERE id=$1 AND business_version=$2 AND asset_status='ACTIVE' FOR UPDATE`, id, m.ExpectedVersion).Scan(&canonicalType); err != nil {
			return errors.New("asset version conflict or asset not found")
		}
		if !semanticquality.Compatible(canonicalType, m.SemanticType) {
			return errors.New("semantic type is incompatible with canonical type")
		}
		tag, err := tx.Exec(ctx, `UPDATE platform.metadata_columns SET business_name=$1,business_description=$2,tags=$3,sensitivity_level=$4,semantic_type=$5,manual_locked=$6,business_version=business_version+1 WHERE id=$7 AND business_version=$8`, strings.TrimSpace(m.BusinessName), strings.TrimSpace(m.BusinessDescription), m.Tags, m.SensitivityLevel, strings.TrimSpace(m.SemanticType), m.ManualLocked, id, m.ExpectedVersion)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("asset version conflict or asset not found")
		}
		if err := audit(ctx, tx, tenantID, actorID, "UPDATE_BUSINESS_METADATA", "COLUMN_ASSET", id, m); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT id::text,table_id::text,column_name,ordinal_position,source_comment,native_type,canonical_type,nullable,business_name,business_description,tags,sensitivity_level::text,semantic_type,manual_locked,asset_status::text,business_version FROM platform.metadata_columns WHERE id=$1`, id).Scan(&item.ID, &item.TableID, &item.ColumnName, &item.OrdinalPosition, &item.SourceComment, &item.NativeType, &item.CanonicalType, &item.Nullable, &item.BusinessName, &item.BusinessDescription, &item.Tags, &item.SensitivityLevel, &item.SemanticType, &item.ManualLocked, &item.AssetStatus, &item.BusinessVersion)
	})
	return item, err
}

// SetTableManagementStatus 只改变 PostgreSQL 表资产可用性，不对源数据库执行任何操作。
func (r *Repository) SetTableManagementStatus(ctx context.Context, tenantID, actorID, id, status string) (Table, error) {
	if status != "ENABLED" && status != "DISABLED" {
		return Table{}, errors.New("invalid table management status")
	}
	err := database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE platform.metadata_tables SET management_status=$1 WHERE id=$2 AND asset_status='ACTIVE'`, status, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("table asset not found")
		}
		return audit(ctx, tx, tenantID, actorID, "UPDATE_ASSET_STATUS", "TABLE_ASSET", id, map[string]string{"managementStatus": status})
	})
	if err != nil {
		return Table{}, err
	}
	return r.GetTable(ctx, tenantID, id)
}

// DeleteTableAsset 软删除 PostgreSQL 资产及字段，不删除或修改源库原表。
func (r *Repository) DeleteTableAsset(ctx context.Context, tenantID, actorID, id string) error {
	return database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE platform.metadata_tables SET asset_status='INACTIVE',management_status='DISABLED' WHERE id=$1 AND asset_status='ACTIVE'`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("table asset not found")
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.metadata_columns SET asset_status='INACTIVE' WHERE table_id=$1`, id); err != nil {
			return err
		}
		return audit(ctx, tx, tenantID, actorID, "DELETE_TABLE_ASSET", "TABLE_ASSET", id, map[string]any{"sourceTableAffected": false})
	})
}

// ListDiffs 查询数据源最近的元数据结构变化记录。
func (r *Repository) ListDiffs(ctx context.Context, tenantID, dataSourceID string, limit int) (items []Diff, err error) {
	items = []Diff{}
	err = database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text,data_source_id::text,object_type,object_key,change_type::text,before_json,after_json,created_at::text FROM platform.metadata_diffs WHERE ($1='' OR data_source_id=$1::uuid) ORDER BY created_at DESC LIMIT $2`, dataSourceID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d Diff
			var before, after []byte
			if err := rows.Scan(&d.ID, &d.DataSourceID, &d.ObjectType, &d.ObjectKey, &d.ChangeType, &before, &after, &d.CreatedAt); err != nil {
				return err
			}
			if len(before) > 0 {
				_ = json.Unmarshal(before, &d.Before)
			}
			if len(after) > 0 {
				_ = json.Unmarshal(after, &d.After)
			}
			items = append(items, d)
		}
		return rows.Err()
	})
	return
}

// Impact 列出依赖目标表的下游数据集、指标和报表组件。
func (r *Repository) Impact(ctx context.Context, tenantID, tableID string) (items []Dependency, err error) {
	items = []Dependency{}
	err = database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text,downstream_type,downstream_id::text,downstream_name,dependency_kind,created_at::text FROM platform.asset_dependencies WHERE upstream_type='TABLE' AND upstream_id=$1 ORDER BY downstream_type,downstream_name`, tableID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d Dependency
			if err := rows.Scan(&d.ID, &d.DownstreamType, &d.DownstreamID, &d.DownstreamName, &d.Kind, &d.CreatedAt); err != nil {
				return err
			}
			items = append(items, d)
		}
		return rows.Err()
	})
	return
}

// audit 在同一事务内写入资产操作审计，确保业务变更与审计一致。
func audit(ctx context.Context, tx pgx.Tx, tenantID, actorID, action, resource, id string, detail any) error {
	payload, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO platform.audit_logs(tenant_id,actor_user_id,action,resource_type,resource_id,detail) VALUES($1,$2,$3,$4,$5,$6)`, tenantID, actorID, action, resource, id, payload)
	return err
}

// normalizeTags 去除空白和重复标签，并保持首次出现顺序。
func normalizeTags(tags []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag != "" && !seen[tag] {
			seen[tag] = true
			out = append(out, tag)
		}
	}
	return out
}
