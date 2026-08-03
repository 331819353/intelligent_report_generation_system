ALTER TABLE platform.reports DROP CONSTRAINT IF EXISTS reports_current_published_version_fk;
ALTER TABLE platform.reports DROP COLUMN IF EXISTS current_published_version_id;
DROP TABLE IF EXISTS platform.report_publication_idempotency;
DROP TABLE IF EXISTS platform.report_version_dependencies;
DROP TABLE IF EXISTS platform.report_version_component_indexes;
DROP TABLE IF EXISTS platform.report_versions;
DROP FUNCTION IF EXISTS platform.reject_report_version_mutation();
