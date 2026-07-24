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
    'warehouse_staging','warehouse_ods','warehouse_dwd',
    'warehouse_dws','warehouse_published'
  ] LOOP
    IF to_regnamespace(schema_name) IS NULL THEN
      RAISE EXCEPTION 'missing warehouse schema: %', schema_name;
    END IF;
  END LOOP;
END
$verify$;

SELECT has_schema_privilege(:'worker_user','warehouse_dwd','CREATE')
  AND has_schema_privilege(:'worker_user','warehouse_dws','CREATE')
  AS worker_can_build
\gset
\if :worker_can_build
\else
  \echo 'warehouse worker cannot create DWD/DWS relations'
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
SQL

echo "warehouse verification passed"
