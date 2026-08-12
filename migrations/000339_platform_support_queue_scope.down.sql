BEGIN;

DROP POLICY support_tickets_read ON platform.support_tickets;
CREATE POLICY support_tickets_read ON platform.support_tickets FOR SELECT
  USING(
    tenant_id=platform.current_tenant_id()
    AND domain_id=platform.current_domain_id()
    AND (
      platform.is_system_access()
      OR reporter_user_id=platform.current_user_id()
      OR platform.user_is_platform_administrator()
      OR platform.user_is_domain_administrator(domain_id)
    )
  );

DROP POLICY support_tickets_manage ON platform.support_tickets;
CREATE POLICY support_tickets_manage ON platform.support_tickets FOR UPDATE
  USING(
    tenant_id=platform.current_tenant_id()
    AND domain_id=platform.current_domain_id()
    AND (
      platform.is_system_access()
      OR platform.user_is_platform_administrator()
      OR platform.user_is_domain_administrator(domain_id)
    )
  )
  WITH CHECK(
    tenant_id=platform.current_tenant_id()
    AND domain_id=platform.current_domain_id()
    AND (
      platform.is_system_access()
      OR platform.user_is_platform_administrator()
      OR platform.user_is_domain_administrator(domain_id)
    )
  );

COMMIT;
