BEGIN;

DO $$
BEGIN
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='report_app') THEN
    REVOKE EXECUTE ON FUNCTION askdata.resolve_subject_attributes(uuid,uuid)
      FROM report_app;
    REVOKE EXECUTE ON FUNCTION askdata.row_access_policy_coverage(uuid,uuid)
      FROM report_app;
  END IF;
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='report_worker') THEN
    REVOKE EXECUTE ON FUNCTION askdata.resolve_subject_attributes(uuid,uuid)
      FROM report_worker;
  END IF;
END
$$;

COMMIT;
