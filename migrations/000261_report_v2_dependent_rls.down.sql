DROP POLICY IF EXISTS report_shares_actor_policy ON platform.report_shares;
CREATE POLICY report_shares_actor_policy ON platform.report_shares
  USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
DROP POLICY IF EXISTS report_insight_artifacts_access ON platform.report_insight_artifacts;
DROP POLICY IF EXISTS report_evidence_artifacts_access ON platform.report_evidence_artifacts;
DROP POLICY IF EXISTS report_ai_operations_access ON platform.report_ai_operations;
DROP POLICY IF EXISTS report_ai_runs_access ON platform.report_ai_runs;
CREATE POLICY report_ai_runs_tenant ON platform.report_ai_runs USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY report_ai_operations_tenant ON platform.report_ai_operations USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY report_evidence_artifacts_tenant ON platform.report_evidence_artifacts USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY report_insight_artifacts_tenant ON platform.report_insight_artifacts USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
DROP POLICY IF EXISTS report_version_dependencies_write ON platform.report_version_dependencies;
DROP POLICY IF EXISTS report_version_dependencies_read ON platform.report_version_dependencies;
DROP POLICY IF EXISTS report_version_component_indexes_write ON platform.report_version_component_indexes;
DROP POLICY IF EXISTS report_version_component_indexes_read ON platform.report_version_component_indexes;
DROP POLICY IF EXISTS report_draft_dependencies_write ON platform.report_draft_dependencies;
DROP POLICY IF EXISTS report_draft_dependencies_read ON platform.report_draft_dependencies;
DROP POLICY IF EXISTS report_draft_component_indexes_write ON platform.report_draft_component_indexes;
DROP POLICY IF EXISTS report_draft_component_indexes_read ON platform.report_draft_component_indexes;
CREATE POLICY report_draft_component_indexes_tenant ON platform.report_draft_component_indexes USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY report_draft_dependencies_tenant ON platform.report_draft_dependencies USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY report_version_component_indexes_tenant ON platform.report_version_component_indexes USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY report_version_dependencies_tenant ON platform.report_version_dependencies USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE OR REPLACE FUNCTION platform.protect_referenced_component_template_version()
RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,platform AS $$
DECLARE dependency_key text;
BEGIN
  SELECT template.type||'@'||OLD.version INTO dependency_key
  FROM platform.component_templates AS template WHERE template.id=OLD.component_template_id;
  IF EXISTS(SELECT 1 FROM platform.report_version_dependencies
    WHERE dependency_type='COMPONENT_TEMPLATE' AND dependency_id=dependency_key) THEN
    RAISE EXCEPTION 'REPORT_TEMPLATE_IN_USE' USING ERRCODE='55000';
  END IF;
  RETURN OLD;
END
$$;
REVOKE ALL ON FUNCTION platform.protect_referenced_component_template_version() FROM PUBLIC;
