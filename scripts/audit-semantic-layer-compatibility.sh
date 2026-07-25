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
  echo "docker is required for the semantic compatibility audit" >&2
  exit 1
fi

cd "$ROOT_DIR"
set -a
. "$ENV_FILE"
set +a

docker compose --env-file "$ENV_FILE" exec -T postgres \
  psql -v ON_ERROR_STOP=1 \
  -U "${POSTGRES_USER:-report_admin}" \
  -d "${POSTGRES_DB:-intelligent_report_control}" <<'SQL'
\pset pager off
\pset null '(null)'
BEGIN TRANSACTION READ ONLY;

\echo '== semantic QA migration ledger 84-95 =='
WITH expected(version) AS (
  VALUES
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
)
SELECT expected.version,
       CASE WHEN applied.version IS NULL THEN 'PENDING' ELSE 'APPLIED' END AS status,
       applied.applied_at
FROM expected
LEFT JOIN platform_schema_migrations AS applied USING(version)
ORDER BY expected.version;

\echo '== current published datasets by layer =='
SELECT version.layer,count(*) AS dataset_count
FROM platform.dataset_versions AS version
JOIN platform.datasets AS owner
  ON owner.id=version.dataset_id
 AND owner.tenant_id=version.tenant_id
 AND owner.current_published_version_id=version.id
WHERE version.status='PUBLISHED'
  AND owner.status='PUBLISHED'
  AND owner.deleted_at IS NULL
GROUP BY version.layer
ORDER BY version.layer;

\echo '== current published dependency matrix =='
WITH current_versions AS (
  SELECT version.id,version.layer,version.dsl_json
  FROM platform.dataset_versions AS version
  JOIN platform.datasets AS owner
    ON owner.id=version.dataset_id
   AND owner.tenant_id=version.tenant_id
   AND owner.current_published_version_id=version.id
  WHERE version.status='PUBLISHED'
    AND owner.status='PUBLISHED'
    AND owner.deleted_at IS NULL
),
edges AS (
  SELECT target.id AS target_version_id,
         target.layer AS target_layer,
         upstream.layer AS input_layer
  FROM current_versions AS target
  CROSS JOIN LATERAL jsonb_array_elements(
    COALESCE(target.dsl_json->'nodes','[]'::jsonb)
  ) AS node
  LEFT JOIN platform.dataset_versions AS upstream
    ON upstream.id::text=node->>'datasetVersionId'
  WHERE node->>'type'='DATASET'
)
SELECT target_layer,COALESCE(input_layer,'UNRESOLVED') AS input_layer,
       count(*) AS edge_count
FROM edges
GROUP BY target_layer,COALESCE(input_layer,'UNRESOLVED')
ORDER BY target_layer,input_layer;

\echo '== target-contract compatibility findings =='
WITH current_versions AS (
  SELECT version.id,version.dataset_id,version.layer,version.dsl_json
  FROM platform.dataset_versions AS version
  JOIN platform.datasets AS owner
    ON owner.id=version.dataset_id
   AND owner.tenant_id=version.tenant_id
   AND owner.current_published_version_id=version.id
  WHERE version.status='PUBLISHED'
    AND owner.status='PUBLISHED'
    AND owner.deleted_at IS NULL
),
edges AS (
  SELECT target.id AS target_version_id,
         target.dataset_id,
         target.layer AS target_layer,
         upstream.layer AS input_layer
  FROM current_versions AS target
  CROSS JOIN LATERAL jsonb_array_elements(
    COALESCE(target.dsl_json->'nodes','[]'::jsonb)
  ) AS node
  LEFT JOIN platform.dataset_versions AS upstream
    ON upstream.id::text=node->>'datasetVersionId'
  WHERE node->>'type'='DATASET'
),
findings AS (
  SELECT target_version_id,dataset_id,target_layer,
         'UNRESOLVED_DATASET_INPUT'::text AS finding
  FROM edges
  WHERE input_layer IS NULL
  UNION ALL
  SELECT target_version_id,dataset_id,target_layer,'DIM_NON_ODS_INPUT'
  FROM edges WHERE target_layer='DIM' AND input_layer<>'ODS'
  UNION ALL
  SELECT target_version_id,dataset_id,target_layer,'DWD_INVALID_INPUT_LAYER'
  FROM edges WHERE target_layer='DWD' AND input_layer NOT IN ('ODS','DIM')
  UNION ALL
  SELECT version.id,version.dataset_id,version.layer,'DWD_MISSING_ODS_FACT'
  FROM current_versions AS version
  WHERE version.layer='DWD' AND NOT EXISTS(
    SELECT 1 FROM edges
    WHERE edges.target_version_id=version.id AND edges.input_layer='ODS'
  )
  UNION ALL
  SELECT target_version_id,dataset_id,target_layer,'DWS_NON_DWD_INPUT'
  FROM edges WHERE target_layer='DWS' AND input_layer<>'DWD'
  UNION ALL
  SELECT version.id,version.dataset_id,version.layer,'DWS_MISSING_DWD_INPUT'
  FROM current_versions AS version
  WHERE version.layer='DWS' AND NOT EXISTS(
    SELECT 1 FROM edges
    WHERE edges.target_version_id=version.id AND edges.input_layer='DWD'
  )
  UNION ALL
  SELECT target_version_id,dataset_id,target_layer,'ADS_NON_DWS_INPUT'
  FROM edges WHERE target_layer='ADS' AND input_layer<>'DWS'
)
SELECT target_layer,finding,count(*) AS affected_versions
FROM findings
GROUP BY target_layer,finding
ORDER BY target_layer,finding;

\echo '== registered build input matrix =='
SELECT run.layer AS target_layer,input.input_layer,count(*) AS input_count
FROM platform.build_run_inputs AS input
JOIN platform.dataset_build_runs AS run
  ON run.id=input.build_run_id AND run.tenant_id=input.tenant_id
GROUP BY run.layer,input.input_layer
ORDER BY run.layer,input.input_layer;

\echo '== ADS consumer contract status =='
SELECT count(*) FILTER (WHERE status='PUBLISHED') AS published_contracts,
       count(*) FILTER (WHERE status='DRAFT') AS draft_contracts,
       'automatic ADS generation disabled' AS generation_policy
FROM platform.semantic_consumer_contracts;
COMMIT;
SQL
