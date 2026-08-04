#!/usr/bin/env sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
COMPOSE_ENV="$ROOT_DIR/.env.example"

compose() {
  if [ -f "$ROOT_DIR/.env" ]; then
    docker compose \
      --env-file "$COMPOSE_ENV" \
      --env-file "$ROOT_DIR/.env" \
      "$@"
  else
    docker compose --env-file "$COMPOSE_ENV" "$@"
  fi
}

wait_for_http() {
  name=$1
  url=$2
  attempts=${3:-30}
  count=0
  while [ "$count" -lt "$attempts" ]; do
    if curl -fsS --max-time 2 "$url" >/dev/null 2>&1; then
      printf '%s healthy (%s)\n' "$name" "$url"
      return
    fi
    count=$((count + 1))
    sleep 1
  done
  printf '%s did not become healthy: %s\n' "$name" "$url" >&2
  compose logs --tail=80 "$name" >&2 || true
  return 1
}

start_all() {
  compose up -d --wait postgres postgres-warehouse minio connector-service
  compose run --rm minio-init
  "$ROOT_DIR/scripts/migrate.sh"
  compose up -d --build --wait api worker connection-test-worker web
  wait_for_http api http://127.0.0.1:8080/health/ready
  wait_for_http connector-service http://127.0.0.1:8090/health/live
  wait_for_http web http://127.0.0.1:5173/
  printf 'development services are ready and managed by Docker Compose\n'
}

stop_all() {
  compose stop web connection-test-worker worker api
}

status_all() {
  failed=0
  running=$(compose ps --status running --services)
  for name in api worker connection-test-worker web; do
    if printf '%s\n' "$running" | grep -Fx "$name" >/dev/null 2>&1; then
      printf '%-24s running\n' "$name"
    else
      printf '%-24s stopped\n' "$name"
      failed=1
    fi
  done
  for item in \
    "api-live|http://127.0.0.1:8080/health/live" \
    "api-ready|http://127.0.0.1:8080/health/ready" \
    "connector|http://127.0.0.1:8090/health/live" \
    "web|http://127.0.0.1:5173/"; do
    name=${item%%|*}
    url=${item#*|}
    http_code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 2 "$url" 2>/dev/null || true)
    if [ "$http_code" = "200" ]; then
      printf '%-24s healthy http=200\n' "$name"
    else
      printf '%-24s unavailable http=%s\n' "$name" "${http_code:-000}"
      failed=1
    fi
  done
  compose ps
  return "$failed"
}

show_logs() {
  compose logs --tail=100 api worker connection-test-worker web
}

case "${1:-}" in
  start) start_all ;;
  stop) stop_all ;;
  restart)
    stop_all
    start_all
    ;;
  status) status_all ;;
  logs) show_logs ;;
  *)
    printf 'usage: %s {start|stop|restart|status|logs}\n' "$0" >&2
    exit 2
    ;;
esac
