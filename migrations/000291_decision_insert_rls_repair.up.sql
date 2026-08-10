-- An INSERT row is not yet visible to decision.can_access(id); retain the
-- owner/creator bootstrap while continuing to use participant access on
-- updates performed by an approver.
DROP POLICY decisions_access ON decision.decisions;
CREATE POLICY decisions_access ON decision.decisions
  USING(tenant_id=decision.current_tenant_id()
    AND (decision.system_access() OR decision.can_access(id)))
  WITH CHECK(tenant_id=decision.current_tenant_id()
    AND (decision.system_access() OR
      (domain_id=decision.current_domain_id() AND
        (owner_user_id=decision.current_actor_id() OR created_by=decision.current_actor_id()
          OR decision.can_access(id)))));
