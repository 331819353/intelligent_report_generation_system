package datasource

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"intelligent-report-generation-system/internal/platform/database"
)

// NextFileVersion 计算文件资产的下一个单调递增版本号。
func (r *PostgresRepository) NextFileVersion(ctx context.Context, tenantID, assetID string) (version int, err error) {
	err = database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT current_version+1 FROM platform.file_assets WHERE id=$1 AND deleted_at IS NULL`, assetID).Scan(&version)
		if errors.Is(err, pgx.ErrNoRows) {
			version = 1
			return nil
		}
		return err
	})
	return
}

// SaveFileVersion 在事务中登记新版本并切换当前版本指针。
func (r *PostgresRepository) SaveFileVersion(ctx context.Context, asset FileAsset, bucket, key string, config map[string]any) error {
	configJSON, err := json.Marshal(config)
	if err != nil {
		return err
	}
	summaryJSON, err := json.Marshal(asset.WorkbookSummary)
	if err != nil {
		return err
	}
	return database.WithTenantTx(ctx, r.pool, asset.TenantID, func(tx pgx.Tx) error {
		if asset.CurrentVersion == 1 {
			if _, err := tx.Exec(ctx, `INSERT INTO platform.file_assets(id,tenant_id,filename,mime_type,current_version) VALUES($1,$2,$3,$4,1)`, asset.ID, asset.TenantID, asset.Filename, asset.MimeType); err != nil {
				return err
			}
		} else {
			tag, err := tx.Exec(ctx, `UPDATE platform.file_assets SET filename=$1,mime_type=$2,current_version=$3 WHERE id=$4 AND current_version=$3-1 AND deleted_at IS NULL`, asset.Filename, asset.MimeType, asset.CurrentVersion, asset.ID)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return errors.New("concurrent file version update")
			}
		}
		_, err := tx.Exec(ctx, `INSERT INTO platform.file_asset_versions(id,tenant_id,file_asset_id,version,filename,mime_type,size_bytes,sha256,storage_bucket,storage_key,parse_config,workbook_summary) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, asset.VersionID, asset.TenantID, asset.ID, asset.CurrentVersion, asset.Filename, asset.MimeType, asset.SizeBytes, asset.SHA256, bucket, key, configJSON, summaryJSON)
		return err
	})
}

// CurrentFileVersion 加载当前版本的对象位置、校验和与解析配置。
func (r *PostgresRepository) CurrentFileVersion(ctx context.Context, tenantID, assetID string) (out FileVersion, err error) {
	err = database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		var configJSON, summaryJSON []byte
		err := tx.QueryRow(ctx, `SELECT a.id::text,a.tenant_id::text,a.filename,a.mime_type,a.current_version,v.id::text,v.size_bytes,v.sha256,v.storage_bucket,v.storage_key,v.parse_config,v.workbook_summary FROM platform.file_assets a JOIN platform.file_asset_versions v ON v.file_asset_id=a.id AND v.version=a.current_version WHERE a.id=$1 AND a.deleted_at IS NULL`, assetID).Scan(&out.ID, &out.TenantID, &out.Filename, &out.MimeType, &out.CurrentVersion, &out.VersionID, &out.SizeBytes, &out.SHA256, &out.StorageBucket, &out.StorageKey, &configJSON, &summaryJSON)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(configJSON, &out.ParseConfig); err != nil {
			return err
		}
		out.Version = out.CurrentVersion
		return json.Unmarshal(summaryJSON, &out.WorkbookSummary)
	})
	return
}

// ListFileVersions 查询文件资产的全部历史版本。
func (r *PostgresRepository) ListFileVersions(ctx context.Context, tenantID, assetID string) (out []FileAsset, err error) {
	err = database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT a.id::text,a.tenant_id::text,v.filename,v.mime_type,a.current_version,v.id::text,v.version,v.size_bytes,v.sha256,v.workbook_summary FROM platform.file_assets a JOIN platform.file_asset_versions v ON v.file_asset_id=a.id WHERE a.id=$1 AND a.deleted_at IS NULL ORDER BY v.version DESC`, assetID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item FileAsset
			var summary []byte
			if err := rows.Scan(&item.ID, &item.TenantID, &item.Filename, &item.MimeType, &item.CurrentVersion, &item.VersionID, &item.Version, &item.SizeBytes, &item.SHA256, &summary); err != nil {
				return err
			}
			if err := json.Unmarshal(summary, &item.WorkbookSummary); err != nil {
				return err
			}
			out = append(out, item)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(out) == 0 {
			return pgx.ErrNoRows
		}
		return nil
	})
	return
}
