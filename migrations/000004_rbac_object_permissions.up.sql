-- 扩展 RBAC，支持用户或角色对具体业务对象的动作授权。
CREATE TYPE platform.permission_subject_type AS ENUM ('USER', 'ROLE');

CREATE TABLE platform.object_permissions (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  subject_type platform.permission_subject_type NOT NULL,
  subject_id uuid NOT NULL,
  object_type text NOT NULL,
  object_id uuid NOT NULL,
  action text NOT NULL,
  granted_by uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT object_permissions_object_type_not_blank CHECK (btrim(object_type) <> ''),
  CONSTRAINT object_permissions_action_not_blank CHECK (btrim(action) <> ''),
  FOREIGN KEY (granted_by, tenant_id) REFERENCES platform.users(id, tenant_id) ON DELETE SET NULL (granted_by),
  UNIQUE (tenant_id, subject_type, subject_id, object_type, object_id, action)
);

CREATE INDEX object_permissions_lookup_idx ON platform.object_permissions
  (tenant_id, object_type, object_id, action, subject_type, subject_id);

ALTER TABLE platform.object_permissions ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.object_permissions FORCE ROW LEVEL SECURITY;
CREATE POLICY object_permissions_tenant_isolation ON platform.object_permissions
  USING (tenant_id = platform.current_tenant_id())
  WITH CHECK (tenant_id = platform.current_tenant_id());

COMMENT ON TABLE platform.object_permissions IS 'Tenant-scoped grants for USER or ROLE subjects; subject tenant membership is validated by API transactions';
