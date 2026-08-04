DROP TRIGGER IF EXISTS domain_memberships_reject_mixed_identity
  ON platform.domain_memberships;
DROP FUNCTION IF EXISTS platform.reject_mixed_domain_identity();

CREATE OR REPLACE FUNCTION platform.user_has_active_domain_membership(
  target_domain_id uuid
)
RETURNS boolean
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
  SELECT EXISTS(
    SELECT 1
    FROM platform.domain_memberships AS membership
    JOIN platform.business_domains AS domain
      ON domain.id=membership.domain_id
     AND domain.tenant_id=membership.tenant_id
    WHERE membership.tenant_id=platform.current_tenant_id()
      AND membership.user_id=platform.current_user_id()
      AND membership.domain_id=target_domain_id
      AND membership.status='ACTIVE'
      AND domain.status='ACTIVE'
      AND domain.deleted_at IS NULL
  )
$$;

CREATE OR REPLACE FUNCTION platform.asset_can_read(
  asset_domain_id uuid,
  asset_owner_user_id uuid,
  asset_scope platform.asset_share_scope
)
RETURNS boolean
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
  SELECT platform.is_system_access()
    OR (
      asset_domain_id=platform.current_domain_id()
      AND platform.user_has_active_domain_membership(asset_domain_id)
      AND (
        asset_scope='DOMAIN'
        OR asset_owner_user_id=platform.current_user_id()
        OR platform.user_is_domain_administrator(asset_domain_id)
      )
    )
$$;

CREATE OR REPLACE FUNCTION platform.asset_can_write(
  asset_domain_id uuid,
  asset_owner_user_id uuid
)
RETURNS boolean
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
  SELECT platform.is_system_access()
    OR (
      asset_domain_id=platform.current_domain_id()
      AND platform.user_has_active_domain_membership(asset_domain_id)
      AND (
        asset_owner_user_id=platform.current_user_id()
        OR platform.user_is_domain_administrator(asset_domain_id)
      )
    )
$$;
