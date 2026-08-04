-- 平台管理员、领域管理员、普通用户三种身份全局互斥。
-- 历史上同时存在普通成员和领域管理员关系的账号只保留管理员关系。
DELETE FROM platform.domain_memberships AS membership
WHERE membership.status='ACTIVE'
  AND membership.member_role='MEMBER'
  AND EXISTS(
    SELECT 1 FROM platform.domain_memberships AS administrator_membership
    WHERE administrator_membership.tenant_id=membership.tenant_id
      AND administrator_membership.user_id=membership.user_id
      AND administrator_membership.status='ACTIVE'
      AND administrator_membership.member_role='DOMAIN_ADMIN'
  );

CREATE OR REPLACE FUNCTION platform.reject_mixed_domain_identity()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
BEGIN
  IF NEW.status='ACTIVE' AND EXISTS(
    SELECT 1 FROM platform.domain_memberships AS membership
    WHERE membership.tenant_id=NEW.tenant_id
      AND membership.user_id=NEW.user_id
      AND membership.status='ACTIVE'
      AND membership.member_role<>NEW.member_role
      AND membership.domain_id<>NEW.domain_id
  ) THEN
    RAISE EXCEPTION 'domain administrator and ordinary user identities cannot be combined'
      USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

CREATE TRIGGER domain_memberships_reject_mixed_identity
BEFORE INSERT OR UPDATE OF domain_id,user_id,status,member_role
ON platform.domain_memberships
FOR EACH ROW
EXECUTE FUNCTION platform.reject_mixed_domain_identity();

-- 平台管理员不保存领域成员关系，但可在选择任意活动领域后进入全部功能。
-- 复用现有 RLS 入口，让数据表策略继续要求明确的当前领域。
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
    FROM platform.business_domains AS domain
    WHERE domain.tenant_id=platform.current_tenant_id()
      AND domain.id=target_domain_id
      AND domain.status='ACTIVE'
      AND domain.deleted_at IS NULL
      AND (
        platform.user_is_platform_administrator()
        OR EXISTS(
          SELECT 1
          FROM platform.domain_memberships AS membership
          WHERE membership.tenant_id=domain.tenant_id
            AND membership.user_id=platform.current_user_id()
            AND membership.domain_id=domain.id
            AND membership.status='ACTIVE'
        )
      )
  )
$$;

COMMENT ON FUNCTION platform.user_has_active_domain_membership(uuid) IS
  '普通用户要求活动成员关系；平台管理员无需成员关系即可访问明确选择的活动领域';

-- 平台管理员在已选择领域内可读写全部资产，包括其他用户的私有草稿。
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
        platform.user_is_platform_administrator()
        OR asset_scope='DOMAIN'
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
        platform.user_is_platform_administrator()
        OR asset_owner_user_id=platform.current_user_id()
        OR platform.user_is_domain_administrator(asset_domain_id)
      )
    )
$$;
