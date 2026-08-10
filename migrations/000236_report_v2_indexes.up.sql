CREATE TABLE platform.report_draft_component_indexes(
  report_id uuid NOT NULL, tenant_id uuid NOT NULL, revision_no bigint NOT NULL CHECK(revision_no>=0),
  component_id text NOT NULL, component_type text NOT NULL, component_version text NOT NULL,
  page_id text NOT NULL, section_id text NOT NULL, block_id text NOT NULL, slot_id text NOT NULL,
  binding_mode text CHECK(binding_mode IS NULL OR binding_mode IN ('SEMANTIC_IR','DATASET_FIELD')),
  PRIMARY KEY(report_id,component_id),
  FOREIGN KEY(report_id,tenant_id) REFERENCES platform.reports(id,tenant_id) ON DELETE CASCADE
);
CREATE TABLE platform.report_draft_dependencies(
  report_id uuid NOT NULL, tenant_id uuid NOT NULL,
  dependency_type text NOT NULL CHECK(dependency_type IN (
    'DATASET_VERSION','SEMANTIC_RELEASE','METRIC_VERSION','DIMENSION_VERSION','MEMBER_VERSION',
    'COMPONENT_TEMPLATE','THEME','REPORT_TEMPLATE','STRUCTURE_TEMPLATE','LAYOUT_TEMPLATE','NARRATIVE_TEMPLATE'
  )),
  dependency_id text NOT NULL CHECK(length(btrim(dependency_id)) BETWEEN 1 AND 512),
  component_ids text[] NOT NULL DEFAULT '{}',
  PRIMARY KEY(report_id,dependency_type,dependency_id),
  FOREIGN KEY(report_id,tenant_id) REFERENCES platform.reports(id,tenant_id) ON DELETE CASCADE
);
CREATE TABLE platform.report_version_component_indexes(
  report_version_id uuid NOT NULL, report_id uuid NOT NULL, tenant_id uuid NOT NULL,
  component_id text NOT NULL, component_type text NOT NULL, component_version text NOT NULL,
  page_id text NOT NULL, section_id text NOT NULL, block_id text NOT NULL, slot_id text NOT NULL,
  binding_mode text CHECK(binding_mode IS NULL OR binding_mode IN ('SEMANTIC_IR','DATASET_FIELD')),
  PRIMARY KEY(report_version_id,component_id),
  FOREIGN KEY(report_version_id,report_id,tenant_id)
    REFERENCES platform.report_versions(id,report_id,tenant_id) ON DELETE RESTRICT
);
CREATE TABLE platform.report_version_dependencies(
  report_version_id uuid NOT NULL, report_id uuid NOT NULL, tenant_id uuid NOT NULL,
  dependency_type text NOT NULL CHECK(dependency_type IN (
    'DATASET_VERSION','SEMANTIC_RELEASE','METRIC_VERSION','DIMENSION_VERSION','MEMBER_VERSION',
    'COMPONENT_TEMPLATE','THEME','REPORT_TEMPLATE','STRUCTURE_TEMPLATE','LAYOUT_TEMPLATE','NARRATIVE_TEMPLATE'
  )),
  dependency_id text NOT NULL CHECK(length(btrim(dependency_id)) BETWEEN 1 AND 512),
  component_ids text[] NOT NULL DEFAULT '{}',
  PRIMARY KEY(report_version_id,dependency_type,dependency_id),
  FOREIGN KEY(report_version_id,report_id,tenant_id)
    REFERENCES platform.report_versions(id,report_id,tenant_id) ON DELETE RESTRICT
);

CREATE INDEX report_draft_dependencies_impact_idx ON platform.report_draft_dependencies(tenant_id,dependency_type,dependency_id);
CREATE INDEX report_version_dependencies_impact_idx ON platform.report_version_dependencies(tenant_id,dependency_type,dependency_id);

CREATE TRIGGER report_v2_version_component_indexes_immutable
BEFORE UPDATE OR DELETE ON platform.report_version_component_indexes
FOR EACH ROW EXECUTE FUNCTION platform.reject_report_v2_immutable_mutation();
CREATE TRIGGER report_v2_version_dependencies_immutable
BEFORE UPDATE OR DELETE ON platform.report_version_dependencies
FOR EACH ROW EXECUTE FUNCTION platform.reject_report_v2_immutable_mutation();

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
CREATE TRIGGER component_template_version_delete_guard
BEFORE DELETE ON platform.component_template_versions
FOR EACH ROW EXECUTE FUNCTION platform.protect_referenced_component_template_version();

ALTER TABLE platform.report_draft_component_indexes ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.report_draft_component_indexes FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.report_draft_dependencies ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.report_draft_dependencies FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.report_version_component_indexes ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.report_version_component_indexes FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.report_version_dependencies ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.report_version_dependencies FORCE ROW LEVEL SECURITY;
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
REVOKE ALL ON FUNCTION platform.protect_referenced_component_template_version() FROM PUBLIC;
