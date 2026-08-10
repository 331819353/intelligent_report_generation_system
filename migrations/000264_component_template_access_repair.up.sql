-- Some early 000256 installations retained the original FOR ALL visibility
-- policies. Split reads from writes and require SYSTEM access for platform
-- built-ins while preserving ordinary tenant-owned template management.
DROP POLICY IF EXISTS component_templates_visible ON platform.component_templates;
DROP POLICY IF EXISTS component_template_versions_visible ON platform.component_template_versions;
DROP POLICY IF EXISTS component_templates_select ON platform.component_templates;
DROP POLICY IF EXISTS component_templates_write ON platform.component_templates;
DROP POLICY IF EXISTS component_template_versions_select ON platform.component_template_versions;
DROP POLICY IF EXISTS component_template_versions_write ON platform.component_template_versions;

CREATE POLICY component_templates_select ON platform.component_templates FOR SELECT
  USING(tenant_id IS NULL OR tenant_id=platform.current_tenant_id());
CREATE POLICY component_templates_write ON platform.component_templates FOR ALL
  USING(tenant_id=platform.current_tenant_id() OR (tenant_id IS NULL AND platform.is_system_access()))
  WITH CHECK(tenant_id=platform.current_tenant_id() OR (tenant_id IS NULL AND platform.is_system_access()));
CREATE POLICY component_template_versions_select ON platform.component_template_versions FOR SELECT
  USING(EXISTS(
    SELECT 1 FROM platform.component_templates AS template
    WHERE template.id=component_template_id
      AND (template.tenant_id IS NULL OR template.tenant_id=platform.current_tenant_id())
  ));
CREATE POLICY component_template_versions_write ON platform.component_template_versions FOR ALL
  USING(EXISTS(
    SELECT 1 FROM platform.component_templates AS template
    WHERE template.id=component_template_id
      AND (template.tenant_id=platform.current_tenant_id()
        OR (template.tenant_id IS NULL AND platform.is_system_access()))
  ))
  WITH CHECK(EXISTS(
    SELECT 1 FROM platform.component_templates AS template
    WHERE template.id=component_template_id
      AND (template.tenant_id=platform.current_tenant_id()
        OR (template.tenant_id IS NULL AND platform.is_system_access()))
  ));
