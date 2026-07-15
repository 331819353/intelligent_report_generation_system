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

if ! command -v docker >/dev/null 2>&1; then
  echo "docker is required to run database migrations" >&2
  exit 1
fi

# 迁移登记表位于 platform 模式之外，确保首次初始化前也可创建。
docker compose --env-file "$ENV_FILE" exec -T postgres \
  psql -v ON_ERROR_STOP=1 -U "${POSTGRES_USER:-report_admin}" -d "${POSTGRES_DB:-intelligent_report}" <<'SQL'
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
    psql -At -U "${POSTGRES_USER:-report_admin}" -d "${POSTGRES_DB:-intelligent_report}" \
    -c "SELECT 1 FROM platform_schema_migrations WHERE version = '$version'" || true)
  if [ "$applied" = "1" ]; then
    echo "skip $version"
    continue
  fi

  echo "apply $version"
  {
    echo 'BEGIN;'
    cat "$migration"
    printf "\nINSERT INTO platform_schema_migrations(version) VALUES ('%s');\n" "$version"
    echo 'COMMIT;'
  } | docker compose --env-file "$ENV_FILE" exec -T postgres \
      psql -v ON_ERROR_STOP=1 -U "${POSTGRES_USER:-report_admin}" -d "${POSTGRES_DB:-intelligent_report}"
done

docker compose --env-file "$ENV_FILE" exec -T postgres \
  psql -v ON_ERROR_STOP=1 -U "${POSTGRES_USER:-report_admin}" -d "${POSTGRES_DB:-intelligent_report}" \
  --set=app_user="${POSTGRES_APP_USER:-report_app}" <<'SQL'
GRANT USAGE ON SCHEMA platform TO :"app_user";
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA platform TO :"app_user";
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA platform TO :"app_user";
ALTER DEFAULT PRIVILEGES IN SCHEMA platform GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO :"app_user";
ALTER DEFAULT PRIVILEGES IN SCHEMA platform GRANT USAGE, SELECT ON SEQUENCES TO :"app_user";
SQL
