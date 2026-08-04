package access

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/platform/database"
)

type AssetSharing struct {
	ResourceType string `json:"resourceType"`
	ResourceID   string `json:"resourceId"`
	DomainID     string `json:"domainId"`
	DomainName   string `json:"domainName"`
	OwnerUserID  string `json:"ownerUserId,omitempty"`
	SharingScope string `json:"sharingScope"`
}

type assetScopeTarget struct {
	table       string
	ownerColumn string
}

var assetScopeTargets = map[string]assetScopeTarget{
	"DATA_SOURCE": {table: "platform.data_sources", ownerColumn: "owner_user_id"},
	"DATASET":     {table: "platform.datasets", ownerColumn: "created_by"},
}

type AssetScopeStore struct{ pool *pgxpool.Pool }

var ErrAssetSharingOwnerDomainRequired = errors.New("only the asset owner, domain administrator or platform administrator can change its sharing scope")

func NewAssetScopeStore(pool *pgxpool.Pool) *AssetScopeStore {
	return &AssetScopeStore{pool: pool}
}

func normalizeAssetScopeTarget(resourceType string) (string, assetScopeTarget, error) {
	resourceType = strings.ToUpper(strings.TrimSpace(resourceType))
	target, ok := assetScopeTargets[resourceType]
	if !ok {
		return "", target, errors.New("unsupported asset resource type")
	}
	return resourceType, target, nil
}

func (s *AssetScopeStore) Get(
	ctx context.Context, tenantID, resourceType, resourceID string,
) (AssetSharing, error) {
	resourceType, target, err := normalizeAssetScopeTarget(resourceType)
	if err != nil {
		return AssetSharing{}, err
	}
	var sharing AssetSharing
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		query := fmt.Sprintf(`SELECT
			  asset.id::text,asset.domain_id::text,domain.name,
			  COALESCE(asset.%s::text,''),asset.sharing_scope::text
			FROM %s AS asset
			JOIN platform.business_domains AS domain
			  ON domain.id=asset.domain_id AND domain.tenant_id=asset.tenant_id
			WHERE asset.id=$1::uuid`, target.ownerColumn, target.table)
		return tx.QueryRow(ctx, query, resourceID).Scan(
			&sharing.ResourceID, &sharing.DomainID, &sharing.DomainName,
			&sharing.OwnerUserID, &sharing.SharingScope,
		)
	})
	sharing.ResourceType = resourceType
	return sharing, err
}

func (s *AssetScopeStore) Update(
	ctx context.Context, tenantID, actorID, resourceType, resourceID, scope string,
) (AssetSharing, error) {
	resourceType, target, err := normalizeAssetScopeTarget(resourceType)
	if err != nil {
		return AssetSharing{}, err
	}
	scope = strings.ToUpper(strings.TrimSpace(scope))
	if scope != "PRIVATE" && scope != "DOMAIN" {
		return AssetSharing{}, errors.New("sharingScope must be PRIVATE or DOMAIN")
	}
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		query := fmt.Sprintf(`UPDATE %s
			SET sharing_scope=$2::platform.asset_share_scope
			WHERE id=$1::uuid
			  AND domain_id=platform.current_domain_id()
			  AND (
			    %s=$3::uuid
			    OR platform.user_is_platform_administrator()
			    OR platform.user_is_domain_administrator(domain_id)
			  )`, target.table, target.ownerColumn)
		result, updateErr := tx.Exec(ctx, query, resourceID, scope, actorID)
		if updateErr != nil {
			return updateErr
		}
		if result.RowsAffected() != 1 {
			return ErrAssetSharingOwnerDomainRequired
		}

		_, updateErr = tx.Exec(ctx, `INSERT INTO platform.audit_logs(
				tenant_id,actor_user_id,action,resource_type,resource_id,detail
			) VALUES($1,$2,'UPDATE_SHARING',$3,$4,jsonb_build_object(
				'sharingScope',$5::text
			))`, tenantID, actorID, resourceType, resourceID, scope)
		return updateErr
	})
	if err != nil {
		return AssetSharing{}, err
	}
	return s.Get(ctx, tenantID, resourceType, resourceID)
}
