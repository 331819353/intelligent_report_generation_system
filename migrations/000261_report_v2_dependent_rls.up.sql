-- Harden all Report V2 dependent tables that were initially tenant-isolated.
DROP POLICY IF EXISTS report_draft_component_indexes_tenant ON platform.report_draft_component_indexes;
DROP POLICY IF EXISTS report_draft_dependencies_tenant ON platform.report_draft_dependencies;
DROP POLICY IF EXISTS report_version_component_indexes_tenant ON platform.report_version_component_indexes;
DROP POLICY IF EXISTS report_version_dependencies_tenant ON platform.report_version_dependencies;
DROP POLICY IF EXISTS report_draft_component_indexes_read ON platform.report_draft_component_indexes;
DROP POLICY IF EXISTS report_draft_component_indexes_write ON platform.report_draft_component_indexes;
DROP POLICY IF EXISTS report_draft_dependencies_read ON platform.report_draft_dependencies;
DROP POLICY IF EXISTS report_draft_dependencies_write ON platform.report_draft_dependencies;
DROP POLICY IF EXISTS report_version_component_indexes_read ON platform.report_version_component_indexes;
DROP POLICY IF EXISTS report_version_component_indexes_write ON platform.report_version_component_indexes;
DROP POLICY IF EXISTS report_version_dependencies_read ON platform.report_version_dependencies;
DROP POLICY IF EXISTS report_version_dependencies_write ON platform.report_version_dependencies;

CREATE POLICY report_draft_component_indexes_read ON platform.report_draft_component_indexes FOR SELECT
  USING(tenant_id=platform.current_tenant_id() AND platform.report_v2_can_access(report_id,ARRAY['VIEW','EDIT','PUBLISH']));
CREATE POLICY report_draft_component_indexes_write ON platform.report_draft_component_indexes FOR ALL
  USING(tenant_id=platform.current_tenant_id() AND platform.report_v2_can_access(report_id,ARRAY['EDIT']))
  WITH CHECK(tenant_id=platform.current_tenant_id() AND platform.report_v2_can_access(report_id,ARRAY['EDIT']));
CREATE POLICY report_draft_dependencies_read ON platform.report_draft_dependencies FOR SELECT
  USING(tenant_id=platform.current_tenant_id() AND platform.report_v2_can_access(report_id,ARRAY['VIEW','EDIT','PUBLISH']));
CREATE POLICY report_draft_dependencies_write ON platform.report_draft_dependencies FOR ALL
  USING(tenant_id=platform.current_tenant_id() AND platform.report_v2_can_access(report_id,ARRAY['EDIT']))
  WITH CHECK(tenant_id=platform.current_tenant_id() AND platform.report_v2_can_access(report_id,ARRAY['EDIT']));
CREATE POLICY report_version_component_indexes_read ON platform.report_version_component_indexes FOR SELECT
  USING(tenant_id=platform.current_tenant_id() AND platform.report_v2_can_access(report_id,ARRAY['VIEW','EDIT','PUBLISH']));
CREATE POLICY report_version_component_indexes_write ON platform.report_version_component_indexes FOR INSERT
  WITH CHECK(tenant_id=platform.current_tenant_id() AND platform.report_v2_can_access(report_id,ARRAY['PUBLISH','EDIT']));
CREATE POLICY report_version_dependencies_read ON platform.report_version_dependencies FOR SELECT
  USING(tenant_id=platform.current_tenant_id() AND platform.report_v2_can_access(report_id,ARRAY['VIEW','EDIT','PUBLISH']));
CREATE POLICY report_version_dependencies_write ON platform.report_version_dependencies FOR INSERT
  WITH CHECK(tenant_id=platform.current_tenant_id() AND platform.report_v2_can_access(report_id,ARRAY['PUBLISH','EDIT']));

CREATE OR REPLACE FUNCTION platform.protect_referenced_component_template_version()
RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,platform AS $$
DECLARE dependency_key text; component_tenant_id uuid;
BEGIN
  SELECT template.type||'@'||OLD.version,template.tenant_id
  INTO dependency_key,component_tenant_id
  FROM platform.component_templates AS template WHERE template.id=OLD.component_template_id;
  IF EXISTS(SELECT 1 FROM platform.report_version_dependencies
    WHERE dependency_type='COMPONENT_TEMPLATE' AND dependency_id=dependency_key
      AND (component_tenant_id IS NULL OR tenant_id=component_tenant_id)) THEN
    RAISE EXCEPTION 'REPORT_TEMPLATE_IN_USE' USING ERRCODE='55000';
  END IF;
  RETURN OLD;
END
$$;
REVOKE ALL ON FUNCTION platform.protect_referenced_component_template_version() FROM PUBLIC;

DROP POLICY IF EXISTS report_ai_runs_tenant ON platform.report_ai_runs;
DROP POLICY IF EXISTS report_ai_operations_tenant ON platform.report_ai_operations;
DROP POLICY IF EXISTS report_evidence_artifacts_tenant ON platform.report_evidence_artifacts;
DROP POLICY IF EXISTS report_insight_artifacts_tenant ON platform.report_insight_artifacts;

CREATE POLICY report_ai_runs_access ON platform.report_ai_runs
  USING(tenant_id=platform.current_tenant_id()
    AND (platform.is_system_access() OR actor_user_id=platform.current_user_id())
    AND platform.report_v2_can_access(report_id,ARRAY['VIEW','EDIT']))
  WITH CHECK(tenant_id=platform.current_tenant_id()
    AND (platform.is_system_access() OR actor_user_id=platform.current_user_id())
    AND platform.report_v2_can_access(report_id,ARRAY['EDIT']));
CREATE POLICY report_ai_operations_access ON platform.report_ai_operations
  USING(tenant_id=platform.current_tenant_id() AND EXISTS(
    SELECT 1 FROM platform.report_ai_runs run
    WHERE run.id=ai_run_id AND run.tenant_id=report_ai_operations.tenant_id
  ))
  WITH CHECK(tenant_id=platform.current_tenant_id() AND EXISTS(
    SELECT 1 FROM platform.report_ai_runs run
    WHERE run.id=ai_run_id AND run.tenant_id=report_ai_operations.tenant_id
  ));
CREATE POLICY report_evidence_artifacts_access ON platform.report_evidence_artifacts
  USING(tenant_id=platform.current_tenant_id()
    AND platform.report_v2_can_access(report_id,ARRAY['VIEW','EDIT']))
  WITH CHECK(tenant_id=platform.current_tenant_id()
    AND platform.report_v2_can_access(report_id,ARRAY['EDIT']));
CREATE POLICY report_insight_artifacts_access ON platform.report_insight_artifacts
  USING(tenant_id=platform.current_tenant_id()
    AND platform.report_v2_can_access(report_id,ARRAY['VIEW','EDIT']))
  WITH CHECK(tenant_id=platform.current_tenant_id()
    AND platform.report_v2_can_access(report_id,ARRAY['EDIT']));

DROP POLICY IF EXISTS report_shares_actor_policy ON platform.report_shares;
CREATE POLICY report_shares_actor_policy ON platform.report_shares
  USING(tenant_id=platform.current_tenant_id()
    AND platform.report_v2_can_access(report_id,ARRAY['VIEW','EDIT','PUBLISH'])
    AND (
      platform.is_system_access() OR created_by=platform.current_user_id() OR
      (share_type='INTERNAL_USER' AND principal_id=platform.current_user_id()) OR
      (share_type='INTERNAL_GROUP' AND EXISTS(
        SELECT 1 FROM platform.user_roles assignment
        JOIN platform.roles role ON role.id=assignment.role_id
          AND role.tenant_id=assignment.tenant_id
        WHERE assignment.tenant_id=report_shares.tenant_id
          AND assignment.user_id=platform.current_user_id()
          AND assignment.role_id=report_shares.principal_id
          AND role.status='ACTIVE' AND role.deleted_at IS NULL
      ))
    ))
  WITH CHECK(tenant_id=platform.current_tenant_id()
    AND (platform.is_system_access() OR created_by=platform.current_user_id())
    AND platform.report_v2_can_access(report_id,ARRAY['EDIT','PUBLISH']));
