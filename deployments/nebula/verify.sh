#!/usr/bin/env sh
set -eu

graph_addr=${NEBULA_GRAPH_ADDR:-nebula-graphd}
graph_port=${NEBULA_GRAPH_PORT:-9669}
space=${NEBULA_SPACE:?NEBULA_SPACE is required}
root_password=${NEBULA_ROOT_PASSWORD:?NEBULA_ROOT_PASSWORD is required}
bootstrap_password=${NEBULA_BOOTSTRAP_ROOT_PASSWORD:?NEBULA_BOOTSTRAP_ROOT_PASSWORD is required}
api_user=${NEBULA_API_USER:?NEBULA_API_USER is required}
api_password=${NEBULA_API_PASSWORD:?NEBULA_API_PASSWORD is required}
worker_user=${NEBULA_WORKER_USER:?NEBULA_WORKER_USER is required}
worker_password=${NEBULA_WORKER_PASSWORD:?NEBULA_WORKER_PASSWORD is required}

fail() {
  printf 'nebula verification failed: %s\n' "$1" >&2
  exit 1
}

LAST_OUTPUT=
capture_query() {
  query_user=$1
  query_password=$2
  query=$3
  LAST_OUTPUT=$(
    nebula-console -addr "$graph_addr" -port "$graph_port" \
      -u "$query_user" -p "$query_password" -e "$query" 2>&1
  ) || return 1
  if printf '%s\n' "$LAST_OUTPUT" | grep -q '\[ERROR'; then
    return 1
  fi
}

capture_query root "$root_password" 'SHOW USERS' || fail 'configured root login failed'
if capture_query root "$bootstrap_password" 'SHOW USERS'; then
  fail 'bootstrap root password is still accepted'
fi

assert_host() {
  service=$1
  expected_host=$2
  expected_port=$3
  capture_query root "$root_password" "SHOW HOSTS $service" || fail "$service host query failed"
  printf '%s\n' "$LAST_OUTPUT" | awk -F '|' \
    -v wanted_host="$expected_host" -v wanted_port="$expected_port" '
      /^\|/ {
        host=$2; port=$3; status=$4; version=$7
        gsub(/^[[:space:]\"]+|[[:space:]\"]+$/, "", host)
        gsub(/^[[:space:]\"]+|[[:space:]\"]+$/, "", port)
        gsub(/^[[:space:]\"]+|[[:space:]\"]+$/, "", status)
        gsub(/^[[:space:]\"]+|[[:space:]\"]+$/, "", version)
        if (host == wanted_host && port == wanted_port && status == "ONLINE" && version == "3.8.0") found=1
      }
      END { exit(found ? 0 : 1) }
    ' || fail "$service expected host is not ONLINE on NebulaGraph v3.8.0"
}

assert_host META nebula-metad 9559
assert_host STORAGE nebula-storaged 9779
assert_host GRAPH nebula-graphd 9669

capture_query root "$root_password" "SHOW ROLES IN $space" || fail 'role query failed'
printf '%s\n' "$LAST_OUTPUT" | grep -Eq "[\" ]$api_user[\" ].*GUEST" ||
  fail 'API user is not a GUEST'
printf '%s\n' "$LAST_OUTPUT" | grep -Eq "[\" ]$worker_user[\" ].*USER" ||
  fail 'Worker user is not a USER'

capture_query "$api_user" "$api_password" "USE $space; SHOW TAGS" ||
  fail 'API GUEST cannot inspect graph tags'
for tag_name in semantic_model metric dimension member; do
  printf '%s\n' "$LAST_OUTPUT" | grep -q "$tag_name" || fail "missing tag $tag_name"
done
capture_query "$api_user" "$api_password" "USE $space; SHOW EDGES" ||
  fail 'API GUEST cannot inspect graph edges'
for edge_name in MODELED_BY HAS_DIMENSION HAS_MEMBER JOINS_TO; do
  printf '%s\n' "$LAST_OUTPUT" | grep -q "$edge_name" || fail "missing edge $edge_name"
done

if capture_query "$api_user" "${api_password}x" "USE $space; RETURN 1"; then
  fail 'wrong API password was accepted'
fi
if capture_query "$worker_user" "${worker_password}x" "USE $space; RETURN 1"; then
  fail 'wrong Worker password was accepted'
fi

# GUEST/USER 的权限必须只落在目标 Space；创建一次性 Space 验证不能横向读取。
verification_space="graph002_denied_$$_$(date +%s)"
cleanup_verification_space() {
  capture_query root "$root_password" "DROP SPACE IF EXISTS $verification_space" >/dev/null 2>&1 || true
}
trap cleanup_verification_space EXIT INT TERM
capture_query root "$root_password" \
  "CREATE SPACE $verification_space(partition_num=1, replica_factor=1, vid_type=FIXED_STRING(32))" ||
  fail 'could not create the role-isolation verification Space'
attempt=0
while [ "$attempt" -lt 60 ]; do
  if capture_query root "$root_password" "USE $verification_space; RETURN 1 AS ready"; then
    break
  fi
  attempt=$((attempt + 1))
  sleep 1
done
[ "$attempt" -lt 60 ] || fail 'role-isolation verification Space did not become ready'
if capture_query "$api_user" "$api_password" "USE $verification_space; RETURN 1"; then
  fail 'API GUEST can access an unauthorized Space'
fi
if capture_query "$worker_user" "$worker_password" "USE $verification_space; RETURN 1"; then
  fail 'Worker USER can access an unauthorized Space'
fi
cleanup_verification_space
trap - EXIT INT TERM

printf 'nebula runtime verification passed\n'
