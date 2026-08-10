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
schema_source=${NEBULA_SCHEMA_SOURCE:-/opt/askdata-nebula/schema.ngql}
schema_attempts=${NEBULA_SCHEMA_CONVERGENCE_ATTEMPTS:-60}

fail() {
  printf 'nebula init failed: %s\n' "$1" >&2
  exit 1
}

validate_identifier() {
  value=$1
  label=$2
  max_length=$3
  case "$value" in
    ''|*[!A-Za-z0-9_]*) fail "$label must contain only ASCII letters, digits, and underscore" ;;
  esac
  case "$value" in
    [A-Za-z]*) ;;
    *) fail "$label must start with an ASCII letter" ;;
  esac
  [ "${#value}" -le "$max_length" ] || fail "$label exceeds $max_length characters"
}

validate_password() {
  value=$1
  label=$2
  case "$value" in
    ''|*[!A-Za-z0-9_]*) fail "$label must contain only ASCII letters, digits, and underscore" ;;
  esac
  [ "${#value}" -ge 8 ] || fail "$label must contain at least 8 characters"
  [ "${#value}" -le 24 ] || fail "$label exceeds NebulaGraph's 24-character limit"
}

validate_bootstrap_password() {
  value=$1
  case "$value" in
    ''|*[!A-Za-z0-9_]*) fail 'NEBULA_BOOTSTRAP_ROOT_PASSWORD must contain only ASCII letters, digits, and underscore' ;;
  esac
  [ "${#value}" -le 24 ] || fail "NEBULA_BOOTSTRAP_ROOT_PASSWORD exceeds NebulaGraph's 24-character limit"
}

validate_identifier "$space" NEBULA_SPACE 32
validate_identifier "$api_user" NEBULA_API_USER 16
validate_identifier "$worker_user" NEBULA_WORKER_USER 16
validate_password "$root_password" NEBULA_ROOT_PASSWORD
validate_bootstrap_password "$bootstrap_password"
validate_password "$api_password" NEBULA_API_PASSWORD
validate_password "$worker_password" NEBULA_WORKER_PASSWORD
case "$schema_attempts" in
  ''|*[!0-9]*) fail 'NEBULA_SCHEMA_CONVERGENCE_ATTEMPTS must be a positive integer' ;;
esac
[ "$schema_attempts" -ge 1 ] && [ "$schema_attempts" -le 300 ] ||
  fail 'NEBULA_SCHEMA_CONVERGENCE_ATTEMPTS must be between 1 and 300'

[ "$api_user" != root ] || fail 'API user cannot be root'
[ "$worker_user" != root ] || fail 'Worker user cannot be root'
[ "$api_user" != "$worker_user" ] || fail 'API and Worker users must be distinct'
[ "$root_password" != "$bootstrap_password" ] || fail 'configured root password must replace the bootstrap password'
[ "$root_password" != "$api_password" ] || fail 'root and API passwords must be distinct'
[ "$root_password" != "$worker_password" ] || fail 'root and Worker passwords must be distinct'
[ "$api_password" != "$worker_password" ] || fail 'API and Worker passwords must be distinct'
[ "$api_password" != "$bootstrap_password" ] || fail 'API password cannot equal the bootstrap password'
[ "$worker_password" != "$bootstrap_password" ] || fail 'Worker password cannot equal the bootstrap password'

case "${APP_ENV:-development}" in
  development|test) ;;
  production)
    for configured_password in "$root_password" "$api_password" "$worker_password"; do
      case "$configured_password" in
        local_*) fail 'production refuses local development NebulaGraph passwords' ;;
      esac
    done
    ;;
  *) fail 'APP_ENV must be development, test, or production' ;;
esac

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

capture_file() {
  query_user=$1
  query_password=$2
  query_file=$3
  LAST_OUTPUT=$(
    nebula-console -addr "$graph_addr" -port "$graph_port" \
      -u "$query_user" -p "$query_password" -f "$query_file" 2>&1
  ) || return 1
  if printf '%s\n' "$LAST_OUTPUT" | grep -q '\[ERROR'; then
    return 1
  fi
}

probe_root_password() {
  capture_query root "$1" 'SHOW USERS'
}

admin_password=
attempt=0
while [ "$attempt" -lt 90 ]; do
  if probe_root_password "$root_password"; then
    admin_password=$root_password
    break
  fi
  if probe_root_password "$bootstrap_password"; then
    admin_password=$bootstrap_password
    break
  fi
  attempt=$((attempt + 1))
  sleep 1
done
[ -n "$admin_password" ] || fail 'graphd did not accept configured or bootstrap root credentials'

# 首次空卷启动时先关闭厂商默认 GOD 凭据窗口，再执行任何其他初始化。
if [ "$admin_password" = "$bootstrap_password" ]; then
  capture_query root "$admin_password" \
    "ALTER USER root WITH PASSWORD '$root_password'" ||
    fail 'could not rotate the bootstrap root password'
fi
attempt=0
while [ "$attempt" -lt 60 ]; do
  if probe_root_password "$root_password"; then
    if ! probe_root_password "$bootstrap_password"; then
      break
    fi
  fi
  attempt=$((attempt + 1))
  sleep 1
done
[ "$attempt" -lt 60 ] || fail 'root password rotation did not converge'
admin_password=$root_password

if ! capture_query root "$admin_password" 'ADD HOSTS "nebula-storaged":9779'; then
  if ! printf '%s\n' "$LAST_OUTPUT" | grep -qi 'exist'; then
    fail 'could not register nebula-storaged'
  fi
fi

attempt=0
while [ "$attempt" -lt 90 ]; do
  if capture_query root "$admin_password" 'SHOW HOSTS STORAGE' &&
    printf '%s\n' "$LAST_OUTPUT" | awk -F '|' '
      /^\|/ {
        host=$2; port=$3; status=$4; version=$7
        gsub(/^[[:space:]\"]+|[[:space:]\"]+$/, "", host)
        gsub(/^[[:space:]\"]+|[[:space:]\"]+$/, "", port)
        gsub(/^[[:space:]\"]+|[[:space:]\"]+$/, "", status)
        gsub(/^[[:space:]\"]+|[[:space:]\"]+$/, "", version)
        if (host == "nebula-storaged" && port == "9779" && status == "ONLINE" && version == "3.8.0") found=1
      }
      END { exit(found ? 0 : 1) }
    '; then
    break
  fi
  attempt=$((attempt + 1))
  sleep 1
done
[ "$attempt" -lt 90 ] || fail 'storage host did not become ONLINE on NebulaGraph v3.8.0'
[ -f "$schema_source" ] || fail "NebulaGraph schema file does not exist: $schema_source"

capture_query root "$admin_password" \
  "CREATE SPACE IF NOT EXISTS $space(partition_num=1, replica_factor=1, vid_type=FIXED_STRING(256))" ||
  fail 'could not create the AskData Space'

attempt=0
while [ "$attempt" -lt 90 ]; do
  if capture_query root "$admin_password" "USE $space; RETURN 1 AS ready"; then
    break
  fi
  attempt=$((attempt + 1))
  sleep 1
done
[ "$attempt" -lt 90 ] || fail 'AskData Space did not become queryable'

capture_query root "$admin_password" "SHOW CREATE SPACE $space" || fail 'could not inspect AskData Space'
printf '%s\n' "$LAST_OUTPUT" | grep -Eq 'partition_num[[:space:]]*=[[:space:]]*1([^0-9]|$)' ||
  fail 'AskData Space partition_num drifted from 1'
printf '%s\n' "$LAST_OUTPUT" | grep -Eq 'replica_factor[[:space:]]*=[[:space:]]*1([^0-9]|$)' ||
  fail 'AskData Space replica_factor drifted from 1'
printf '%s\n' "$LAST_OUTPUT" | grep -Eq 'FIXED_STRING\(256\)' ||
  fail 'AskData Space VID type drifted from FIXED_STRING(256)'

schema_file=$(mktemp /tmp/askdata-nebula-schema.XXXXXX)
trap 'rm -f -- "$schema_file"' EXIT INT TERM
{
  printf 'USE %s;\n' "$space"
  cat "$schema_source"
} >"$schema_file"
capture_file root "$admin_password" "$schema_file" || fail 'could not apply the AskData graph schema'

schema_signature() {
  object_kind=$1
  object_name=$2
  capture_query root "$admin_password" "USE $space; DESCRIBE $object_kind $object_name" || return 1
  printf '%s\n' "$LAST_OUTPUT" | awk -F '|' '
    /^\|/ {
      field=$2; type=$3; nullable=$4; default_value=$5; comment=$6
      gsub(/^[[:space:]\"]+|[[:space:]\"]+$/, "", field)
      gsub(/^[[:space:]\"]+|[[:space:]\"]+$/, "", type)
      gsub(/^[[:space:]\"]+|[[:space:]\"]+$/, "", nullable)
      gsub(/^[[:space:]\"]+|[[:space:]\"]+$/, "", default_value)
      gsub(/^[[:space:]\"]+|[[:space:]\"]+$/, "", comment)
      if (field != "" && field != "Field") print field ":" type ":" nullable ":" default_value ":" comment
    }
  '
}

assert_schema() {
  object_kind=$1
  object_name=$2
  expected=$3
  attempt=0
  while [ "$attempt" -lt "$schema_attempts" ]; do
    actual=$(schema_signature "$object_kind" "$object_name" || true)
    if [ "$actual" = "$expected" ]; then
      if capture_query root "$admin_password" "USE $space; SHOW CREATE $object_kind $object_name" &&
        printf '%s\n' "$LAST_OUTPUT" | grep -Eq 'ttl_duration[[:space:]]*=[[:space:]]*0,[[:space:]]*ttl_col[[:space:]]*=[[:space:]]*""'; then
        return
      fi
    fi
    attempt=$((attempt + 1))
    sleep 1
  done
  fail "$object_kind $object_name does not match the frozen GraphPlan schema"
}

common_vertex='tenant_id:string:YES::
domain_id:string:YES::
release_hash:string:YES::
object_id:string:YES::
version_id:string:YES::
version_no:int64:YES::'
assert_schema TAG semantic_model "$common_vertex"
assert_schema TAG metric "$common_vertex"
assert_schema TAG dimension "$common_vertex"
assert_schema TAG member "$common_vertex
member_status:string:YES::"
common_edge='tenant_id:string:YES::
domain_id:string:YES::
release_hash:string:YES::'
assert_schema EDGE MODELED_BY "$common_edge"
assert_schema EDGE HAS_DIMENSION "$common_edge"
assert_schema EDGE HAS_MEMBER "$common_edge"
assert_schema EDGE JOINS_TO "$common_edge
relationship_version_id:string:YES::
join_type:string:YES::
cardinality:string:YES::
fanout_policy:string:YES::
certified:bool:YES::"

capture_query root "$admin_password" \
  "CREATE USER IF NOT EXISTS $api_user WITH PASSWORD '$api_password'" ||
  fail 'could not create the API graph user'
capture_query root "$admin_password" \
  "ALTER USER $api_user WITH PASSWORD '$api_password'" ||
  fail 'could not set the API graph password'
capture_query root "$admin_password" \
  "CREATE USER IF NOT EXISTS $worker_user WITH PASSWORD '$worker_password'" ||
  fail 'could not create the Worker graph user'
capture_query root "$admin_password" \
  "ALTER USER $worker_user WITH PASSWORD '$worker_password'" ||
  fail 'could not set the Worker graph password'

ensure_role() {
  role_user=$1
  expected_role=$2
  attempt=0
  while [ "$attempt" -lt "$schema_attempts" ]; do
    if capture_query root "$admin_password" "SHOW ROLES IN $space"; then
      break
    fi
    attempt=$((attempt + 1))
    sleep 1
  done
  [ "$attempt" -lt "$schema_attempts" ] || fail 'could not inspect graph roles'
  actual_roles=$(printf '%s\n' "$LAST_OUTPUT" | awk -F '|' -v wanted="$role_user" '
    /^\|/ {
      account=$2; role=$3
      gsub(/^[[:space:]\"]+|[[:space:]\"]+$/, "", account)
      gsub(/^[[:space:]\"]+|[[:space:]\"]+$/, "", role)
      if (account == wanted) print role
    }
  ')
  if [ -z "$actual_roles" ]; then
    capture_query root "$admin_password" "GRANT ROLE $expected_role ON $space TO $role_user" ||
      fail "could not grant $expected_role to $role_user"
  elif [ "$actual_roles" != "$expected_role" ]; then
    fail "$role_user has an unexpected role in $space"
  fi
  attempt=0
  while [ "$attempt" -lt "$schema_attempts" ]; do
    if capture_query root "$admin_password" "SHOW ROLES IN $space"; then
      break
    fi
    attempt=$((attempt + 1))
    sleep 1
  done
  [ "$attempt" -lt "$schema_attempts" ] || fail 'could not verify graph roles'
  verified_roles=$(printf '%s\n' "$LAST_OUTPUT" | awk -F '|' -v wanted="$role_user" '
    /^\|/ {
      account=$2; role=$3
      gsub(/^[[:space:]\"]+|[[:space:]\"]+$/, "", account)
      gsub(/^[[:space:]\"]+|[[:space:]\"]+$/, "", role)
      if (account == wanted) print role
    }
  ')
  [ "$verified_roles" = "$expected_role" ] || fail "$role_user role verification failed"
}

ensure_role "$api_user" GUEST
ensure_role "$worker_user" USER

capture_query root "$admin_password" 'SHOW SPACES' || fail 'could not enumerate graph Spaces'
space_names=$(printf '%s\n' "$LAST_OUTPUT" | awk -F '|' '
  /^\|/ {
    name=$2
    gsub(/^[[:space:]\"]+|[[:space:]\"]+$/, "", name)
    if (name != "" && name != "Name") print name
  }
')
for granted_space in $space_names; do
  [ "$granted_space" = "$space" ] && continue
  capture_query root "$admin_password" "SHOW ROLES IN $granted_space" ||
    fail "could not inspect roles in $granted_space"
  if printf '%s\n' "$LAST_OUTPUT" | awk -F '|' -v api="$api_user" -v worker="$worker_user" '
    /^\|/ {
      account=$2
      gsub(/^[[:space:]\"]+|[[:space:]\"]+$/, "", account)
      if (account == api || account == worker) found=1
    }
    END { exit(found ? 0 : 1) }
  '; then
    fail 'API/Worker graph account has a role outside the configured Space'
  fi
done

attempt=0
while [ "$attempt" -lt 60 ]; do
  if capture_query "$api_user" "$api_password" "USE $space; RETURN 1 AS readable" &&
    capture_query "$worker_user" "$worker_password" "USE $space; RETURN 1 AS writable_scope"; then
    break
  fi
  attempt=$((attempt + 1))
  sleep 1
done
[ "$attempt" -lt 60 ] || fail 'API/Worker graph credentials did not converge'

printf 'nebula initialization completed\n'
