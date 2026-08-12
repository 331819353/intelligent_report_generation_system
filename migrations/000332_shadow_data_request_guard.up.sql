BEGIN;

CREATE OR REPLACE FUNCTION platform.reject_shadow_data_request_source()
RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,platform,askdata
AS $$
BEGIN
  IF NEW.source_question_run_id IS NOT NULL AND EXISTS(
    SELECT 1 FROM askdata.question_runs
    WHERE tenant_id=NEW.tenant_id AND id=NEW.source_question_run_id
      AND execution_mode='SHADOW'
  ) THEN
    RAISE EXCEPTION 'shadow question run cannot create a data request' USING ERRCODE='42501';
  END IF;
  RETURN NEW;
END
$$;

CREATE TRIGGER platform_data_requests_00_user_run
BEFORE INSERT OR UPDATE ON platform.data_requests
FOR EACH ROW EXECUTE FUNCTION platform.reject_shadow_data_request_source();

REVOKE ALL ON FUNCTION platform.reject_shadow_data_request_source() FROM PUBLIC;

COMMIT;
