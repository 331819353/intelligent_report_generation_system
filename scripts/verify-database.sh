#!/usr/bin/env sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
# 与迁移脚本保持相同的环境文件选择规则。
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

# 使用应用账号运行事务化验证，覆盖 RLS、审计不可变性与策略隔离。
docker compose --env-file "$ENV_FILE" exec -T postgres \
  psql -v ON_ERROR_STOP=1 -U "${POSTGRES_APP_USER:-report_app}" -d "${POSTGRES_DB:-intelligent_report}" <<'SQL'
BEGIN;

DO $$
DECLARE
  tenant_a uuid;
  tenant_b uuid;
  user_a uuid;
  user_b uuid;
  role_a uuid;
  visible_count integer;
  cross_tenant_rejected boolean := false;
  audit_immutable boolean := false;
  row_policy_visible integer;
  invalid_subject_rejected boolean := false;
BEGIN
  -- 构造两个租户的最小数据集，验证跨租户访问始终被拒绝。
  INSERT INTO platform.tenants(code, name) VALUES ('verify-a', 'Verify Tenant A') RETURNING id INTO tenant_a;
  INSERT INTO platform.tenants(code, name) VALUES ('verify-b', 'Verify Tenant B') RETURNING id INTO tenant_b;

  PERFORM set_config('app.tenant_id', tenant_a::text, true);
  INSERT INTO platform.users(tenant_id, email, display_name, password_hash)
  VALUES (tenant_a, 'a@example.test', 'User A', 'test-hash') RETURNING id INTO user_a;
  INSERT INTO platform.roles(tenant_id, code, name)
  VALUES (tenant_a, 'viewer', 'Viewer') RETURNING id INTO role_a;

  PERFORM set_config('app.tenant_id', tenant_b::text, true);
  INSERT INTO platform.users(tenant_id, email, display_name, password_hash)
  VALUES (tenant_b, 'b@example.test', 'User B', 'test-hash') RETURNING id INTO user_b;

  PERFORM set_config('app.tenant_id', tenant_a::text, true);
  SELECT count(*) INTO visible_count FROM platform.users;
  IF visible_count <> 1 THEN
    RAISE EXCEPTION 'tenant isolation failed: expected 1 visible user, got %', visible_count;
  END IF;

  BEGIN
    INSERT INTO platform.user_roles(tenant_id, user_id, role_id, assigned_by)
    VALUES (tenant_a, user_b, role_a, user_a);
  EXCEPTION WHEN foreign_key_violation THEN
    cross_tenant_rejected := true;
  END;
  IF NOT cross_tenant_rejected THEN
    RAISE EXCEPTION 'cross-tenant user-role assignment was accepted';
  END IF;

  INSERT INTO platform.audit_logs(tenant_id, actor_user_id, action, resource_type, resource_id)
  VALUES (tenant_a, user_a, 'VERIFY', 'SYSTEM', 'database-verification');
  BEGIN
    UPDATE platform.audit_logs SET result = 'FAILURE' WHERE resource_id = 'database-verification';
  EXCEPTION WHEN raise_exception THEN
    audit_immutable := true;
  END;
  IF NOT audit_immutable THEN
    RAISE EXCEPTION 'audit log update was accepted';
  END IF;

  INSERT INTO platform.data_row_policies(tenant_id, object_type, object_id, name, expression_dsl)
  VALUES (tenant_a, 'DATASET', gen_random_uuid(), 'region scope', '{"type":"EQUALS","left":{"type":"FIELD_REF","fieldCode":"region_code"},"right":{"type":"USER_ATTRIBUTE_REF","attribute":"region_code"}}');
  SELECT count(*) INTO row_policy_visible FROM platform.data_row_policies;
  IF row_policy_visible <> 1 THEN
    RAISE EXCEPTION 'row policy tenant visibility failed';
  END IF;

  PERFORM set_config('app.tenant_id', tenant_b::text, true);
  SELECT count(*) INTO row_policy_visible FROM platform.data_row_policies;
  IF row_policy_visible <> 0 THEN
    RAISE EXCEPTION 'row policy leaked across tenants';
  END IF;

  BEGIN
    INSERT INTO platform.object_permissions(tenant_id, subject_type, subject_id, object_type, object_id, action)
    VALUES (tenant_b, 'USER', user_a, 'REPORT', gen_random_uuid(), 'READ');
  EXCEPTION WHEN foreign_key_violation THEN
    invalid_subject_rejected := true;
  END;
  IF NOT invalid_subject_rejected THEN
    RAISE EXCEPTION 'cross-tenant object permission subject was accepted';
  END IF;

  PERFORM set_config('app.tenant_id', '', true);
  SELECT count(*) INTO visible_count FROM platform.users;
  IF visible_count <> 0 THEN
    RAISE EXCEPTION 'RLS without tenant context exposed % users', visible_count;
  END IF;
END
$$;

ROLLBACK;
SELECT 'database verification passed' AS result;
SQL
