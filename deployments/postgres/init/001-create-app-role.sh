#!/usr/bin/env sh
set -eu

: "${POSTGRES_APP_USER:?POSTGRES_APP_USER is required}"
: "${POSTGRES_APP_PASSWORD:?POSTGRES_APP_PASSWORD is required}"

# 通过 psql 变量传值和 format 标识符转义，避免把凭据直接拼接进 SQL。
psql -v ON_ERROR_STOP=1 \
  --username "$POSTGRES_USER" \
  --dbname "$POSTGRES_DB" \
  --set=app_user="$POSTGRES_APP_USER" \
  --set=app_password="$POSTGRES_APP_PASSWORD" <<'SQL'
SELECT format(
  'CREATE ROLE %I LOGIN PASSWORD %L NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT',
  :'app_user',
  :'app_password'
) WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'app_user')
\gexec

SELECT format('GRANT CONNECT ON DATABASE %I TO %I', current_database(), :'app_user')
\gexec
SQL
