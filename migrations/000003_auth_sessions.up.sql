-- 保存可轮换刷新令牌的摘要，并通过 RLS 隔离租户会话。
CREATE TABLE platform.auth_sessions (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  user_id uuid NOT NULL,
  refresh_token_hash bytea NOT NULL,
  user_agent text,
  ip_address inet,
  expires_at timestamptz NOT NULL,
  last_used_at timestamptz NOT NULL DEFAULT now(),
  revoked_at timestamptz,
  revoke_reason text,
  created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (user_id, tenant_id) REFERENCES platform.users(id, tenant_id) ON DELETE CASCADE,
  CONSTRAINT auth_sessions_expiry_valid CHECK (expires_at > created_at)
);

CREATE INDEX auth_sessions_user_active_idx
  ON platform.auth_sessions (tenant_id, user_id, expires_at DESC)
  WHERE revoked_at IS NULL;
CREATE INDEX auth_sessions_expiry_idx
  ON platform.auth_sessions (expires_at)
  WHERE revoked_at IS NULL;

ALTER TABLE platform.auth_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.auth_sessions FORCE ROW LEVEL SECURITY;
CREATE POLICY auth_sessions_tenant_isolation ON platform.auth_sessions
  USING (tenant_id = platform.current_tenant_id())
  WITH CHECK (tenant_id = platform.current_tenant_id());

COMMENT ON TABLE platform.auth_sessions IS 'Rotating refresh-token sessions; only token hashes are stored';
