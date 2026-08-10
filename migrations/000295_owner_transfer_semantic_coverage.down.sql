CREATE OR REPLACE FUNCTION askdata.protect_certified_version()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.status='CERTIFIED' THEN
    RAISE EXCEPTION 'certified askdata versions are immutable' USING ERRCODE='55000';
  END IF;
  RETURN CASE WHEN TG_OP='DELETE' THEN OLD ELSE NEW END;
END
$$;

CREATE OR REPLACE FUNCTION askdata.validate_governed_import_version()
RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,askdata AS $$
BEGIN
  IF TG_OP='UPDATE' AND OLD.status='CERTIFIED' THEN
    RAISE EXCEPTION 'certified askdata versions are immutable' USING ERRCODE='55000';
  END IF;
  IF TG_OP='DELETE' AND OLD.status='CERTIFIED' THEN
    RAISE EXCEPTION 'certified askdata versions are immutable' USING ERRCODE='55000';
  END IF;
  RETURN CASE WHEN TG_OP='DELETE' THEN OLD ELSE NEW END;
END
$$;

CREATE OR REPLACE FUNCTION platform.user_has_open_responsibility(selected_user_id uuid)
RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,platform,askdata,decision AS $$
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
 OR EXISTS(SELECT 1 FROM decision.decision_approvals approval WHERE approval.tenant_id=platform.current_tenant_id() AND approval.approver_user_id=selected_user_id AND approval.status='PENDING' AND NOT EXISTS(SELECT 1 FROM decision.decision_approval_events event WHERE event.tenant_id=approval.tenant_id AND event.approval_id=approval.id))
 OR EXISTS(SELECT 1 FROM platform.report_subscriptions WHERE tenant_id=platform.current_tenant_id() AND recipient_user_id=selected_user_id AND state='ACTIVE')
 OR EXISTS(SELECT 1 FROM platform.report_deliveries WHERE tenant_id=platform.current_tenant_id() AND recipient_user_id=selected_user_id AND state IN('PENDING','RUNNING','FAILED'))
 OR EXISTS(SELECT 1 FROM platform.user_roles assignment JOIN platform.roles role ON role.tenant_id=assignment.tenant_id AND role.id=assignment.role_id WHERE assignment.tenant_id=platform.current_tenant_id() AND assignment.user_id=selected_user_id AND role.code::text='platform_admin' AND role.status='ACTIVE' AND role.deleted_at IS NULL)
$$;
REVOKE ALL ON FUNCTION platform.user_has_open_responsibility(uuid) FROM PUBLIC;
