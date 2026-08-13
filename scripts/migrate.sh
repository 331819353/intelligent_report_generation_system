#!/usr/bin/env sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
# 优先使用显式环境文件，其次使用本地 .env，最后回退到示例配置。
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

compose() {
  docker compose --env-file "$ROOT_DIR/.env.example" --env-file "$ENV_FILE" "$@"
}

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

if ! command -v docker >/dev/null 2>&1; then
  echo "docker is required to run database migrations" >&2
  exit 1
fi

# 迁移登记表位于 platform 模式之外，确保首次初始化前也可创建。
compose exec -T postgres \
  psql -v ON_ERROR_STOP=1 -U "${POSTGRES_USER:-report_admin}" -d "${POSTGRES_DB:-intelligent_report_control}" <<'SQL'
CREATE TABLE IF NOT EXISTS platform_schema_migrations (
  version text PRIMARY KEY,
  applied_at timestamptz NOT NULL DEFAULT now()
);
SQL

# 按文件名顺序执行尚未登记的迁移，并在成功后写入版本。
for migration in "$ROOT_DIR"/migrations/*.up.sql; do
  [ -f "$migration" ] || continue
  version=$(basename "$migration" .up.sql)
  applied=$(compose exec -T postgres \
    psql -At -U "${POSTGRES_USER:-report_admin}" -d "${POSTGRES_DB:-intelligent_report_control}" \
    -c "SELECT 1 FROM platform_schema_migrations WHERE version = '$version'" || true)
  if [ "$applied" = "1" ]; then
    echo "skip $version"
    continue
  fi

  # 000150 already wrote the plain-domain DWD trigger in fresh databases.
  # The later historical repair intentionally fails when there is nothing to
  # replace, so record it as satisfied when its target definition is present.
  if [ "$version" = "000161_plain_domain_dwd_trigger" ]; then
    already_plain=$(compose exec -T postgres \
      psql -At -U "${POSTGRES_USER:-report_admin}" -d "${POSTGRES_DB:-intelligent_report_control}" \
      -c "SELECT position('''领域:''||domain.name AS domain_key' IN definition)=0 AND position('domain.name AS domain_key' IN definition)>0 FROM (SELECT pg_get_functiondef('platform.trigger_manual_dwd_modeling(uuid)'::regprocedure) AS definition) AS current_definition")
    if [ "$already_plain" = "t" ]; then
      compose exec -T postgres \
        psql -v ON_ERROR_STOP=1 -U "${POSTGRES_USER:-report_admin}" -d "${POSTGRES_DB:-intelligent_report_control}" \
        -c "INSERT INTO platform_schema_migrations(version) VALUES ('$version')" >/dev/null
      echo "record $version (already satisfied)"
      continue
    fi
  fi

  # 000300 was first shipped while the runner wrapped files that already owned
  # BEGIN/COMMIT. PostgreSQL therefore committed the migration before the
  # registry insert, and an interrupted restart could leave a fully installed
  # lease contract without its version row. Recognize only the complete shape;
  # a genuinely partial contract must still fail instead of being blessed.
  if [ "$version" = "000300_askdata_question_run_lease" ]; then
    already_installed=$(compose exec -T postgres \
      psql -At -U "${POSTGRES_USER:-report_admin}" -d "${POSTGRES_DB:-intelligent_report_control}" \
      -c "SELECT
        to_regclass('askdata.question_run_leases') IS NOT NULL
        AND to_regprocedure('askdata.claim_question_run(uuid,text,integer)') IS NOT NULL
        AND to_regprocedure('askdata.heartbeat_question_run(uuid,uuid,integer)') IS NOT NULL
        AND to_regprocedure('askdata.release_question_run(uuid,uuid)') IS NOT NULL
        AND EXISTS(
          SELECT 1 FROM pg_indexes
          WHERE schemaname='askdata'
            AND tablename='question_run_leases'
            AND indexname='askdata_question_run_leases_claimable_idx'
        )
        AND EXISTS(
          SELECT 1 FROM pg_policies
          WHERE schemaname='askdata'
            AND tablename='question_run_leases'
            AND policyname='askdata_question_run_leases_domain_isolation'
        )
        AND EXISTS(
          SELECT 1 FROM pg_trigger
          WHERE tgrelid='askdata.question_run_leases'::regclass
            AND tgname='askdata_question_run_leases_set_updated_at'
            AND NOT tgisinternal
        )")
    if [ "$already_installed" = "t" ]; then
      compose exec -T postgres \
        psql -v ON_ERROR_STOP=1 -U "${POSTGRES_USER:-report_admin}" -d "${POSTGRES_DB:-intelligent_report_control}" \
        -c "INSERT INTO platform_schema_migrations(version) VALUES ('$version') ON CONFLICT DO NOTHING" >/dev/null
      echo "record $version (complete contract already installed)"
      continue
    fi
  fi

  echo "apply $version"
  begin_count=$(grep -Ec '^[[:space:]]*BEGIN;[[:space:]]*$' "$migration" || true)
  commit_count=$(grep -Ec '^[[:space:]]*COMMIT;[[:space:]]*$' "$migration" || true)
  if [ "$begin_count" != "1" ] || [ "$commit_count" != "1" ]; then
    echo "$version must contain exactly one top-level BEGIN and COMMIT" >&2
    exit 1
  fi
  {
    echo 'BEGIN;'
    # Migration files own a top-level transaction for direct execution. Strip
    # only that pair here so the schema change and version registry row commit
    # atomically under the runner's transaction.
    sed \
      -e '/^[[:space:]]*BEGIN;[[:space:]]*$/d' \
      -e '/^[[:space:]]*COMMIT;[[:space:]]*$/d' \
      "$migration"
    printf "\nINSERT INTO platform_schema_migrations(version) VALUES ('%s');\n" "$version"
    echo 'COMMIT;'
  } | compose exec -T postgres \
      psql -v ON_ERROR_STOP=1 -U "${POSTGRES_USER:-report_admin}" -d "${POSTGRES_DB:-intelligent_report_control}"
done

# 已有本地数据卷不会重新执行 docker-entrypoint-initdb.d，因此迁移脚本还要
# 幂等补齐后台执行角色。密码仅作为 psql 变量参与 format(%L)，不拼接 SQL。
compose exec -T postgres \
  psql -v ON_ERROR_STOP=1 -U "${POSTGRES_USER:-report_admin}" -d "${POSTGRES_DB:-intelligent_report_control}" \
  --set=app_user="$APP_ROLE" \
  --set=worker_user="${POSTGRES_WORKER_USER:-report_worker}" \
  --set=worker_password="${POSTGRES_WORKER_PASSWORD:-local_worker_password}" \
  --set=connection_test_user="${POSTGRES_CONNECTION_TEST_USER:-report_connection_tester}" \
  --set=connection_test_password="${POSTGRES_CONNECTION_TEST_PASSWORD:-local_connection_test_password}" <<'SQL'
BEGIN;
SELECT format(
  'CREATE ROLE %I LOGIN PASSWORD %L NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS',
  :'worker_user',
  :'worker_password'
) WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname=:'worker_user')
\gexec
SELECT format('GRANT CONNECT ON DATABASE %I TO %I',current_database(),:'worker_user')
\gexec
SELECT format(
  'CREATE ROLE %I LOGIN PASSWORD %L NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS',
  :'connection_test_user',
  :'connection_test_password'
) WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname=:'connection_test_user')
\gexec
SELECT format('GRANT CONNECT ON DATABASE %I TO %I',current_database(),:'connection_test_user')
\gexec
SELECT (
  count(*)=3
  AND bool_and(
    rolcanlogin AND NOT rolsuper AND NOT rolcreatedb AND NOT rolcreaterole
    AND NOT rolreplication AND NOT rolbypassrls AND NOT rolinherit
  )
  AND NOT EXISTS(
    SELECT 1
    FROM pg_auth_members AS membership
    JOIN pg_roles AS member_role ON member_role.oid=membership.member
    WHERE member_role.rolname IN (
      :'app_user',:'worker_user',:'connection_test_user'
    )
  )
) AS dedicated_roles_secure
FROM pg_roles
WHERE rolname IN (:'app_user',:'worker_user',:'connection_test_user')
\gset
\if :dedicated_roles_secure
\else
  \echo 'dedicated database role attributes or memberships are unsafe'
  SELECT 1/0;
\endif
COMMIT;
SQL

# 数仓使用独立 PostgreSQL，控制面迁移不会在该实例创建 schema。已有数据卷
# 不会重放 docker-entrypoint-initdb.d，因此这里幂等补齐新分层数据面。
compose exec -T postgres-warehouse \
  psql -v ON_ERROR_STOP=1 \
  -U "${WAREHOUSE_POSTGRES_USER:-warehouse_admin}" \
  -d "${WAREHOUSE_POSTGRES_DB:-intelligent_report_warehouse}" \
  --set=worker_user="${WAREHOUSE_WORKER_USER:-report_warehouse_worker}" <<'SQL'
BEGIN;
CREATE SCHEMA IF NOT EXISTS warehouse_dim;
CREATE SCHEMA IF NOT EXISTS warehouse_ads;
REVOKE ALL ON SCHEMA warehouse_dim,warehouse_ads FROM PUBLIC;
GRANT USAGE,CREATE ON SCHEMA warehouse_dim,warehouse_ads TO :"worker_user";
COMMENT ON SCHEMA warehouse_dim IS
  '从 ODS 抽离并治理的人物、商品等实体说明信息';
COMMENT ON SCHEMA warehouse_ads IS
  '由 DWS 组合形成的应用和交付场景数据';

COMMIT;
SQL

compose exec -T postgres \
  psql -v ON_ERROR_STOP=1 -U "${POSTGRES_USER:-report_admin}" -d "${POSTGRES_DB:-intelligent_report_control}" \
  --set=app_user="${POSTGRES_APP_USER:-report_app}" \
  --set=worker_user="${POSTGRES_WORKER_USER:-report_worker}" \
  --set=connection_test_user="${POSTGRES_CONNECTION_TEST_USER:-report_connection_tester}" <<'SQL'
BEGIN;
GRANT USAGE ON SCHEMA platform TO :"app_user";
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA platform TO :"app_user";
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA platform TO :"app_user";
ALTER DEFAULT PRIVILEGES IN SCHEMA platform
  REVOKE INSERT, UPDATE, DELETE ON TABLES FROM :"app_user";
ALTER DEFAULT PRIVILEGES IN SCHEMA platform GRANT SELECT ON TABLES TO :"app_user";
ALTER DEFAULT PRIVILEGES IN SCHEMA platform GRANT USAGE, SELECT ON SEQUENCES TO :"app_user";

GRANT USAGE ON SCHEMA platform TO :"worker_user";
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA platform TO :"worker_user";
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA platform TO :"worker_user";
ALTER DEFAULT PRIVILEGES IN SCHEMA platform
  REVOKE INSERT, UPDATE, DELETE ON TABLES FROM :"worker_user";
ALTER DEFAULT PRIVILEGES IN SCHEMA platform GRANT SELECT ON TABLES TO :"worker_user";
ALTER DEFAULT PRIVILEGES IN SCHEMA platform GRANT USAGE, SELECT ON SEQUENCES TO :"worker_user";

SELECT format(
  'GRANT EXECUTE ON FUNCTION platform.report_v2_can_access(uuid,text[]), platform.report_v2_row_can_access(uuid,uuid,uuid,text[]) TO %I, %I',
  :'app_user',:'worker_user'
)
WHERE to_regprocedure('platform.report_v2_can_access(uuid,text[])') IS NOT NULL
\gexec

-- 报告语义运行时只能经精确版本/来源运行/计划哈希校验入口读取不可执行
-- Query Artifact；API 与导出 worker 均按被认证的当前查看者上下文调用，连接
-- 测试角色不能调用或读写用户执行审计。
SELECT format(
  'REVOKE ALL ON FUNCTION platform.load_report_runtime_query_artifact(uuid,uuid,text) FROM PUBLIC, %I',
  :'connection_test_user'
)
WHERE to_regprocedure(
  'platform.load_report_runtime_query_artifact(uuid,uuid,text)'
) IS NOT NULL
\gexec
SELECT format(
  'GRANT EXECUTE ON FUNCTION platform.load_report_runtime_query_artifact(uuid,uuid,text) TO %I, %I',
  :'app_user',:'worker_user'
)
WHERE to_regprocedure(
  'platform.load_report_runtime_query_artifact(uuid,uuid,text)'
) IS NOT NULL
\gexec
SELECT format(
  'REVOKE ALL ON FUNCTION platform.load_report_runtime_compilation_artifact(uuid,uuid,text) FROM PUBLIC, %I',
  :'connection_test_user'
)
WHERE to_regprocedure(
  'platform.load_report_runtime_compilation_artifact(uuid,uuid,text)'
) IS NOT NULL
\gexec
SELECT format(
  'GRANT EXECUTE ON FUNCTION platform.load_report_runtime_compilation_artifact(uuid,uuid,text) TO %I, %I',
  :'app_user',:'worker_user'
)
WHERE to_regprocedure(
  'platform.load_report_runtime_compilation_artifact(uuid,uuid,text)'
) IS NOT NULL
\gexec
SELECT format(
  'REVOKE ALL ON TABLE platform.report_semantic_compilations FROM %I, %I, %I',
  :'app_user',:'worker_user',:'connection_test_user'
)
WHERE to_regclass('platform.report_semantic_compilations') IS NOT NULL
\gexec
SELECT format(
  'GRANT SELECT,INSERT ON TABLE platform.report_semantic_compilations TO %I',
  :'app_user'
)
WHERE to_regclass('platform.report_semantic_compilations') IS NOT NULL
\gexec
SELECT format(
  'REVOKE ALL ON TABLE platform.semantic_query_execution_runs FROM %I',
  :'connection_test_user'
)
WHERE to_regclass('platform.semantic_query_execution_runs') IS NOT NULL
\gexec
SELECT format(
  'REVOKE DELETE ON TABLE platform.semantic_query_execution_runs FROM %I, %I',
  :'app_user',:'worker_user'
)
WHERE to_regclass('platform.semantic_query_execution_runs') IS NOT NULL
\gexec
SELECT format(
  'GRANT SELECT,INSERT,UPDATE ON TABLE platform.semantic_query_execution_runs TO %I, %I',
  :'app_user',:'worker_user'
)
WHERE to_regclass('platform.semantic_query_execution_runs') IS NOT NULL
\gexec

SELECT format(
  'GRANT EXECUTE ON FUNCTION platform.list_report_export_tenants() TO %I',
  :'worker_user'
)
WHERE to_regprocedure('platform.list_report_export_tenants()') IS NOT NULL
\gexec

-- 明细取数工单是用户/审批人控制面：API 仅可读、创建和按状态机更新。
-- 通用 worker 只能读取/完成受控导出队列，不能访问申请正文或审计事件；
-- 连接测试角色没有任何取数申请权限。
SELECT format(
  'REVOKE DELETE ON TABLE platform.data_requests FROM %I',
  :'app_user'
)
WHERE to_regclass('platform.data_requests') IS NOT NULL
\gexec
SELECT format(
  'REVOKE UPDATE,DELETE ON TABLE platform.data_request_events FROM %I',
  :'app_user'
)
WHERE to_regclass('platform.data_request_events') IS NOT NULL
\gexec
SELECT format(
  'REVOKE ALL ON TABLE platform.data_requests, platform.data_request_events FROM %I, %I',
  :'worker_user',:'connection_test_user'
)
WHERE to_regclass('platform.data_requests') IS NOT NULL
\gexec
SELECT format(
  'GRANT SELECT,INSERT,UPDATE ON TABLE platform.data_requests TO %I',
  :'app_user'
)
WHERE to_regclass('platform.data_requests') IS NOT NULL
\gexec
SELECT format(
  'GRANT SELECT,INSERT ON TABLE platform.data_request_events TO %I',
  :'app_user'
)
WHERE to_regclass('platform.data_request_events') IS NOT NULL
\gexec
SELECT format(
  'REVOKE DELETE ON TABLE platform.data_request_export_jobs FROM %I',
  :'app_user'
)
WHERE to_regclass('platform.data_request_export_jobs') IS NOT NULL
\gexec
SELECT format(
  'GRANT SELECT,INSERT,UPDATE ON TABLE platform.data_request_export_jobs TO %I',
  :'app_user'
)
WHERE to_regclass('platform.data_request_export_jobs') IS NOT NULL
\gexec
SELECT format(
  'REVOKE INSERT,DELETE ON TABLE platform.data_request_export_jobs FROM %I',
  :'worker_user'
)
WHERE to_regclass('platform.data_request_export_jobs') IS NOT NULL
\gexec
SELECT format(
  'GRANT SELECT,UPDATE ON TABLE platform.data_request_export_jobs TO %I',
  :'worker_user'
)
WHERE to_regclass('platform.data_request_export_jobs') IS NOT NULL
\gexec
SELECT format(
  'REVOKE ALL ON TABLE platform.data_request_export_jobs FROM %I',
  :'connection_test_user'
)
WHERE to_regclass('platform.data_request_export_jobs') IS NOT NULL
\gexec
SELECT format(
  'GRANT EXECUTE ON FUNCTION platform.data_request_context_valid(jsonb), platform.data_request_fields_valid(jsonb), platform.data_request_actor_is_domain_admin(uuid,uuid,uuid), platform.data_request_can_access(uuid,uuid,uuid,uuid[],uuid,uuid), platform.data_request_event_can_access(uuid,uuid,uuid) TO %I',
  :'app_user'
)
WHERE to_regprocedure(
  'platform.data_request_can_access(uuid,uuid,uuid,uuid[],uuid,uuid)'
) IS NOT NULL
\gexec
SELECT format(
  'GRANT EXECUTE ON FUNCTION platform.data_request_event_can_access(uuid,uuid,uuid) TO %I',
  :'worker_user'
)
WHERE to_regprocedure('platform.data_request_event_can_access(uuid,uuid,uuid)') IS NOT NULL
\gexec

SELECT format(
  'REVOKE INSERT, UPDATE, DELETE ON TABLE platform.dwd_modeling_stage_jobs FROM %I',
  :'app_user'
)
WHERE to_regclass('platform.dwd_modeling_stage_jobs') IS NOT NULL
\gexec

-- 人工智能建模由租户隔离的 SECURITY DEFINER 入口登记 durable outbox。
-- API 仍不能直接写 worker 表，只能执行三个有界函数。
SELECT format(
  'REVOKE ALL ON FUNCTION platform.trigger_manual_dim_modeling(uuid) FROM PUBLIC, %I, %I',
  :'worker_user',
  :'connection_test_user'
)
WHERE to_regprocedure('platform.trigger_manual_dim_modeling(uuid)') IS NOT NULL
\gexec
SELECT format(
  'GRANT EXECUTE ON FUNCTION platform.trigger_manual_dim_modeling(uuid) TO %I',
  :'app_user'
)
WHERE to_regprocedure('platform.trigger_manual_dim_modeling(uuid)') IS NOT NULL
\gexec
SELECT format(
  'REVOKE ALL ON FUNCTION platform.trigger_manual_dwd_modeling(uuid) FROM PUBLIC, %I, %I',
  :'worker_user',
  :'connection_test_user'
)
WHERE to_regprocedure('platform.trigger_manual_dwd_modeling(uuid)') IS NOT NULL
\gexec
SELECT format(
  'GRANT EXECUTE ON FUNCTION platform.trigger_manual_dwd_modeling(uuid) TO %I',
  :'app_user'
)
WHERE to_regprocedure('platform.trigger_manual_dwd_modeling(uuid)') IS NOT NULL
\gexec
SELECT format(
  'REVOKE ALL ON FUNCTION platform.cancel_dwd_modeling_stage_task(uuid,uuid), platform.retry_dwd_modeling_stage_task(uuid,uuid) FROM PUBLIC, %I, %I',
  :'worker_user',
  :'connection_test_user'
)
WHERE to_regprocedure(
  'platform.cancel_dwd_modeling_stage_task(uuid,uuid)'
) IS NOT NULL
\gexec
SELECT format(
  'GRANT EXECUTE ON FUNCTION platform.cancel_dwd_modeling_stage_task(uuid,uuid), platform.retry_dwd_modeling_stage_task(uuid,uuid) TO %I',
  :'app_user'
)
WHERE to_regprocedure(
  'platform.cancel_dwd_modeling_stage_task(uuid,uuid)'
) IS NOT NULL
\gexec

-- 连接测试任务和证明是受保护控制事实。宽泛的平台 DML 授权必须在这里
-- 显式收回；API 只可入队，通用 worker 没有写入或执行权限，专用 worker
-- 也只能通过租约函数改变状态。
REVOKE INSERT, UPDATE, DELETE ON TABLE
  platform.data_source_test_runs,
  platform.data_source_connection_test_jobs,
  platform.data_source_connection_test_attestations
FROM :"app_user", :"worker_user";

GRANT USAGE ON SCHEMA platform TO :"connection_test_user";
GRANT SELECT ON TABLE
  platform.file_assets,
  platform.file_asset_versions,
  platform.file_asset_inspections
TO :"connection_test_user";
REVOKE INSERT, UPDATE, DELETE ON TABLE
  platform.data_source_test_runs,
  platform.data_source_connection_test_jobs,
  platform.data_source_connection_test_attestations
FROM :"connection_test_user";

REVOKE ALL ON FUNCTION
  platform.enqueue_data_source_connection_test(uuid,uuid,text),
  platform.list_connection_test_job_tenant_ids(),
  platform.claim_data_source_connection_test(text,integer),
  platform.heartbeat_data_source_connection_test(uuid,uuid,integer),
  platform.update_data_source_connection_test_stage(uuid,uuid,text),
  platform.complete_data_source_connection_test(uuid,uuid,text,bigint),
  platform.fail_data_source_connection_test(uuid,uuid,text,boolean)
FROM :"app_user", :"worker_user", :"connection_test_user";

GRANT EXECUTE ON FUNCTION
  platform.enqueue_data_source_connection_test(uuid,uuid,text)
TO :"app_user";

GRANT EXECUTE ON FUNCTION
  platform.list_connection_test_job_tenant_ids(),
  platform.claim_data_source_connection_test(text,integer),
  platform.heartbeat_data_source_connection_test(uuid,uuid,integer),
  platform.update_data_source_connection_test_stage(uuid,uuid,text),
  platform.complete_data_source_connection_test(uuid,uuid,text,bigint),
  platform.fail_data_source_connection_test(uuid,uuid,text,boolean)
TO :"connection_test_user";

-- 发布来源事实只能由 dataset_versions 触发器调用；运行角色不能直接把任意
-- 复合行交给 SECURITY DEFINER helper 试探或绕开服务端发布路径。
SELECT format(
  'REVOKE ALL ON FUNCTION platform.dataset_publication_origin_facts_match(platform.dataset_versions) FROM PUBLIC, %I, %I, %I',
  :'app_user',
  :'worker_user',
  :'connection_test_user'
)
WHERE to_regprocedure(
  'platform.dataset_publication_origin_facts_match(platform.dataset_versions)'
) IS NOT NULL
\gexec

SELECT format(
  'GRANT EXECUTE ON FUNCTION platform.trigger_manual_dws_dimension_identification(uuid) TO %I',
  :'app_user'
)
WHERE to_regprocedure(
  'platform.trigger_manual_dws_dimension_identification(uuid)'
) IS NOT NULL
\gexec

-- 有效领域解析只返回一个规范化领域名，用于 API 清单和 worker 的领域隔离。
-- 保持 PUBLIC/连接测试角色不可执行，仅开放给业务 API 与后台 worker。
SELECT format(
  'REVOKE ALL ON FUNCTION platform.dataset_version_effective_domain(uuid) FROM PUBLIC, %I',
  :'connection_test_user'
)
WHERE to_regprocedure(
  'platform.dataset_version_effective_domain(uuid)'
) IS NOT NULL
\gexec

SELECT format(
  'GRANT EXECUTE ON FUNCTION platform.dataset_version_effective_domain(uuid) TO %I, %I',
  :'app_user',
  :'worker_user'
)
WHERE to_regprocedure(
  'platform.dataset_version_effective_domain(uuid)'
) IS NOT NULL
\gexec

-- Cross-context inbox, report scheduling, lifecycle and runtime configuration
-- are control-plane tables. Reset the earlier broad platform grants and keep
-- mutation rights aligned with their API/worker responsibilities.
REVOKE ALL ON TABLE
  platform.work_item_receipts,
  platform.report_schedules,
  platform.report_subscriptions,
  platform.report_deliveries,
  platform.report_delivery_events,
  platform.user_lifecycle_batches,
  platform.user_lifecycle_batch_items,
  platform.user_lifecycle_events,
  platform.runtime_config_versions,
  platform.runtime_config_effective,
  platform.runtime_config_rollout_nodes,
  platform.runtime_config_events,
  platform.report_follows
FROM :"app_user", :"worker_user", :"connection_test_user";

GRANT SELECT,INSERT,UPDATE ON TABLE platform.work_item_receipts TO :"app_user";
GRANT SELECT,INSERT,DELETE ON TABLE platform.report_follows TO :"app_user";
GRANT SELECT,INSERT,UPDATE ON TABLE
  platform.report_schedules,platform.report_subscriptions
TO :"app_user";
-- Manual backfill is authorized by the API and then executes in SYSTEM mode;
-- ordinary user-mode delivery inserts remain rejected by RLS.
GRANT SELECT,INSERT,UPDATE ON TABLE platform.report_deliveries TO :"app_user";
GRANT SELECT,INSERT ON TABLE platform.report_delivery_events TO :"app_user";
GRANT SELECT,INSERT,UPDATE ON TABLE
  platform.user_lifecycle_batches,platform.user_lifecycle_batch_items
TO :"app_user";
GRANT SELECT,INSERT ON TABLE platform.user_lifecycle_events TO :"app_user";
GRANT SELECT,INSERT,UPDATE ON TABLE
  platform.runtime_config_versions,platform.runtime_config_effective,
  platform.runtime_config_rollout_nodes
TO :"app_user";
GRANT SELECT,INSERT ON TABLE platform.runtime_config_events TO :"app_user";

GRANT SELECT,INSERT,UPDATE ON TABLE
  platform.report_schedules,platform.report_subscriptions,platform.report_deliveries
TO :"worker_user";
GRANT SELECT,INSERT ON TABLE platform.report_delivery_events TO :"worker_user";
GRANT SELECT,UPDATE ON TABLE
  platform.runtime_config_versions,platform.runtime_config_effective,
  platform.runtime_config_rollout_nodes
TO :"worker_user";
GRANT SELECT,INSERT ON TABLE platform.runtime_config_events TO :"worker_user";

GRANT EXECUTE ON FUNCTION
  platform.report_schedule_work_tenants(),platform.runtime_config_rollout_tenants()
TO :"worker_user";

-- askdata is a fail-closed control-plane schema. Reset all runtime grants on
-- every deployment, then grant only the API authoring surface and the worker
-- projection/outbox surface. The connection-test role has no askdata access.
REVOKE ALL ON SCHEMA askdata FROM PUBLIC, :"connection_test_user";
GRANT USAGE ON SCHEMA askdata TO :"app_user", :"worker_user";
REVOKE ALL ON ALL TABLES IN SCHEMA askdata
  FROM :"app_user", :"worker_user", :"connection_test_user";
REVOKE ALL ON ALL SEQUENCES IN SCHEMA askdata
  FROM :"app_user", :"worker_user", :"connection_test_user";
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA askdata
  FROM PUBLIC, :"app_user", :"worker_user", :"connection_test_user";

GRANT SELECT ON ALL TABLES IN SCHEMA askdata TO :"app_user", :"worker_user";
REVOKE SELECT ON TABLE askdata.semantic_export_jobs FROM :"worker_user";
REVOKE SELECT, INSERT, UPDATE, DELETE ON TABLE askdata.search_query_samples FROM :"app_user";
SELECT format(
  'GRANT EXECUTE ON FUNCTION askdata.active_learning_member_signals(uuid), askdata.active_learning_data_request_signals(uuid) TO %I',
  :'worker_user'
)
WHERE to_regprocedure('askdata.active_learning_member_signals(uuid)') IS NOT NULL
  AND to_regprocedure('askdata.active_learning_data_request_signals(uuid)') IS NOT NULL
\gexec
-- Raw dimension-member material is never a general registry read surface.
-- The API may perform governed authoring DML, while runtime lookup goes only
-- through the hash-only SECURITY DEFINER function below. The profile worker
-- receives just the two hash-history columns needed for generation diffs.
REVOKE SELECT ON TABLE
  askdata.dimension_members,
  askdata.dimension_member_aliases,
  askdata.dimension_profile_members
FROM PUBLIC, :"app_user", :"worker_user", :"connection_test_user";
-- Table-level REVOKE does not clear historical column ACLs. Reset every
-- column ACL on all three sensitive tables before granting the safe views.
REVOKE SELECT(
  id,tenant_id,domain_id,member_id,version_no,dimension_version_id,
  member_key,member_key_hash,canonical_label,parent_member_version_id,
  sensitivity,valid_from,valid_to,status,content_hash,created_by,created_at,
  updated_at
)
  ON TABLE askdata.dimension_members
  FROM PUBLIC, :"app_user", :"worker_user", :"connection_test_user";
REVOKE SELECT(
  id,tenant_id,domain_id,dimension_version_id,member_version_id,alias,
  normalized_alias,source,priority,status,content_hash,created_by,created_at,
  updated_at,alias_key_hash
)
  ON TABLE askdata.dimension_member_aliases
  FROM PUBLIC, :"app_user", :"worker_user", :"connection_test_user";
REVOKE SELECT(
  id,tenant_id,domain_id,dimension_version_id,profile_id,generation,
  member_key_hash,canonical_label,normalized_value,observed_aliases,
  observed_count,sensitivity,eligible_for_llm,content_hash,created_at
)
  ON TABLE askdata.dimension_profile_members
  FROM PUBLIC, :"app_user", :"worker_user", :"connection_test_user";
GRANT SELECT(
  id,tenant_id,domain_id,member_id,version_no,dimension_version_id,
  parent_member_version_id,sensitivity,valid_from,valid_to,status,content_hash,
  created_by,created_at,updated_at
) ON TABLE askdata.dimension_members TO :"app_user";
GRANT SELECT(
  id,tenant_id,domain_id,dimension_version_id,member_version_id,source,
  priority,status,content_hash,created_by,created_at,updated_at
) ON TABLE askdata.dimension_member_aliases TO :"app_user";
GRANT SELECT(profile_id,member_key_hash)
  ON TABLE askdata.dimension_profile_members TO :"worker_user";
GRANT INSERT, UPDATE, DELETE ON TABLE
  askdata.domains,
  askdata.entities,
  askdata.semantic_models,
  askdata.measures,
  askdata.metrics,
  askdata.metric_versions,
  askdata.metric_version_measures,
  askdata.dimensions,
  askdata.hierarchies,
  askdata.hierarchy_levels,
  askdata.relationships,
  askdata.quality_rules,
  askdata.business_terms,
  askdata.business_term_versions,
  askdata.metric_dimensions,
  askdata.metric_dimension_versions,
  askdata.certified_examples,
  askdata.certified_example_versions,
  askdata.kpi_bundles,
  askdata.kpi_bundle_versions,
  askdata.evaluation_case_assets,
  askdata.evaluation_case_versions,
  askdata.time_contracts,
  askdata.time_contract_versions,
  askdata.semantic_imports,
  askdata.semantic_import_rows,
  askdata.dimension_members,
  askdata.dimension_member_aliases,
  askdata.semantic_aliases
TO :"app_user";
GRANT INSERT(
  tenant_id,domain_id,object_type,object_version_id,view_type,sensitivity,
  index_policy,document,metadata,input_hash
) ON TABLE askdata.search_documents TO :"app_user";
GRANT UPDATE(
  sensitivity,index_policy,document,metadata,input_hash
) ON TABLE askdata.search_documents TO :"app_user";
GRANT INSERT ON TABLE askdata.semantic_export_jobs TO :"app_user";
GRANT INSERT ON TABLE
  askdata.audit_events,
  askdata.releases,
  askdata.release_objects,
  askdata.release_rollouts,
  askdata.release_rollout_events
TO :"app_user";
GRANT UPDATE(
  stage,state,canary_percent,reason_hash,updated_by,version,stage_started_at,
  paused_at,stopped_at,accepted_at,completed_at,rolled_back_at,updated_at
) ON askdata.release_rollouts TO :"app_user";
GRANT INSERT, UPDATE ON TABLE
  askdata.release_references
TO :"app_user";
GRANT INSERT, UPDATE ON TABLE
  askdata.conversations,
  askdata.question_runs,
  askdata.idempotency_records,
  askdata.saved_questions,
  askdata.feedback_tickets,
  askdata.add_to_report_intents
TO :"app_user";

-- Shadow dispatch is worker-only. The browser can read aggregate rollout
-- evidence through release_rollout_observability but cannot mutate queue or
-- paired observations.
GRANT INSERT ON TABLE askdata.question_runs TO :"worker_user";
GRANT INSERT ON TABLE askdata.question_envelopes TO :"worker_user";
GRANT INSERT ON TABLE askdata.question_envelopes TO :"app_user";
GRANT UPDATE ON TABLE
  askdata.active_learning_candidates,
  askdata.report_semantic_assets
TO :"app_user";
GRANT INSERT ON TABLE
  askdata.saved_question_dependencies,
  askdata.saved_question_shares,
  askdata.feedback_ticket_events,
  askdata.report_asset_certifications,
  askdata.narrative_verification_failures
TO :"app_user";
GRANT DELETE ON TABLE askdata.idempotency_records TO :"app_user", :"worker_user";
GRANT INSERT, UPDATE, DELETE ON TABLE
  askdata.evaluation_sets,
  askdata.evaluation_cases,
  askdata.evaluation_case_reviews
TO :"app_user";
GRANT INSERT, UPDATE ON TABLE
  askdata.query_feedback
TO :"app_user";
GRANT INSERT ON TABLE
  askdata.question_run_events,
  askdata.question_artifacts,
  askdata.tool_calls,
  askdata.question_seed_contexts,
  askdata.question_saved_seed_contexts
TO :"app_user";

-- 澄清超时 worker 只能复用与 API 相同的受触发器保护列；不给表级
-- UPDATE/INSERT，避免后台角色绕过 Question runtime 的追加式边界。
GRANT UPDATE(
  current_state,disposition,completion_code,completion_artifact_hash,
  understanding_hash,binding_bundle_hash,graph_plan_hash,semantic_ir_hash,
  query_plan_hash,result_hash,step_count,llm_calls_used,tool_calls_used,
  formal_queries_used,validation_queries_used,elapsed_ms,budget_exhausted,
  clarification_deadline,budget_frozen_at,budget_consumed_json,record_version
) ON TABLE askdata.question_runs TO :"worker_user";
GRANT UPDATE(
  pinned_release_id,pinned_at,pin_drift_acknowledged,updated_at
) ON TABLE askdata.conversations TO :"worker_user";
GRANT INSERT(
  id,tenant_id,domain_id,actor_id,question_run_id,release_id,
  release_content_hash,policy_scope_hash,event_index,run_version,state,
  event_type,stage,status,code,tool_call_id,ai_request_id,action_hash,
  artifact_hash,evidence_ids,summary_json,event_hash,duration_ms
) ON TABLE askdata.question_run_events TO :"worker_user";
GRANT INSERT ON TABLE
  askdata.question_artifacts,
  askdata.tool_calls
TO :"worker_user";
GRANT DELETE ON TABLE askdata.question_envelopes TO :"worker_user";

GRANT INSERT ON TABLE askdata.audit_events TO :"worker_user";
GRANT INSERT, UPDATE, DELETE ON TABLE
  askdata.embedding_outbox,
  askdata.search_documents,
  askdata.release_projection_artifacts,
  askdata.graph_plan_cache,
  askdata.dimension_profile_jobs
TO :"worker_user";
GRANT INSERT, UPDATE ON TABLE
  askdata.semantic_imports,
  askdata.semantic_import_rows
TO :"worker_user";
GRANT INSERT ON TABLE
  askdata.dimension_profiles,
  askdata.dimension_profile_members,
  askdata.evaluation_runs,
  askdata.evaluation_narrative_results,
  askdata.search_recall_audits,
  askdata.active_learning_candidates,
  askdata.report_semantic_assets,
  askdata.narrative_verification_failures
TO :"worker_user";
GRANT UPDATE ON TABLE
  askdata.saved_questions,
  askdata.active_learning_candidates,
  askdata.report_semantic_assets,
  askdata.report_asset_extraction_outbox,
  askdata.report_asset_projection_outbox,
  askdata.add_to_report_intents,
  askdata.add_to_report_outbox
TO :"worker_user";
GRANT DELETE ON TABLE askdata.search_query_samples TO :"worker_user";

GRANT EXECUTE ON FUNCTION
  askdata.current_tenant_id(),
  askdata.current_actor_id(),
  askdata.current_domain_id(),
  askdata.system_access(),
  askdata.tenant_matches(uuid),
  askdata.domain_can_access(uuid),
  askdata.json_is_safe(jsonb),
  askdata.report_operation_json_is_safe(jsonb),
  askdata.question_audit_json_is_safe(jsonb),
  askdata.question_runtime_can_access(uuid,uuid,uuid),
  askdata.evaluation_control_can_access(uuid,uuid),
  askdata.evaluation_case_can_access(uuid,uuid,uuid),
  askdata.feedback_ticket_can_access(uuid,uuid,uuid,uuid),
  askdata.saved_question_can_read(uuid,uuid,uuid,uuid,text),
  askdata.release_manifest_hash(uuid),
  askdata.release_registry_facts_complete(uuid),
  askdata.resolve_subject_attributes(uuid,uuid)
TO :"app_user", :"worker_user";

GRANT EXECUTE ON FUNCTION
  askdata.resolve_question_release(uuid,uuid,uuid),
  askdata.release_rollout_observability(uuid),
  askdata.release_rollout_bucket(text,uuid),
  askdata.row_access_policy_coverage(uuid,uuid)
TO :"app_user";
SELECT format(
  'GRANT EXECUTE ON FUNCTION askdata.list_add_to_report_tenants() TO %I',
  :'worker_user'
)
WHERE to_regprocedure('askdata.list_add_to_report_tenants()') IS NOT NULL
\gexec
SELECT format(
  'GRANT EXECUTE ON FUNCTION askdata.enqueue_add_to_report_intent(uuid) TO %I',
  :'app_user'
)
WHERE to_regprocedure('askdata.enqueue_add_to_report_intent(uuid)') IS NOT NULL
\gexec
SELECT format(
  'GRANT EXECUTE ON FUNCTION askdata.list_report_asset_projection_tenants() TO %I',
  :'worker_user'
)
WHERE to_regprocedure('askdata.list_report_asset_projection_tenants()') IS NOT NULL
\gexec
SELECT format(
  'GRANT EXECUTE ON FUNCTION askdata.list_question_run_tenants(), askdata.claim_question_run(uuid,text,integer), askdata.heartbeat_question_run(uuid,uuid,integer), askdata.release_question_run(uuid,uuid) TO %I',
  :'worker_user'
)
WHERE to_regprocedure('askdata.list_question_run_tenants()') IS NOT NULL
  AND to_regprocedure('askdata.claim_question_run(uuid,text,integer)') IS NOT NULL
  AND to_regprocedure('askdata.heartbeat_question_run(uuid,uuid,integer)') IS NOT NULL
  AND to_regprocedure('askdata.release_question_run(uuid,uuid)') IS NOT NULL
\gexec
SELECT format(
  'GRANT EXECUTE ON FUNCTION askdata.list_question_run_actor_roles(uuid,uuid,uuid) TO %I',
  :'worker_user'
)
WHERE to_regprocedure(
  'askdata.list_question_run_actor_roles(uuid,uuid,uuid)'
) IS NOT NULL
\gexec
SELECT format(
  'GRANT EXECUTE ON FUNCTION askdata.list_release_shadow_job_tenants(), askdata.claim_release_shadow_job(uuid,text,integer), askdata.complete_release_shadow_job(uuid,uuid,uuid,text) TO %I',
  :'worker_user'
)
WHERE to_regprocedure('askdata.list_release_shadow_job_tenants()') IS NOT NULL
  AND to_regprocedure('askdata.claim_release_shadow_job(uuid,text,integer)') IS NOT NULL
  AND to_regprocedure('askdata.complete_release_shadow_job(uuid,uuid,uuid,text)') IS NOT NULL
\gexec
GRANT EXECUTE ON FUNCTION
  askdata.start_release_projection(uuid,uuid,jsonb),
  askdata.retry_failed_release_projections(uuid,uuid),
  askdata.seal_evaluation_set(uuid,uuid),
  askdata.record_release_error_budget(uuid,uuid,uuid,jsonb,uuid),
  askdata.plan_evaluation_batch(uuid,uuid,text,uuid),
  askdata.expose_evaluation_shard(uuid,smallint,uuid),
  askdata.recompute_release_evaluation_gate(uuid,uuid,uuid,uuid),
  askdata.record_release_review_report(uuid,uuid,uuid,text,text,jsonb,uuid),
  askdata.submit_release_approval_v2(uuid,uuid,uuid,text,text,text,text,uuid,uuid),
  askdata.withdraw_release_approval(uuid,text,text,text,uuid),
  askdata.reset_rejected_release_approvals(uuid,text,text,uuid),
  askdata.escalate_release_approval(uuid,text,text,uuid),
  askdata.active_release_approval_count(uuid,text),
  askdata.activate_release(uuid,uuid,uuid,uuid,bigint),
  askdata.load_quota_usage_snapshots(uuid,uuid,uuid,timestamptz),
  askdata.record_cost_usage(uuid,uuid,uuid,uuid,text,text,text,bigint,bigint,bigint,bigint),
  askdata.lookup_exact_dimension_member(uuid,text,uuid,text),
  askdata.retire_release(uuid)
TO :"app_user";
GRANT EXECUTE ON FUNCTION
  askdata.record_cost_usage(uuid,uuid,uuid,uuid,text,text,text,bigint,bigint,bigint,bigint),
  askdata.load_quota_usage_snapshots(uuid,uuid,uuid,timestamptz)
TO :"worker_user";
GRANT EXECUTE ON FUNCTION
  askdata.record_search_query_sample(uuid,uuid,text,text,text,text,integer,text)
TO :"app_user", :"worker_user";
GRANT EXECUTE ON FUNCTION
  askdata.list_release_projection_tenants(),
  askdata.list_release_projection_tenants(text),
  askdata.claim_release_projection(uuid,text,integer),
  askdata.claim_release_projection(uuid,text,text,integer),
  askdata.heartbeat_release_projection(uuid,uuid,text,uuid,integer),
  askdata.load_release_graph_projection(uuid,uuid,text,uuid),
  askdata.complete_release_projection(uuid,uuid,text,uuid,text,text,integer,jsonb),
  askdata.fail_release_projection(uuid,uuid,text,uuid,text,boolean),
  askdata.cleanup_retained_release_projections(uuid)
TO :"worker_user";
GRANT EXECUTE ON FUNCTION
  askdata.semantic_import_errors_valid(jsonb)
TO :"app_user", :"worker_user";
GRANT EXECUTE ON FUNCTION
  askdata.resolve_governed_import_member(uuid,uuid,text)
TO :"app_user", :"worker_user";
GRANT EXECUTE ON FUNCTION
  askdata.list_semantic_import_tenants(),
  askdata.claim_semantic_import(uuid,text,integer),
  askdata.heartbeat_semantic_import(uuid,uuid,text,uuid,integer)
TO :"worker_user";
GRANT EXECUTE ON FUNCTION
  askdata.semantic_export_asset_types_valid(text[])
TO :"app_user";
GRANT EXECUTE ON FUNCTION
  askdata.list_semantic_export_tenants(),
  askdata.claim_semantic_export(uuid,text,integer),
  askdata.complete_semantic_export(uuid,uuid,text,uuid,text,text,integer,integer),
  askdata.fail_semantic_export(uuid,uuid,text,uuid,text,boolean)
TO :"worker_user";

-- Decision is an independent fail-closed schema. Evidence and event tables
-- are append-only at the database layer; no runtime role receives DELETE.
REVOKE ALL ON SCHEMA decision FROM PUBLIC, :"connection_test_user";
GRANT USAGE ON SCHEMA decision TO :"app_user", :"worker_user";
REVOKE ALL ON ALL TABLES IN SCHEMA decision
  FROM :"app_user", :"worker_user", :"connection_test_user";
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA decision
  FROM PUBLIC, :"app_user", :"worker_user", :"connection_test_user";
GRANT SELECT ON ALL TABLES IN SCHEMA decision TO :"app_user";
GRANT INSERT ON TABLE
  decision.decisions,decision.decision_options,decision.decision_evidence,
  decision.decision_approvals,decision.decision_approval_events,
  decision.action_items,decision.action_events,
  decision.outcome_metrics,decision.outcome_reviews,decision.decision_events,
  decision.decision_notifications
TO :"app_user";
GRANT UPDATE ON TABLE
  decision.decisions,decision.action_items,
  decision.outcome_metrics,decision.outcome_reviews,decision.decision_notifications
TO :"app_user";
GRANT SELECT,UPDATE ON TABLE
  decision.decisions,decision.action_items,decision.decision_notifications
TO :"worker_user";
GRANT SELECT ON TABLE decision.decision_events,decision.action_events TO :"worker_user";
GRANT INSERT ON TABLE decision.decision_events,decision.decision_notifications TO :"worker_user";
GRANT EXECUTE ON FUNCTION
  decision.current_tenant_id(),decision.current_actor_id(),
  decision.current_domain_id(),decision.system_access(),decision.can_access(uuid)
TO :"app_user", :"worker_user";
GRANT EXECUTE ON FUNCTION decision.list_work_tenants() TO :"worker_user", :"app_user";

ALTER DEFAULT PRIVILEGES IN SCHEMA decision REVOKE ALL ON TABLES
  FROM PUBLIC, :"app_user", :"worker_user", :"connection_test_user";
ALTER DEFAULT PRIVILEGES IN SCHEMA decision REVOKE ALL ON FUNCTIONS
  FROM PUBLIC, :"app_user", :"worker_user", :"connection_test_user";

ALTER DEFAULT PRIVILEGES IN SCHEMA askdata REVOKE ALL ON TABLES
  FROM PUBLIC, :"app_user", :"worker_user", :"connection_test_user";
ALTER DEFAULT PRIVILEGES IN SCHEMA askdata REVOKE ALL ON SEQUENCES
  FROM PUBLIC, :"app_user", :"worker_user", :"connection_test_user";
ALTER DEFAULT PRIVILEGES IN SCHEMA askdata REVOKE ALL ON FUNCTIONS
  FROM PUBLIC, :"app_user", :"worker_user", :"connection_test_user";
COMMIT;
SQL
