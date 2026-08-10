DROP TRIGGER IF EXISTS report_version_dependency_release_reference
  ON platform.report_version_dependencies;
DROP FUNCTION IF EXISTS platform.sync_report_version_release_reference();
DROP FUNCTION IF EXISTS platform.retain_report_version_release(uuid,uuid,uuid,uuid);

DELETE FROM askdata.release_references WHERE reference_type='REPORT_VERSION';

DROP TRIGGER IF EXISTS report_v2_version_dependencies_immutable
  ON platform.report_version_dependencies;
DELETE FROM platform.report_version_dependencies
  WHERE dependency_type IN('ANALYSIS_METHOD','PROMPT_VERSION','MODEL_POLICY');
CREATE TRIGGER report_v2_version_dependencies_immutable
BEFORE UPDATE OR DELETE ON platform.report_version_dependencies
FOR EACH ROW EXECUTE FUNCTION platform.reject_report_v2_immutable_mutation();

DELETE FROM platform.report_draft_dependencies
  WHERE dependency_type IN('ANALYSIS_METHOD','PROMPT_VERSION','MODEL_POLICY');

ALTER TABLE platform.report_draft_dependencies
  DROP CONSTRAINT report_draft_dependencies_dependency_type_check,
  ADD CONSTRAINT report_draft_dependencies_dependency_type_check CHECK(dependency_type IN (
    'DATASET_VERSION','SEMANTIC_RELEASE','METRIC_VERSION','DIMENSION_VERSION','MEMBER_VERSION',
    'COMPONENT_TEMPLATE','THEME','REPORT_TEMPLATE','STRUCTURE_TEMPLATE','LAYOUT_TEMPLATE','NARRATIVE_TEMPLATE'
  ));

ALTER TABLE platform.report_version_dependencies
  DROP CONSTRAINT report_version_dependencies_dependency_type_check,
  ADD CONSTRAINT report_version_dependencies_dependency_type_check CHECK(dependency_type IN (
    'DATASET_VERSION','SEMANTIC_RELEASE','METRIC_VERSION','DIMENSION_VERSION','MEMBER_VERSION',
    'COMPONENT_TEMPLATE','THEME','REPORT_TEMPLATE','STRUCTURE_TEMPLATE','LAYOUT_TEMPLATE','NARRATIVE_TEMPLATE'
  ));
