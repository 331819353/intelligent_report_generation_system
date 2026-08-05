-- Platform administrators deliberately do not occupy a domain membership, but
-- the platform control plane allows them to enter every active domain. Manual
-- DIM/DWD modeling must follow that same access rule instead of rejecting them
-- with a membership-only check.

CREATE OR REPLACE FUNCTION platform.modeling_actor_can_access_current_domain(
  actor_id uuid
)
RETURNS boolean
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
  SELECT
    platform.current_tenant_id() IS NOT NULL
    AND platform.current_domain_id() IS NOT NULL
    AND platform.current_user_id()=actor_id
    AND EXISTS(
      SELECT 1
      FROM platform.users AS actor
      JOIN platform.business_domains AS domain
        ON domain.tenant_id=actor.tenant_id
       AND domain.id=platform.current_domain_id()
       AND domain.status='ACTIVE'
      WHERE actor.tenant_id=platform.current_tenant_id()
        AND actor.id=actor_id
        AND actor.status='ACTIVE'
        AND actor.deleted_at IS NULL
        AND (
          platform.user_is_platform_administrator()
          OR EXISTS(
            SELECT 1
            FROM platform.domain_memberships AS membership
            WHERE membership.tenant_id=actor.tenant_id
              AND membership.user_id=actor.id
              AND membership.domain_id=domain.id
              AND membership.status='ACTIVE'
          )
        )
    )
$$;

REVOKE ALL ON FUNCTION
  platform.modeling_actor_can_access_current_domain(uuid) FROM PUBLIC;

DO $migration$
DECLARE
  target regprocedure;
  definition text;
  original text;
  membership_only_check constant text := $authorization$
     OR NOT EXISTS(
       SELECT 1
       FROM platform.users AS actor
       JOIN platform.domain_memberships AS membership
         ON membership.tenant_id=actor.tenant_id
        AND membership.user_id=actor.id
        AND membership.domain_id=platform.current_domain_id()
        AND membership.status='ACTIVE'
       JOIN platform.business_domains AS domain
         ON domain.tenant_id=membership.tenant_id
        AND domain.id=membership.domain_id
        AND domain.status='ACTIVE'
       WHERE actor.tenant_id=platform.current_tenant_id()
         AND actor.id=actor_id
         AND actor.status='ACTIVE'
         AND actor.deleted_at IS NULL
     )$authorization$;
BEGIN
  FOREACH target IN ARRAY ARRAY[
    'platform.trigger_manual_dim_modeling(uuid,uuid[])'::regprocedure,
    'platform.trigger_manual_dwd_modeling(uuid)'::regprocedure
  ] LOOP
    SELECT pg_get_functiondef(target) INTO definition;
    original := definition;
    definition := replace(
      definition,
      membership_only_check,
      E'     OR NOT platform.modeling_actor_can_access_current_domain(actor_id)'
    );
    IF definition=original
       OR position(
         'modeling_actor_can_access_current_domain(actor_id)' IN definition
       )=0 THEN
      RAISE EXCEPTION '无法更新建模入口 % 的领域授权规则',target;
    END IF;
    EXECUTE definition;
  END LOOP;
END
$migration$;

COMMENT ON FUNCTION
  platform.modeling_actor_can_access_current_domain(uuid) IS
  '校验建模发起人可进入当前领域：平台管理员可进入任意启用领域，普通用户需有效领域成员身份';
