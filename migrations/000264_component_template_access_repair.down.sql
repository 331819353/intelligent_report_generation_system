DROP POLICY IF EXISTS component_template_versions_write ON platform.component_template_versions;
DROP POLICY IF EXISTS component_template_versions_select ON platform.component_template_versions;
DROP POLICY IF EXISTS component_templates_write ON platform.component_templates;
DROP POLICY IF EXISTS component_templates_select ON platform.component_templates;

CREATE POLICY component_templates_visible ON platform.component_templates
  USING(tenant_id IS NULL OR tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id IS NULL OR tenant_id=platform.current_tenant_id());
CREATE POLICY component_template_versions_visible ON platform.component_template_versions
  USING(EXISTS(
    SELECT 1 FROM platform.component_templates AS template
    WHERE template.id=component_template_id
      AND (template.tenant_id IS NULL OR template.tenant_id=platform.current_tenant_id())
  ))
  WITH CHECK(EXISTS(
    SELECT 1 FROM platform.component_templates AS template
    WHERE template.id=component_template_id
      AND (template.tenant_id IS NULL OR template.tenant_id=platform.current_tenant_id())
  ));
