package inbound

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/platform/database"
	"intelligent-report-generation-system/internal/report"
	"intelligent-report-generation-system/internal/report/store"
)

type PostgresAuthorizer struct{ pool *pgxpool.Pool }

func NewPostgresAuthorizer(pool *pgxpool.Pool) *PostgresAuthorizer {
	return &PostgresAuthorizer{pool: pool}
}

func (authorizer *PostgresAuthorizer) AuthorizeReportEdit(
	ctx context.Context, identity store.Identity, reportID askdata.ID,
) error {
	if authorizer == nil || authorizer.pool == nil || identity.Validate() != nil || reportID.Validate() != nil {
		return ErrUnauthorized
	}
	ctx = database.WithAccessContext(ctx, string(identity.ActorID), string(identity.DomainID))
	allowed := false
	err := database.WithTenantTx(ctx, authorizer.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1 FROM platform.reports AS report
			JOIN platform.users AS actor ON actor.id=$2 AND actor.tenant_id=report.tenant_id
			JOIN platform.domain_memberships AS membership
			  ON membership.tenant_id=report.tenant_id AND membership.domain_id=report.domain_id
			 AND membership.user_id=actor.id AND membership.status='ACTIVE'
			WHERE report.id=$1 AND report.tenant_id=$3 AND report.domain_id=$4 AND report.status='ACTIVE'
			  AND actor.status='ACTIVE' AND actor.deleted_at IS NULL
			  AND (
			    report.owner_user_id=actor.id OR membership.member_role='DOMAIN_ADMIN'
			    OR platform.user_is_asset_administrator()
			    OR EXISTS(
			      SELECT 1 FROM platform.object_permissions AS grant
			      WHERE grant.tenant_id=report.tenant_id AND grant.object_type='REPORT'
			        AND grant.object_id=report.id AND grant.action='EDIT'
			        AND ((grant.subject_type='USER' AND grant.subject_id=actor.id)
			          OR (grant.subject_type='ROLE' AND EXISTS(
			            SELECT 1 FROM platform.user_roles AS assignment
			            JOIN platform.roles AS role ON role.id=assignment.role_id AND role.tenant_id=assignment.tenant_id
			            WHERE assignment.tenant_id=report.tenant_id AND assignment.user_id=actor.id
			              AND assignment.role_id=grant.subject_id AND role.status='ACTIVE' AND role.deleted_at IS NULL
			          )))
			    )
			  )
		)`, reportID, identity.ActorID, identity.TenantID, identity.DomainID).Scan(&allowed)
	})
	if err != nil {
		return err
	}
	if !allowed {
		return ErrUnauthorized
	}
	return nil
}

func (authorizer *PostgresAuthorizer) AuthorizeSemanticBinding(
	ctx context.Context, identity store.Identity, reference report.SemanticQueryRef,
) error {
	if authorizer == nil || authorizer.pool == nil || identity.Validate() != nil ||
		reference.DatasetVersionID == nil || reference.ResolvedTimeSpec == nil || len(reference.EvidenceRefs) == 0 {
		return ErrUnauthorized
	}
	versionIDs := semanticVersionIDs(reference)
	if len(versionIDs) == 0 {
		return ErrUnauthorized
	}
	ctx = database.WithAccessContext(ctx, string(identity.ActorID), string(identity.DomainID))
	allowed := false
	err := database.WithTenantTx(ctx, authorizer.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT
			EXISTS(
			 SELECT 1 FROM askdata.releases AS release
			 WHERE release.id=$1 AND release.domain_id=$2 AND release.content_hash=$3
			   AND release.status IN ('ACTIVE','SUPERSEDED','RETAINED')
			)
			AND (
			 SELECT count(DISTINCT object_version_id)=cardinality($4::uuid[])
			 FROM askdata.release_objects
			 WHERE release_id=$1 AND domain_id=$2 AND object_version_id=ANY($4::uuid[])
			)
			AND EXISTS(
			 SELECT 1 FROM platform.dataset_versions AS version
			 WHERE version.id=$5 AND version.status IN ('PUBLISHED','STALE')
			   AND platform.dataset_version_can_read(version.id)
			)`, reference.SemanticReleaseID, identity.DomainID, reference.SemanticContentHash,
			versionIDs, *reference.DatasetVersionID).Scan(&allowed)
	})
	if err != nil {
		return err
	}
	if !allowed {
		return ErrUnauthorized
	}
	return nil
}

func semanticVersionIDs(reference report.SemanticQueryRef) []string {
	set := map[askdata.ID]struct{}{reference.SemanticIR.ModelVersionID: {}}
	for _, metric := range reference.SemanticIR.Metrics {
		set[metric.MetricVersionID] = struct{}{}
	}
	for _, group := range reference.SemanticIR.GroupBy {
		set[group.DimensionVersionID] = struct{}{}
	}
	for _, filter := range reference.SemanticIR.Filters {
		set[filter.DimensionVersionID] = struct{}{}
		for _, memberID := range filter.MemberVersionIDs {
			set[memberID] = struct{}{}
		}
	}
	if reference.SemanticIR.TimeRange != nil {
		set[reference.SemanticIR.TimeRange.DimensionVersionID] = struct{}{}
	}
	result := make([]string, 0, len(set))
	for id := range set {
		if id.Validate() != nil {
			return nil
		}
		result = append(result, string(id))
	}
	return result
}

var _ Authorizer = (*PostgresAuthorizer)(nil)
