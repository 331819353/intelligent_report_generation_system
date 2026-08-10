CREATE TABLE platform.report_shares(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL,
  report_id uuid NOT NULL, report_version_id uuid,
  share_type text NOT NULL CHECK(share_type IN ('INTERNAL_USER','INTERNAL_GROUP','EXTERNAL_ACCOUNT')),
  principal_id uuid NOT NULL,
  share_token_hash text NOT NULL CHECK(share_token_hash ~ '^[0-9a-f]{64}$'),
  filter_snapshot_json jsonb CHECK(filter_snapshot_json IS NULL OR jsonb_typeof(filter_snapshot_json)='object'),
  created_by uuid NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL, revoked_at timestamptz,
  access_count bigint NOT NULL DEFAULT 0 CHECK(access_count>=0), last_accessed_at timestamptz,
  UNIQUE(share_token_hash),
  FOREIGN KEY(report_id,tenant_id) REFERENCES platform.reports(id,tenant_id) ON DELETE CASCADE,
  FOREIGN KEY(report_version_id,report_id,tenant_id) REFERENCES platform.report_versions(id,report_id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(created_by,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CHECK(expires_at>created_at AND expires_at<=created_at+interval '180 days'),
  CHECK(revoked_at IS NULL OR revoked_at>=created_at)
);
CREATE INDEX report_shares_expiry_idx ON platform.report_shares(tenant_id,expires_at) WHERE revoked_at IS NULL;

ALTER TABLE platform.report_shares ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.report_shares FORCE ROW LEVEL SECURITY;
CREATE POLICY report_shares_actor_policy ON platform.report_shares
  USING(tenant_id=platform.current_tenant_id() AND (
    platform.is_system_access() OR created_by=platform.current_user_id() OR
    (share_type='INTERNAL_USER' AND principal_id=platform.current_user_id()) OR
    (share_type='INTERNAL_GROUP' AND EXISTS(
      SELECT 1 FROM platform.user_roles AS assignment
      JOIN platform.roles AS role
        ON role.id=assignment.role_id AND role.tenant_id=assignment.tenant_id
      WHERE assignment.tenant_id=report_shares.tenant_id
        AND assignment.user_id=platform.current_user_id()
        AND assignment.role_id=report_shares.principal_id
        AND role.status='ACTIVE' AND role.deleted_at IS NULL
    ))
  ))
  WITH CHECK(tenant_id=platform.current_tenant_id() AND (
    platform.is_system_access() OR created_by=platform.current_user_id()
  ));
