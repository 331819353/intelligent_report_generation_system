DROP POLICY decisions_access ON decision.decisions;
CREATE POLICY decisions_access ON decision.decisions
  USING(tenant_id=decision.current_tenant_id()
    AND (decision.system_access() OR decision.can_access(id)))
  WITH CHECK(tenant_id=decision.current_tenant_id()
    AND (decision.system_access() OR
      (domain_id=decision.current_domain_id() AND decision.can_access(id))));
