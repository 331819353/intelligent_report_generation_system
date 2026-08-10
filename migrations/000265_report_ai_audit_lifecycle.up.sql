-- Report AI audit rows are evidence, not mutable workflow scratch state.
ALTER TABLE platform.report_ai_runs
  ADD CONSTRAINT report_ai_runs_error_shape_check CHECK(
    (state='RUNNING' AND error_code IS NULL)
    OR (state='SUCCEEDED' AND error_code IS NULL)
    OR (state IN ('FAILED','REJECTED')
      AND length(btrim(error_code)) BETWEEN 1 AND 128)
  ),
  ADD CONSTRAINT report_ai_runs_response_summary_size_check CHECK(
    response_summary_json IS NULL OR pg_column_size(response_summary_json)<=65536
  );

ALTER TABLE platform.report_ai_operations
  ADD CONSTRAINT report_ai_operations_applied_revision_check CHECK(
    applied_revision_no IS NULL
    OR (validation_state='VALID' AND applied_revision_no>0)
  );

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
  IF EXISTS(
      SELECT 1
      FROM jsonb_array_elements(
        COALESCE(NEW.request_summary_json->'selectionIds','[]'::jsonb)
      ) AS item
      WHERE jsonb_typeof(item)<>'string'
    )
    OR EXISTS(
      SELECT 1
      FROM jsonb_array_elements(
        COALESCE(NEW.request_summary_json->'availableFields','[]'::jsonb)
      ) AS item
      WHERE jsonb_typeof(item)<>'string'
    ) THEN
    RAISE EXCEPTION 'request summary arrays must contain only strings' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION platform.guard_report_ai_run_lifecycle()
RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,platform AS $$
BEGIN
  IF TG_OP='DELETE' THEN
    RAISE EXCEPTION 'report AI runs are append-only' USING ERRCODE='55000';
  END IF;
  IF TG_OP='INSERT' THEN
    IF NEW.state<>'RUNNING' OR NEW.response_summary_json IS NOT NULL
      OR NEW.error_code IS NOT NULL OR NEW.finished_at IS NOT NULL THEN
      RAISE EXCEPTION 'report AI run must begin in RUNNING' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.id<>NEW.id OR OLD.tenant_id<>NEW.tenant_id OR OLD.report_id<>NEW.report_id
    OR OLD.kind<>NEW.kind OR OLD.actor_user_id<>NEW.actor_user_id
    OR OLD.prompt_version<>NEW.prompt_version OR OLD.model_policy<>NEW.model_policy
    OR OLD.request_summary_json<>NEW.request_summary_json
    OR OLD.base_revision_no IS DISTINCT FROM NEW.base_revision_no
    OR OLD.scope_json IS DISTINCT FROM NEW.scope_json OR OLD.created_at<>NEW.created_at THEN
    RAISE EXCEPTION 'report AI run identity and request are immutable' USING ERRCODE='55000';
  END IF;
  IF OLD.state<>'RUNNING' OR NEW.state NOT IN ('SUCCEEDED','FAILED','REJECTED')
    OR NEW.finished_at IS NULL
    OR (NEW.state='SUCCEEDED' AND NEW.error_code IS NOT NULL)
    OR (NEW.state IN ('FAILED','REJECTED')
      AND length(btrim(COALESCE(NEW.error_code,''))) NOT BETWEEN 1 AND 128) THEN
    RAISE EXCEPTION 'illegal report AI run transition' USING ERRCODE='55000';
  END IF;
  RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION platform.guard_report_ai_operation_lifecycle()
RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,platform AS $$
BEGIN
  IF TG_OP='DELETE' THEN
    RAISE EXCEPTION 'report AI operations are append-only' USING ERRCODE='55000';
  END IF;
  IF TG_OP='INSERT' THEN
    IF NEW.applied_revision_no IS NOT NULL THEN
      RAISE EXCEPTION 'report AI operation cannot begin applied' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.id<>NEW.id OR OLD.tenant_id<>NEW.tenant_id OR OLD.ai_run_id<>NEW.ai_run_id
    OR OLD.operation_json<>NEW.operation_json OR OLD.validation_state<>NEW.validation_state
    OR OLD.rejection_code IS DISTINCT FROM NEW.rejection_code
    OR OLD.created_at<>NEW.created_at OR OLD.applied_revision_no IS NOT NULL
    OR NEW.applied_revision_no IS NULL OR NEW.applied_revision_no<=0
    OR OLD.validation_state<>'VALID'
    OR NOT EXISTS(
      SELECT 1 FROM platform.report_ai_runs AS run
      WHERE run.id=OLD.ai_run_id AND run.tenant_id=OLD.tenant_id
        AND run.state='SUCCEEDED'
    ) THEN
    RAISE EXCEPTION 'report AI operation audit is immutable' USING ERRCODE='55000';
  END IF;
  RETURN NEW;
END
$$;

DROP TRIGGER IF EXISTS report_ai_run_lifecycle_guard ON platform.report_ai_runs;
CREATE TRIGGER report_ai_run_lifecycle_guard
BEFORE INSERT OR UPDATE OR DELETE ON platform.report_ai_runs
FOR EACH ROW EXECUTE FUNCTION platform.guard_report_ai_run_lifecycle();

DROP TRIGGER IF EXISTS report_ai_operation_lifecycle_guard ON platform.report_ai_operations;
CREATE TRIGGER report_ai_operation_lifecycle_guard
BEFORE INSERT OR UPDATE OR DELETE ON platform.report_ai_operations
FOR EACH ROW EXECUTE FUNCTION platform.guard_report_ai_operation_lifecycle();

REVOKE ALL ON FUNCTION platform.guard_report_ai_summary() FROM PUBLIC;
REVOKE ALL ON FUNCTION platform.guard_report_ai_run_lifecycle() FROM PUBLIC;
REVOKE ALL ON FUNCTION platform.guard_report_ai_operation_lifecycle() FROM PUBLIC;
