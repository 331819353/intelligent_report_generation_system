package semanticmanagement

import (
	"context"

	"github.com/jackc/pgx/v5"
	"intelligent-report-generation-system/internal/platform/database"
)

func (s *PostgresStore) ListDimensionWhereDecisions(
	ctx context.Context,
	tenantID string,
	filter DimensionWhereDecisionFilter,
) (items []DimensionWhereDecision, total int, err error) {
	items = []DimensionWhereDecision{}
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rows, queryErr := tx.Query(ctx, `SELECT
				decision.id::text,decision.vector_key,
				decision.vector_key_hash,decision.embedding_model,
				decision.dimension_id::text,dimension.name,
				decision.dimension_field_id,decision.dimension_field_name,
				decision.dimension_description,decision.canonical_value,
				decision.aliases,decision.selected_member_count,
				decision.metric_id::text,decision.metric_version_id::text,
				decision.dataset_version_id::text,decision.metric_code,
				decision.metric_name,decision.metric_field_id,
				decision.materialization_id::text,decision.table_schema,
				decision.table_name,decision.predicate_operator,
				decision.where_condition,decision.compiled_condition,
				decision.llm_model,decision.llm_prompt_version,
				decision.llm_reason,
				COALESCE(decision.latest_query_plan_id::text,''),
				COALESCE(decision.dimension_member_id::text,''),
				decision.source_type,decision.source_input_hash,
				decision.observation_count,decision.first_seen_at,
				decision.last_seen_at,count(*) OVER()::int
			FROM platform.dimension_where_decisions AS decision
			JOIN platform.semantic_dimensions AS dimension
			  ON dimension.tenant_id=decision.tenant_id
			 AND dimension.id=decision.dimension_id
			WHERE decision.tenant_id=platform.current_tenant_id()
			  AND (
			    $1=''
			    OR decision.vector_key ILIKE '%'||$1||'%'
			    OR decision.canonical_value ILIKE '%'||$1||'%'
			    OR decision.dimension_field_name ILIKE '%'||$1||'%'
			    OR decision.metric_name ILIKE '%'||$1||'%'
			    OR decision.metric_code ILIKE '%'||$1||'%'
			    OR EXISTS(
			      SELECT 1 FROM unnest(decision.aliases) AS expanded(alias)
			      WHERE alias ILIKE '%'||$1||'%'
			    )
			  )
			  AND (
			    $2=''
			    OR decision.table_name ILIKE '%'||$2||'%'
			    OR decision.table_schema||'.'||decision.table_name
			       ILIKE '%'||$2||'%'
			  )
			  AND ($3='' OR decision.dimension_id::text=$3)
			ORDER BY decision.table_schema,decision.table_name,
				decision.dimension_field_name,decision.canonical_value,
				decision.metric_name,decision.id
			LIMIT $4 OFFSET $5`,
			filter.Query, filter.TableName, filter.DimensionID,
			filter.Limit, filter.Offset,
		)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var item DimensionWhereDecision
			if err := rows.Scan(
				&item.ID, &item.VectorKey, &item.VectorKeyHash,
				&item.EmbeddingModel, &item.DimensionID,
				&item.DimensionName, &item.DimensionFieldID,
				&item.DimensionFieldName, &item.DimensionDescription,
				&item.CanonicalValue, &item.Aliases,
				&item.SelectedMemberCount, &item.MetricID,
				&item.MetricVersionID, &item.DatasetVersionID,
				&item.MetricCode, &item.MetricName, &item.MetricFieldID,
				&item.MaterializationID, &item.TableSchema, &item.TableName,
				&item.PredicateOperator, &item.WhereCondition,
				&item.CompiledCondition, &item.LLMModel,
				&item.LLMPromptVersion, &item.LLMReason,
				&item.LatestQueryPlanID, &item.DimensionMemberID,
				&item.SourceType, &item.SourceInputHash,
				&item.ObservationCount,
				&item.FirstSeenAt, &item.LastSeenAt, &total,
			); err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, total, err
}

func (s *PostgresStore) ListDimensionWhereDecisionGroups(
	ctx context.Context,
	tenantID string,
) (items []DimensionWhereDecisionGroup, err error) {
	items = []DimensionWhereDecisionGroup{}
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rows, queryErr := tx.Query(ctx, `WITH active_dws AS (
				SELECT dimension.id,dimension.name,dimension.description,
					dimension.member_index_policy,
					dimension.code::text AS field_name
				FROM platform.semantic_dimensions AS dimension
				JOIN platform.dataset_versions AS version
				  ON version.tenant_id=dimension.tenant_id
				 AND version.id=dimension.dataset_version_id
				 AND version.dataset_id=dimension.dataset_id
				 AND version.layer='DWS' AND version.status='PUBLISHED'
				JOIN platform.datasets AS dataset
				  ON dataset.tenant_id=version.tenant_id
				 AND dataset.id=version.dataset_id
				 AND dataset.layer='DWS' AND dataset.status='PUBLISHED'
				 AND dataset.current_published_version_id=version.id
				 AND dataset.deleted_at IS NULL
				JOIN platform.dataset_fields AS field
				  ON field.tenant_id=dimension.tenant_id
				 AND field.dataset_version_id=dimension.dataset_version_id
				 AND field.field_id=dimension.field_id
				WHERE dimension.tenant_id=platform.current_tenant_id()
				  AND dimension.status='PUBLISHED'
			), member_counts AS (
				SELECT member.dimension_id,count(*)::bigint AS member_count
				FROM platform.dimension_members AS member
				WHERE member.tenant_id=platform.current_tenant_id()
				  AND member.status='ACTIVE'
				GROUP BY member.dimension_id
			), pending_vectors AS (
				SELECT document.dimension_id,count(*)::bigint AS pending_count
				FROM platform.dimension_member_semantic_documents AS document
				WHERE document.tenant_id=platform.current_tenant_id()
				  AND document.embedding_status<>'SUCCEEDED'
				GROUP BY document.dimension_id
			), decision_counts AS (
				SELECT decision.dimension_id,
					count(*)::bigint AS decision_count,
					count(DISTINCT decision.metric_version_id)::bigint
					  AS metric_count,
					count(DISTINCT ROW(
					  decision.table_schema,decision.table_name
					))::bigint AS table_count,
					max(decision.last_seen_at) AS last_built_at
				FROM platform.dimension_where_decisions AS decision
				WHERE decision.tenant_id=platform.current_tenant_id()
				GROUP BY decision.dimension_id
			), policy_states AS (
				SELECT policy.dimension_id,
					bool_or(policy.status='RUNNING') AS running,
					bool_or(policy.status='FAILED') AS failed,
					bool_or(policy.status='PENDING') AS pending,
					bool_or(policy.status='SUCCEEDED') AS succeeded
				FROM platform.dimension_where_design_policies AS policy
				WHERE policy.tenant_id=platform.current_tenant_id()
				GROUP BY policy.dimension_id
			)
			SELECT dimension.id::text,dimension.name,dimension.field_name,
				dimension.description,dimension.member_index_policy,
				COALESCE(member.member_count,0),
				COALESCE(decision.decision_count,0),
				COALESCE(vector.pending_count,0),
				COALESCE(decision.metric_count,0),
				COALESCE(decision.table_count,0),
				CASE
				  WHEN policy.failed THEN 'FAILED'
				  WHEN policy.running THEN 'RUNNING'
				  WHEN policy.pending OR COALESCE(vector.pending_count,0)>0
				    THEN 'BUILDING'
				  WHEN policy.succeeded
				       AND COALESCE(decision.decision_count,0)>=
				           COALESCE(member.member_count,0)
				    THEN 'READY'
				  WHEN dimension.member_index_policy<>'FULL'
				    THEN 'EXACT_ONLY'
				  WHEN COALESCE(member.member_count,0)=0 THEN 'EMPTY'
				  ELSE 'PENDING'
				END,
				decision.last_built_at
			FROM active_dws AS dimension
			LEFT JOIN member_counts AS member
			  ON member.dimension_id=dimension.id
			LEFT JOIN pending_vectors AS vector
			  ON vector.dimension_id=dimension.id
			LEFT JOIN decision_counts AS decision
			  ON decision.dimension_id=dimension.id
			LEFT JOIN policy_states AS policy
			  ON policy.dimension_id=dimension.id
			ORDER BY dimension.name,dimension.field_name,dimension.id`)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var item DimensionWhereDecisionGroup
			if scanErr := rows.Scan(
				&item.DimensionID, &item.DimensionName,
				&item.DimensionFieldName, &item.DimensionDescription,
				&item.MemberIndexPolicy, &item.MemberCount,
				&item.DecisionCount, &item.PendingVectorCount,
				&item.MetricCount, &item.TableCount, &item.BuildStatus,
				&item.LastBuiltAt,
			); scanErr != nil {
				return scanErr
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, err
}
