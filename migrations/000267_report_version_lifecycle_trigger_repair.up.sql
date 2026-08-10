-- Some installations applied an early 000234 whose version trigger called the
-- blanket immutable-row guard. Rebind it to the content-immutable lifecycle
-- guard so PENDING/RETRY may legitimately become READY.
DROP TRIGGER IF EXISTS report_v2_versions_definition_immutable ON platform.report_versions;
CREATE TRIGGER report_v2_versions_definition_immutable
BEFORE UPDATE OR DELETE ON platform.report_versions
FOR EACH ROW EXECUTE FUNCTION platform.guard_report_v2_version_mutation();
