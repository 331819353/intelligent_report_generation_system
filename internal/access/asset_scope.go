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

// AssetAccessDependent is one governed object that reads this asset. Narrowing
// an asset to PRIVATE does not delete anything, but it removes the reader
// permission every one of these depends on, so they are the real cost of the
// change and must be shown before it is applied rather than discovered after.
type AssetAccessDependent struct {
	Type        string `json:"type"`
	ID          string `json:"id"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	OwnerUserID string `json:"ownerUserId,omitempty"`
	// ForeignOwner marks a dependent owned by somebody other than the asset
	// owner: those are the ones a narrow would break for another person.
	ForeignOwner bool `json:"foreignOwner"`
}

type AssetAccessImpact struct {
	AssetSharing
	TargetScope        string                 `json:"targetScope"`
	DomainMemberCount  int                    `json:"domainMemberCount"`
	Dependents         []AssetAccessDependent `json:"dependents"`
	BlockingDependents int                    `json:"blockingDependents"`
	CanApply           bool                   `json:"canApply"`
	BlockedCode        string                 `json:"blockedCode,omitempty"`
}

type AssetScopeStore struct{ pool *pgxpool.Pool }

var (
	ErrAssetSharingOwnerDomainRequired = errors.New("only the asset owner, domain administrator or platform administrator can change its sharing scope")
	// ErrAssetScopeNarrowBlocked keeps a narrow from silently breaking other
	// people's governed objects.
	ErrAssetScopeNarrowBlocked = errors.New("asset still has governed dependents owned by other users; transfer or retire them before restricting access")
	// ErrAssetScopeOwnerRequired is the object-level last-owner protection: a
	// PRIVATE asset with no owner is reachable by nobody but administrators,
	// which is an orphaned asset rather than a private one.
	ErrAssetScopeOwnerRequired = errors.New("asset has no owner; transfer ownership before restricting it to PRIVATE")
)

const (
	assetScopeNarrowBlockedCode = "ASSET_SCOPE_NARROW_BLOCKED"
	assetScopeOwnerRequiredCode = "ASSET_SCOPE_OWNER_REQUIRED"
)

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

// Impact previews a scope change without applying it. Widening never removes a
// reader, so only a narrow to PRIVATE reports blocking dependents.
func (s *AssetScopeStore) Impact(
	ctx context.Context, tenantID, resourceType, resourceID, targetScope string,
) (AssetAccessImpact, error) {
	sharing, err := s.Get(ctx, tenantID, resourceType, resourceID)
	if err != nil {
		return AssetAccessImpact{}, err
	}
	targetScope = strings.ToUpper(strings.TrimSpace(targetScope))
	if targetScope == "" {
		targetScope = sharing.SharingScope
	}
	if targetScope != "PRIVATE" && targetScope != "DOMAIN" {
		return AssetAccessImpact{}, errors.New("sharingScope must be PRIVATE or DOMAIN")
	}
	impact := AssetAccessImpact{
		AssetSharing: sharing, TargetScope: targetScope,
		Dependents: []AssetAccessDependent{},
	}
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*)
			FROM platform.domain_memberships AS membership
			JOIN platform.users AS member
			  ON member.id=membership.user_id AND member.tenant_id=membership.tenant_id
			WHERE membership.tenant_id=$1::uuid AND membership.domain_id=$2::uuid
			  AND membership.status='ACTIVE' AND member.status='ACTIVE'
			  AND member.deleted_at IS NULL AND member.id<>$3::uuid`,
			tenantID, sharing.DomainID, nullableUUID(sharing.OwnerUserID),
		).Scan(&impact.DomainMemberCount); err != nil {
			return err
		}
		dependents, err := loadAssetDependentsTx(ctx, tx, tenantID, sharing)
		if err != nil {
			return err
		}
		impact.Dependents = dependents
		return nil
	})
	if err != nil {
		return AssetAccessImpact{}, err
	}
	for _, dependent := range impact.Dependents {
		if dependent.ForeignOwner {
			impact.BlockingDependents++
		}
	}
	impact.CanApply = true
	// Only a narrow can take access away, so only a narrow can be blocked.
	if targetScope == "PRIVATE" && sharing.SharingScope != "PRIVATE" {
		switch {
		case strings.TrimSpace(sharing.OwnerUserID) == "":
			impact.CanApply, impact.BlockedCode = false, assetScopeOwnerRequiredCode
		case impact.BlockingDependents > 0:
			impact.CanApply, impact.BlockedCode = false, assetScopeNarrowBlockedCode
		}
	}
	return impact, nil
}

// loadAssetDependentsTx walks the governed edges that actually exist for each
// supported asset type. A data source is reached through its metadata tables;
// a dataset is read directly by semantic models and by published reports.
func loadAssetDependentsTx(
	ctx context.Context, tx pgx.Tx, tenantID string, sharing AssetSharing,
) ([]AssetAccessDependent, error) {
	dependents := []AssetAccessDependent{}
	var query string
	switch sharing.ResourceType {
	case "DATA_SOURCE":
		query = `SELECT DISTINCT 'DATASET',dataset.id::text,dataset.name,dataset.status::text,
			COALESCE(dataset.created_by::text,'')
		FROM platform.metadata_tables AS table_asset
		JOIN platform.asset_dependencies AS dependency
		  ON dependency.tenant_id=table_asset.tenant_id
		 AND dependency.upstream_type='TABLE' AND dependency.upstream_id=table_asset.id
		 AND dependency.downstream_type='DATASET'
		JOIN platform.datasets AS dataset
		  ON dataset.id=dependency.downstream_id AND dataset.tenant_id=dependency.tenant_id
		WHERE table_asset.tenant_id=$1::uuid AND table_asset.data_source_id=$2::uuid
		  AND dataset.deleted_at IS NULL
		ORDER BY 2`
	case "DATASET":
		// Semantic models pin a dataset version, and published report versions
		// bind DATASET_VERSION dependencies. Both stop resolving if the reader
		// permission behind them disappears.
		query = `SELECT 'SEMANTIC_MODEL',model.id::text,model.name,model.status,
			COALESCE(model.owner_id::text,'')
		FROM askdata.semantic_models AS model
		WHERE model.tenant_id=$1::uuid AND model.dataset_id=$2::uuid
		  AND model.status<>'DEPRECATED'
		UNION ALL
		SELECT DISTINCT 'REPORT',report.id::text,report.name,report.status,
			COALESCE(report.owner_user_id::text,'')
		FROM platform.report_version_dependencies AS dependency
		JOIN platform.report_versions AS version
		  ON version.id=dependency.report_version_id AND version.tenant_id=dependency.tenant_id
		JOIN platform.reports AS report
		  ON report.id=version.report_id AND report.tenant_id=version.tenant_id
		JOIN platform.dataset_versions AS dataset_version
		  ON dataset_version.id::text=dependency.dependency_id
		 AND dataset_version.tenant_id=dependency.tenant_id
		WHERE dependency.tenant_id=$1::uuid AND dependency.dependency_type='DATASET_VERSION'
		  AND dataset_version.dataset_id=$2::uuid AND report.status<>'ARCHIVED'
		ORDER BY 2`
	default:
		return dependents, nil
	}
	rows, err := tx.Query(ctx, query, tenantID, sharing.ResourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var item AssetAccessDependent
		if err := rows.Scan(&item.Type, &item.ID, &item.Name, &item.Status, &item.OwnerUserID); err != nil {
			return nil, err
		}
		item.ForeignOwner = item.OwnerUserID != "" && item.OwnerUserID != sharing.OwnerUserID
		dependents = append(dependents, item)
	}
	return dependents, rows.Err()
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
	// The preview and the write share one rule, so what the reviewer was shown
	// is what the server enforces.
	impact, err := s.Impact(ctx, tenantID, resourceType, resourceID, scope)
	if err != nil {
		return AssetSharing{}, err
	}
	if !impact.CanApply {
		switch impact.BlockedCode {
		case assetScopeOwnerRequiredCode:
			return AssetSharing{}, ErrAssetScopeOwnerRequired
		default:
			return AssetSharing{}, ErrAssetScopeNarrowBlocked
		}
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
