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
		// Keep worker delivery authorization identical to the interactive report
		// editor. A second hand-written permission query had drifted from this
		// policy and also used the reserved SQL word "grant" as an alias, causing
		// every otherwise-valid add-to-report job to be rejected.
		return tx.QueryRow(ctx,
			`SELECT platform.report_v2_can_access($1,ARRAY['EDIT']::text[])`,
			reportID,
		).Scan(&allowed)
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
		!semanticBindingShapeAllowed(reference) {
		return ErrUnauthorized
	}
	// A timeless governed aggregation is a valid semantic binding. When an
	// explicit time range exists it must carry the compiler-produced resolved
	// contract; requiring one for every query made otherwise valid KPI answers
	// impossible to write back to a report.
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

func semanticBindingShapeAllowed(reference report.SemanticQueryRef) bool {
	return reference.DatasetVersionID != nil && len(reference.EvidenceRefs) > 0 &&
		(reference.SemanticIR.TimeRange == nil || reference.ResolvedTimeSpec != nil)
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
