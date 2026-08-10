DROP TRIGGER IF EXISTS report_share_lifecycle_guard ON platform.report_shares;
DROP FUNCTION IF EXISTS platform.guard_report_share_lifecycle();
DROP FUNCTION IF EXISTS platform.report_share_principal_valid(uuid,text,uuid);

DROP POLICY IF EXISTS report_shares_update ON platform.report_shares;
DROP POLICY IF EXISTS report_shares_create ON platform.report_shares;
DROP POLICY IF EXISTS report_shares_read ON platform.report_shares;
CREATE POLICY report_shares_actor_policy ON platform.report_shares
  USING(tenant_id=platform.current_tenant_id()
    AND platform.report_v2_can_access(report_id,ARRAY['VIEW','EDIT','PUBLISH'])
    AND (
      platform.is_system_access() OR created_by=platform.current_user_id() OR
      (share_type='INTERNAL_USER' AND principal_id=platform.current_user_id()) OR
      (share_type='INTERNAL_GROUP' AND EXISTS(
        SELECT 1 FROM platform.user_roles assignment
        JOIN platform.roles role ON role.id=assignment.role_id
          AND role.tenant_id=assignment.tenant_id
        WHERE assignment.tenant_id=report_shares.tenant_id
          AND assignment.user_id=platform.current_user_id()
          AND assignment.role_id=report_shares.principal_id
          AND role.status='ACTIVE' AND role.deleted_at IS NULL
      ))
    ))
  WITH CHECK(tenant_id=platform.current_tenant_id()
    AND (platform.is_system_access() OR created_by=platform.current_user_id())
    AND platform.report_v2_can_access(report_id,ARRAY['EDIT','PUBLISH']));

DROP INDEX IF EXISTS platform.report_shares_expiry_idx;
CREATE INDEX report_shares_expiry_idx
  ON platform.report_shares(tenant_id,expires_at) WHERE revoked_at IS NULL;

ALTER TABLE platform.report_shares
  DROP CONSTRAINT IF EXISTS report_shares_last_access_shape_check,
  DROP CONSTRAINT IF EXISTS report_shares_expired_shape_check,
  DROP COLUMN IF EXISTS expired_at;
