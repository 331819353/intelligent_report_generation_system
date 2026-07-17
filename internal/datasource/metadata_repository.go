package datasource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"intelligent-report-generation-system/internal/platform/database"
)

// ApplyMetadata 在单一租户事务中保存快照、更新当前资产并记录增删改差异。
func (r *PostgresRepository) ApplyMetadata(ctx context.Context, source Source, result SyncResult) error {
	watermark, err := time.Parse(time.RFC3339Nano, result.Watermark)
	if err != nil {
		return errors.New("invalid metadata watermark")
	}
	snapshot, err := json.Marshal(result.Tables)
	if err != nil {
		return err
	}
	return database.WithTenantTx(ctx, r.pool, source.TenantID, func(tx pgx.Tx) error {
		// 先保存不可变快照，再更新当前态，便于审计和后续差异回放。
		if _, err := tx.Exec(ctx, `INSERT INTO platform.metadata_snapshots(tenant_id,data_source_id,snapshot_hash,snapshot_json) VALUES($1,$2,$3,$4)`, source.TenantID, source.ID, result.SnapshotHash, snapshot); err != nil {
			return err
		}
		tableKeys := make([]string, 0, len(result.Tables))
		for _, table := range result.Tables {
			key := table.CatalogName + "\x1f" + table.SchemaName + "\x1f" + table.Name
			tableKeys = append(tableKeys, key)
			if _, err := r.upsertMetadataTable(ctx, tx, source, table, watermark); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.metadata_diffs(tenant_id,data_source_id,object_type,object_key,change_type,before_json)
			SELECT tenant_id,data_source_id,'TABLE',catalog_name||'.'||schema_name||'.'||table_name,'REMOVED',to_jsonb(t)
			FROM platform.metadata_tables t WHERE data_source_id=$1 AND asset_status='ACTIVE'
			AND NOT ((catalog_name||E'\x1f'||schema_name||E'\x1f'||table_name)=ANY($2::text[]))`, source.ID, tableKeys); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE platform.metadata_tables SET asset_status='INACTIVE',last_sync_at=$2 WHERE data_source_id=$1 AND NOT ((catalog_name||E'\x1f'||schema_name||E'\x1f'||table_name)=ANY($3::text[]))`, source.ID, watermark, tableKeys)
		return err
	})
}

// ApplySelectedMetadata 只新增或刷新用户选中的表，不会把同一数据源的其他资产误判为移除。
func (r *PostgresRepository) ApplySelectedMetadata(ctx context.Context, source Source, result SyncResult) (ids map[string]string, err error) {
	watermark, err := time.Parse(time.RFC3339Nano, result.Watermark)
	if err != nil {
		return nil, errors.New("invalid metadata watermark")
	}
	snapshot, err := json.Marshal(result.Tables)
	if err != nil {
		return nil, err
	}
	ids = make(map[string]string, len(result.Tables))
	err = database.WithTenantTx(ctx, r.pool, source.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO platform.metadata_snapshots(tenant_id,data_source_id,snapshot_hash,snapshot_json) VALUES($1,$2,$3,$4)`, source.TenantID, source.ID, result.SnapshotHash, snapshot); err != nil {
			return err
		}
		for _, table := range result.Tables {
			id, err := r.upsertMetadataTable(ctx, tx, source, table, watermark)
			if err != nil {
				return err
			}
			ids[metadataTableKey(table)] = id
		}
		return nil
	})
	return ids, err
}

// ListActiveTableSelections 返回当前已纳管的活动表业务键，供全量刷新复用用户原有选择范围。
func (r *PostgresRepository) ListActiveTableSelections(ctx context.Context, tenantID, sourceID string) (items []TableSelection, err error) {
	items = []TableSelection{}
	err = database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT catalog_name,schema_name,table_name
			FROM platform.metadata_tables WHERE data_source_id=$1 AND asset_status='ACTIVE'
			ORDER BY catalog_name,schema_name,table_name`, sourceID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item TableSelection
			if err := rows.Scan(&item.CatalogName, &item.SchemaName, &item.TableName); err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, err
}

func metadataTableKey(table MetadataTable) string {
	return table.CatalogName + "\x1f" + table.SchemaName + "\x1f" + table.Name
}

// upsertMetadataTable 按稳定业务键更新表资产，并基于结构哈希判断变化类型。
func (r *PostgresRepository) upsertMetadataTable(ctx context.Context, tx pgx.Tx, source Source, table MetadataTable, watermark time.Time) (string, error) {
	hash, payload, err := metadataTableHash(table)
	if err != nil {
		return "", err
	}
	constraints, _ := json.Marshal(table.Constraints)
	indexes, _ := json.Marshal(table.Indexes)
	var id, oldHash, oldStatus string
	var oldPayload []byte
	err = tx.QueryRow(ctx, `SELECT id::text,structure_hash,asset_status::text,to_jsonb(metadata_tables) FROM platform.metadata_tables WHERE data_source_id=$1 AND catalog_name=$2 AND schema_name=$3 AND table_name=$4`, source.ID, table.CatalogName, table.SchemaName, table.Name).Scan(&id, &oldHash, &oldStatus, &oldPayload)
	change := "CHANGED"
	if errors.Is(err, pgx.ErrNoRows) {
		change = "ADDED"
	} else if err != nil {
		return "", err
	}
	// 结构未变但曾被移除的资产单独标记为重新激活，便于审计区分。
	if oldStatus == "INACTIVE" && oldHash == hash {
		change = "REACTIVATED"
	}
	err = tx.QueryRow(ctx, `INSERT INTO platform.metadata_tables(tenant_id,data_source_id,catalog_name,schema_name,table_name,table_type,source_comment,estimated_row_count,primary_key_columns,constraints_json,indexes_json,structure_hash,last_sync_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		ON CONFLICT(tenant_id,data_source_id,catalog_name,schema_name,table_name) DO UPDATE SET table_type=EXCLUDED.table_type,source_comment=EXCLUDED.source_comment,estimated_row_count=EXCLUDED.estimated_row_count,primary_key_columns=EXCLUDED.primary_key_columns,constraints_json=EXCLUDED.constraints_json,indexes_json=EXCLUDED.indexes_json,structure_hash=EXCLUDED.structure_hash,metadata_version=CASE WHEN metadata_tables.structure_hash<>EXCLUDED.structure_hash THEN metadata_tables.metadata_version+1 ELSE metadata_tables.metadata_version END,management_status=CASE WHEN metadata_tables.asset_status='INACTIVE' THEN 'ENABLED' ELSE metadata_tables.management_status END,asset_status='ACTIVE',last_sync_at=EXCLUDED.last_sync_at
		RETURNING id::text`, source.TenantID, source.ID, table.CatalogName, table.SchemaName, table.Name, table.Type, table.SourceComment, table.EstimatedRowCount, table.PrimaryKeyColumns, constraints, indexes, hash, watermark).Scan(&id)
	if err != nil {
		return "", err
	}
	if change == "ADDED" || change == "REACTIVATED" || oldHash != hash {
		if _, err := tx.Exec(ctx, `INSERT INTO platform.metadata_diffs(tenant_id,data_source_id,object_type,object_key,change_type,before_json,after_json) VALUES($1,$2,'TABLE',$3,$4,$5,$6)`, source.TenantID, source.ID, table.SchemaName+"."+table.Name, change, nullJSON(oldPayload), payload); err != nil {
			return "", err
		}
	}
	columnNames := make([]string, 0, len(table.Columns))
	for _, column := range table.Columns {
		columnNames = append(columnNames, column.Name)
		if err := r.upsertMetadataColumn(ctx, tx, source, id, table, column, watermark); err != nil {
			return "", err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.metadata_diffs(tenant_id,data_source_id,object_type,object_key,change_type,before_json)
		SELECT tenant_id,$2,'COLUMN',$3||'.'||column_name,'REMOVED',to_jsonb(c) FROM platform.metadata_columns c
		WHERE table_id=$1 AND asset_status='ACTIVE' AND NOT(column_name=ANY($4::text[]))`, id, source.ID, table.SchemaName+"."+table.Name, columnNames); err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, `UPDATE platform.metadata_columns SET asset_status='INACTIVE',last_sync_at=$2 WHERE table_id=$1 AND NOT(column_name=ANY($3::text[]))`, id, watermark, columnNames)
	return id, err
}

// upsertMetadataColumn 更新字段技术元数据，并保留前后快照用于差异追踪。
func (r *PostgresRepository) upsertMetadataColumn(ctx context.Context, tx pgx.Tx, source Source, tableID string, table MetadataTable, column MetadataColumn, watermark time.Time) error {
	hash, payload, err := metadataHash(column)
	if err != nil {
		return err
	}
	var oldHash, oldStatus string
	var oldPayload []byte
	err = tx.QueryRow(ctx, `SELECT structure_hash,asset_status::text,to_jsonb(metadata_columns) FROM platform.metadata_columns WHERE table_id=$1 AND column_name=$2`, tableID, column.Name).Scan(&oldHash, &oldStatus, &oldPayload)
	change := "CHANGED"
	if errors.Is(err, pgx.ErrNoRows) {
		change = "ADDED"
	} else if err != nil {
		return err
	}
	if oldStatus == "INACTIVE" && oldHash == hash {
		change = "REACTIVATED"
	}
	_, err = tx.Exec(ctx, `INSERT INTO platform.metadata_columns(tenant_id,table_id,column_name,ordinal_position,source_comment,native_type,canonical_type,length,numeric_precision,numeric_scale,nullable,default_value,is_primary_key,is_foreign_key,is_unique,structure_hash,last_sync_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
		ON CONFLICT(tenant_id,table_id,column_name) DO UPDATE SET ordinal_position=EXCLUDED.ordinal_position,source_comment=EXCLUDED.source_comment,native_type=EXCLUDED.native_type,canonical_type=EXCLUDED.canonical_type,length=EXCLUDED.length,numeric_precision=EXCLUDED.numeric_precision,numeric_scale=EXCLUDED.numeric_scale,nullable=EXCLUDED.nullable,default_value=EXCLUDED.default_value,is_primary_key=EXCLUDED.is_primary_key,is_foreign_key=EXCLUDED.is_foreign_key,is_unique=EXCLUDED.is_unique,structure_hash=EXCLUDED.structure_hash,asset_status='ACTIVE',last_sync_at=EXCLUDED.last_sync_at`, source.TenantID, tableID, column.Name, column.OrdinalPosition, column.SourceComment, column.NativeType, column.CanonicalType, column.Length, column.Precision, column.Scale, column.Nullable, column.DefaultValue, column.PrimaryKey, column.ForeignKey, column.Unique, hash, watermark)
	if err != nil {
		return err
	}
	if change == "ADDED" || change == "REACTIVATED" || oldHash != hash {
		_, err = tx.Exec(ctx, `INSERT INTO platform.metadata_diffs(tenant_id,data_source_id,object_type,object_key,change_type,before_json,after_json) VALUES($1,$2,'COLUMN',$3,$4,$5,$6)`, source.TenantID, source.ID, table.SchemaName+"."+table.Name+"."+column.Name, change, nullJSON(oldPayload), payload)
	}
	return err
}

// nullJSON 将空历史快照转换为数据库 NULL。
func nullJSON(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}

// metadataHash 对规范 JSON 计算 SHA-256，同时返回可持久化快照。
func metadataHash(value any) (string, []byte, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), payload, nil
}

// metadataTableHash 排除易波动统计信息后计算表结构哈希。
func metadataTableHash(table MetadataTable) (string, []byte, error) {
	// 估算行数会随统计信息波动，不应触发结构版本变化。
	table.EstimatedRowCount = nil
	return metadataHash(table)
}
