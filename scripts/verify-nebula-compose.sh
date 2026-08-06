#!/usr/bin/env bash
set -euo pipefail
export COMPOSE_PROGRESS=${COMPOSE_PROGRESS:-plain}

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
example_env="$repo_root/.env.example"
verify_nonce="$(date +%s)-$$"
verify_project="askdata-graph002-verify-$verify_nonce"
verify_space="g002_verify_${verify_nonce//-/_}"

is_verification_project() {
  [[ "$1" =~ ^askdata-graph002-verify-[0-9]{10}-[1-9][0-9]{0,6}$ ]]
}

if ! is_verification_project "$verify_project" ||
  [[ ! "$verify_space" =~ ^g002_verify_[0-9]{10}_[1-9][0-9]{0,6}$ ]]; then
  printf 'unsafe GRAPH-002 verification project or Space name\n' >&2
  exit 1
fi

# This verifier intentionally ignores the user's .env and canonical Compose
# project. All data, accounts, ports, and volumes belong to this one run.
export APP_ENV=development
export ASKDATA_NEBULA_PORT=0
export ASKDATA_NEBULA_SPACE="$verify_space"
export ASKDATA_NEBULA_ROOT_PASSWORD=local_g002_root_1
export ASKDATA_NEBULA_BOOTSTRAP_ROOT_PASSWORD=nebula
export ASKDATA_NEBULA_API_USER=g002_reader
export ASKDATA_NEBULA_API_PASSWORD=local_g002_reader_1
export ASKDATA_NEBULA_WORKER_USER=g002_writer
export ASKDATA_NEBULA_WORKER_PASSWORD=local_g002_writer_1

compose_args=(
  compose
  --project-name "$verify_project"
  --file "$repo_root/compose.yaml"
  --file "$repo_root/deployments/nebula/verification.override.yaml"
  --env-file "$example_env"
)

compose() {
  docker "${compose_args[@]}" "$@"
}

cleanup() {
  local status=$?
  trap - EXIT INT TERM
  if is_verification_project "$verify_project"; then
    compose --profile verification --profile graph-access \
      down --volumes --remove-orphans >/dev/null 2>&1 || true
  fi
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

assert_expected_failure() {
  local expected=$1
  shift
  local output status
  set +e
  output=$("$@" 2>&1)
  status=$?
  set -e
  if [[ "$status" -eq 0 ]]; then
    printf 'expected command to fail: %s\n' "$expected" >&2
    exit 1
  fi
  if ! grep -Fq "$expected" <<<"$output"; then
    printf 'command failed for the wrong reason; expected: %s\n' "$expected" >&2
    exit 1
  fi
}

run_init() {
  compose up \
    --no-deps \
    --force-recreate \
    --abort-on-container-exit \
    --exit-code-from nebula-init \
    nebula-init
}

root_query() {
  local query=$1
  compose --profile verification run --rm --no-deps \
    --entrypoint nebula-console nebula-verify \
    -addr nebula-graphd-client -port 9669 -u root \
    -p "$ASKDATA_NEBULA_ROOT_PASSWORD" -e "$query" >/dev/null
}

root_query_output() {
  local query=$1
  compose --profile verification run --rm --no-deps \
    --entrypoint nebula-console nebula-verify \
    -addr nebula-graphd-client -port 9669 -u root \
    -p "$ASKDATA_NEBULA_ROOT_PASSWORD" -e "$query"
}

assert_expected_failure \
  'nebula init failed: production refuses local development NebulaGraph passwords' \
  compose run --rm --no-deps -e APP_ENV=production nebula-init
assert_expected_failure \
  'nebula init failed: APP_ENV must be development, test, or production' \
  compose run --rm --no-deps -e APP_ENV=prod nebula-init

compose up -d nebula-metad nebula-storaged nebula-graphd
compose up -d --wait nebula-metad nebula-graphd

# The second run proves bootstrap idempotence against the same persistent Meta volume.
run_init
run_init
compose up -d --wait nebula-storaged
compose --profile verification run --rm --no-deps nebula-verify

# graphd has no host port. The credential-free proxy starts only after init has
# completed, and receives a random loopback port inside this isolated project.
compose --profile graph-access up -d --wait nebula-loopback-proxy
proxy_endpoint=$(compose --profile graph-access port nebula-loopback-proxy 9669 | tail -n 1)
case "$proxy_endpoint" in
  127.0.0.1:[0-9]*) ;;
  *) printf 'unexpected NebulaGraph loopback proxy endpoint: %s\n' "$proxy_endpoint" >&2; exit 1 ;;
esac

rendered_config=$(compose --profile verification --profile graph-access config --no-env-resolution --format json)
jq -e \
  --arg reader "$ASKDATA_NEBULA_API_USER" \
  --arg writer "$ASKDATA_NEBULA_WORKER_USER" '
  def hasnet($service; $network): .services[$service].networks | has($network);
  def emptyenv($service; $name): (.services[$service].environment[$name] // "") == "";
  (.services["nebula-metad"].ports == null) and
  (.services["nebula-storaged"].ports == null) and
  (.services["nebula-graphd"].ports == null) and
  hasnet("nebula-metad"; "askdata_graph_cluster_net") and
  (hasnet("nebula-metad"; "askdata_graph_client_net") | not) and
  hasnet("nebula-storaged"; "askdata_graph_cluster_net") and
  (hasnet("nebula-storaged"; "askdata_graph_client_net") | not) and
  hasnet("nebula-graphd"; "askdata_graph_cluster_net") and
  hasnet("nebula-graphd"; "askdata_graph_client_net") and
  (hasnet("nebula-graphd"; "askdata_graph_host_net") | not) and
  hasnet("nebula-loopback-proxy"; "askdata_graph_client_net") and
  hasnet("nebula-loopback-proxy"; "askdata_graph_host_net") and
  (.services["nebula-loopback-proxy"].ports | all(.host_ip == "127.0.0.1")) and
  (.services.api.env_file == null) and
  (.services.worker.env_file == null) and
  (.services["connection-test-worker"].env_file == null) and
  hasnet("api"; "askdata_graph_client_net") and
  hasnet("worker"; "askdata_graph_client_net") and
  (hasnet("api"; "askdata_graph_cluster_net") | not) and
  (hasnet("worker"; "askdata_graph_cluster_net") | not) and
  (hasnet("connection-test-worker"; "askdata_graph_client_net") | not) and
  (hasnet("web"; "askdata_graph_client_net") | not) and
  (.services.api.environment.ASKDATA_NEBULA_USERNAME == $reader) and
  (.services.worker.environment.ASKDATA_NEBULA_USERNAME == $writer) and
  (.services.api.environment.ASKDATA_NEBULA_PASSWORD | length > 0) and
  (.services.worker.environment.ASKDATA_NEBULA_PASSWORD | length > 0) and
  emptyenv("api"; "ASKDATA_NEBULA_ROOT_PASSWORD") and
  emptyenv("api"; "ASKDATA_NEBULA_BOOTSTRAP_ROOT_PASSWORD") and
  emptyenv("api"; "ASKDATA_NEBULA_API_PASSWORD") and
  emptyenv("api"; "ASKDATA_NEBULA_WORKER_PASSWORD") and
  emptyenv("worker"; "ASKDATA_NEBULA_ROOT_PASSWORD") and
  emptyenv("worker"; "ASKDATA_NEBULA_BOOTSTRAP_ROOT_PASSWORD") and
  emptyenv("worker"; "ASKDATA_NEBULA_API_PASSWORD") and
  emptyenv("worker"; "ASKDATA_NEBULA_WORKER_PASSWORD") and
  emptyenv("connection-test-worker"; "ASKDATA_NEBULA_ADDRESSES") and
  emptyenv("connection-test-worker"; "ASKDATA_NEBULA_SPACE") and
  emptyenv("connection-test-worker"; "ASKDATA_NEBULA_USERNAME") and
  emptyenv("connection-test-worker"; "ASKDATA_NEBULA_PASSWORD") and
  emptyenv("connection-test-worker"; "ASKDATA_NEBULA_TLS_ENABLED") and
  emptyenv("connection-test-worker"; "ASKDATA_NEBULA_ROOT_PASSWORD") and
  emptyenv("connection-test-worker"; "ASKDATA_NEBULA_BOOTSTRAP_ROOT_PASSWORD") and
  emptyenv("connection-test-worker"; "ASKDATA_NEBULA_API_PASSWORD") and
  emptyenv("connection-test-worker"; "ASKDATA_NEBULA_WORKER_PASSWORD")
' <<<"$rendered_config" >/dev/null

export ASKDATA_NEBULA_COMPOSE_INTEGRATION=1
export ASKDATA_NEBULA_COMPOSE_ISOLATED=1
export ASKDATA_NEBULA_COMPOSE_RECREATE=1
export ASKDATA_NEBULA_COMPOSE_PROJECT="$verify_project"
export ASKDATA_NEBULA_COMPOSE_ADDRESSES="$proxy_endpoint"
export ASKDATA_NEBULA_COMPOSE_SPACE="$ASKDATA_NEBULA_SPACE"
export ASKDATA_NEBULA_COMPOSE_READER_USER="$ASKDATA_NEBULA_API_USER"
export ASKDATA_NEBULA_COMPOSE_READER_PASSWORD="$ASKDATA_NEBULA_API_PASSWORD"
export ASKDATA_NEBULA_COMPOSE_WRITER_USER="$ASKDATA_NEBULA_WORKER_USER"
export ASKDATA_NEBULA_COMPOSE_WRITER_PASSWORD="$ASKDATA_NEBULA_WORKER_PASSWORD"
go test ./internal/askdata/graph -run '^TestNebulaComposeRolesPersistenceAndGraphPlan$' -count=1 -v

compose --profile verification run --rm --no-deps nebula-verify

# GRAPH-004 disaster-rebuild proof: remove the entire isolated Space, wait for
# metadata convergence, recreate only the frozen schema/roles through init,
# then let the application projector rebuild all vertices and edges again.
root_query "DROP SPACE $ASKDATA_NEBULA_SPACE"
space_drop_attempt=0
while [[ "$space_drop_attempt" -lt 60 ]]; do
  if ! root_query_output 'SHOW SPACES' 2>/dev/null | grep -Fq "$ASKDATA_NEBULA_SPACE"; then
    break
  fi
  space_drop_attempt=$((space_drop_attempt + 1))
  sleep 1
done
if [[ "$space_drop_attempt" -ge 60 ]]; then
  printf 'isolated NebulaGraph Space did not finish dropping\n' >&2
  exit 1
fi
run_init
compose --profile verification run --rm --no-deps nebula-verify
export ASKDATA_NEBULA_COMPOSE_RECREATE=0
go test ./internal/askdata/graph -run '^TestNebulaComposeRolesPersistenceAndGraphPlan$' -count=1 -v

# Intentionally corrupt isolated objects and prove init rejects exact drift.
root_query 'CREATE SPACE graph002_partition_drift(partition_num=10, replica_factor=1, vid_type=FIXED_STRING(256))'
assert_expected_failure \
  'nebula init failed: AskData Space partition_num drifted from 1' \
  compose run --rm --no-deps -e NEBULA_SPACE=graph002_partition_drift nebula-init

root_query "USE $ASKDATA_NEBULA_SPACE; ALTER TAG semantic_model ADD (graph002_drift string)"
assert_expected_failure \
  'nebula init failed: TAG semantic_model does not match the frozen GraphPlan schema' \
  compose run --rm --no-deps -e NEBULA_SCHEMA_CONVERGENCE_ATTEMPTS=2 nebula-init

printf 'formal isolated NebulaGraph Compose verification passed\n'
