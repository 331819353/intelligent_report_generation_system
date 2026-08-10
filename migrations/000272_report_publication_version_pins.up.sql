-- RPT-004 freezes analysis/runtime policy provenance in dependency indexes and
-- retains every semantic release referenced by an immutable report version.
ALTER TABLE platform.report_draft_dependencies
  DROP CONSTRAINT report_draft_dependencies_dependency_type_check,
  ADD CONSTRAINT report_draft_dependencies_dependency_type_check CHECK(dependency_type IN (
    'DATASET_VERSION','SEMANTIC_RELEASE','METRIC_VERSION','DIMENSION_VERSION','MEMBER_VERSION',
    'COMPONENT_TEMPLATE','THEME','REPORT_TEMPLATE','STRUCTURE_TEMPLATE','LAYOUT_TEMPLATE','NARRATIVE_TEMPLATE',
    'ANALYSIS_METHOD','PROMPT_VERSION','MODEL_POLICY'
  ));

ALTER TABLE platform.report_version_dependencies
  DROP CONSTRAINT report_version_dependencies_dependency_type_check,
  ADD CONSTRAINT report_version_dependencies_dependency_type_check CHECK(dependency_type IN (
    'DATASET_VERSION','SEMANTIC_RELEASE','METRIC_VERSION','DIMENSION_VERSION','MEMBER_VERSION',
    'COMPONENT_TEMPLATE','THEME','REPORT_TEMPLATE','STRUCTURE_TEMPLATE','LAYOUT_TEMPLATE','NARRATIVE_TEMPLATE',
    'ANALYSIS_METHOD','PROMPT_VERSION','MODEL_POLICY'
  ));

CREATE OR REPLACE FUNCTION platform.retain_report_version_release(
  selected_version_id uuid,
  selected_report_id uuid,
  selected_tenant_id uuid,
  selected_release_id uuid
)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform,askdata
AS $$
DECLARE selected_version platform.report_versions%ROWTYPE;
DECLARE selected_name text;
BEGIN
  SELECT version.* INTO STRICT selected_version
  FROM platform.report_versions AS version
  WHERE version.id=selected_version_id
    AND version.report_id=selected_report_id
    AND version.tenant_id=selected_tenant_id;
  IF NOT EXISTS(
    SELECT 1 FROM askdata.releases AS release
    WHERE release.id=selected_release_id AND release.tenant_id=selected_tenant_id
  ) THEN
    RAISE EXCEPTION 'REPORT_BINDING_RELEASE_RETIRED' USING ERRCODE='23503';
  END IF;
  SELECT COALESCE(
    NULLIF(left(regexp_replace(btrim(report.name)||' v'||selected_version.version_no::text,
      '[[:cntrl:]]','','g'),200),''),
    'report version '||selected_version.id::text
  ) INTO selected_name
  FROM platform.reports AS report
  WHERE report.id=selected_report_id AND report.tenant_id=selected_tenant_id;
  PERFORM askdata.upsert_release_reference(
    selected_tenant_id,selected_release_id,'REPORT_VERSION',selected_version_id,
    selected_name,selected_version.published_by
  );
END
$$;

CREATE OR REPLACE FUNCTION platform.sync_report_version_release_reference()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform,askdata
AS $$
BEGIN
  PERFORM platform.retain_report_version_release(
    NEW.report_version_id,NEW.report_id,NEW.tenant_id,NEW.dependency_id::uuid
  );
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.retain_report_version_release(uuid,uuid,uuid,uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION platform.sync_report_version_release_reference() FROM PUBLIC;

CREATE TRIGGER report_version_dependency_release_reference
AFTER INSERT ON platform.report_version_dependencies
FOR EACH ROW WHEN(NEW.dependency_type='SEMANTIC_RELEASE')
EXECUTE FUNCTION platform.sync_report_version_release_reference();

DO $$
DECLARE dependency record;
BEGIN
  FOR dependency IN
    SELECT indexed.*,release.id AS release_id
    FROM platform.report_version_dependencies AS indexed
    JOIN askdata.releases AS release
      ON release.id::text=indexed.dependency_id AND release.tenant_id=indexed.tenant_id
    WHERE indexed.dependency_type='SEMANTIC_RELEASE'
  LOOP
    PERFORM platform.retain_report_version_release(
      dependency.report_version_id,dependency.report_id,dependency.tenant_id,
      dependency.release_id
    );
  END LOOP;
END
$$;
