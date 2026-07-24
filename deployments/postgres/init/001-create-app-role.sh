#!/usr/bin/env sh
set -eu

: "${POSTGRES_USER:?POSTGRES_USER is required}"
: "${POSTGRES_APP_USER:?POSTGRES_APP_USER is required}"
: "${POSTGRES_APP_PASSWORD:?POSTGRES_APP_PASSWORD is required}"
: "${POSTGRES_WORKER_USER:?POSTGRES_WORKER_USER is required}"
: "${POSTGRES_WORKER_PASSWORD:?POSTGRES_WORKER_PASSWORD is required}"
: "${POSTGRES_CONNECTION_TEST_USER:?POSTGRES_CONNECTION_TEST_USER is required}"
: "${POSTGRES_CONNECTION_TEST_PASSWORD:?POSTGRES_CONNECTION_TEST_PASSWORD is required}"

if [ "$POSTGRES_APP_USER" = "$POSTGRES_WORKER_USER" ] ||
  [ "$POSTGRES_APP_USER" = "$POSTGRES_CONNECTION_TEST_USER" ] ||
  [ "$POSTGRES_WORKER_USER" = "$POSTGRES_CONNECTION_TEST_USER" ] ||
  [ "$POSTGRES_APP_USER" = "$POSTGRES_USER" ] ||
  [ "$POSTGRES_WORKER_USER" = "$POSTGRES_USER" ] ||
  [ "$POSTGRES_CONNECTION_TEST_USER" = "$POSTGRES_USER" ]; then
  echo "admin and all runtime database roles must be distinct" >&2
  exit 1
fi

# 通过 psql 变量传值和 format 标识符转义，避免把凭据直接拼接进 SQL。
psql -v ON_ERROR_STOP=1 \
  --username "$POSTGRES_USER" \
  --dbname "$POSTGRES_DB" \
  --set=app_user="$POSTGRES_APP_USER" \
  --set=app_password="$POSTGRES_APP_PASSWORD" \
  --set=worker_user="$POSTGRES_WORKER_USER" \
  --set=worker_password="$POSTGRES_WORKER_PASSWORD" \
  --set=connection_test_user="$POSTGRES_CONNECTION_TEST_USER" \
  --set=connection_test_password="$POSTGRES_CONNECTION_TEST_PASSWORD" <<'SQL'
BEGIN;
SELECT format(
  'CREATE ROLE %I LOGIN PASSWORD %L NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS',
  :'app_user',
  :'app_password'
) WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'app_user')
\gexec

SELECT format('GRANT CONNECT ON DATABASE %I TO %I', current_database(), :'app_user')
\gexec

SELECT format(
  'CREATE ROLE %I LOGIN PASSWORD %L NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS',
  :'worker_user',
  :'worker_password'
) WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'worker_user')
\gexec

SELECT format('GRANT CONNECT ON DATABASE %I TO %I', current_database(), :'worker_user')
\gexec

SELECT format(
  'CREATE ROLE %I LOGIN PASSWORD %L NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS',
  :'connection_test_user',
  :'connection_test_password'
) WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'connection_test_user')
\gexec

SELECT format(
  'GRANT CONNECT ON DATABASE %I TO %I',
  current_database(),
  :'connection_test_user'
)
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
