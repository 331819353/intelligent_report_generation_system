-- Repair worker discovery and SYSTEM-mode lifecycle paths after the initial
-- decision, scheduling and user lifecycle migrations. Public access remains
-- revoked; runtime roles are granted explicitly by scripts/migrate.sh.

CREATE OR REPLACE FUNCTION platform.report_schedule_work_tenants() RETURNS TABLE(tenant_id uuid)
LANGUAGE sql SECURITY DEFINER SET search_path=pg_catalog,platform AS $$
 SELECT tenant.id FROM platform.tenants AS tenant
 WHERE tenant.status='ACTIVE' AND tenant.deleted_at IS NULL AND (
   EXISTS(SELECT 1 FROM platform.report_schedules AS schedule WHERE schedule.tenant_id=tenant.id AND schedule.state='ACTIVE' AND schedule.next_run_at<=now())
   OR EXISTS(SELECT 1 FROM platform.report_deliveries AS delivery WHERE delivery.tenant_id=tenant.id AND delivery.state IN ('PENDING','RUNNING','FAILED') AND delivery.next_attempt_at<=now())
 ) ORDER BY tenant.id
$$;
REVOKE ALL ON FUNCTION platform.report_schedule_work_tenants() FROM PUBLIC;

ALTER TABLE platform.user_lifecycle_batch_items ALTER COLUMN domain_id DROP NOT NULL;

CREATE OR REPLACE FUNCTION platform.user_has_open_responsibility(selected_user_id uuid) RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,platform,askdata,decision AS $$
 SELECT EXISTS(SELECT 1 FROM platform.data_sources WHERE tenant_id=platform.current_tenant_id() AND owner_user_id=selected_user_id AND status<>'DELETED')
 OR EXISTS(SELECT 1 FROM platform.datasets WHERE tenant_id=platform.current_tenant_id() AND owner_user_id=selected_user_id AND deleted_at IS NULL)
 OR EXISTS(SELECT 1 FROM askdata.saved_questions WHERE tenant_id=platform.current_tenant_id() AND owner_user_id=selected_user_id AND status='ACTIVE')
 OR EXISTS(SELECT 1 FROM platform.reports WHERE tenant_id=platform.current_tenant_id() AND owner_user_id=selected_user_id)
 OR EXISTS(SELECT 1 FROM platform.report_schedules WHERE tenant_id=platform.current_tenant_id() AND owner_user_id=selected_user_id AND state<>'DISABLED')
 OR EXISTS(SELECT 1 FROM askdata.feedback_tickets WHERE tenant_id=platform.current_tenant_id() AND owner_user_id=selected_user_id AND status NOT IN('REJECTED','CLOSED'))
 OR EXISTS(SELECT 1 FROM platform.data_requests WHERE tenant_id=platform.current_tenant_id() AND assignee_user_id=selected_user_id AND state IN('IN_PROGRESS','DELIVERED'))
 OR EXISTS(SELECT 1 FROM decision.decisions WHERE tenant_id=platform.current_tenant_id() AND owner_user_id=selected_user_id AND status NOT IN('CLOSED','CANCELED'))
 OR EXISTS(SELECT 1 FROM decision.action_items WHERE tenant_id=platform.current_tenant_id() AND assignee_user_id=selected_user_id AND status NOT IN('DONE','CANCELED'))
 OR EXISTS(SELECT 1 FROM askdata.kpi_bundles WHERE tenant_id=platform.current_tenant_id() AND owner_user_id=selected_user_id)
 OR EXISTS(SELECT 1 FROM askdata.time_contracts WHERE tenant_id=platform.current_tenant_id() AND owner_user_id=selected_user_id)
 OR EXISTS(SELECT 1 FROM askdata.metrics WHERE tenant_id=platform.current_tenant_id() AND owner_id=selected_user_id)
 OR EXISTS(SELECT 1 FROM askdata.dimensions WHERE tenant_id=platform.current_tenant_id() AND owner_id=selected_user_id)
 OR EXISTS(SELECT 1 FROM askdata.relationships WHERE tenant_id=platform.current_tenant_id() AND owner_id=selected_user_id)
 OR EXISTS(SELECT 1 FROM askdata.business_term_versions WHERE tenant_id=platform.current_tenant_id() AND owner_id=selected_user_id)
 OR EXISTS(SELECT 1 FROM askdata.certified_example_versions WHERE tenant_id=platform.current_tenant_id() AND owner_id=selected_user_id)
 OR EXISTS(SELECT 1 FROM platform.data_requests WHERE tenant_id=platform.current_tenant_id() AND selected_user_id=ANY(approver_user_ids) AND state='SUBMITTED')
 OR EXISTS(SELECT 1 FROM decision.decision_approvals WHERE tenant_id=platform.current_tenant_id() AND approver_user_id=selected_user_id AND status='PENDING')
 OR EXISTS(SELECT 1 FROM platform.report_subscriptions WHERE tenant_id=platform.current_tenant_id() AND recipient_user_id=selected_user_id AND state='ACTIVE')
 OR EXISTS(SELECT 1 FROM platform.report_deliveries WHERE tenant_id=platform.current_tenant_id() AND recipient_user_id=selected_user_id AND state IN('PENDING','RUNNING','FAILED'))
 OR EXISTS(SELECT 1 FROM platform.user_roles assignment JOIN platform.roles role ON role.tenant_id=assignment.tenant_id AND role.id=assignment.role_id WHERE assignment.tenant_id=platform.current_tenant_id() AND assignment.user_id=selected_user_id AND role.code::text='platform_admin' AND role.status='ACTIVE' AND role.deleted_at IS NULL)
$$;
REVOKE ALL ON FUNCTION platform.user_has_open_responsibility(uuid) FROM PUBLIC;

DROP POLICY approval_policies_access ON decision.approval_policies;
CREATE POLICY approval_policies_access ON decision.approval_policies
  USING(tenant_id=decision.current_tenant_id() AND (decision.system_access() OR domain_id=decision.current_domain_id()))
  WITH CHECK(tenant_id=decision.current_tenant_id() AND decision.system_access());
DROP POLICY approval_policy_approvers_access ON decision.approval_policy_approvers;
CREATE POLICY approval_policy_approvers_access ON decision.approval_policy_approvers
  USING(tenant_id=decision.current_tenant_id() AND (decision.system_access() OR domain_id=decision.current_domain_id()))
  WITH CHECK(tenant_id=decision.current_tenant_id() AND decision.system_access());
DROP POLICY decisions_access ON decision.decisions;
CREATE POLICY decisions_access ON decision.decisions
  USING(tenant_id=decision.current_tenant_id() AND (decision.system_access() OR decision.can_access(id)))
  WITH CHECK(tenant_id=decision.current_tenant_id() AND (decision.system_access() OR
    (domain_id=decision.current_domain_id() AND (owner_user_id=decision.current_actor_id() OR created_by=decision.current_actor_id()))));

DO $$ DECLARE table_name text; BEGIN
  FOREACH table_name IN ARRAY ARRAY['decision_options','decision_evidence','decision_approvals','action_items','action_events','outcome_metrics','outcome_reviews','decision_events','decision_notifications'] LOOP
    EXECUTE format('DROP POLICY %I ON decision.%I',table_name||'_access',table_name);
    EXECUTE format('CREATE POLICY %I ON decision.%I USING(tenant_id=decision.current_tenant_id() AND (decision.system_access() OR decision.can_access(decision_id))) WITH CHECK(tenant_id=decision.current_tenant_id() AND (decision.system_access() OR (domain_id=decision.current_domain_id() AND decision.can_access(decision_id))))',table_name||'_access',table_name);
  END LOOP;
END $$;
