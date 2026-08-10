-- Forward repair for installations that applied 000235 before bundled
-- component placeholders could be hydrated from the embedded Go registry.
ALTER TABLE platform.report_structure_template_versions
  DROP CONSTRAINT IF EXISTS report_structure_template_versions_version_check,
  ADD CONSTRAINT report_structure_template_versions_version_check
    CHECK(version ~ '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$');
ALTER TABLE platform.report_layout_template_versions
  DROP CONSTRAINT IF EXISTS report_layout_template_versions_version_check,
  ADD CONSTRAINT report_layout_template_versions_version_check
    CHECK(version ~ '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$');
ALTER TABLE platform.report_theme_versions
  DROP CONSTRAINT IF EXISTS report_theme_versions_version_check,
  ADD CONSTRAINT report_theme_versions_version_check
    CHECK(version ~ '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$');
ALTER TABLE platform.report_narrative_template_versions
  DROP CONSTRAINT IF EXISTS report_narrative_template_versions_version_check,
  ADD CONSTRAINT report_narrative_template_versions_version_check
    CHECK(version ~ '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$');
ALTER TABLE platform.report_template_versions
  DROP CONSTRAINT IF EXISTS report_template_versions_version_check,
  ADD CONSTRAINT report_template_versions_version_check
    CHECK(version ~ '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$');

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
  USING(EXISTS(SELECT 1 FROM platform.component_templates AS template WHERE template.id=component_template_id
    AND (template.tenant_id IS NULL OR template.tenant_id=platform.current_tenant_id())));
CREATE POLICY component_template_versions_write ON platform.component_template_versions FOR ALL
  USING(EXISTS(SELECT 1 FROM platform.component_templates AS template WHERE template.id=component_template_id
    AND (template.tenant_id=platform.current_tenant_id() OR (template.tenant_id IS NULL AND platform.is_system_access()))))
  WITH CHECK(EXISTS(SELECT 1 FROM platform.component_templates AS template WHERE template.id=component_template_id
    AND (template.tenant_id=platform.current_tenant_id() OR (template.tenant_id IS NULL AND platform.is_system_access()))));

CREATE OR REPLACE FUNCTION platform.enforce_component_template_state()
RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,platform AS $$
BEGIN
  IF OLD.manifest_json->>'seed'='embedded-registry' THEN
    IF NEW.manifest_json ? 'seed'
      OR OLD.id<>NEW.id
      OR OLD.component_template_id<>NEW.component_template_id
      OR OLD.status<>NEW.status
      OR OLD.version<>NEW.version
      OR OLD.migrator_id IS DISTINCT FROM NEW.migrator_id THEN
      RAISE EXCEPTION 'invalid embedded component manifest hydration' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status='ACTIVE' AND NEW.status NOT IN ('ACTIVE','DEPRECATED') THEN
    RAISE EXCEPTION 'invalid component template state transition' USING ERRCODE='23514';
  ELSIF OLD.status='DEPRECATED' AND NEW.status NOT IN ('DEPRECATED','RETAINED') THEN
    RAISE EXCEPTION 'invalid component template state transition' USING ERRCODE='23514';
  ELSIF OLD.status='RETAINED' AND NEW.status<>'RETAINED' THEN
    RAISE EXCEPTION 'retained component template is immutable' USING ERRCODE='55000';
  END IF;
  IF OLD.manifest_json IS DISTINCT FROM NEW.manifest_json
    OR OLD.content_hash IS DISTINCT FROM NEW.content_hash
    OR OLD.id<>NEW.id
    OR OLD.component_template_id<>NEW.component_template_id
    OR OLD.version IS DISTINCT FROM NEW.version
    OR OLD.migrator_id IS DISTINCT FROM NEW.migrator_id THEN
    RAISE EXCEPTION 'component template version content is immutable' USING ERRCODE='55000';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enforce_component_template_state() FROM PUBLIC;
