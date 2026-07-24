#!/usr/bin/env sh
set -eu

: "${POSTGRES_USER:?POSTGRES_USER is required}"
: "${WAREHOUSE_READER_USER:?WAREHOUSE_READER_USER is required}"
: "${WAREHOUSE_READER_PASSWORD:?WAREHOUSE_READER_PASSWORD is required}"
: "${WAREHOUSE_WORKER_USER:?WAREHOUSE_WORKER_USER is required}"
: "${WAREHOUSE_WORKER_PASSWORD:?WAREHOUSE_WORKER_PASSWORD is required}"

if [ "$WAREHOUSE_READER_USER" = "$WAREHOUSE_WORKER_USER" ] ||
  [ "$WAREHOUSE_READER_USER" = "$POSTGRES_USER" ] ||
  [ "$WAREHOUSE_WORKER_USER" = "$POSTGRES_USER" ]; then
  echo "warehouse admin, reader and worker roles must be distinct" >&2
  exit 1
fi

psql -v ON_ERROR_STOP=1 \
  --username "$POSTGRES_USER" \
  --dbname "$POSTGRES_DB" \
  --set=reader_user="$WAREHOUSE_READER_USER" \
  --set=reader_password="$WAREHOUSE_READER_PASSWORD" \
  --set=worker_user="$WAREHOUSE_WORKER_USER" \
  --set=worker_password="$WAREHOUSE_WORKER_PASSWORD" <<'SQL'
BEGIN;
SELECT format(
  'CREATE ROLE %I LOGIN PASSWORD %L NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS',
  :'reader_user', :'reader_password'
) WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname=:'reader_user')
\gexec
SELECT format(
  'CREATE ROLE %I LOGIN PASSWORD %L NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS',
  :'worker_user', :'worker_password'
) WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname=:'worker_user')
\gexec

SELECT format('GRANT CONNECT ON DATABASE %I TO %I',current_database(),:'reader_user')
\gexec
SELECT format('GRANT CONNECT ON DATABASE %I TO %I',current_database(),:'worker_user')
\gexec

CREATE SCHEMA IF NOT EXISTS warehouse_staging;
CREATE SCHEMA IF NOT EXISTS warehouse_ods;
CREATE SCHEMA IF NOT EXISTS warehouse_dwd;
CREATE SCHEMA IF NOT EXISTS warehouse_dws;
CREATE SCHEMA IF NOT EXISTS warehouse_published;

REVOKE ALL ON SCHEMA
  warehouse_staging,warehouse_ods,warehouse_dwd,warehouse_dws,warehouse_published
FROM PUBLIC;
GRANT USAGE,CREATE ON SCHEMA
  warehouse_staging,warehouse_ods,warehouse_dwd,warehouse_dws,warehouse_published
TO :"worker_user";
GRANT USAGE ON SCHEMA warehouse_published TO :"reader_user";
GRANT SELECT ON ALL TABLES IN SCHEMA warehouse_published TO :"reader_user";
ALTER DEFAULT PRIVILEGES FOR ROLE :"worker_user" IN SCHEMA warehouse_published
  GRANT SELECT ON TABLES TO :"reader_user";

COMMENT ON SCHEMA warehouse_staging IS '跨源导入的有界临时数据，仅供仓库 worker 使用';
COMMENT ON SCHEMA warehouse_ods IS '外部源表或文件的不可变 ODS 物理快照';
COMMENT ON SCHEMA warehouse_dwd IS '无分组聚合的清洗、转换和关联明细';
COMMENT ON SCHEMA warehouse_dws IS '在 PostgreSQL 内完成分组聚合的主题汇总';
COMMENT ON SCHEMA warehouse_published IS 'API 只读的稳定发布视图';
COMMIT;
SQL
