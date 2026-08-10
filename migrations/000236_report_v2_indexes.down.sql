DROP TRIGGER IF EXISTS component_template_version_delete_guard ON platform.component_template_versions;
DROP FUNCTION IF EXISTS platform.protect_referenced_component_template_version();
DROP TABLE IF EXISTS platform.report_version_dependencies;
DROP TABLE IF EXISTS platform.report_version_component_indexes;
DROP TABLE IF EXISTS platform.report_draft_dependencies;
DROP TABLE IF EXISTS platform.report_draft_component_indexes;
