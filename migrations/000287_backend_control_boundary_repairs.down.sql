DO $$ DECLARE table_name text; BEGIN
  FOREACH table_name IN ARRAY ARRAY['decision_options','decision_evidence','decision_approvals','action_items','action_events','outcome_metrics','outcome_reviews','decision_events','decision_notifications'] LOOP
    EXECUTE format('DROP POLICY %I ON decision.%I',table_name||'_access',table_name);
    EXECUTE format('CREATE POLICY %I ON decision.%I USING(tenant_id=decision.current_tenant_id() AND decision.can_access(decision_id)) WITH CHECK(tenant_id=decision.current_tenant_id() AND domain_id=decision.current_domain_id() AND decision.can_access(decision_id))',table_name||'_access',table_name);
  END LOOP;
END $$;
DROP POLICY decisions_access ON decision.decisions;
CREATE POLICY decisions_access ON decision.decisions USING(tenant_id=decision.current_tenant_id() AND decision.can_access(id)) WITH CHECK(tenant_id=decision.current_tenant_id() AND domain_id=decision.current_domain_id() AND (decision.system_access() OR owner_user_id=decision.current_actor_id() OR created_by=decision.current_actor_id()));
DROP POLICY approval_policy_approvers_access ON decision.approval_policy_approvers;
CREATE POLICY approval_policy_approvers_access ON decision.approval_policy_approvers USING(tenant_id=decision.current_tenant_id() AND domain_id=decision.current_domain_id()) WITH CHECK(tenant_id=decision.current_tenant_id() AND domain_id=decision.current_domain_id() AND decision.system_access());
DROP POLICY approval_policies_access ON decision.approval_policies;
CREATE POLICY approval_policies_access ON decision.approval_policies USING(tenant_id=decision.current_tenant_id() AND domain_id=decision.current_domain_id()) WITH CHECK(tenant_id=decision.current_tenant_id() AND domain_id=decision.current_domain_id() AND decision.system_access());

ALTER TABLE platform.user_lifecycle_batch_items ALTER COLUMN domain_id SET NOT NULL;
DROP FUNCTION IF EXISTS platform.report_schedule_work_tenants();
