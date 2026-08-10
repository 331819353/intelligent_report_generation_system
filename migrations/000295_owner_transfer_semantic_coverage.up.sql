-- Certified semantic content remains immutable. Lifecycle control may change
-- only the responsibility owner metadata (and updated_at) in a dedicated
-- SYSTEM transaction; content, status and hashes remain byte-for-byte fixed.
CREATE OR REPLACE FUNCTION askdata.protect_certified_version()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.status='CERTIFIED' THEN
    IF TG_OP='DELETE' OR NOT (
      platform.is_system_access()
      AND current_setting('app.owner_transfer_mode',true)='on'
      AND NEW.owner_id IS DISTINCT FROM OLD.owner_id
      AND (to_jsonb(NEW)-'owner_id'-'updated_at')
          IS NOT DISTINCT FROM (to_jsonb(OLD)-'owner_id'-'updated_at')
    ) THEN
      RAISE EXCEPTION 'certified askdata versions are immutable'
        USING ERRCODE='55000';
    END IF;
  END IF;
  RETURN CASE WHEN TG_OP='DELETE' THEN OLD ELSE NEW END;
END
$$;

CREATE OR REPLACE FUNCTION askdata.validate_governed_import_version()
RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,askdata,platform AS $$
BEGIN
  IF OLD.status='CERTIFIED' THEN
    IF TG_OP='DELETE' OR NOT (
      platform.is_system_access()
      AND current_setting('app.owner_transfer_mode',true)='on'
      AND NEW.owner_id IS DISTINCT FROM OLD.owner_id
      AND (to_jsonb(NEW)-'owner_id'-'updated_at')
          IS NOT DISTINCT FROM (to_jsonb(OLD)-'owner_id'-'updated_at')
    ) THEN
      RAISE EXCEPTION 'certified askdata versions are immutable'
        USING ERRCODE='55000';
    END IF;
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
 OR EXISTS(SELECT 1 FROM askdata.domains WHERE tenant_id=platform.current_tenant_id() AND owner_id=selected_user_id AND status<>'DEPRECATED')
 OR EXISTS(SELECT 1 FROM askdata.entities WHERE tenant_id=platform.current_tenant_id() AND owner_id=selected_user_id AND status<>'DEPRECATED')
 OR EXISTS(SELECT 1 FROM askdata.semantic_models WHERE tenant_id=platform.current_tenant_id() AND owner_id=selected_user_id AND status<>'DEPRECATED')
 OR EXISTS(SELECT 1 FROM askdata.measures WHERE tenant_id=platform.current_tenant_id() AND owner_id=selected_user_id AND status<>'DEPRECATED')
 OR EXISTS(SELECT 1 FROM askdata.metrics WHERE tenant_id=platform.current_tenant_id() AND owner_id=selected_user_id)
 OR EXISTS(SELECT 1 FROM askdata.metric_versions WHERE tenant_id=platform.current_tenant_id() AND owner_id=selected_user_id AND status<>'DEPRECATED')
 OR EXISTS(SELECT 1 FROM askdata.dimensions WHERE tenant_id=platform.current_tenant_id() AND owner_id=selected_user_id AND status<>'DEPRECATED')
 OR EXISTS(SELECT 1 FROM askdata.metric_dimension_versions WHERE tenant_id=platform.current_tenant_id() AND owner_id=selected_user_id AND status<>'DEPRECATED')
 OR EXISTS(SELECT 1 FROM askdata.hierarchies WHERE tenant_id=platform.current_tenant_id() AND owner_id=selected_user_id AND status<>'DEPRECATED')
 OR EXISTS(SELECT 1 FROM askdata.relationships WHERE tenant_id=platform.current_tenant_id() AND owner_id=selected_user_id AND status<>'DEPRECATED')
 OR EXISTS(SELECT 1 FROM askdata.quality_rules WHERE tenant_id=platform.current_tenant_id() AND owner_id=selected_user_id AND status<>'DEPRECATED')
 OR EXISTS(SELECT 1 FROM askdata.kpi_bundles WHERE tenant_id=platform.current_tenant_id() AND owner_user_id=selected_user_id)
 OR EXISTS(SELECT 1 FROM askdata.kpi_bundle_versions WHERE tenant_id=platform.current_tenant_id() AND owner_id=selected_user_id AND status<>'DEPRECATED')
 OR EXISTS(SELECT 1 FROM askdata.time_contracts WHERE tenant_id=platform.current_tenant_id() AND owner_user_id=selected_user_id)
 OR EXISTS(SELECT 1 FROM askdata.business_term_versions WHERE tenant_id=platform.current_tenant_id() AND owner_id=selected_user_id AND status<>'DEPRECATED')
 OR EXISTS(SELECT 1 FROM askdata.certified_example_versions WHERE tenant_id=platform.current_tenant_id() AND owner_id=selected_user_id AND status<>'DEPRECATED')
 OR EXISTS(SELECT 1 FROM askdata.evaluation_case_versions WHERE tenant_id=platform.current_tenant_id() AND owner_id=selected_user_id AND status<>'DEPRECATED')
 OR EXISTS(SELECT 1 FROM askdata.release_references WHERE tenant_id=platform.current_tenant_id() AND owner_id=selected_user_id AND released_at IS NULL)
 OR EXISTS(SELECT 1 FROM platform.report_templates WHERE tenant_id=platform.current_tenant_id() AND owner_user_id=selected_user_id)
 OR EXISTS(SELECT 1 FROM platform.report_structure_templates WHERE tenant_id=platform.current_tenant_id() AND owner_user_id=selected_user_id)
 OR EXISTS(SELECT 1 FROM platform.report_layout_templates WHERE tenant_id=platform.current_tenant_id() AND owner_user_id=selected_user_id)
 OR EXISTS(SELECT 1 FROM platform.report_themes WHERE tenant_id=platform.current_tenant_id() AND owner_user_id=selected_user_id)
 OR EXISTS(SELECT 1 FROM platform.report_narrative_templates WHERE tenant_id=platform.current_tenant_id() AND owner_user_id=selected_user_id)
 OR EXISTS(SELECT 1 FROM platform.data_requests WHERE tenant_id=platform.current_tenant_id() AND selected_user_id=ANY(approver_user_ids) AND state='SUBMITTED')
 OR EXISTS(SELECT 1 FROM decision.decision_approvals approval WHERE approval.tenant_id=platform.current_tenant_id() AND approval.approver_user_id=selected_user_id AND approval.status='PENDING' AND NOT EXISTS(SELECT 1 FROM decision.decision_approval_events event WHERE event.tenant_id=approval.tenant_id AND event.approval_id=approval.id))
 OR EXISTS(SELECT 1 FROM platform.report_subscriptions WHERE tenant_id=platform.current_tenant_id() AND recipient_user_id=selected_user_id AND state='ACTIVE')
 OR EXISTS(SELECT 1 FROM platform.report_deliveries WHERE tenant_id=platform.current_tenant_id() AND recipient_user_id=selected_user_id AND state IN('PENDING','RUNNING','FAILED'))
 OR EXISTS(SELECT 1 FROM platform.runtime_config_versions WHERE tenant_id=platform.current_tenant_id() AND created_by=selected_user_id AND state='DRAFT')
 OR EXISTS(SELECT 1 FROM platform.user_roles assignment JOIN platform.roles role ON role.tenant_id=assignment.tenant_id AND role.id=assignment.role_id WHERE assignment.tenant_id=platform.current_tenant_id() AND assignment.user_id=selected_user_id AND role.code::text='platform_admin' AND role.status='ACTIVE' AND role.deleted_at IS NULL)
$$;
REVOKE ALL ON FUNCTION platform.user_has_open_responsibility(uuid) FROM PUBLIC;
