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

cd "$ROOT_DIR"
set -a
. "$ENV_FILE"
set +a

APP_ROLE=${POSTGRES_APP_USER:-report_app}
WORKER_ROLE=${POSTGRES_WORKER_USER:-report_worker}
CONNECTION_TEST_ROLE=${POSTGRES_CONNECTION_TEST_USER:-report_connection_tester}
ADMIN_ROLE=${POSTGRES_USER:-report_admin}
if [ "$APP_ROLE" = "$WORKER_ROLE" ] ||
  [ "$APP_ROLE" = "$CONNECTION_TEST_ROLE" ] ||
  [ "$WORKER_ROLE" = "$CONNECTION_TEST_ROLE" ] ||
  [ "$APP_ROLE" = "$ADMIN_ROLE" ] ||
  [ "$WORKER_ROLE" = "$ADMIN_ROLE" ] ||
  [ "$CONNECTION_TEST_ROLE" = "$ADMIN_ROLE" ]; then
  echo "admin and all runtime database roles must be distinct" >&2
  exit 1
fi

docker compose --env-file "$ENV_FILE" exec -T postgres \
  psql -v ON_ERROR_STOP=1 \
  -U "${POSTGRES_USER:-report_admin}" \
  -d "${POSTGRES_DB:-intelligent_report_control}" \
  --set=app_user="$APP_ROLE" \
  --set=worker_user="$WORKER_ROLE" \
  --set=connection_test_user="$CONNECTION_TEST_ROLE" <<'SQL'
SELECT (
  count(*)=3
  AND bool_and(
    rolcanlogin AND NOT rolsuper AND NOT rolcreatedb AND NOT rolcreaterole
    AND NOT rolreplication AND NOT rolbypassrls AND NOT rolinherit
  )
) AS dedicated_roles_secure
FROM pg_roles
WHERE rolname IN (:'app_user',:'worker_user',:'connection_test_user')
\gset
\if :dedicated_roles_secure
\else
  \echo 'dedicated database role attributes are unsafe'
  \quit 1
\endif

DO $$
DECLARE
  relation_name text;
BEGIN
  FOREACH relation_name IN ARRAY ARRAY[
    'tenants','users','roles','permissions','role_permissions','user_roles',
    'business_domains','domain_memberships','domain_access_applications',
    'data_sources','data_source_versions','data_source_publication_requests',
    'data_source_connection_test_jobs','metadata_tables','metadata_columns',
    'datasets','dataset_versions','dataset_draft_revisions',
    'dataset_publication_requests','dataset_dependencies',
    'dataset_materializations','dataset_build_runs',
    'dws_modeling_jobs','dws_modeling_outputs'
  ] LOOP
    IF to_regclass('platform.'||relation_name) IS NULL THEN
      RAISE EXCEPTION 'missing retained relation platform.%', relation_name;
    END IF;
  END LOOP;
END
$$;

DO $$
DECLARE
  relation_name text;
BEGIN
  FOREACH relation_name IN ARRAY ARRAY[
    'metrics','metric_versions','metric_candidates',
    'reports','report_drafts','report_versions',
    'semantic_question_runs','semantic_releases','semantic_graph_nodes',
    'ads_modeling_jobs'
  ] LOOP
    IF to_regclass('platform.'||relation_name) IS NOT NULL THEN
      RAISE EXCEPTION 'decommissioned relation still exists: platform.%', relation_name;
    END IF;
  END LOOP;
END
$$;

DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM platform.permissions
    WHERE resource_type IN ('METRIC','REPORT','AI')
  ) THEN
    RAISE EXCEPTION 'decommissioned permission resources still exist';
  END IF;
  IF EXISTS (
    SELECT 1 FROM platform.roles
    WHERE is_system AND code IN ('analyst','report_designer','viewer')
  ) THEN
    RAISE EXCEPTION 'decommissioned system roles still exist';
  END IF;
  IF to_regprocedure('platform.enqueue_data_source_connection_test(uuid,uuid,text)') IS NULL THEN
    RAISE EXCEPTION 'data-source connection-test boundary is missing';
  END IF;
  IF to_regprocedure('platform.trigger_manual_dim_modeling(uuid)') IS NULL OR
     to_regprocedure('platform.trigger_manual_dwd_modeling(uuid)') IS NULL THEN
    RAISE EXCEPTION 'retained dataset modeling boundaries are missing';
  END IF;
  IF to_regprocedure(
       'platform.modeling_actor_can_access_current_domain(uuid)'
     ) IS NULL OR
     position(
       'modeling_actor_can_access_current_domain(actor_id)' IN
       pg_get_functiondef(
         'platform.trigger_manual_dim_modeling(uuid,uuid[])'::regprocedure
       )
     )=0 OR
     position(
       'modeling_actor_can_access_current_domain(actor_id)' IN
       pg_get_functiondef(
         'platform.trigger_manual_dwd_modeling(uuid)'::regprocedure
       )
     )=0 THEN
    RAISE EXCEPTION 'dataset modeling domain authorization boundary is missing';
  END IF;
  IF position(
       'dataset-tag-suggestion-v7' IN
       pg_get_functiondef(
         'platform.enqueue_dataset_tag_suggestion()'::regprocedure
       )
     )=0 THEN
    RAISE EXCEPTION 'dataset tag suggestion enqueue prompt is not v7';
  END IF;
END
$$;

SELECT (
  has_function_privilege(
    :'app_user','platform.semantic_tag_can_read(uuid)','EXECUTE'
  )
  AND has_function_privilege(
    :'app_user','platform.semantic_tag_can_write(uuid)','EXECUTE'
  )
  AND has_function_privilege(
    :'worker_user','platform.semantic_tag_can_read(uuid)','EXECUTE'
  )
  AND has_function_privilege(
    :'worker_user','platform.semantic_tag_can_write(uuid)','EXECUTE'
  )
) AS dataset_tag_policy_helpers_executable
\gset
\if :dataset_tag_policy_helpers_executable
\else
  \echo 'dataset tag policy helper runtime privileges are missing'
  \quit 1
\endif

SELECT 'retained permission, data-source and dataset schema passed' AS result;
SQL
