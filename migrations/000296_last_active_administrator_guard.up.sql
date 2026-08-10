-- A user assignment is a blocking lifecycle responsibility only when disabling
-- that user would leave the tenant or one of its domains without an active
-- administrator. Historical assignments remain intact for audit purposes.
CREATE OR REPLACE FUNCTION platform.user_is_last_active_administrator(
  selected_user_id uuid
) RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,platform AS $$
 SELECT EXISTS(
   SELECT 1
   FROM platform.domain_memberships membership
   JOIN platform.users target_user
     ON target_user.tenant_id=membership.tenant_id
    AND target_user.id=membership.user_id
   WHERE membership.tenant_id=platform.current_tenant_id()
     AND membership.user_id=selected_user_id
     AND membership.status='ACTIVE'
     AND membership.member_role='DOMAIN_ADMIN'
     AND target_user.status='ACTIVE'
     AND target_user.deleted_at IS NULL
     AND NOT EXISTS(
       SELECT 1
       FROM platform.domain_memberships other
       JOIN platform.users other_user
         ON other_user.tenant_id=other.tenant_id
        AND other_user.id=other.user_id
       WHERE other.tenant_id=membership.tenant_id
         AND other.domain_id=membership.domain_id
         AND other.user_id<>membership.user_id
         AND other.status='ACTIVE'
         AND other.member_role='DOMAIN_ADMIN'
         AND other_user.status='ACTIVE'
         AND other_user.deleted_at IS NULL
     )
 ) OR EXISTS(
   SELECT 1
   FROM platform.user_roles assignment
   JOIN platform.roles role
     ON role.tenant_id=assignment.tenant_id
    AND role.id=assignment.role_id
   JOIN platform.users target_user
     ON target_user.tenant_id=assignment.tenant_id
    AND target_user.id=assignment.user_id
   WHERE assignment.tenant_id=platform.current_tenant_id()
     AND assignment.user_id=selected_user_id
     AND role.code::text='platform_admin'
     AND role.status='ACTIVE'
     AND role.deleted_at IS NULL
     AND target_user.status='ACTIVE'
     AND target_user.deleted_at IS NULL
     AND NOT EXISTS(
       SELECT 1
       FROM platform.user_roles other_assignment
       JOIN platform.roles other_role
         ON other_role.tenant_id=other_assignment.tenant_id
        AND other_role.id=other_assignment.role_id
       JOIN platform.users other_user
         ON other_user.tenant_id=other_assignment.tenant_id
        AND other_user.id=other_assignment.user_id
       WHERE other_assignment.tenant_id=assignment.tenant_id
         AND other_assignment.user_id<>assignment.user_id
         AND other_role.code::text='platform_admin'
         AND other_role.status='ACTIVE'
         AND other_role.deleted_at IS NULL
         AND other_user.status='ACTIVE'
         AND other_user.deleted_at IS NULL
     )
 )
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
 OR platform.user_is_last_active_administrator(selected_user_id)
$$;

REVOKE ALL ON FUNCTION platform.user_is_last_active_administrator(uuid),
  platform.user_has_open_responsibility(uuid) FROM PUBLIC;
