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
  report_revision_immutable boolean := false;
  report_revision_constraint boolean := false;
  row_policy_visible integer;
  report_visible integer;
  report_a uuid;
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

  -- 报告草稿、语义修订和派生索引必须共享租户边界，修订写入后不可变。
  INSERT INTO platform.reports(tenant_id,code,name,report_type,created_by,updated_by)
  VALUES (tenant_a,'verify_report','Verify Report','REPORT',user_a,user_a) RETURNING id INTO report_a;
  INSERT INTO platform.report_drafts(report_id,tenant_id,schema_version,definition_json,definition_hash,revision_no,editor_state_json,updated_by)
  VALUES (report_a,tenant_a,'1.0',jsonb_build_object('schemaVersion','1.0','report',jsonb_build_object('id',report_a::text)),repeat('a',64),1,'{"minimumRowsByPage":{}}',user_a);
  INSERT INTO platform.report_revisions(tenant_id,report_id,base_revision_no,revision_no,idempotency_key,request_hash,change_index,change_count,operation_type,source,target_json,patch_json,patch_count,patch_hash,before_hash,after_hash,actor_user_id)
  VALUES (tenant_a,report_a,0,1,'verify-create',repeat('b',64),1,1,'REPORT_CREATE','USER','{}','[{"op":"add","path":"","value":{}}]',1,repeat('c',64),repeat('0',64),repeat('a',64),user_a);
  INSERT INTO platform.report_draft_component_indexes(tenant_id,report_id,revision_no,page_id,block_id,component_id,component_type)
  VALUES (tenant_a,report_a,1,'page_verify','block_verify','component_verify','TITLE');
  BEGIN
    UPDATE platform.report_revisions SET operation_type='BLOCK_MOVE' WHERE report_id=report_a;
  EXCEPTION WHEN raise_exception THEN
    report_revision_immutable := true;
  END;
  IF NOT report_revision_immutable THEN
    RAISE EXCEPTION 'report revision update was accepted';
  END IF;
  BEGIN
    INSERT INTO platform.report_revisions(tenant_id,report_id,base_revision_no,revision_no,idempotency_key,request_hash,change_index,change_count,client_operation_id,operation_type,source,target_json,patch_json,patch_count,patch_hash,before_hash,after_hash,actor_user_id)
    VALUES (tenant_a,report_a,1,3,'verify-invalid-step',repeat('d',64),1,1,gen_random_uuid(),'BLOCK_MOVE','USER','{"pageId":"page_verify","blockId":"block_verify"}','[{"op":"replace","path":"/pages/0/blocks/0/grid/x","value":1}]',1,repeat('e',64),repeat('a',64),repeat('f',64),user_a);
  EXCEPTION WHEN check_violation THEN
    report_revision_constraint := true;
  END;
  IF NOT report_revision_constraint THEN
    RAISE EXCEPTION 'invalid report revision sequence was accepted';
  END IF;

  INSERT INTO platform.data_row_policies(tenant_id, object_type, object_id, name, expression_dsl)
  VALUES (tenant_a, 'DATASET', gen_random_uuid(), 'region scope', '{"type":"EQUALS","left":{"type":"FIELD_REF","fieldCode":"region_code"},"right":{"type":"USER_ATTRIBUTE_REF","attribute":"region_code"}}');
  SELECT count(*) INTO row_policy_visible FROM platform.data_row_policies;
  IF row_policy_visible <> 1 THEN
    RAISE EXCEPTION 'row policy tenant visibility failed';
  END IF;

  PERFORM set_config('app.tenant_id', tenant_b::text, true);
  SELECT count(*) INTO report_visible FROM platform.reports;
  IF report_visible <> 0 THEN
    RAISE EXCEPTION 'report draft leaked across tenants';
  END IF;
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
