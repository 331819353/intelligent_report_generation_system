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
  IF to_regnamespace('askdata') IS NULL THEN
    RAISE EXCEPTION 'missing askdata control-plane schema';
  END IF;
  FOREACH relation_name IN ARRAY ARRAY[
    'audit_events','domains','entities','semantic_models','measures','metrics',
    'metric_versions','metric_version_measures','dimensions','hierarchies',
    'hierarchy_levels','relationships','quality_rules','business_terms',
    'dimension_members','dimension_member_aliases','semantic_aliases',
    'search_documents','embedding_outbox','releases','release_objects',
    'release_projections','release_projection_artifacts','release_state',
    'release_events','graph_plan_cache'
  ] LOOP
    IF to_regclass('askdata.'||relation_name) IS NULL THEN
      RAISE EXCEPTION 'missing askdata relation: askdata.%', relation_name;
    END IF;
    IF NOT EXISTS(
      SELECT 1
      FROM pg_class AS relation
      JOIN pg_namespace AS namespace ON namespace.oid=relation.relnamespace
      WHERE namespace.nspname='askdata' AND relation.relname=relation_name
        AND relation.relrowsecurity AND relation.relforcerowsecurity
    ) THEN
      RAISE EXCEPTION 'askdata.% must enable and force RLS', relation_name;
    END IF;
  END LOOP;

  IF EXISTS(
    SELECT 1
    FROM pg_constraint AS constraint_record
    JOIN pg_class AS relation ON relation.oid=constraint_record.conrelid
    JOIN pg_namespace AS namespace ON namespace.oid=relation.relnamespace
    WHERE namespace.nspname='askdata' AND constraint_record.contype='f'
      AND NOT EXISTS(
        SELECT 1
        FROM unnest(constraint_record.conkey) AS key(attnum)
        JOIN pg_attribute AS attribute
          ON attribute.attrelid=relation.oid AND attribute.attnum=key.attnum
        WHERE attribute.attname='tenant_id'
      )
  ) THEN
    RAISE EXCEPTION 'every askdata foreign key must include tenant_id';
  END IF;

  IF NOT EXISTS(
    SELECT 1
    FROM pg_attribute AS attribute
    WHERE attribute.attrelid='askdata.search_documents'::regclass
      AND attribute.attname='embedding'
      AND format_type(attribute.atttypid,attribute.atttypmod)='halfvec(2560)'
  ) OR NOT EXISTS(
    SELECT 1
    FROM pg_index AS index_record
    JOIN pg_class AS index_relation ON index_relation.oid=index_record.indexrelid
    JOIN pg_am AS access_method ON access_method.oid=index_relation.relam
    WHERE index_record.indrelid='askdata.search_documents'::regclass
      AND access_method.amname='hnsw'
  ) THEN
    RAISE EXCEPTION 'askdata halfvec(2560) HNSW index is missing';
  END IF;

  IF to_regprocedure('askdata.start_release_projection(uuid,uuid,jsonb)') IS NULL
    OR to_regprocedure('askdata.claim_release_projection(uuid,text,integer)') IS NULL
    OR to_regprocedure('askdata.complete_release_projection(uuid,uuid,text,uuid,text,text,integer,jsonb)') IS NULL
    OR to_regprocedure('askdata.fail_release_projection(uuid,uuid,text,uuid,text,boolean)') IS NULL
    OR to_regprocedure('askdata.activate_release(uuid,uuid)') IS NOT NULL THEN
    RAISE EXCEPTION 'askdata projection boundary is incomplete or unsafe activation exists before evaluation gates';
  END IF;

  IF position(
       'count(*)=4' IN
       pg_get_functiondef(
         'askdata.complete_release_projection(uuid,uuid,text,uuid,text,text,integer,jsonb)'::regprocedure
       )
     )=0 OR NOT EXISTS(
       SELECT 1 FROM pg_indexes
       WHERE schemaname='askdata' AND indexname='askdata_releases_one_active_idx'
         AND indexdef LIKE '%WHERE (status = ''ACTIVE''%'
  ) THEN
    RAISE EXCEPTION 'release four-projection or single-active invariant is missing';
  END IF;

  IF NOT EXISTS(
    SELECT 1
    FROM pg_constraint
    WHERE conrelid='platform.ai_tenant_policies'::regclass
      AND conname='ai_tenant_policies_purposes_check'
      AND position('SEMANTIC_QUESTION' IN pg_get_constraintdef(oid))>0
  ) OR NOT EXISTS(
    SELECT 1
    FROM pg_constraint
    WHERE conrelid='platform.ai_requests'::regclass
      AND conname='ai_requests_purpose_check'
      AND position('SEMANTIC_QUESTION' IN pg_get_constraintdef(oid))>0
  ) OR EXISTS(
    SELECT 1
    FROM information_schema.columns
    WHERE table_schema='platform' AND table_name='ai_tenant_policies'
      AND column_name='allowed_purposes'
      AND position('SEMANTIC_QUESTION' IN COALESCE(column_default,''))>0
  ) THEN
    RAISE EXCEPTION 'SEMANTIC_QUESTION must be policy-supported, audited and default-denied';
  END IF;
END
$$;

SELECT (
  NOT has_schema_privilege('public','askdata','USAGE')
  AND has_schema_privilege(:'app_user','askdata','USAGE')
  AND has_schema_privilege(:'worker_user','askdata','USAGE')
  AND NOT has_schema_privilege(:'connection_test_user','askdata','USAGE')
  AND has_table_privilege(:'app_user','askdata.metric_versions','INSERT')
  AND has_table_privilege(:'app_user','askdata.metric_versions','UPDATE')
  AND has_table_privilege(:'app_user','askdata.releases','INSERT')
  AND NOT has_table_privilege(:'app_user','askdata.releases','UPDATE')
  AND NOT has_table_privilege(:'app_user','askdata.embedding_outbox','INSERT')
  AND NOT has_table_privilege(:'app_user','askdata.release_projections','UPDATE')
  AND NOT has_table_privilege(:'app_user','askdata.release_events','INSERT')
  AND has_table_privilege(:'worker_user','askdata.embedding_outbox','UPDATE')
  AND has_table_privilege(:'worker_user','askdata.graph_plan_cache','INSERT')
  AND NOT has_table_privilege(:'worker_user','askdata.entities','INSERT')
  AND NOT has_table_privilege(:'worker_user','askdata.release_objects','INSERT')
  AND NOT has_table_privilege(:'worker_user','askdata.release_projections','UPDATE')
  AND NOT has_table_privilege(:'connection_test_user','askdata.domains','SELECT')
  AND has_function_privilege(
    :'app_user','askdata.start_release_projection(uuid,uuid,jsonb)','EXECUTE'
  )
  AND NOT has_function_privilege(
    :'worker_user','askdata.start_release_projection(uuid,uuid,jsonb)','EXECUTE'
  )
  AND has_function_privilege(
    :'worker_user','askdata.claim_release_projection(uuid,text,integer)','EXECUTE'
  )
  AND NOT has_function_privilege(
    :'app_user','askdata.claim_release_projection(uuid,text,integer)','EXECUTE'
  )
  AND NOT has_function_privilege(
    :'connection_test_user','askdata.release_manifest_hash(uuid)','EXECUTE'
  )
) AS askdata_runtime_privileges_secure
\gset
\if :askdata_runtime_privileges_secure
\else
  \echo 'askdata runtime privileges are unsafe'
  \quit 1
\endif

BEGIN;
INSERT INTO platform.tenants(code,name)
VALUES('verify_askdata_rls','askdata verification tenant')
RETURNING id AS tenant_id
\gset askdata_ctx_
INSERT INTO platform.users(
  tenant_id,employee_no,email,display_name,password_hash,status
) VALUES(
  :'askdata_ctx_tenant_id','ASKVERIFY001','askdata.verify@example.invalid',
  'askdata verification user','verification-only-not-a-login-secret','ACTIVE'
)
RETURNING id AS user_id
\gset askdata_ctx_
INSERT INTO platform.business_domains(
  tenant_id,code,name,is_default,created_by
) VALUES(
  :'askdata_ctx_tenant_id','verify_current','verify current domain',true,
  :'askdata_ctx_user_id'
)
RETURNING id AS domain_id
\gset askdata_ctx_
INSERT INTO platform.business_domains(
  tenant_id,code,name,is_default,created_by
) VALUES(
  :'askdata_ctx_tenant_id','verify_other','verify other domain',false,
  :'askdata_ctx_user_id'
)
RETURNING id
\gset askdata_other_
INSERT INTO platform.domain_memberships(
  tenant_id,domain_id,user_id,status,member_role,assigned_by
) VALUES(
  :'askdata_ctx_tenant_id',:'askdata_ctx_domain_id',:'askdata_ctx_user_id',
  'ACTIVE','MEMBER',:'askdata_ctx_user_id'
);
INSERT INTO askdata.domains(id,tenant_id,code,name,owner_id)
VALUES(
  :'askdata_ctx_domain_id',:'askdata_ctx_tenant_id',
  'verify_current_domain','verify current domain',:'askdata_ctx_user_id'
)
ON CONFLICT(id) DO NOTHING;
INSERT INTO askdata.domains(id,tenant_id,code,name,owner_id)
VALUES(
  :'askdata_other_id',:'askdata_ctx_tenant_id',
  'verify_other_domain','verify other domain',:'askdata_ctx_user_id'
)
ON CONFLICT(id) DO NOTHING;
SET LOCAL ROLE :"app_user";
SELECT set_config('app.tenant_id',:'askdata_ctx_tenant_id',true);
SELECT set_config('app.user_id',:'askdata_ctx_user_id',true);
SELECT set_config('app.domain_id',:'askdata_ctx_domain_id',true);
SELECT set_config('app.access_mode','USER',true);
SELECT count(*)=1 AS askdata_domain_rls_isolated
FROM askdata.domains
WHERE id IN (:'askdata_ctx_domain_id',:'askdata_other_id')
\gset
\if :askdata_domain_rls_isolated
\else
  \echo 'askdata domain RLS isolation failed'
  \quit 1
\endif
ROLLBACK;

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
  IF to_regprocedure(
    'platform.apply_dimension_profile_resource_limits(uuid,integer,text,uuid)'
  ) IS NOT NULL THEN
    RAISE EXCEPTION 'decommissioned dimension profile resource helper still exists';
  END IF;
  IF to_regprocedure('platform.enqueue_data_source_connection_test(uuid,uuid,text)') IS NULL THEN
    RAISE EXCEPTION 'data-source connection-test boundary is missing';
  END IF;
  IF EXISTS(
    SELECT 1 FROM pg_trigger
    WHERE tgrelid='platform.tenants'::regclass
      AND tgname='tenants_initialize_semantic_release_state'
      AND NOT tgisinternal
  ) OR to_regprocedure(
    'platform.initialize_semantic_release_state()'
  ) IS NOT NULL THEN
    RAISE EXCEPTION 'retired semantic tenant trigger or function still exists';
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

SELECT 'retained platform and askdata permission/schema checks passed' AS result;
SQL
