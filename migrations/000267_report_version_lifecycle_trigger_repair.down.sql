DROP TRIGGER IF EXISTS report_v2_versions_definition_immutable ON platform.report_versions;
CREATE TRIGGER report_v2_versions_definition_immutable
BEFORE UPDATE OR DELETE ON platform.report_versions
FOR EACH ROW EXECUTE FUNCTION platform.reject_report_v2_immutable_mutation();
