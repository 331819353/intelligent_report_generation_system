#!/usr/bin/env sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
COMPOSE_FILE="$ROOT_DIR/compose.yaml"

required_services="postgres postgres-warehouse minio minio-init nebula-metad nebula-storaged nebula-graphd nebula-init nebula-loopback-proxy connector-service api worker connection-test-worker web"
# 检查开发环境依赖服务是否全部声明。
for service in $required_services; do
  if ! grep -q "^  $service:" "$COMPOSE_FILE"; then
    echo "missing compose service: $service" >&2
    exit 1
  fi
done

# 检查健康探针与必需对象存储桶等关键配置标记。
for marker in \
  "pg_isready" \
  "minio/health/live" \
  "uploads" \
  "snapshots" \
  "vesoft/nebula-metad:v3.8.0" \
  "vesoft/nebula-storaged:v3.8.0" \
  "vesoft/nebula-graphd:v3.8.0" \
  "--enable_authorize=true" \
  "--auth_type=password" \
  "nebula_meta_data" \
  "nebula_storage_data" \
  "service_completed_successfully" \
  "askdata_graph_cluster_net" \
  "askdata_graph_client_net" \
  "askdata_graph_host_net" \
  '127.0.0.1:${ASKDATA_NEBULA_PORT:-9669}:9669' \
  "internal: true"; do
  if ! grep -Fq -- "$marker" "$COMPOSE_FILE"; then
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

for nebula_file in \
  "$ROOT_DIR/deployments/nebula/init.sh" \
  "$ROOT_DIR/deployments/nebula/schema.ngql" \
  "$ROOT_DIR/deployments/nebula/verify.sh" \
  "$ROOT_DIR/scripts/verify-nebula-compose.sh" \
  "$ROOT_DIR/scripts/run-with-nebula-role.sh" \
  "$ROOT_DIR/scripts/verify-nebula-env-isolation.sh"; do
  if [ ! -x "$nebula_file" ] && [ "${nebula_file##*.}" = sh ]; then
    echo "NebulaGraph script is not executable: $nebula_file" >&2
    exit 1
  fi
  if [ ! -f "$nebula_file" ]; then
    echo "missing NebulaGraph deployment file: $nebula_file" >&2
    exit 1
  fi
done

for schema_marker in \
  "CREATE TAG IF NOT EXISTS semantic_model" \
  "CREATE TAG IF NOT EXISTS metric" \
  "CREATE TAG IF NOT EXISTS dimension" \
  "CREATE TAG IF NOT EXISTS member" \
  "CREATE EDGE IF NOT EXISTS MODELED_BY" \
  "CREATE EDGE IF NOT EXISTS HAS_DIMENSION" \
  "CREATE EDGE IF NOT EXISTS HAS_MEMBER" \
  "CREATE EDGE IF NOT EXISTS JOINS_TO"; do
  if ! grep -q "$schema_marker" "$ROOT_DIR/deployments/nebula/schema.ngql"; then
    echo "missing NebulaGraph schema marker: $schema_marker" >&2
    exit 1
  fi
done

compose_json=$(docker compose --env-file "$ROOT_DIR/.env.example" --file "$COMPOSE_FILE" \
  --profile graph-access --profile verification config --format json)
jq -e '
  def hasnet($service; $network): .services[$service].networks | has($network);
  (.services["nebula-metad"].ports == null) and
  (.services["nebula-storaged"].ports == null) and
  (.services["nebula-graphd"].ports == null) and
  hasnet("nebula-metad"; "askdata_graph_cluster_net") and
  hasnet("nebula-storaged"; "askdata_graph_cluster_net") and
  hasnet("nebula-graphd"; "askdata_graph_cluster_net") and
  hasnet("nebula-graphd"; "askdata_graph_client_net") and
  (hasnet("nebula-graphd"; "askdata_graph_host_net") | not) and
  hasnet("nebula-loopback-proxy"; "askdata_graph_client_net") and
  hasnet("nebula-loopback-proxy"; "askdata_graph_host_net") and
  (.services["nebula-loopback-proxy"].ports | all(.host_ip == "127.0.0.1")) and
  hasnet("api"; "askdata_graph_client_net") and
  hasnet("worker"; "askdata_graph_client_net") and
  (hasnet("api"; "askdata_graph_cluster_net") | not) and
  (hasnet("worker"; "askdata_graph_cluster_net") | not) and
  (hasnet("connection-test-worker"; "askdata_graph_client_net") | not) and
  (hasnet("web"; "askdata_graph_client_net") | not)
' <<EOF >/dev/null
$compose_json
EOF
"$ROOT_DIR/scripts/verify-nebula-env-isolation.sh"

echo "compose static checks passed"
