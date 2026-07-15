-- 建立多租户身份、角色、权限、审计以及行级安全基础模型。
CREATE TYPE platform.tenant_status AS ENUM ('ACTIVE', 'SUSPENDED', 'DELETED');
CREATE TYPE platform.user_status AS ENUM ('ACTIVE', 'DISABLED', 'LOCKED');
CREATE TYPE platform.role_status AS ENUM ('ACTIVE', 'DISABLED');

CREATE TABLE platform.tenants (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  code citext NOT NULL UNIQUE,
  name text NOT NULL,
  status platform.tenant_status NOT NULL DEFAULT 'ACTIVE',
  settings jsonb NOT NULL DEFAULT '{}'::jsonb,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  deleted_at timestamptz,
  CONSTRAINT tenants_code_not_blank CHECK (btrim(code::text) <> ''),
  CONSTRAINT tenants_name_not_blank CHECK (btrim(name) <> '')
);

CREATE TABLE platform.users (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  email citext NOT NULL,
  display_name text NOT NULL,
  password_hash text NOT NULL,
  status platform.user_status NOT NULL DEFAULT 'ACTIVE',
  token_version bigint NOT NULL DEFAULT 1 CHECK (token_version > 0),
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  last_login_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  deleted_at timestamptz,
  CONSTRAINT users_email_not_blank CHECK (btrim(email::text) <> ''),
  CONSTRAINT users_display_name_not_blank CHECK (btrim(display_name) <> ''),
  CONSTRAINT users_password_hash_not_blank CHECK (btrim(password_hash) <> ''),
  UNIQUE (tenant_id, email),
  UNIQUE (id, tenant_id)
);

CREATE TABLE platform.roles (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  code citext NOT NULL,
  name text NOT NULL,
  description text NOT NULL DEFAULT '',
  is_system boolean NOT NULL DEFAULT false,
  status platform.role_status NOT NULL DEFAULT 'ACTIVE',
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  deleted_at timestamptz,
  CONSTRAINT roles_code_not_blank CHECK (btrim(code::text) <> ''),
  CONSTRAINT roles_name_not_blank CHECK (btrim(name) <> ''),
  UNIQUE (tenant_id, code),
  UNIQUE (id, tenant_id)
);

CREATE TABLE platform.permissions (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  code citext NOT NULL,
  name text NOT NULL,
  resource_type text NOT NULL,
  action text NOT NULL,
  description text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT permissions_code_not_blank CHECK (btrim(code::text) <> ''),
  CONSTRAINT permissions_resource_not_blank CHECK (btrim(resource_type) <> ''),
  CONSTRAINT permissions_action_not_blank CHECK (btrim(action) <> ''),
  UNIQUE (tenant_id, code),
  UNIQUE (tenant_id, resource_type, action),
  UNIQUE (id, tenant_id)
);

CREATE TABLE platform.user_roles (
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  user_id uuid NOT NULL,
  role_id uuid NOT NULL,
  assigned_by uuid,
  assigned_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, user_id, role_id),
  FOREIGN KEY (user_id, tenant_id) REFERENCES platform.users(id, tenant_id) ON DELETE CASCADE,
  FOREIGN KEY (role_id, tenant_id) REFERENCES platform.roles(id, tenant_id) ON DELETE CASCADE,
  FOREIGN KEY (assigned_by, tenant_id) REFERENCES platform.users(id, tenant_id) ON DELETE SET NULL (assigned_by)
);

CREATE TABLE platform.role_permissions (
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  role_id uuid NOT NULL,
  permission_id uuid NOT NULL,
  granted_by uuid,
  granted_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, role_id, permission_id),
  FOREIGN KEY (role_id, tenant_id) REFERENCES platform.roles(id, tenant_id) ON DELETE CASCADE,
  FOREIGN KEY (permission_id, tenant_id) REFERENCES platform.permissions(id, tenant_id) ON DELETE CASCADE,
  FOREIGN KEY (granted_by, tenant_id) REFERENCES platform.users(id, tenant_id) ON DELETE SET NULL (granted_by)
);

CREATE TABLE platform.audit_logs (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  actor_user_id uuid,
  action text NOT NULL,
  resource_type text NOT NULL,
  resource_id text,
  request_id text,
  ip_address inet,
  user_agent text,
  result text NOT NULL DEFAULT 'SUCCESS',
  detail jsonb NOT NULL DEFAULT '{}'::jsonb,
  occurred_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT audit_action_not_blank CHECK (btrim(action) <> ''),
  CONSTRAINT audit_resource_not_blank CHECK (btrim(resource_type) <> ''),
  CONSTRAINT audit_result_valid CHECK (result IN ('SUCCESS', 'FAILURE', 'DENIED')),
  FOREIGN KEY (actor_user_id, tenant_id) REFERENCES platform.users(id, tenant_id) ON DELETE SET NULL (actor_user_id)
);

CREATE INDEX users_tenant_status_idx ON platform.users (tenant_id, status) WHERE deleted_at IS NULL;
CREATE INDEX roles_tenant_status_idx ON platform.roles (tenant_id, status) WHERE deleted_at IS NULL;
CREATE INDEX permissions_tenant_resource_idx ON platform.permissions (tenant_id, resource_type);
CREATE INDEX user_roles_user_idx ON platform.user_roles (tenant_id, user_id);
CREATE INDEX user_roles_role_idx ON platform.user_roles (tenant_id, role_id);
CREATE INDEX role_permissions_role_idx ON platform.role_permissions (tenant_id, role_id);
CREATE INDEX audit_logs_tenant_time_idx ON platform.audit_logs (tenant_id, occurred_at DESC);
CREATE INDEX audit_logs_resource_idx ON platform.audit_logs (tenant_id, resource_type, resource_id);
CREATE INDEX audit_logs_request_idx ON platform.audit_logs (request_id) WHERE request_id IS NOT NULL;

CREATE OR REPLACE FUNCTION platform.current_tenant_id()
RETURNS uuid
LANGUAGE sql
STABLE
AS $$
  SELECT NULLIF(current_setting('app.tenant_id', true), '')::uuid
$$;

CREATE OR REPLACE FUNCTION platform.set_updated_at()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  NEW.updated_at = now();
  RETURN NEW;
END
$$;

CREATE TRIGGER tenants_set_updated_at BEFORE UPDATE ON platform.tenants
FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();
CREATE TRIGGER users_set_updated_at BEFORE UPDATE ON platform.users
FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();
CREATE TRIGGER roles_set_updated_at BEFORE UPDATE ON platform.roles
FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();
CREATE TRIGGER permissions_set_updated_at BEFORE UPDATE ON platform.permissions
FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();

CREATE OR REPLACE FUNCTION platform.reject_audit_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  RAISE EXCEPTION 'audit logs are immutable';
END
$$;

CREATE TRIGGER audit_logs_immutable
BEFORE UPDATE OR DELETE ON platform.audit_logs
FOR EACH ROW EXECUTE FUNCTION platform.reject_audit_mutation();

ALTER TABLE platform.users ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.users FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.roles ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.roles FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.permissions ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.permissions FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.user_roles ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.user_roles FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.role_permissions ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.role_permissions FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.audit_logs ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.audit_logs FORCE ROW LEVEL SECURITY;

CREATE POLICY users_tenant_isolation ON platform.users
  USING (tenant_id = platform.current_tenant_id())
  WITH CHECK (tenant_id = platform.current_tenant_id());
CREATE POLICY roles_tenant_isolation ON platform.roles
  USING (tenant_id = platform.current_tenant_id())
  WITH CHECK (tenant_id = platform.current_tenant_id());
CREATE POLICY permissions_tenant_isolation ON platform.permissions
  USING (tenant_id = platform.current_tenant_id())
  WITH CHECK (tenant_id = platform.current_tenant_id());
CREATE POLICY user_roles_tenant_isolation ON platform.user_roles
  USING (tenant_id = platform.current_tenant_id())
  WITH CHECK (tenant_id = platform.current_tenant_id());
CREATE POLICY role_permissions_tenant_isolation ON platform.role_permissions
  USING (tenant_id = platform.current_tenant_id())
  WITH CHECK (tenant_id = platform.current_tenant_id());
CREATE POLICY audit_logs_tenant_isolation ON platform.audit_logs
  USING (tenant_id = platform.current_tenant_id())
  WITH CHECK (tenant_id = platform.current_tenant_id());

COMMENT ON FUNCTION platform.current_tenant_id() IS 'Returns the tenant UUID set by the API transaction using SET LOCAL app.tenant_id';
COMMENT ON TABLE platform.audit_logs IS 'Append-only tenant audit trail; updates and deletes are rejected';
