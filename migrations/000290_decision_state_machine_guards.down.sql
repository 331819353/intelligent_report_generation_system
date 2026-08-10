DROP POLICY decisions_access ON decision.decisions;
CREATE POLICY decisions_access ON decision.decisions
  USING(tenant_id=decision.current_tenant_id() AND (decision.system_access() OR decision.can_access(id)))
  WITH CHECK(tenant_id=decision.current_tenant_id() AND (decision.system_access() OR
    (domain_id=decision.current_domain_id() AND (owner_user_id=decision.current_actor_id() OR created_by=decision.current_actor_id()))));

DROP TRIGGER IF EXISTS decision_action_mutation_guard ON decision.action_items;
DROP TRIGGER IF EXISTS decision_mutation_guard ON decision.decisions;
DROP FUNCTION IF EXISTS decision.guard_action_mutation();
DROP FUNCTION IF EXISTS decision.guard_decision_mutation();
DROP FUNCTION IF EXISTS decision.action_transition_allowed(text,text);
DROP FUNCTION IF EXISTS decision.status_transition_allowed(text,text);
