#!/usr/bin/env sh
set -eu

usage() {
  echo "usage: $0 --tenant-id UUID --release-id UUID --confirm-drop-space SPACE" >&2
  exit 2
}

root_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tenant_id=
release_id=
confirmed_space=
while [ "$#" -gt 0 ]; do
  case "$1" in
    --tenant-id) [ "$#" -ge 2 ] || usage; tenant_id=$2; shift 2 ;;
    --release-id) [ "$#" -ge 2 ] || usage; release_id=$2; shift 2 ;;
    --confirm-drop-space) [ "$#" -ge 2 ] || usage; confirmed_space=$2; shift 2 ;;
    *) usage ;;
  esac
done

space=${ASKDATA_NEBULA_SPACE:-}
[ -n "$tenant_id" ] && [ -n "$release_id" ] && [ -n "$space" ] || usage
[ "$confirmed_space" = "$space" ] || {
  echo 'confirmation must exactly match ASKDATA_NEBULA_SPACE' >&2
  exit 2
}
case "$space" in
  ''|*[!A-Za-z0-9_]*|[0-9]*) echo 'unsafe Nebula Space identifier' >&2; exit 2 ;;
esac

database_url=${ASKDATA_CONTROL_DATABASE_URL:-${WORKER_DATABASE_URL:-}}
graph_addr=${NEBULA_GRAPH_ADDR:-127.0.0.1}
graph_port=${NEBULA_GRAPH_PORT:-9669}
root_password=${ASKDATA_NEBULA_ROOT_PASSWORD:?ASKDATA_NEBULA_ROOT_PASSWORD is required}
worker_user=${ASKDATA_NEBULA_WORKER_USER:-${ASKDATA_NEBULA_USERNAME:-}}
worker_password=${ASKDATA_NEBULA_WORKER_PASSWORD:-${ASKDATA_NEBULA_PASSWORD:-}}
[ -n "$database_url" ] && [ -n "$worker_user" ] && [ -n "$worker_password" ] || {
  echo 'control database URL and Worker graph credentials are required' >&2
  exit 1
}
command -v go >/dev/null 2>&1 || { echo 'go is required' >&2; exit 1; }
command -v nebula-console >/dev/null 2>&1 || { echo 'nebula-console is required' >&2; exit 1; }

before=$(mktemp "${TMPDIR:-/tmp}/askdata-graph-before.XXXXXX")
after=$(mktemp "${TMPDIR:-/tmp}/askdata-graph-after.XXXXXX")
trap 'rm -f -- "$before" "$after"' EXIT INT TERM

(cd "$root_dir" && ASKDATA_CONTROL_DATABASE_URL=$database_url go run ./cmd/askdata-graph-rebuild \
  --tenant-id "$tenant_id" --release-id "$release_id") >"$before"

drop_output=$(nebula-console -addr "$graph_addr" -port "$graph_port" \
  -u root -p "$root_password" -e "DROP SPACE IF EXISTS $space" 2>&1) || {
  printf '%s\n' "$drop_output" >&2
  exit 1
}
printf '%s\n' "$drop_output" | grep -q '\[ERROR' && {
  printf '%s\n' "$drop_output" >&2
  exit 1
}

NEBULA_GRAPH_ADDR=$graph_addr \
NEBULA_GRAPH_PORT=$graph_port \
NEBULA_SPACE=$space \
NEBULA_ROOT_PASSWORD=$root_password \
NEBULA_BOOTSTRAP_ROOT_PASSWORD=${ASKDATA_NEBULA_BOOTSTRAP_ROOT_PASSWORD:?ASKDATA_NEBULA_BOOTSTRAP_ROOT_PASSWORD is required} \
NEBULA_API_USER=${ASKDATA_NEBULA_API_USER:?ASKDATA_NEBULA_API_USER is required} \
NEBULA_API_PASSWORD=${ASKDATA_NEBULA_API_PASSWORD:?ASKDATA_NEBULA_API_PASSWORD is required} \
NEBULA_WORKER_USER=$worker_user \
NEBULA_WORKER_PASSWORD=$worker_password \
NEBULA_SCHEMA_SOURCE="$root_dir/deployments/nebula/schema.ngql" \
"$root_dir/deployments/nebula/init.sh"

addresses=${ASKDATA_NEBULA_ADDRESSES:-$graph_addr:$graph_port}
(cd "$root_dir" && \
  ASKDATA_CONTROL_DATABASE_URL=$database_url \
  ASKDATA_NEBULA_ADDRESSES=$addresses \
  ASKDATA_NEBULA_USERNAME=$worker_user \
  ASKDATA_NEBULA_PASSWORD=$worker_password \
  ASKDATA_NEBULA_SPACE=$space \
  go run ./cmd/askdata-graph-rebuild --apply \
    --tenant-id "$tenant_id" --release-id "$release_id") >"$after"

if ! cmp -s "$before" "$after"; then
  echo 'graph rebuild proof differs from the pre-drop canonical proof' >&2
  diff -u "$before" "$after" >&2 || true
  exit 1
fi
printf 'AskData graph rebuild verified for release %s: %s' "$release_id" "$(cat "$after")"
