DROP TRIGGER IF EXISTS report_ai_operation_lifecycle_guard ON platform.report_ai_operations;
DROP TRIGGER IF EXISTS report_ai_run_lifecycle_guard ON platform.report_ai_runs;
DROP FUNCTION IF EXISTS platform.guard_report_ai_operation_lifecycle();
DROP FUNCTION IF EXISTS platform.guard_report_ai_run_lifecycle();

ALTER TABLE platform.report_ai_operations
  DROP CONSTRAINT IF EXISTS report_ai_operations_applied_revision_check;
ALTER TABLE platform.report_ai_runs
  DROP CONSTRAINT IF EXISTS report_ai_runs_response_summary_size_check,
  DROP CONSTRAINT IF EXISTS report_ai_runs_error_shape_check;

CREATE OR REPLACE FUNCTION platform.guard_report_ai_summary()
RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,platform AS $$
DECLARE summary_key text;
BEGIN
  FOR summary_key IN SELECT jsonb_object_keys(NEW.request_summary_json) LOOP
    IF summary_key NOT IN ('intent','selectionIds','availableFields') THEN
      RAISE EXCEPTION 'request summary contains unsupported field %',summary_key
        USING ERRCODE='23514';
    END IF;
  END LOOP;
  IF jsonb_typeof(COALESCE(NEW.request_summary_json->'selectionIds','[]'::jsonb))<>'array'
    OR jsonb_typeof(COALESCE(NEW.request_summary_json->'availableFields','[]'::jsonb))<>'array'
    OR jsonb_typeof(COALESCE(NEW.request_summary_json->'intent','""'::jsonb))<>'string' THEN
    RAISE EXCEPTION 'request summary field types are invalid' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;
REVOKE ALL ON FUNCTION platform.guard_report_ai_summary() FROM PUBLIC;
