BEGIN;

CREATE OR REPLACE FUNCTION askdata.record_release_shadow_observation()
RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE selected_job askdata.release_shadow_jobs%ROWTYPE;
DECLARE source askdata.question_runs%ROWTYPE;
DECLARE is_aligned boolean := false;
DECLARE security_ok boolean := true;
DECLARE leaked boolean := false;
DECLARE source_cost bigint := 0;
DECLARE shadow_cost bigint := 0;
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
  is_aligned:=source.current_state=NEW.current_state
    AND source.disposition=NEW.disposition
    AND source.completion_code=NEW.completion_code
    AND CASE WHEN source.current_state='ANSWERED'
      THEN source.semantic_ir_hash IS NOT DISTINCT FROM NEW.semantic_ir_hash
        AND source.result_hash IS NOT DISTINCT FROM NEW.result_hash
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
    source.result_hash,NEW.result_hash,source.elapsed_ms,NEW.elapsed_ms,source_cost,shadow_cost
  ) ON CONFLICT(tenant_id,job_id) DO NOTHING;
  UPDATE askdata.release_shadow_jobs SET state='COMPLETED',updated_at=clock_timestamp()
  WHERE id=selected_job.id AND state='DISPATCHED';
  RETURN NEW;
END
$$;

DROP FUNCTION IF EXISTS askdata.release_shadow_business_result_hash(jsonb);

COMMIT;
