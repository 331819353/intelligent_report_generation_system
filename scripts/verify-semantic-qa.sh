#!/usr/bin/env sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
if [ -z "${ENV_FILE:-}" ]; then
  if [ -f "$ROOT_DIR/.env" ]; then
    ENV_FILE="$ROOT_DIR/.env"
  else
    ENV_FILE="$ROOT_DIR/.env.example"
  fi
fi

if ! command -v docker >/dev/null 2>&1; then
  echo "docker is required for semantic QA verification" >&2
  exit 1
fi

cd "$ROOT_DIR"
set -a
. "$ENV_FILE"
set +a

docker compose --env-file "$ENV_FILE" exec -T postgres \
  psql -v ON_ERROR_STOP=1 \
  -U "${POSTGRES_USER:-report_admin}" \
  -d "${POSTGRES_DB:-intelligent_report_control}" \
  --set=app_user="${POSTGRES_APP_USER:-report_app}" \
  --set=worker_user="${POSTGRES_WORKER_USER:-report_worker}" <<'SQL'
\pset pager off
BEGIN TRANSACTION READ ONLY;

DO $verify$
DECLARE
  invalid_count bigint;
BEGIN
  SELECT count(*) INTO invalid_count
  FROM (VALUES
    ('000084_dim_ads_layers'),
    ('000085_resumable_warehouse_modeling'),
    ('000086_semantic_relationship_contracts'),
    ('000087_semantic_qa_control_plane'),
    ('000088_semantic_query_ai_purpose'),
    ('000089_semantic_qa_candidate_patch'),
    ('000090_semantic_qa_quality_catalog'),
    ('000091_semantic_query_execution_evidence'),
    ('000092_semantic_graph_contract_rebuild'),
    ('000093_dws_analysis_modeling'),
    ('000094_semantic_materialization_graph_event'),
    ('000095_semantic_query_execution_quality')
  ) AS expected(version)
  LEFT JOIN platform_schema_migrations AS applied USING(version)
  WHERE applied.version IS NULL;
  IF invalid_count<>0 THEN
    RAISE EXCEPTION 'semantic QA migrations missing: %',invalid_count;
  END IF;

  SELECT count(*) INTO invalid_count
  FROM pg_class AS relation
  JOIN pg_namespace AS namespace ON namespace.oid=relation.relnamespace
  WHERE namespace.nspname='platform' AND relation.relkind='r'
    AND (
      relation.relname LIKE 'semantic\_%' ESCAPE '\'
      OR relation.relname LIKE 'warehouse\_dag\_%' ESCAPE '\'
      OR relation.relname LIKE 'dws\_modeling\_%' ESCAPE '\'
    )
    AND (NOT relation.relrowsecurity OR NOT relation.relforcerowsecurity);
  IF invalid_count<>0 THEN
    RAISE EXCEPTION 'semantic QA tables missing forced RLS: %',invalid_count;
  END IF;

  SELECT count(*) INTO invalid_count
  FROM platform.semantic_qa_settings AS setting
  LEFT JOIN platform.semantic_graph_projection_state AS state
    ON state.tenant_id=setting.tenant_id
  LEFT JOIN platform.semantic_graph_generations AS generation
    ON generation.tenant_id=state.tenant_id
   AND generation.id=state.current_generation_id
  WHERE setting.enabled AND setting.graph_projection_enabled
    AND (
      state.status<>'READY'
      OR state.applied_event_version<>state.requested_event_version
      OR generation.status<>'READY'
      OR generation.node_count<>(
        SELECT count(*) FROM platform.semantic_graph_nodes AS node
        WHERE node.tenant_id=generation.tenant_id
          AND node.generation_id=generation.id
      )
      OR generation.edge_count<>(
        SELECT count(*) FROM platform.semantic_graph_edges AS edge
        WHERE edge.tenant_id=generation.tenant_id
          AND edge.generation_id=generation.id
      )
    );
  IF invalid_count<>0 THEN
    RAISE EXCEPTION 'enabled semantic graphs are stale or incomplete: %',invalid_count;
  END IF;

  SELECT count(*) INTO invalid_count
  FROM platform.semantic_graph_edges AS edge
  LEFT JOIN platform.semantic_graph_nodes AS source_node
    ON source_node.tenant_id=edge.tenant_id
   AND source_node.generation_id=edge.generation_id
   AND source_node.node_key=edge.from_node_key
  LEFT JOIN platform.semantic_graph_nodes AS target_node
    ON target_node.tenant_id=edge.tenant_id
   AND target_node.generation_id=edge.generation_id
   AND target_node.node_key=edge.to_node_key
  WHERE source_node.node_key IS NULL OR target_node.node_key IS NULL;
  IF invalid_count<>0 THEN
    RAISE EXCEPTION 'semantic graph has dangling edges: %',invalid_count;
  END IF;

  SELECT count(*) INTO invalid_count
  FROM platform.semantic_graph_projection_state AS state
  JOIN platform.semantic_graph_nodes AS node
    ON node.tenant_id=state.tenant_id
   AND node.generation_id=state.current_generation_id
   AND node.node_type='MATERIALIZATION'
  LEFT JOIN platform.dataset_materializations AS materialization
    ON materialization.tenant_id=node.tenant_id
   AND materialization.id::text=node.subject_ref
  WHERE state.status='READY'
    AND (
      materialization.id IS NULL
      OR materialization.status<>'ACTIVE'
      OR materialization.schema_hash<>(
        SELECT version.schema_hash
        FROM platform.dataset_versions AS version
        WHERE version.tenant_id=materialization.tenant_id
          AND version.id=materialization.dataset_version_id
      )
    );
  IF invalid_count<>0 THEN
    RAISE EXCEPTION 'current graph contains stale materialization evidence: %',invalid_count;
  END IF;

  SELECT count(*) INTO invalid_count
  FROM platform.semantic_query_plans AS plan
  WHERE plan.status IN ('READY','EXECUTED')
    AND (
      plan.selected_metric_id IS NULL
      OR plan.selected_metric_version_id IS NULL
      OR plan.selected_dataset_version_id IS NULL
      OR plan.selected_materialization_id IS NULL
      OR plan.path_hash=''
      OR NOT EXISTS(
        SELECT 1 FROM platform.semantic_query_plan_evidence AS evidence
        WHERE evidence.tenant_id=plan.tenant_id
          AND evidence.query_plan_id=plan.id
          AND evidence.subject_type='SOURCE'
      )
      OR NOT EXISTS(
        SELECT 1 FROM platform.semantic_query_plan_evidence AS evidence
        WHERE evidence.tenant_id=plan.tenant_id
          AND evidence.query_plan_id=plan.id
          AND evidence.subject_type='MATERIALIZATION'
          AND evidence.subject_ref=plan.selected_materialization_id::text
      )
      OR (
        COALESCE(
          (plan.normalized_request_json->>'hasMemberValue')::boolean,false
        )
        AND NOT EXISTS(
          SELECT 1 FROM platform.semantic_query_plan_evidence AS evidence
          WHERE evidence.tenant_id=plan.tenant_id
            AND evidence.query_plan_id=plan.id
            AND evidence.subject_type='MEMBER'
        )
      )
    );
  IF invalid_count<>0 THEN
    RAISE EXCEPTION 'ready query plans have incomplete authority evidence: %',invalid_count;
  END IF;

  SELECT count(*) INTO invalid_count
  FROM platform.warehouse_dag_change_sets AS change_set
  WHERE change_set.expected_operation_count<>(
    SELECT count(*)
    FROM platform.warehouse_dag_change_operations AS operation
    WHERE operation.tenant_id=change_set.tenant_id
      AND operation.change_set_id=change_set.id
  );
  IF invalid_count<>0 THEN
    RAISE EXCEPTION 'ChangeSet operation counts are inconsistent: %',invalid_count;
  END IF;

  SELECT count(*) INTO invalid_count
  FROM platform.dataset_versions AS version
  WHERE version.layer='ADS' AND version.status='PUBLISHED'
    AND (
      COALESCE(version.dsl_json #>> '{dataset,consumerContractId}','')=''
      OR NOT EXISTS(
        SELECT 1
        FROM platform.semantic_consumer_contracts AS contract
        WHERE contract.tenant_id=version.tenant_id
          AND contract.id=(version.dsl_json #>> '{dataset,consumerContractId}')::uuid
          AND contract.status='PUBLISHED'
      )
    );
  IF invalid_count<>0 THEN
    RAISE EXCEPTION 'published ADS versions lack a published consumer contract: %',invalid_count;
  END IF;
END
$verify$;

SELECT (
  NOT has_table_privilege(
    :'app_user','platform.semantic_graph_generations','INSERT,UPDATE,DELETE'
  )
  AND NOT has_table_privilege(
    :'app_user','platform.semantic_graph_nodes','INSERT,UPDATE,DELETE'
  )
  AND NOT has_table_privilege(
    :'app_user','platform.semantic_graph_edges','INSERT,UPDATE,DELETE'
  )
  AND NOT has_table_privilege(
    :'app_user','platform.dws_modeling_jobs','INSERT,UPDATE,DELETE'
  )
  AND NOT has_table_privilege(
    :'app_user','platform.dws_modeling_outputs','INSERT,UPDATE,DELETE'
  )
  AND NOT has_table_privilege(
    :'worker_user','platform.warehouse_dag_change_sets','INSERT,UPDATE,DELETE'
  )
  AND NOT has_table_privilege(
    :'worker_user','platform.semantic_query_plans','INSERT,UPDATE,DELETE'
  )
) AS semantic_role_boundary_valid
\gset
\if :semantic_role_boundary_valid
\else
  \echo 'semantic QA application/worker role boundary is invalid'
  \quit 1
\endif

SELECT 'semantic QA verification passed' AS result;
COMMIT;
SQL
