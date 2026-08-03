package semanticasset

import (
	"context"

	"github.com/jackc/pgx/v5"
	"intelligent-report-generation-system/internal/platform/database"
)

func (store *PostgresStore) Readiness(
	ctx context.Context,
	tenantID string,
) (snapshot ReadinessSnapshot, err error) {
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		return readReadinessSnapshot(ctx, tx, &snapshot)
	})
	return snapshot, err
}

func readReadinessSnapshot(
	ctx context.Context,
	tx pgx.Tx,
	snapshot *ReadinessSnapshot,
) error {
	return tx.QueryRow(ctx, `WITH metric_counts AS (
				SELECT count(*)::int AS total,
					count(*) FILTER(
					  WHERE metric.status='PUBLISHED'
					    AND metric.current_published_version_id IS NOT NULL
					    AND version.status='PUBLISHED'
					)::int AS ready
				FROM platform.metrics AS metric
				LEFT JOIN platform.metric_versions AS version
				  ON version.tenant_id=metric.tenant_id
				 AND version.metric_id=metric.id
				 AND version.id=metric.current_published_version_id
				WHERE metric.tenant_id=platform.current_tenant_id()
				  AND metric.deleted_at IS NULL
			), latest_dimension_jobs AS (
				SELECT DISTINCT ON (job.dimension_id)
					job.dimension_id,job.dimension_version,job.status
				FROM platform.dimension_member_refresh_jobs AS job
				WHERE job.tenant_id=platform.current_tenant_id()
				ORDER BY job.dimension_id,job.created_at DESC,job.id DESC
			), dimension_counts AS (
				SELECT count(*)::int AS total,
					count(*) FILTER(WHERE dimension.status='PUBLISHED')::int AS published,
					count(*) FILTER(
					  WHERE dimension.status='PUBLISHED' AND (
					    dimension.member_index_policy IN ('EXACT_ONLY','NONE')
					    OR (
					      dimension.member_index_policy='FULL'
					      AND job.dimension_version=dimension.version
					      AND job.status='SUCCEEDED'
					    )
					  )
					)::int AS ready
				FROM platform.semantic_dimensions AS dimension
				LEFT JOIN latest_dimension_jobs AS job
				  ON job.dimension_id=dimension.id
				WHERE dimension.tenant_id=platform.current_tenant_id()
			), term_counts AS (
				SELECT count(*) FILTER(WHERE status='ACTIVE')::int AS active,
					count(*) FILTER(
					  WHERE status='ACTIVE'
					    AND embedding_status IN ('SUCCEEDED','SKIPPED')
					)::int AS projected
				FROM platform.semantic_term_assets
				WHERE tenant_id=platform.current_tenant_id()
			), rule_counts AS (
				SELECT count(*)::int AS total,
					count(*) FILTER(WHERE status='ACTIVE')::int AS active
				FROM platform.semantic_parsing_rules
				WHERE tenant_id IS NULL
				   OR tenant_id=platform.current_tenant_id()
			), decision_policy AS (
				SELECT dimension.id AS dimension_id,
					dimension.member_index_policy,
					bool_or(policy.status IN ('PENDING','RUNNING','FAILED')) AS blocked,
					bool_or(policy.status='SUCCEEDED') AS succeeded
				FROM platform.semantic_dimensions AS dimension
				LEFT JOIN platform.dimension_where_design_policies AS policy
				  ON policy.tenant_id=dimension.tenant_id
				 AND policy.dimension_id=dimension.id
				WHERE dimension.tenant_id=platform.current_tenant_id()
				  AND dimension.status='PUBLISHED'
				GROUP BY dimension.id,dimension.member_index_policy
			), decision_counts AS (
				SELECT
					(SELECT count(*)::int
					 FROM platform.dimension_where_decisions
					 WHERE tenant_id=platform.current_tenant_id()) AS decisions,
					count(*)::int AS groups,
					count(*) FILTER(
					  WHERE NOT COALESCE(blocked,false)
					    AND (member_index_policy<>'FULL'
					      OR COALESCE(succeeded,false) OR NOT EXISTS(
					      SELECT 1 FROM platform.dimension_members AS member
					      WHERE member.tenant_id=platform.current_tenant_id()
					        AND member.dimension_id=decision_policy.dimension_id
					        AND member.status='ACTIVE'
					    ))
					)::int AS ready
				FROM decision_policy
			), graph AS (
				SELECT state.status,COALESCE(generation.id::text,'') AS id,
					COALESCE(generation.generation,0) AS generation,
					COALESCE(generation.status,'') AS generation_status,
					state.requested_event_version,state.applied_event_version,
					COALESCE(generation.node_count,0) AS node_count,
					COALESCE(generation.edge_count,0) AS edge_count,state.error_code
				FROM platform.semantic_graph_projection_state AS state
				LEFT JOIN platform.semantic_graph_generations AS generation
				  ON generation.tenant_id=state.tenant_id
				 AND generation.id=state.current_generation_id
				WHERE state.tenant_id=platform.current_tenant_id()
			)
			SELECT metric_counts.total,metric_counts.ready,
				dimension_counts.total,dimension_counts.published,
				dimension_counts.ready,term_counts.active,term_counts.projected,
				rule_counts.total,rule_counts.active,
				decision_counts.decisions,decision_counts.groups,
				decision_counts.ready,
				COALESCE(graph.status,'PENDING'),COALESCE(graph.id,''),
				COALESCE(graph.generation,0),COALESCE(graph.generation_status,''),
				COALESCE(graph.requested_event_version,0),
				COALESCE(graph.applied_event_version,0),
				COALESCE(graph.node_count,0),COALESCE(graph.edge_count,0),
				COALESCE(graph.error_code,'')
			FROM metric_counts CROSS JOIN dimension_counts
			CROSS JOIN term_counts CROSS JOIN rule_counts
			CROSS JOIN decision_counts LEFT JOIN graph ON true`).Scan(
		&snapshot.MetricTotal, &snapshot.MetricReady,
		&snapshot.DimensionTotal, &snapshot.DimensionPublished,
		&snapshot.DimensionReady, &snapshot.TermActive,
		&snapshot.TermProjected, &snapshot.ParsingRuleTotal,
		&snapshot.ParsingRuleActive, &snapshot.DecisionCount,
		&snapshot.DecisionGroupTotal, &snapshot.DecisionGroupReady,
		&snapshot.GraphState, &snapshot.GraphGenerationID,
		&snapshot.GraphGeneration, &snapshot.GraphGenerationState,
		&snapshot.GraphRequestedVersion, &snapshot.GraphAppliedVersion,
		&snapshot.GraphNodeCount, &snapshot.GraphEdgeCount,
		&snapshot.GraphErrorCode,
	)
}
