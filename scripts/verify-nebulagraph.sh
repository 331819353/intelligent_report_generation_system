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

SPACE=${NEBULA_GRAPH_SPACE:-smart_query_dev}
case "$SPACE" in
  ''|*[!A-Za-z0-9_]*)
    echo "NEBULA_GRAPH_SPACE is invalid" >&2
    exit 1
    ;;
esac

tag_output=$(docker compose --env-file "$ENV_FILE" run --rm --no-deps \
  --entrypoint nebula-console nebula-init \
  -addr nebula-graphd -port 9669 -u "${NEBULA_GRAPH_USERNAME:-root}" \
  -p "${NEBULA_GRAPH_PASSWORD:-nebula}" \
  -e "USE $SPACE; SHOW TAGS")
edge_output=$(docker compose --env-file "$ENV_FILE" run --rm --no-deps \
  --entrypoint nebula-console nebula-init \
  -addr nebula-graphd -port 9669 -u "${NEBULA_GRAPH_USERNAME:-root}" \
  -p "${NEBULA_GRAPH_PASSWORD:-nebula}" \
  -e "USE $SPACE; SHOW EDGES")

for expected_tag in metric dimension dimension_value dataset role quality_rule; do
  echo "$tag_output" | grep -q "$expected_tag" || {
    echo "NebulaGraph tag is missing: $expected_tag" >&2
    exit 1
  }
done
for expected_edge in groupable_by has_value joins_to can_access derived_from guards; do
  echo "$edge_output" | grep -q "$expected_edge" || {
    echo "NebulaGraph edge is missing: $expected_edge" >&2
    exit 1
  }
done

NEBULA_INTEGRATION=1 go test ./internal/semanticgraph \
  -run TestNebulaProjectionAndBoundedPathIntegration -count=1

echo "NebulaGraph semantic runtime verification passed"
