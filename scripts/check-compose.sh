#!/usr/bin/env sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
COMPOSE_FILE="$ROOT_DIR/compose.yaml"

required_services="postgres postgres-warehouse minio minio-init connector-service api worker connection-test-worker web"
# 检查开发环境依赖服务是否全部声明。
for service in $required_services; do
  if ! grep -q "^  $service:" "$COMPOSE_FILE"; then
    echo "missing compose service: $service" >&2
    exit 1
  fi
done

# 检查健康探针与必需对象存储桶等关键配置标记。
for marker in "pg_isready" "minio/health/live" "uploads" "snapshots"; do
  if ! grep -q "$marker" "$COMPOSE_FILE"; then
    echo "missing compose marker: $marker" >&2
    exit 1
  fi
done

for dockerfile in \
  "$ROOT_DIR/deployments/app/Dockerfile" \
  "$ROOT_DIR/web/Dockerfile"; do
  if [ ! -f "$dockerfile" ]; then
    echo "missing application Dockerfile: $dockerfile" >&2
    exit 1
  fi
done

echo "compose static checks passed"
