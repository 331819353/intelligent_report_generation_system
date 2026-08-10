package reportasset

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/platform/database"
)

type CertificationIdentity struct{ TenantID, DomainID, ActorID askdata.ID }

func (identity CertificationIdentity) Validate() error {
	for _, id := range []askdata.ID{identity.TenantID, identity.DomainID, identity.ActorID} {
		if id.Validate() != nil {
			return ErrAssetIneligible
		}
	}
	return nil
}

type AssetView struct {
	ID, ReportID, ReportVersionID, ComponentID askdata.ID
	State                                      string
	ComponentContentHash                       askdata.ContentHash
	ProjectionState                            string
}

func (store *PostgresProjectionRuntimeStore) ListAssets(ctx context.Context, identity CertificationIdentity) ([]AssetView, error) {
	if store == nil || store.pool == nil || identity.Validate() != nil {
		return nil, ErrAssetIneligible
	}
	result := []AssetView{}
	err := database.WithTenantTx(ctx, store.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text,report_id::text,report_version_id::text,component_id,state,component_content_hash,projection_state FROM askdata.report_semantic_assets WHERE tenant_id=$1 AND domain_id=$2 ORDER BY created_at DESC,id`, identity.TenantID, identity.DomainID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item AssetView
			if err := rows.Scan(&item.ID, &item.ReportID, &item.ReportVersionID, &item.ComponentID, &item.State, &item.ComponentContentHash, &item.ProjectionState); err != nil {
				return err
			}
			result = append(result, item)
		}
		return rows.Err()
	})
	return result, err
}
func (store *PostgresProjectionRuntimeStore) Certify(ctx context.Context, identity CertificationIdentity, assetID askdata.ID, role string, hash askdata.ContentHash, note string) error {
	if store == nil || store.pool == nil || identity.Validate() != nil || assetID.Validate() != nil || hash.Validate() != nil || (role != "REPORT_OWNER" && role != "SEMANTIC_OWNER") || len(strings.TrimSpace(note)) > 2000 {
		return ErrAssetIneligible
	}
	return database.WithTenantTx(ctx, store.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO askdata.report_asset_certifications(tenant_id,report_semantic_asset_id,approver_user_id,approver_role,component_content_hash,note) SELECT tenant_id,id,$3,$4,$5,$6 FROM askdata.report_semantic_assets WHERE tenant_id=$1 AND domain_id=$2 AND id=$7 AND state='PENDING' ON CONFLICT(report_semantic_asset_id,approver_user_id) DO NOTHING`, identity.TenantID, identity.DomainID, identity.ActorID, role, hash, strings.TrimSpace(note), assetID)
		if err != nil || tag.RowsAffected() != 1 {
			return errors.Join(err, ErrAssetIneligible)
		}
		return nil
	})
}
func (store *PostgresProjectionRuntimeStore) Revoke(ctx context.Context, identity CertificationIdentity, assetID askdata.ID) error {
	if store == nil || store.pool == nil || identity.Validate() != nil || assetID.Validate() != nil {
		return ErrAssetIneligible
	}
	return database.WithTenantTx(ctx, store.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE askdata.report_semantic_assets asset SET state='REVOKED',updated_at=now() FROM platform.reports report WHERE asset.id=$1 AND asset.tenant_id=$2 AND asset.domain_id=$3 AND report.id=asset.report_id AND report.tenant_id=asset.tenant_id AND asset.state IN('PENDING','CERTIFIED','INVALIDATED') AND (report.owner_user_id=$4 OR platform.user_is_domain_administrator(asset.domain_id) OR platform.user_is_platform_administrator() OR EXISTS(SELECT 1 FROM askdata.metric_versions metric WHERE metric.id=ANY(asset.metric_version_ids) AND metric.owner_id=$4) OR EXISTS(SELECT 1 FROM askdata.dimensions dimension WHERE dimension.id=ANY(asset.dimension_version_ids) AND dimension.owner_id=$4))`, assetID, identity.TenantID, identity.DomainID, identity.ActorID)
		if err != nil || tag.RowsAffected() != 1 {
			return errors.Join(err, ErrAssetIneligible)
		}
		return nil
	})
}
