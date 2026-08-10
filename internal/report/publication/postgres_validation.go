package publication

import (
	"context"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/ircontract"
	"intelligent-report-generation-system/internal/platform/database"
	reportmodel "intelligent-report-generation-system/internal/report"
	"intelligent-report-generation-system/internal/report/compiler"
	"intelligent-report-generation-system/internal/report/store"
)

type PostgresDependencyValidator struct{ pool *pgxpool.Pool }

func NewPostgresDependencyValidator(pool *pgxpool.Pool) *PostgresDependencyValidator {
	return &PostgresDependencyValidator{pool: pool}
}

func (validator *PostgresDependencyValidator) ValidateReportDependencies(
	ctx context.Context, identity store.Identity, definition reportmodel.ReportDefinition,
) compiler.ValidationIssues {
	if validator == nil || validator.pool == nil {
		return compiler.ValidationIssues{{Code: "REPORT_DEPENDENCY_LOOKUP_FAILED", Path: "dataContexts", Message: "dependency validator is unavailable"}}
	}
	ctx = database.WithAccessContext(ctx, string(identity.ActorID), string(identity.DomainID))
	issues := compiler.ValidationIssues{}
	err := database.WithTenantTx(ctx, validator.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		for index, dataContext := range definition.DataContexts {
			var published bool
			err := tx.QueryRow(ctx, `SELECT version.status='PUBLISHED' AND version.dataset_id=$2
				FROM platform.dataset_versions version WHERE version.id=$1`,
				dataContext.DatasetVersionID, dataContext.DatasetID).Scan(&published)
			if err != nil || !published {
				issues = append(issues, compiler.ValidationIssue{Code: "REPORT_BINDING_DATASET_NOT_ACTIVE", Path: fmt.Sprintf("dataContexts[%d]", index), Message: "dataset version is unavailable, unauthorized, or not published"})
			}
		}
		for index, component := range definition.Components {
			if component.DataBinding == nil || component.DataBinding.BindingMode != reportmodel.BindingSemanticIR || component.DataBinding.SemanticQueryRef == nil {
				continue
			}
			reference := component.DataBinding.SemanticQueryRef
			var eligible bool
			err := tx.QueryRow(ctx, `SELECT release.content_hash=$2
				 AND release.status IN('READY','ACTIVE','SUPERSEDED','RETAINED')
				 AND release.domain_id=$3
				 AND (SELECT count(*)=4 FROM askdata.release_projections projection
				      WHERE projection.release_id=release.id AND projection.status='READY'
				        AND projection.expected_content_hash=release.content_hash
				        AND projection.applied_content_hash=release.content_hash)
				FROM askdata.releases release WHERE release.id=$1`, reference.SemanticReleaseID,
				reference.SemanticContentHash, reference.SemanticIR.DomainID).Scan(&eligible)
			if err != nil || !eligible {
				issues = append(issues, compiler.ValidationIssue{Code: "REPORT_BINDING_RELEASE_RETIRED", Path: fmt.Sprintf("components[%d].dataBinding.semanticQueryRef", index), Message: "semantic release is unavailable, retired, or its projections are not ready"})
				continue
			}
			objects := semanticDependencies(reference.SemanticIR)
			for objectType, ids := range objects {
				if len(ids) == 0 {
					continue
				}
				var matched int
				if err := tx.QueryRow(ctx, `SELECT count(DISTINCT object_version_id) FROM askdata.release_objects
					WHERE release_id=$1 AND object_type=$2 AND object_version_id=ANY($3::uuid[])`,
					reference.SemanticReleaseID, objectType, ids).Scan(&matched); err != nil || matched != len(ids) {
					issues = append(issues, compiler.ValidationIssue{Code: "REPORT_BINDING_OBJECT_NOT_CERTIFIED", Path: fmt.Sprintf("components[%d].dataBinding.semanticQueryRef.semanticIr", index), Message: fmt.Sprintf("%s references are not fully certified in the pinned release", objectType)})
				}
			}
			metricIDs := objects["METRIC"]
			if len(metricIDs) > 1 {
				var unitCount, currencyCount int
				err := tx.QueryRow(ctx, `SELECT
					count(DISTINCT upper(btrim(unit))),
					count(DISTINCT NULLIF(upper(btrim(COALESCE(currency,''))),''))
					FROM askdata.metric_versions WHERE id=ANY($1::uuid[])`, metricIDs).Scan(&unitCount, &currencyCount)
				if err != nil {
					issues = append(issues, compiler.ValidationIssue{Code: "REPORT_DEPENDENCY_LOOKUP_FAILED", Path: fmt.Sprintf("components[%d].dataBinding.semanticQueryRef.semanticIr.metrics", index), Message: "metric unit compatibility could not be verified"})
				} else if unitCount > 1 || currencyCount > 1 {
					issues = append(issues, compiler.ValidationIssue{Code: "INCOMPATIBLE_UNIT", Path: fmt.Sprintf("components[%d].dataBinding.semanticQueryRef.semanticIr.metrics", index), Message: "one component cannot mix incomparable units or currencies"})
				}
			}
		}
		return nil
	})
	if err != nil {
		issues = append(issues, compiler.ValidationIssue{Code: "REPORT_DEPENDENCY_LOOKUP_FAILED", Path: "$", Message: err.Error()})
	}
	return issues
}

func semanticDependencies(ir ircontract.SemanticIR) map[string][]string {
	sets := map[string]map[string]struct{}{
		"SEMANTIC_MODEL": {string(ir.ModelVersionID): {}},
		"METRIC":         {}, "DIMENSION": {}, "MEMBER": {},
	}
	for _, metric := range ir.Metrics {
		sets["METRIC"][string(metric.MetricVersionID)] = struct{}{}
	}
	for _, group := range ir.GroupBy {
		sets["DIMENSION"][string(group.DimensionVersionID)] = struct{}{}
	}
	for _, filter := range ir.Filters {
		sets["DIMENSION"][string(filter.DimensionVersionID)] = struct{}{}
		for _, memberID := range filter.MemberVersionIDs {
			sets["MEMBER"][string(memberID)] = struct{}{}
		}
	}
	if ir.TimeRange != nil {
		sets["DIMENSION"][string(ir.TimeRange.DimensionVersionID)] = struct{}{}
	}
	result := map[string][]string{}
	for objectType, values := range sets {
		for id := range values {
			result[objectType] = append(result[objectType], id)
		}
		sort.Strings(result[objectType])
	}
	return result
}

type PostgresInsightValidator struct{ pool *pgxpool.Pool }

func NewPostgresInsightValidator(pool *pgxpool.Pool) *PostgresInsightValidator {
	return &PostgresInsightValidator{pool: pool}
}

func (validator *PostgresInsightValidator) ValidateReportInsights(
	ctx context.Context, identity store.Identity, reportID askdata.ID, acknowledge bool,
) compiler.ValidationIssues {
	if acknowledge {
		return nil
	}
	ctx = database.WithAccessContext(ctx, string(identity.ActorID), string(identity.DomainID))
	var stale []string
	err := database.WithTenantTx(ctx, validator.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT component_id FROM (
			SELECT DISTINCT ON(component_id) component_id,status FROM platform.report_insight_artifacts
			WHERE report_id=$1 ORDER BY component_id,created_at DESC,id DESC
		) latest WHERE status='STALE' ORDER BY component_id`, reportID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			stale = append(stale, id)
		}
		return rows.Err()
	})
	if err != nil {
		return compiler.ValidationIssues{{Code: "REPORT_INSIGHT_LOOKUP_FAILED", Path: "components", Message: err.Error()}}
	}
	issues := compiler.ValidationIssues{}
	for _, id := range stale {
		issues = append(issues, compiler.ValidationIssue{Code: "REPORT_INSIGHT_STALE", Path: "components." + id, Message: "stale insight must be regenerated or explicitly acknowledged"})
	}
	return issues
}
