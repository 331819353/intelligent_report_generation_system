BEGIN;

-- Cross-release executions intentionally have different release, IR, query-plan
-- and validation identities.  Those hashes prove provenance, but cannot prove
-- whether the user-visible answer changed.  Shadow alignment therefore hashes
-- only the stable business-result projection retained in the terminal ANSWER.
CREATE OR REPLACE FUNCTION askdata.release_shadow_business_result_hash(
  selected_result jsonb
)
RETURNS text
LANGUAGE plpgsql IMMUTABLE PARALLEL SAFE
SET search_path=pg_catalog,public
AS $$
DECLARE business_result jsonb;
BEGIN
  IF selected_result IS NULL OR jsonb_typeof(selected_result)<>'object'
    OR jsonb_typeof(selected_result->'summary')<>'object'
    OR jsonb_typeof(selected_result->'datasets')<>'array'
    OR jsonb_typeof(selected_result->'views')<>'array' THEN
    RETURN NULL;
  END IF;
  business_result:=jsonb_build_object(
    'schemaVersion',selected_result->'schemaVersion',
    'title',selected_result->'title',
    -- The fallback snapshot timestamp is execution-time metadata and differs
    -- between the control and candidate.  A governed resolved time contract,
    -- when present, remains part of the business result.
    'resolvedTimeSpec',selected_result->'resolvedTimeSpec',
    'summary',jsonb_build_object(
      'metricLabel',selected_result#>'{summary,metricLabel}',
      'value',selected_result#>'{summary,value}',
      'formattedValue',selected_result#>'{summary,formattedValue}',
      'unit',selected_result#>'{summary,unit}'
    ),
    'datasets',selected_result->'datasets',
    'views',selected_result->'views',
    'defaultViewId',selected_result->'defaultViewId',
    'recommendedViewId',selected_result->'recommendedViewId'
  );
  RETURN encode(public.digest(business_result::text,'sha256'),'hex');
END
$$;

CREATE OR REPLACE FUNCTION askdata.record_release_shadow_observation()
RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,askdata,public
AS $$
DECLARE selected_job askdata.release_shadow_jobs%ROWTYPE;
DECLARE source askdata.question_runs%ROWTYPE;
DECLARE is_aligned boolean := false;
DECLARE security_ok boolean := true;
DECLARE leaked boolean := false;
DECLARE source_cost bigint := 0;
DECLARE shadow_cost bigint := 0;
DECLARE source_result jsonb;
DECLARE shadow_result jsonb;
DECLARE source_business_hash text;
DECLARE shadow_business_hash text;
BEGIN
  IF NEW.execution_mode<>'SHADOW'
    OR NEW.current_state NOT IN(
      'ANSWERED','BLOCKED','CLARIFICATION_REQUIRED','CLARIFICATION_EXPIRED','OUT_OF_SCOPE'
    ) OR (TG_OP='UPDATE' AND NEW.current_state IS NOT DISTINCT FROM OLD.current_state) THEN
    RETURN NEW;
  END IF;
  SELECT * INTO selected_job FROM askdata.release_shadow_jobs
  WHERE tenant_id=NEW.tenant_id AND shadow_run_id=NEW.id AND state='DISPATCHED'
  FOR UPDATE;
  IF selected_job.id IS NULL THEN RETURN NEW; END IF;
  SELECT * INTO source FROM askdata.question_runs
  WHERE tenant_id=NEW.tenant_id AND id=NEW.source_run_id;
  IF source.id IS NULL THEN RETURN NEW; END IF;

  leaked:=NEW.completion_code IN(
    'SENSITIVE_LEAK_DETECTED','SECURITY_POLICY_FAILED','SECURITY_REGRESSION'
  );
  security_ok:=NOT leaked;
  IF source.current_state='ANSWERED' AND NEW.current_state='ANSWERED' THEN
    SELECT artifact.payload_json->'result' INTO source_result
    FROM askdata.question_artifacts AS artifact
    WHERE artifact.tenant_id=source.tenant_id
      AND artifact.question_run_id=source.id
      AND artifact.artifact_hash=source.completion_artifact_hash
      AND artifact.artifact_type='ANSWER';
    SELECT artifact.payload_json->'result' INTO shadow_result
    FROM askdata.question_artifacts AS artifact
    WHERE artifact.tenant_id=NEW.tenant_id
      AND artifact.question_run_id=NEW.id
      AND artifact.artifact_hash=NEW.completion_artifact_hash
      AND artifact.artifact_type='ANSWER';
    source_business_hash:=askdata.release_shadow_business_result_hash(source_result);
    shadow_business_hash:=askdata.release_shadow_business_result_hash(shadow_result);
  END IF;
  is_aligned:=source.current_state=NEW.current_state
    AND source.disposition=NEW.disposition
    AND source.completion_code=NEW.completion_code
    AND CASE WHEN source.current_state='ANSWERED'
      THEN source_business_hash IS NOT NULL
        AND source_business_hash=shadow_business_hash
      ELSE true END;
  SELECT COALESCE(sum(cost_cents),0)::bigint INTO source_cost
    FROM askdata.cost_records WHERE tenant_id=NEW.tenant_id AND run_id=source.id;
  SELECT COALESCE(sum(cost_cents),0)::bigint INTO shadow_cost
    FROM askdata.cost_records WHERE tenant_id=NEW.tenant_id AND run_id=NEW.id;

  INSERT INTO askdata.release_shadow_observations(
    tenant_id,domain_id,rollout_id,job_id,source_run_id,shadow_run_id,
    control_release_id,candidate_release_id,aligned,security_passed,sensitive_leak,
    control_state,candidate_state,control_completion_code,candidate_completion_code,
    control_result_hash,candidate_result_hash,control_latency_ms,candidate_latency_ms,
    control_cost_cents,candidate_cost_cents
  ) VALUES(
    NEW.tenant_id,NEW.domain_id,selected_job.rollout_id,selected_job.id,source.id,NEW.id,
    source.release_id,NEW.release_id,is_aligned,security_ok,leaked,
    source.current_state,NEW.current_state,source.completion_code,NEW.completion_code,
    source_business_hash,shadow_business_hash,source.elapsed_ms,NEW.elapsed_ms,source_cost,shadow_cost
  ) ON CONFLICT(tenant_id,job_id) DO NOTHING;
  UPDATE askdata.release_shadow_jobs SET state='COMPLETED',updated_at=clock_timestamp()
  WHERE id=selected_job.id AND state='DISPATCHED';
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION askdata.release_shadow_business_result_hash(jsonb) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION askdata.release_shadow_business_result_hash(jsonb),
  askdata.record_release_shadow_observation() TO report_app,report_worker;

COMMIT;
