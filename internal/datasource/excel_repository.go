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

// SaveFileVersion 在事务中登记新版本并切换当前版本指针。覆盖已有文件时，所有
// 引用该文件资产的数据源会回到待验证状态并推进版本，阻止旧文件上的后台任务落库。
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
		if _, err := tx.Exec(ctx, `INSERT INTO platform.file_asset_versions(id,tenant_id,file_asset_id,version,filename,mime_type,size_bytes,sha256,storage_bucket,storage_key,parse_config,workbook_summary) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, asset.VersionID, asset.TenantID, asset.ID, asset.CurrentVersion, asset.Filename, asset.MimeType, asset.SizeBytes, asset.SHA256, bucket, key, configJSON, summaryJSON); err != nil {
			return err
		}
		if asset.CurrentVersion > 1 {
			drafts, err := lockFileSourceDrafts(ctx, tx, asset.TenantID, asset.ID)
			if err != nil {
				return err
			}
			for _, draft := range drafts {
				configHash, err := fileSourceVersionConfigurationHash(draft.Source, asset.VersionID)
				if err != nil {
					return err
				}
				var versionID string
				if err := tx.QueryRow(ctx, `INSERT INTO platform.data_source_versions(
						tenant_id,data_source_id,version_no,source_type,config,secret_ref,file_asset_id,
						file_version_id,config_hash,created_by)
					VALUES($1,$2,$3,$4,$5,NULLIF($6,''),$7,$8,$9,NULLIF($10,'')::uuid)
					RETURNING id::text`,
					asset.TenantID, draft.Source.ID, draft.NextVersion, draft.Source.Type,
					draft.ConfigJSON, draft.Source.SecretRef, draft.Source.FileAssetID,
					asset.VersionID, configHash, draft.CreatedBy).Scan(&versionID); err != nil {
					return err
				}
				tag, err := tx.Exec(ctx, `UPDATE platform.data_sources AS source SET
					current_draft_version_id=version.id,validation_status='UNTESTED',
					last_tested_at=NULL,last_tested_version_id=NULL,last_tested_config_hash=NULL,test_expires_at=NULL,
					status=CASE WHEN source.current_published_version_id IS NULL
						THEN 'DRAFT'::platform.data_source_status ELSE source.status END,
					last_error=CASE WHEN source.current_published_version_id IS NULL THEN NULL ELSE source.last_error END,
					version=source.version+1
					FROM platform.data_source_versions AS version
					WHERE source.id=$1 AND source.current_draft_version_id=$2
					  AND source.deleted_at IS NULL AND version.id=$3
					  AND version.data_source_id=source.id`,
					draft.Source.ID, draft.DraftVersionID, versionID)
				if err != nil {
					return err
				}
				if tag.RowsAffected() != 1 {
					return errors.New("concurrent data source draft update")
				}
			}
		}
		return nil
	})
}

func fileSourceVersionConfigurationHash(source Source, fileVersionID string) (string, error) {
	source.FileVersionID = fileVersionID
	return sourceConfigurationHash(source)
}

type fileSourceDraft struct {
	Source         Source
	DraftVersionID string
	ConfigJSON     []byte
	CreatedBy      string
	NextVersion    int64
}

// lockFileSourceDrafts 固定所有引用当前文件资产的数据源草稿。配置摘要必须在
// Go 中复用 sourceConfigurationHash；PostgreSQL jsonb 的键顺序和空白表示与
// encoding/json 不同，不能在 SQL 中重新实现同名摘要。
func lockFileSourceDrafts(
	ctx context.Context,
	tx pgx.Tx,
	tenantID, assetID string,
) ([]fileSourceDraft, error) {
	rows, err := tx.Query(ctx, `SELECT
			source.id::text,draft.id::text,draft.source_type::text,draft.config,
			COALESCE(draft.secret_ref,''),draft.file_asset_id::text,
			COALESCE(source.updated_by::text,''),
			(SELECT COALESCE(max(existing.version_no),0)+1
			 FROM platform.data_source_versions AS existing
			 WHERE existing.data_source_id=source.id)
		FROM platform.data_sources AS source
		JOIN platform.data_source_versions AS draft
		  ON draft.id=source.current_draft_version_id
		WHERE source.tenant_id=$1 AND draft.file_asset_id=$2
		  AND source.deleted_at IS NULL
		ORDER BY source.id
		FOR UPDATE OF source`, tenantID, assetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	drafts := make([]fileSourceDraft, 0)
	for rows.Next() {
		var draft fileSourceDraft
		draft.Source.TenantID = tenantID
		if err := rows.Scan(
			&draft.Source.ID, &draft.DraftVersionID, &draft.Source.Type, &draft.ConfigJSON,
			&draft.Source.SecretRef, &draft.Source.FileAssetID, &draft.CreatedBy,
			&draft.NextVersion,
		); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(draft.ConfigJSON, &draft.Source.Config); err != nil {
			return nil, err
		}
		drafts = append(drafts, draft)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return drafts, nil
}

// SaveFileInspection finalizes the deterministic parse plan beside one exact immutable
// file version. A concurrent re-upload cannot misattach the plan because versionID and
// assetID are both checked; retaining inspections for older published versions is required
// while a newer draft is still waiting for publication.
func (r *PostgresRepository) SaveFileInspection(ctx context.Context, tenantID, assetID, versionID string, config, summary map[string]any) error {
	configJSON, err := json.Marshal(config)
	if err != nil {
		return err
	}
	summaryJSON, err := json.Marshal(summary)
	if err != nil {
		return err
	}
	return database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO platform.file_asset_inspections(file_version_id,tenant_id,parse_config,workbook_summary)
			SELECT v.id,v.tenant_id,$1,$2 FROM platform.file_asset_versions AS v JOIN platform.file_assets AS a ON a.id=v.file_asset_id AND a.tenant_id=v.tenant_id
			WHERE v.id=$3 AND v.file_asset_id=$4 AND a.deleted_at IS NULL
			ON CONFLICT(file_version_id) DO UPDATE SET parse_config=EXCLUDED.parse_config,workbook_summary=EXCLUDED.workbook_summary`, configJSON, summaryJSON, versionID, assetID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return pgx.ErrNoRows
		}
		return nil
	})
}

// CurrentFileVersion 加载当前版本的对象位置、校验和与解析配置。
func (r *PostgresRepository) CurrentFileVersion(ctx context.Context, tenantID, assetID string) (out FileVersion, err error) {
	err = database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		var configJSON, summaryJSON []byte
		err := tx.QueryRow(ctx, `SELECT a.id::text,a.tenant_id::text,a.filename,a.mime_type,a.current_version,v.id::text,v.size_bytes,v.sha256,v.storage_bucket,v.storage_key,COALESCE(i.parse_config,v.parse_config),COALESCE(i.workbook_summary,v.workbook_summary) FROM platform.file_assets a JOIN platform.file_asset_versions v ON v.file_asset_id=a.id AND v.version=a.current_version LEFT JOIN platform.file_asset_inspections i ON i.file_version_id=v.id AND i.tenant_id=v.tenant_id WHERE a.id=$1 AND a.deleted_at IS NULL`, assetID).Scan(&out.ID, &out.TenantID, &out.Filename, &out.MimeType, &out.CurrentVersion, &out.VersionID, &out.SizeBytes, &out.SHA256, &out.StorageBucket, &out.StorageKey, &configJSON, &summaryJSON)
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

// FileVersionByID 按不可变版本标识加载对象位置，禁止查询阶段回退到当前版本。
func (r *PostgresRepository) FileVersionByID(ctx context.Context, tenantID, versionID string) (out FileVersion, err error) {
	err = database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		var configJSON, summaryJSON []byte
		err := tx.QueryRow(ctx, `SELECT a.id::text,a.tenant_id::text,v.filename,v.mime_type,a.current_version,v.id::text,v.version,v.size_bytes,v.sha256,v.storage_bucket,v.storage_key,COALESCE(i.parse_config,v.parse_config),COALESCE(i.workbook_summary,v.workbook_summary)
			FROM platform.file_asset_versions v JOIN platform.file_assets a ON a.id=v.file_asset_id AND a.tenant_id=v.tenant_id
			LEFT JOIN platform.file_asset_inspections i ON i.file_version_id=v.id AND i.tenant_id=v.tenant_id
			WHERE v.id::text=$1 AND a.deleted_at IS NULL`, versionID).
			Scan(&out.ID, &out.TenantID, &out.Filename, &out.MimeType, &out.CurrentVersion, &out.VersionID, &out.Version, &out.SizeBytes, &out.SHA256, &out.StorageBucket, &out.StorageKey, &configJSON, &summaryJSON)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(configJSON, &out.ParseConfig); err != nil {
			return err
		}
		return json.Unmarshal(summaryJSON, &out.WorkbookSummary)
	})
	return out, err
}

// ListFileVersions 查询文件资产的全部历史版本。
func (r *PostgresRepository) ListFileVersions(ctx context.Context, tenantID, assetID string) (out []FileAsset, err error) {
	err = database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT a.id::text,a.tenant_id::text,v.filename,v.mime_type,a.current_version,v.id::text,v.version,v.size_bytes,v.sha256,COALESCE(i.workbook_summary,v.workbook_summary) FROM platform.file_assets a JOIN platform.file_asset_versions v ON v.file_asset_id=a.id LEFT JOIN platform.file_asset_inspections i ON i.file_version_id=v.id AND i.tenant_id=v.tenant_id WHERE a.id=$1 AND a.deleted_at IS NULL ORDER BY v.version DESC`, assetID)
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
