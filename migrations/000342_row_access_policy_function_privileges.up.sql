-- Row-access helpers deliberately revoke PUBLIC execution in migration 000341,
-- but the narrow API/worker grants were not restored. The compiler therefore
-- failed before it could enforce policies whenever it resolved the current
-- viewer's subject attributes (including published semantic report playback).
BEGIN;

DO $$
BEGIN
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='report_app') THEN
    GRANT EXECUTE ON FUNCTION askdata.resolve_subject_attributes(uuid,uuid)
      TO report_app;
    GRANT EXECUTE ON FUNCTION askdata.row_access_policy_coverage(uuid,uuid)
      TO report_app;
  END IF;
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='report_worker') THEN
    GRANT EXECUTE ON FUNCTION askdata.resolve_subject_attributes(uuid,uuid)
      TO report_worker;
  END IF;
END
$$;

COMMIT;
