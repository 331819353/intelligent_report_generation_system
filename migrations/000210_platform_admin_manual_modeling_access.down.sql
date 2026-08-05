DO $migration$
DECLARE
  target regprocedure;
  definition text;
  original text;
  membership_only_check constant text := $authorization$     OR NOT EXISTS(
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
      '     OR NOT platform.modeling_actor_can_access_current_domain(actor_id)',
      membership_only_check
    );
    IF definition=original THEN
      RAISE EXCEPTION '无法还原建模入口 % 的领域授权规则',target;
    END IF;
    EXECUTE definition;
  END LOOP;
END
$migration$;

DROP FUNCTION platform.modeling_actor_can_access_current_domain(uuid);
