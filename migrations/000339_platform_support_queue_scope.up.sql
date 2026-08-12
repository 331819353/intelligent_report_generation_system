BEGIN;

-- A platform administrator is intentionally not a business-domain member.
-- The service desk queue is nevertheless a tenant control plane and must span
-- domains after the fixed platform role has been authorized by the API.
DROP POLICY support_tickets_read ON platform.support_tickets;
CREATE POLICY support_tickets_read ON platform.support_tickets FOR SELECT
  USING(
    tenant_id=platform.current_tenant_id()
    AND (
      platform.is_system_access()
      OR (
        domain_id=platform.current_domain_id()
        AND (
          reporter_user_id=platform.current_user_id()
          OR platform.user_is_domain_administrator(domain_id)
        )
      )
      OR platform.user_is_platform_administrator()
    )
  );

DROP POLICY support_tickets_manage ON platform.support_tickets;
CREATE POLICY support_tickets_manage ON platform.support_tickets FOR UPDATE
  USING(
    tenant_id=platform.current_tenant_id()
    AND (
      platform.is_system_access()
      OR platform.user_is_platform_administrator()
      OR (
        domain_id=platform.current_domain_id()
        AND platform.user_is_domain_administrator(domain_id)
      )
    )
  )
  WITH CHECK(
    tenant_id=platform.current_tenant_id()
    AND (
      platform.is_system_access()
      OR platform.user_is_platform_administrator()
      OR (
        domain_id=platform.current_domain_id()
        AND platform.user_is_domain_administrator(domain_id)
      )
    )
  );

COMMIT;
