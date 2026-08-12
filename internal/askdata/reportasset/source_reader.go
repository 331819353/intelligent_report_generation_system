package reportasset

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/toolhost"
	"intelligent-report-generation-system/internal/platform/database"
)

const maxReportSourceIDs = 100

// ReportSources resolves only certified, projected assets that the current
// viewer may open. The SQL reuses report_v2_can_access so a search-index hit can
// never become a report-title or deep-link side channel.
func (store *PostgresProjectionRuntimeStore) ReportSources(
	ctx context.Context,
	scope askdata.PolicyScope,
	domainID askdata.ID,
	assetIDs []askdata.ID,
) (map[askdata.ID]toolhost.ReportSourceSummary, error) {
	if store == nil || store.pool == nil || scope.Validate() != nil || domainID.Validate() != nil ||
		len(assetIDs) > maxReportSourceIDs {
		return nil, ErrAssetIneligible
	}
	domainAllowed := false
	ids := make([]string, 0, len(assetIDs))
	seen := map[askdata.ID]bool{}
	for _, allowed := range scope.DomainIDs {
		domainAllowed = domainAllowed || allowed == domainID
	}
	for _, id := range assetIDs {
		if id.Validate() != nil {
			return nil, ErrAssetIneligible
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, string(id))
		}
	}
	if !domainAllowed {
		return nil, ErrAssetIneligible
	}
	result := make(map[askdata.ID]toolhost.ReportSourceSummary, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	ctx = database.WithAccessContext(ctx, string(scope.ActorID), string(domainID))
	err := database.WithTenantTx(ctx, store.pool, string(scope.TenantID), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT asset.id::text,asset.report_id::text,asset.report_version_id::text,
			asset.component_id,asset.report_title,asset.block_title,asset.chart_type,asset.chart_version,
			asset.semantic_release_id::text,asset.component_content_hash
		FROM askdata.report_semantic_assets AS asset
		JOIN platform.reports AS report ON report.id=asset.report_id AND report.tenant_id=asset.tenant_id
		JOIN platform.report_versions AS version ON version.id=asset.report_version_id
			AND version.report_id=asset.report_id AND version.tenant_id=asset.tenant_id
		WHERE asset.tenant_id=$1::uuid AND asset.domain_id=$2::uuid
			AND asset.id::text=ANY($3::text[]) AND asset.state='CERTIFIED'
			AND asset.projection_state='READY' AND asset.semantic_release_id=$4::uuid
			AND version.artifact_state='READY' AND report.current_published_version_id=version.id
			AND platform.report_v2_can_access(asset.report_id,ARRAY['VIEW']::text[])
		ORDER BY asset.id`, scope.TenantID, domainID, ids, scope.Release.ReleaseID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id askdata.ID
			var source toolhost.ReportSourceSummary
			if err := rows.Scan(&id, &source.ReportID, &source.ReportVersionID, &source.ComponentID,
				&source.ReportTitle, &source.ComponentTitle, &source.ComponentType, &source.ComponentVersion,
				&source.SemanticReleaseID, &source.ComponentHash); err != nil {
				return err
			}
			source.ReportTitle = strings.TrimSpace(source.ReportTitle)
			source.ComponentTitle = strings.TrimSpace(source.ComponentTitle)
			if source.ReportTitle == "" || source.Validate() != nil {
				return fmt.Errorf("%w: report source metadata is invalid", ErrAssetIneligible)
			}
			result[id] = source
		}
		return rows.Err()
	})
	return result, err
}
