DROP FUNCTION IF EXISTS decision.list_work_tenants();

CREATE OR REPLACE FUNCTION decision.can_access(selected_decision_id uuid)
RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,platform,decision AS $$
  SELECT decision.system_access() OR EXISTS(
    SELECT 1 FROM decision.decisions d
    WHERE d.id=selected_decision_id AND d.tenant_id=decision.current_tenant_id()
      AND (d.owner_user_id=decision.current_actor_id() OR d.created_by=decision.current_actor_id()
        OR EXISTS(SELECT 1 FROM decision.decision_approvals a WHERE a.decision_id=d.id AND a.tenant_id=d.tenant_id AND a.approver_user_id=decision.current_actor_id())
        OR EXISTS(SELECT 1 FROM decision.action_items i WHERE i.decision_id=d.id AND i.tenant_id=d.tenant_id AND i.assignee_user_id=decision.current_actor_id()))
  )
$$;

DROP TRIGGER IF EXISTS decision_approvals_immutable ON decision.decision_approvals;
DROP TABLE IF EXISTS decision.decision_approval_events;
DROP FUNCTION IF EXISTS decision.reject_approval_fact_mutation();

-- The prior lifecycle guard is restored without referencing approval events.
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

REVOKE ALL ON FUNCTION decision.can_access(uuid),platform.user_has_open_responsibility(uuid) FROM PUBLIC;
