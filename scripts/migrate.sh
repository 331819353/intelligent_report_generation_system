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
docker compose --env-file "$ENV_FILE" exec -T postgres \
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
  applied=$(docker compose --env-file "$ENV_FILE" exec -T postgres \
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
    already_plain=$(docker compose --env-file "$ENV_FILE" exec -T postgres \
      psql -At -U "${POSTGRES_USER:-report_admin}" -d "${POSTGRES_DB:-intelligent_report_control}" \
      -c "SELECT position('''领域:''||domain.name AS domain_key' IN definition)=0 AND position('domain.name AS domain_key' IN definition)>0 FROM (SELECT pg_get_functiondef('platform.trigger_manual_dwd_modeling(uuid)'::regprocedure) AS definition) AS current_definition")
    if [ "$already_plain" = "t" ]; then
      docker compose --env-file "$ENV_FILE" exec -T postgres \
        psql -v ON_ERROR_STOP=1 -U "${POSTGRES_USER:-report_admin}" -d "${POSTGRES_DB:-intelligent_report_control}" \
        -c "INSERT INTO platform_schema_migrations(version) VALUES ('$version')" >/dev/null
      echo "record $version (already satisfied)"
      continue
    fi
  fi

  echo "apply $version"
  {
    echo 'BEGIN;'
    cat "$migration"
    printf "\nINSERT INTO platform_schema_migrations(version) VALUES ('%s');\n" "$version"
    echo 'COMMIT;'
  } | docker compose --env-file "$ENV_FILE" exec -T postgres \
      psql -v ON_ERROR_STOP=1 -U "${POSTGRES_USER:-report_admin}" -d "${POSTGRES_DB:-intelligent_report_control}"
done

# 已有本地数据卷不会重新执行 docker-entrypoint-initdb.d，因此迁移脚本还要
# 幂等补齐后台执行角色。密码仅作为 psql 变量参与 format(%L)，不拼接 SQL。
docker compose --env-file "$ENV_FILE" exec -T postgres \
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
  \quit 1
\endif
COMMIT;
SQL

# 数仓使用独立 PostgreSQL，控制面迁移不会在该实例创建 schema。已有数据卷
# 不会重放 docker-entrypoint-initdb.d，因此这里幂等补齐新分层数据面。
docker compose --env-file "$ENV_FILE" exec -T postgres-warehouse \
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

docker compose --env-file "$ENV_FILE" exec -T postgres \
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
COMMIT;
SQL
