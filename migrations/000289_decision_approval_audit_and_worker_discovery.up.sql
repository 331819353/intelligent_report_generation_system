-- Make approval decisions append-only, close the decision domain-switch RLS
-- boundary, and give the worker a least-privilege due-work discovery entry.
CREATE TABLE decision.decision_approval_events(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL,
  domain_id uuid NOT NULL,
  decision_id uuid NOT NULL,
  approval_id uuid NOT NULL,
  status text NOT NULL CHECK(status IN ('APPROVED','REJECTED')),
  comment text NOT NULL DEFAULT '' CHECK(length(comment)<=4096),
  actor_user_id uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(id,tenant_id),
  UNIQUE(tenant_id,approval_id),
  FOREIGN KEY(approval_id,tenant_id)
    REFERENCES decision.decision_approvals(id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(decision_id,domain_id,tenant_id)
    REFERENCES decision.decisions(id,domain_id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(actor_user_id,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);

CREATE OR REPLACE FUNCTION decision.reject_approval_fact_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'decision approval facts are append only' USING ERRCODE='55000';
END
$$;
CREATE TRIGGER decision_approvals_immutable
BEFORE UPDATE OR DELETE ON decision.decision_approvals
FOR EACH ROW EXECUTE FUNCTION decision.reject_approval_fact_mutation();
CREATE TRIGGER decision_approval_events_immutable
BEFORE UPDATE OR DELETE ON decision.decision_approval_events
FOR EACH ROW EXECUTE FUNCTION decision.reject_approval_fact_mutation();

ALTER TABLE decision.decision_approval_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE decision.decision_approval_events FORCE ROW LEVEL SECURITY;

CREATE OR REPLACE FUNCTION decision.can_access(selected_decision_id uuid)
RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,platform,decision AS $$
  SELECT decision.system_access() OR EXISTS(
    SELECT 1 FROM decision.decisions d
    WHERE d.id=selected_decision_id
      AND d.tenant_id=decision.current_tenant_id()
      AND d.domain_id=decision.current_domain_id()
      AND (d.owner_user_id=decision.current_actor_id() OR d.created_by=decision.current_actor_id()
        OR EXISTS(SELECT 1 FROM decision.decision_approvals a
          WHERE a.decision_id=d.id AND a.tenant_id=d.tenant_id
            AND a.approver_user_id=decision.current_actor_id())
        OR EXISTS(SELECT 1 FROM decision.action_items i
          WHERE i.decision_id=d.id AND i.tenant_id=d.tenant_id
            AND i.assignee_user_id=decision.current_actor_id()))
  )
$$;

CREATE POLICY decision_approval_events_access ON decision.decision_approval_events
  USING(tenant_id=decision.current_tenant_id()
    AND (decision.system_access() OR decision.can_access(decision_id)))
  WITH CHECK(tenant_id=decision.current_tenant_id()
    AND (decision.system_access() OR
      (domain_id=decision.current_domain_id() AND decision.can_access(decision_id))));

CREATE OR REPLACE FUNCTION decision.list_work_tenants() RETURNS TABLE(tenant_id uuid)
LANGUAGE sql SECURITY DEFINER SET search_path=pg_catalog,platform,decision AS $$
  SELECT tenant.id
  FROM platform.tenants AS tenant
  WHERE tenant.status='ACTIVE' AND tenant.deleted_at IS NULL AND (
    EXISTS(SELECT 1 FROM decision.decisions value
      WHERE value.tenant_id=tenant.id
        AND value.status IN ('IN_EXECUTION','REOPENED') AND value.review_at<=now())
    OR EXISTS(SELECT 1 FROM decision.action_items action
      WHERE action.tenant_id=tenant.id
        AND action.status NOT IN ('DONE','CANCELED') AND action.due_at<now())
  )
  ORDER BY tenant.id
$$;

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
 OR EXISTS(SELECT 1 FROM decision.decision_approvals approval
   WHERE approval.tenant_id=platform.current_tenant_id()
     AND approval.approver_user_id=selected_user_id AND approval.status='PENDING'
     AND NOT EXISTS(SELECT 1 FROM decision.decision_approval_events event
       WHERE event.tenant_id=approval.tenant_id AND event.approval_id=approval.id))
 OR EXISTS(SELECT 1 FROM platform.report_subscriptions WHERE tenant_id=platform.current_tenant_id() AND recipient_user_id=selected_user_id AND state='ACTIVE')
 OR EXISTS(SELECT 1 FROM platform.report_deliveries WHERE tenant_id=platform.current_tenant_id() AND recipient_user_id=selected_user_id AND state IN('PENDING','RUNNING','FAILED'))
 OR EXISTS(SELECT 1 FROM platform.user_roles assignment JOIN platform.roles role ON role.tenant_id=assignment.tenant_id AND role.id=assignment.role_id WHERE assignment.tenant_id=platform.current_tenant_id() AND assignment.user_id=selected_user_id AND role.code::text='platform_admin' AND role.status='ACTIVE' AND role.deleted_at IS NULL)
$$;

REVOKE ALL ON FUNCTION decision.reject_approval_fact_mutation(),
  decision.list_work_tenants(),decision.can_access(uuid) FROM PUBLIC;
REVOKE ALL ON TABLE decision.decision_approval_events FROM PUBLIC;
