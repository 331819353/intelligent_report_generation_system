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

docker compose --env-file "$ENV_FILE" exec -T postgres-warehouse \
  psql -v ON_ERROR_STOP=1 \
  -U "${WAREHOUSE_POSTGRES_USER:-warehouse_admin}" \
  -d "${WAREHOUSE_POSTGRES_DB:-intelligent_report_warehouse}" \
  --set=reader_user="${WAREHOUSE_READER_USER:-report_warehouse_reader}" \
  --set=worker_user="${WAREHOUSE_WORKER_USER:-report_warehouse_worker}" <<'SQL'
DO $verify$
DECLARE
  schema_name text;
BEGIN
  FOREACH schema_name IN ARRAY ARRAY[
    'warehouse_staging','warehouse_ods','warehouse_dim','warehouse_dwd',
    'warehouse_dws','warehouse_ads','warehouse_published'
  ] LOOP
    IF to_regnamespace(schema_name) IS NULL THEN
      RAISE EXCEPTION 'missing warehouse schema: %', schema_name;
    END IF;
  END LOOP;
END
$verify$;

SELECT has_schema_privilege(:'worker_user','warehouse_dwd','CREATE')
  AND has_schema_privilege(:'worker_user','warehouse_dim','CREATE')
  AND has_schema_privilege(:'worker_user','warehouse_dws','CREATE')
  AND has_schema_privilege(:'worker_user','warehouse_ads','CREATE')
  AS worker_can_build
\gset
\if :worker_can_build
\else
  \echo 'warehouse worker cannot create DIM/DWD/DWS/ADS relations'
  \quit 1
\endif

SELECT has_schema_privilege(:'reader_user','warehouse_published','USAGE')
  AND NOT has_schema_privilege(:'reader_user','warehouse_dws','CREATE')
  AS reader_is_read_only
\gset
\if :reader_is_read_only
\else
  \echo 'warehouse reader privileges are unsafe'
  \quit 1
\endif

SELECT NOT has_function_privilege(
    'public',
    'warehouse_published.apply_dimension_profile_resource_limits(integer,integer,integer)',
    'EXECUTE'
  )
  AND NOT has_function_privilege(
    :'reader_user',
    'warehouse_published.apply_dimension_profile_resource_limits(integer,integer,integer)',
    'EXECUTE'
  )
  AND has_function_privilege(
    :'worker_user',
    'warehouse_published.apply_dimension_profile_resource_limits(integer,integer,integer)',
    'EXECUTE'
  )
  AS profile_limits_are_scoped
\gset
\if :profile_limits_are_scoped
\else
  \echo 'warehouse dimension profile resource limit privileges are unsafe'
  \quit 1
\endif
SQL

echo "warehouse verification passed"
