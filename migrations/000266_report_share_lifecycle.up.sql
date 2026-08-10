-- Forward-repair Report V2 share lifecycle after the initial 000238/000261
-- deployment. A token only locates a row; RLS and report object permission
-- still authorize every read and mutation.
ALTER TABLE platform.report_shares
  ADD COLUMN expired_at timestamptz,
  ADD CONSTRAINT report_shares_expired_shape_check CHECK(
    expired_at IS NULL OR expired_at>=expires_at
  ),
  ADD CONSTRAINT report_shares_last_access_shape_check CHECK(
    last_accessed_at IS NULL OR last_accessed_at>=created_at
  );

DROP INDEX IF EXISTS platform.report_shares_expiry_idx;
CREATE INDEX report_shares_expiry_idx
  ON platform.report_shares(tenant_id,expires_at,id)
  WHERE revoked_at IS NULL AND expired_at IS NULL;

CREATE OR REPLACE FUNCTION platform.report_share_principal_valid(
  target_tenant_id uuid,
  target_share_type text,
  target_principal_id uuid
)
RETURNS boolean
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
  SELECT CASE target_share_type
    WHEN 'INTERNAL_USER' THEN EXISTS(
      SELECT 1 FROM platform.users AS target
      WHERE target.id=target_principal_id AND target.tenant_id=target_tenant_id
        AND target.status='ACTIVE' AND target.deleted_at IS NULL
    )
    WHEN 'INTERNAL_GROUP' THEN EXISTS(
      SELECT 1 FROM platform.roles AS target
      WHERE target.id=target_principal_id AND target.tenant_id=target_tenant_id
        AND target.status='ACTIVE' AND target.deleted_at IS NULL
    )
    WHEN 'EXTERNAL_ACCOUNT' THEN true
    ELSE false
  END
$$;

CREATE OR REPLACE FUNCTION platform.guard_report_share_lifecycle()
RETURNS trigger
LANGUAGE plpgsql
SET search_path=pg_catalog,platform
AS $$
BEGIN
  IF TG_OP='INSERT' THEN
    IF NEW.access_count<>0 OR NEW.last_accessed_at IS NOT NULL OR
       NEW.revoked_at IS NOT NULL OR NEW.expired_at IS NOT NULL THEN
      RAISE EXCEPTION 'new report share must start active and unused' USING ERRCODE='23514';
    END IF;
    IF NOT platform.report_share_principal_valid(
      NEW.tenant_id,NEW.share_type,NEW.principal_id
    ) THEN
      RAISE EXCEPTION 'report share principal is invalid' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
  END IF;

  IF ROW(OLD.id,OLD.tenant_id,OLD.report_id,OLD.report_version_id,
         OLD.share_type,OLD.principal_id,OLD.share_token_hash,
         OLD.filter_snapshot_json,OLD.created_by,OLD.created_at,OLD.expires_at)
     IS DISTINCT FROM
     ROW(NEW.id,NEW.tenant_id,NEW.report_id,NEW.report_version_id,
         NEW.share_type,NEW.principal_id,NEW.share_token_hash,
         NEW.filter_snapshot_json,NEW.created_by,NEW.created_at,NEW.expires_at) THEN
    RAISE EXCEPTION 'report share identity and authorization inputs are immutable'
      USING ERRCODE='55000';
  END IF;

  -- Creator revocation is a single-purpose, one-way update.
  IF OLD.revoked_at IS NULL AND NEW.revoked_at IS NOT NULL
     AND NEW.expired_at IS NOT DISTINCT FROM OLD.expired_at
     AND NEW.access_count=OLD.access_count
     AND NEW.last_accessed_at IS NOT DISTINCT FROM OLD.last_accessed_at
     AND (platform.is_system_access() OR OLD.created_by=platform.current_user_id()) THEN
    RETURN NEW;
  END IF;

  -- The background worker only materializes an already elapsed deadline.
  IF OLD.expired_at IS NULL AND NEW.expired_at IS NOT NULL
     AND NEW.expired_at>=OLD.expires_at
     AND NEW.revoked_at IS NOT DISTINCT FROM OLD.revoked_at
     AND NEW.access_count=OLD.access_count
     AND NEW.last_accessed_at IS NOT DISTINCT FROM OLD.last_accessed_at
     AND platform.is_system_access() THEN
    RETURN NEW;
  END IF;

  -- A currently authorized viewer may only increment access telemetry once.
  IF OLD.revoked_at IS NULL AND OLD.expired_at IS NULL
     AND clock_timestamp()<OLD.expires_at
     AND NEW.revoked_at IS NULL AND NEW.expired_at IS NULL
     AND NEW.access_count=OLD.access_count+1
     AND NEW.last_accessed_at IS NOT NULL
     AND NEW.last_accessed_at>=COALESCE(OLD.last_accessed_at,OLD.created_at)
     AND NEW.last_accessed_at<=clock_timestamp()+interval '1 minute' THEN
    RETURN NEW;
  END IF;

  RAISE EXCEPTION 'invalid report share lifecycle mutation' USING ERRCODE='55000';
END
$$;

DROP TRIGGER IF EXISTS report_share_lifecycle_guard ON platform.report_shares;
CREATE TRIGGER report_share_lifecycle_guard
BEFORE INSERT OR UPDATE ON platform.report_shares
FOR EACH ROW EXECUTE FUNCTION platform.guard_report_share_lifecycle();

DROP POLICY IF EXISTS report_shares_actor_policy ON platform.report_shares;
DROP POLICY IF EXISTS report_shares_read ON platform.report_shares;
DROP POLICY IF EXISTS report_shares_create ON platform.report_shares;
DROP POLICY IF EXISTS report_shares_update ON platform.report_shares;

CREATE POLICY report_shares_read ON platform.report_shares FOR SELECT
  USING(tenant_id=platform.current_tenant_id()
    AND platform.report_v2_can_access(report_id,ARRAY['VIEW','EDIT','PUBLISH'])
    AND (
      platform.is_system_access() OR created_by=platform.current_user_id() OR
      (share_type='INTERNAL_USER' AND principal_id=platform.current_user_id()) OR
      (share_type='INTERNAL_GROUP' AND EXISTS(
        SELECT 1 FROM platform.user_roles AS assignment
        JOIN platform.roles AS role ON role.id=assignment.role_id
          AND role.tenant_id=assignment.tenant_id
        WHERE assignment.tenant_id=report_shares.tenant_id
          AND assignment.user_id=platform.current_user_id()
          AND assignment.role_id=report_shares.principal_id
          AND role.status='ACTIVE' AND role.deleted_at IS NULL
      ))
    ));

CREATE POLICY report_shares_create ON platform.report_shares FOR INSERT
  WITH CHECK(tenant_id=platform.current_tenant_id()
    AND (platform.is_system_access() OR created_by=platform.current_user_id())
    AND platform.report_v2_can_access(report_id,ARRAY['EDIT','PUBLISH']));

CREATE POLICY report_shares_update ON platform.report_shares FOR UPDATE
  USING(tenant_id=platform.current_tenant_id()
    AND platform.report_v2_can_access(report_id,ARRAY['VIEW','EDIT','PUBLISH'])
    AND (
      platform.is_system_access() OR created_by=platform.current_user_id() OR
      (share_type='INTERNAL_USER' AND principal_id=platform.current_user_id()) OR
      (share_type='INTERNAL_GROUP' AND EXISTS(
        SELECT 1 FROM platform.user_roles AS assignment
        JOIN platform.roles AS role ON role.id=assignment.role_id
          AND role.tenant_id=assignment.tenant_id
        WHERE assignment.tenant_id=report_shares.tenant_id
          AND assignment.user_id=platform.current_user_id()
          AND assignment.role_id=report_shares.principal_id
          AND role.status='ACTIVE' AND role.deleted_at IS NULL
      ))
    ))
  WITH CHECK(tenant_id=platform.current_tenant_id()
    AND platform.report_v2_can_access(report_id,ARRAY['VIEW','EDIT','PUBLISH'])
    AND (
      platform.is_system_access() OR created_by=platform.current_user_id() OR
      (share_type='INTERNAL_USER' AND principal_id=platform.current_user_id()) OR
      (share_type='INTERNAL_GROUP' AND EXISTS(
        SELECT 1 FROM platform.user_roles AS assignment
        JOIN platform.roles AS role ON role.id=assignment.role_id
          AND role.tenant_id=assignment.tenant_id
        WHERE assignment.tenant_id=report_shares.tenant_id
          AND assignment.user_id=platform.current_user_id()
          AND assignment.role_id=report_shares.principal_id
          AND role.status='ACTIVE' AND role.deleted_at IS NULL
      ))
    ));

REVOKE ALL ON FUNCTION platform.report_share_principal_valid(uuid,text,uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION platform.guard_report_share_lifecycle() FROM PUBLIC;
