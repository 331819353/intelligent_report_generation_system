-- 报告草稿以完整规范 JSON 为当前事实来源，以不可变 Patch 修订提供审计和历史回放。
CREATE TABLE platform.reports(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  code citext NOT NULL,
  name text NOT NULL CHECK(btrim(name)<>''),
  description text NOT NULL DEFAULT '',
  report_type text NOT NULL CHECK(report_type IN ('DASHBOARD','REPORT')),
  status text NOT NULL DEFAULT 'DRAFT' CHECK(status IN ('DRAFT','PUBLISHED','ARCHIVED')),
  version bigint NOT NULL DEFAULT 1 CHECK(version>0),
  created_by uuid,
  updated_by uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  deleted_at timestamptz,
  FOREIGN KEY(created_by,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE SET NULL (created_by),
  FOREIGN KEY(updated_by,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE SET NULL (updated_by),
  UNIQUE(tenant_id,code),
  UNIQUE(id,tenant_id)
);

CREATE TABLE platform.report_drafts(
  report_id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  schema_version text NOT NULL CHECK(schema_version='1.0'),
  definition_json jsonb NOT NULL CHECK(jsonb_typeof(definition_json)='object'),
  definition_hash text NOT NULL CHECK(length(definition_hash)=64),
  revision_no bigint NOT NULL DEFAULT 1 CHECK(revision_no>0),
  editor_state_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK(jsonb_typeof(editor_state_json)='object'),
  updated_by uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY(report_id,tenant_id) REFERENCES platform.reports(id,tenant_id) ON DELETE CASCADE,
  FOREIGN KEY(updated_by,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE SET NULL (updated_by),
  UNIQUE(report_id,tenant_id)
);

CREATE TABLE platform.report_revisions(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  report_id uuid NOT NULL,
  base_revision_no bigint NOT NULL CHECK(base_revision_no>=0),
  revision_no bigint NOT NULL CHECK(revision_no>0),
  idempotency_key text NOT NULL CHECK(length(idempotency_key) BETWEEN 1 AND 128),
  request_hash text NOT NULL CHECK(length(request_hash)=64),
  change_index integer NOT NULL CHECK(change_index>0),
  change_count integer NOT NULL CHECK(change_count BETWEEN 1 AND 100),
  client_operation_id uuid,
  operation_type text NOT NULL CHECK(operation_type IN ('REPORT_CREATE','BLOCK_MOVE','BLOCK_RESIZE','BLOCK_CREATE','BLOCK_CLEAR','BLOCK_DELETE','BLOCK_STICKY_UPDATE','COMPONENT_MOVE','COMPONENT_RESIZE','COMPONENT_CREATE','COMPONENT_COPY','COMPONENT_DELETE','COMPONENT_STICKY_UPDATE','UNDO','REDO')),
  source text NOT NULL DEFAULT 'USER' CHECK(source IN ('USER','AI','IMPORT','SYSTEM')),
  target_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK(jsonb_typeof(target_json)='object'),
  patch_json jsonb NOT NULL CHECK(jsonb_typeof(patch_json)='array'),
  patch_count integer NOT NULL CHECK(patch_count BETWEEN 1 AND 100),
  patch_hash text NOT NULL CHECK(length(patch_hash)=64),
  before_hash text NOT NULL CHECK(length(before_hash)=64),
  after_hash text NOT NULL CHECK(length(after_hash)=64),
  actor_user_id uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY(report_id,tenant_id) REFERENCES platform.reports(id,tenant_id) ON DELETE CASCADE,
  FOREIGN KEY(actor_user_id,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE SET NULL (actor_user_id),
  UNIQUE(tenant_id,report_id,revision_no),
  UNIQUE(tenant_id,report_id,idempotency_key,change_index),
  UNIQUE(tenant_id,report_id,client_operation_id)
);

-- 幂等记录保存原始响应快照，后续草稿继续变化时仍能精确重放首次结果。
CREATE TABLE platform.report_idempotency_records(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  scope text NOT NULL CHECK(scope IN ('CREATE','UPDATE')),
  report_id uuid NOT NULL,
  idempotency_key text NOT NULL CHECK(length(idempotency_key) BETWEEN 1 AND 128),
  request_hash text NOT NULL CHECK(length(request_hash)=64),
  http_status integer NOT NULL CHECK(http_status IN (200,201)),
  response_json jsonb NOT NULL CHECK(jsonb_typeof(response_json)='object'),
  created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY(report_id,tenant_id) REFERENCES platform.reports(id,tenant_id) ON DELETE CASCADE
);

-- 以下索引表均可由 definition_json 重建，不能作为第二份报告事实来源。
CREATE TABLE platform.report_draft_component_indexes(
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  report_id uuid NOT NULL,
  revision_no bigint NOT NULL CHECK(revision_no>0),
  page_id text NOT NULL,
  block_id text NOT NULL,
  component_id text NOT NULL,
  component_type text NOT NULL,
  PRIMARY KEY(tenant_id,report_id,component_id),
  FOREIGN KEY(report_id,tenant_id) REFERENCES platform.reports(id,tenant_id) ON DELETE CASCADE
);

CREATE TABLE platform.report_draft_dependencies(
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  report_id uuid NOT NULL,
  revision_no bigint NOT NULL CHECK(revision_no>0),
  dependency_type text NOT NULL CHECK(dependency_type IN ('DATASET_VERSION','METRIC','SOURCE_TRACE')),
  dependency_id text NOT NULL CHECK(btrim(dependency_id)<>''),
  json_path text NOT NULL CHECK(btrim(json_path)<>''),
  PRIMARY KEY(tenant_id,report_id,dependency_type,dependency_id,json_path),
  FOREIGN KEY(report_id,tenant_id) REFERENCES platform.reports(id,tenant_id) ON DELETE CASCADE
);

-- T0409 只消费编辑占用；发布任务在 T0601 中负责创建、续租、释放和过期回收。
CREATE TABLE platform.report_edit_guards(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  report_id uuid NOT NULL,
  block_id text,
  holder_type text NOT NULL CHECK(holder_type IN ('PUBLISH_TASK','EXPORT_TASK','EDIT_LEASE')),
  holder_id text NOT NULL CHECK(btrim(holder_id)<>''),
  expires_at timestamptz NOT NULL,
  released_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY(report_id,tenant_id) REFERENCES platform.reports(id,tenant_id) ON DELETE CASCADE
);

CREATE INDEX reports_tenant_status_idx ON platform.reports(tenant_id,status,updated_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX report_revisions_report_time_idx ON platform.report_revisions(tenant_id,report_id,revision_no DESC);
CREATE INDEX report_revisions_request_idx ON platform.report_revisions(tenant_id,report_id,idempotency_key);
CREATE UNIQUE INDEX report_idempotency_create_idx ON platform.report_idempotency_records(tenant_id,idempotency_key) WHERE scope='CREATE';
CREATE UNIQUE INDEX report_idempotency_update_idx ON platform.report_idempotency_records(tenant_id,report_id,idempotency_key) WHERE scope='UPDATE';
CREATE INDEX report_draft_dependencies_source_idx ON platform.report_draft_dependencies(tenant_id,dependency_type,dependency_id);
CREATE INDEX report_edit_guards_active_idx ON platform.report_edit_guards(tenant_id,report_id,expires_at) WHERE released_at IS NULL;

CREATE TRIGGER reports_set_updated_at BEFORE UPDATE ON platform.reports FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();
CREATE TRIGGER report_drafts_set_updated_at BEFORE UPDATE ON platform.report_drafts FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();

CREATE OR REPLACE FUNCTION platform.reject_report_revision_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  RAISE EXCEPTION 'report revisions are immutable';
END
$$;

CREATE TRIGGER report_revisions_immutable
BEFORE UPDATE OR DELETE ON platform.report_revisions
FOR EACH ROW EXECUTE FUNCTION platform.reject_report_revision_mutation();

ALTER TABLE platform.reports ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.reports FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.report_drafts ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.report_drafts FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.report_revisions ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.report_revisions FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.report_idempotency_records ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.report_idempotency_records FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.report_draft_component_indexes ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.report_draft_component_indexes FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.report_draft_dependencies ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.report_draft_dependencies FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.report_edit_guards ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.report_edit_guards FORCE ROW LEVEL SECURITY;

CREATE POLICY reports_tenant_isolation ON platform.reports USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY report_drafts_tenant_isolation ON platform.report_drafts USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY report_revisions_tenant_isolation ON platform.report_revisions USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY report_idempotency_records_tenant_isolation ON platform.report_idempotency_records USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY report_draft_component_indexes_tenant_isolation ON platform.report_draft_component_indexes USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY report_draft_dependencies_tenant_isolation ON platform.report_draft_dependencies USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY report_edit_guards_tenant_isolation ON platform.report_edit_guards USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());

COMMENT ON TABLE platform.report_drafts IS '报告当前完整规范草稿及独立编辑器状态';
COMMENT ON TABLE platform.report_revisions IS '每个服务端已验证 Patch 操作对应一条不可变报告修订';
COMMENT ON TABLE platform.report_idempotency_records IS '报告创建与保存请求的精确幂等响应快照';
COMMENT ON TABLE platform.report_draft_component_indexes IS '由报告草稿 JSON 可重建的组件索引';
COMMENT ON TABLE platform.report_draft_dependencies IS '由报告草稿 JSON 可重建的依赖索引';
COMMENT ON TABLE platform.report_edit_guards IS '发布、导出或编辑租约对报告及分块的临时占用合同';
