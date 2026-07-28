-- 建立租户内可切换的业务领域目录；权限仍由既有 RBAC 模型统一管理。
CREATE TABLE platform.business_domains (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  code citext NOT NULL,
  name text NOT NULL,
  description text NOT NULL DEFAULT '',
  status platform.role_status NOT NULL DEFAULT 'ACTIVE',
  is_default boolean NOT NULL DEFAULT false,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_by uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  deleted_at timestamptz,
  CONSTRAINT business_domains_code_not_blank CHECK (btrim(code::text) <> ''),
  CONSTRAINT business_domains_code_format CHECK (code::text ~ '^[a-z][a-z0-9_-]{1,31}$'),
  CONSTRAINT business_domains_name_not_blank CHECK (btrim(name) <> ''),
  UNIQUE (tenant_id, code),
  UNIQUE (id, tenant_id),
  FOREIGN KEY (created_by, tenant_id)
    REFERENCES platform.users(id, tenant_id) ON DELETE SET NULL (created_by)
);

CREATE UNIQUE INDEX business_domains_one_default_idx
  ON platform.business_domains (tenant_id)
  WHERE is_default AND deleted_at IS NULL;
CREATE INDEX business_domains_tenant_status_idx
  ON platform.business_domains (tenant_id, status, name)
  WHERE deleted_at IS NULL;

CREATE TRIGGER business_domains_set_updated_at
BEFORE UPDATE ON platform.business_domains
FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();

ALTER TABLE platform.business_domains ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.business_domains FORCE ROW LEVEL SECURITY;
CREATE POLICY business_domains_tenant_isolation ON platform.business_domains
  USING (tenant_id = platform.current_tenant_id())
  WITH CHECK (tenant_id = platform.current_tenant_id());

-- 为已有租户补齐一个稳定默认领域，确保升级后侧栏立即可切换。
INSERT INTO platform.business_domains(tenant_id, code, name, description, is_default)
SELECT id, 'enterprise', '企业经营', '企业级经营分析与管理驾驶舱', true
FROM platform.tenants
WHERE status = 'ACTIVE' AND deleted_at IS NULL
ON CONFLICT (tenant_id, code) DO NOTHING;

COMMENT ON TABLE platform.business_domains IS
  'Tenant-scoped business domain catalog used by the workspace domain switcher';
