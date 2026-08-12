-- Database-derived rollout evidence and automatic stop-loss. Runtime samples
-- are aggregated from immutable Release pins on question runs; browser input
-- can never manufacture traffic or safety evidence.
BEGIN;

ALTER TABLE askdata.release_rollout_events
  DROP CONSTRAINT release_rollout_events_event_type_check;
ALTER TABLE askdata.release_rollout_events
  ADD CONSTRAINT release_rollout_events_event_type_check CHECK(event_type IN (
    'STARTED','ADVANCED','PAUSED','RESUMED','STOPPED','AUTO_STOPPED',
    'ACCEPTED','ACTIVATED','ROLLED_BACK'
  ));

CREATE OR REPLACE FUNCTION askdata.release_rollout_observability_internal(selected_rollout_id uuid)
RETURNS jsonb
LANGUAGE plpgsql STABLE SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE selected_rollout askdata.release_rollouts%ROWTYPE;
DECLARE gate_passed boolean := false;
DECLARE stage_elapsed_seconds bigint := 0;
DECLARE minimum_samples integer := 0;
DECLARE minimum_duration_seconds integer := 900;
DECLARE control_count integer := 0;
DECLARE candidate_count integer := 0;
DECLARE control_answered integer := 0;
DECLARE candidate_answered integer := 0;
DECLARE control_clarification integer := 0;
DECLARE candidate_clarification integer := 0;
DECLARE control_blocked integer := 0;
DECLARE candidate_blocked integer := 0;
DECLARE control_p95_ms bigint := 0;
DECLARE candidate_p95_ms bigint := 0;
DECLARE control_average_cost numeric := 0;
DECLARE candidate_average_cost numeric := 0;
DECLARE security_failure_count integer := 0;
DECLARE stop_codes text[] := ARRAY[]::text[];
DECLARE advance_codes text[] := ARRAY[]::text[];
DECLARE stop_required boolean := false;
DECLARE advance_allowed boolean := false;
BEGIN
  SELECT * INTO selected_rollout FROM askdata.release_rollouts
  WHERE id=selected_rollout_id;
  IF selected_rollout.id IS NULL THEN RETURN NULL; END IF;

  stage_elapsed_seconds := GREATEST(0,extract(epoch FROM (clock_timestamp()-selected_rollout.stage_started_at))::bigint);
  minimum_samples := CASE WHEN selected_rollout.stage='SHADOW' THEN 5 ELSE 20 END;
  SELECT EXISTS(SELECT 1 FROM askdata.release_evaluation_gate_receipts AS receipt
    WHERE receipt.tenant_id=selected_rollout.tenant_id
      AND receipt.domain_id=selected_rollout.domain_id
      AND receipt.release_id=selected_rollout.candidate_release_id
      AND receipt.passed) INTO gate_passed;

  WITH run_cost AS (
    SELECT run.id,run.release_id,run.current_state,run.disposition,run.completion_code,
      run.elapsed_ms,COALESCE(sum(cost.cost_cents),0)::numeric AS cost_cents
    FROM askdata.question_runs AS run
    LEFT JOIN askdata.cost_records AS cost
      ON cost.tenant_id=run.tenant_id AND cost.run_id=run.id
    WHERE run.tenant_id=selected_rollout.tenant_id
      AND run.domain_id=selected_rollout.domain_id
      AND run.release_id IN(selected_rollout.control_release_id,selected_rollout.candidate_release_id)
      AND run.created_at>=selected_rollout.stage_started_at
      AND run.current_state IN('ANSWERED','BLOCKED','CLARIFICATION_REQUIRED','CLARIFICATION_EXPIRED','OUT_OF_SCOPE')
    GROUP BY run.id,run.release_id,run.current_state,run.disposition,run.completion_code,run.elapsed_ms
  )
  SELECT
    count(*) FILTER(WHERE release_id=selected_rollout.control_release_id)::integer,
    count(*) FILTER(WHERE release_id=selected_rollout.candidate_release_id)::integer,
    count(*) FILTER(WHERE release_id=selected_rollout.control_release_id AND current_state='ANSWERED')::integer,
    count(*) FILTER(WHERE release_id=selected_rollout.candidate_release_id AND current_state='ANSWERED')::integer,
    count(*) FILTER(WHERE release_id=selected_rollout.control_release_id AND disposition='CLARIFY')::integer,
    count(*) FILTER(WHERE release_id=selected_rollout.candidate_release_id AND disposition='CLARIFY')::integer,
    count(*) FILTER(WHERE release_id=selected_rollout.control_release_id AND current_state IN('BLOCKED','OUT_OF_SCOPE'))::integer,
    count(*) FILTER(WHERE release_id=selected_rollout.candidate_release_id AND current_state IN('BLOCKED','OUT_OF_SCOPE'))::integer,
    COALESCE(percentile_disc(.95) WITHIN GROUP(ORDER BY elapsed_ms) FILTER(WHERE release_id=selected_rollout.control_release_id),0)::bigint,
    COALESCE(percentile_disc(.95) WITHIN GROUP(ORDER BY elapsed_ms) FILTER(WHERE release_id=selected_rollout.candidate_release_id),0)::bigint,
    COALESCE(avg(cost_cents) FILTER(WHERE release_id=selected_rollout.control_release_id),0),
    COALESCE(avg(cost_cents) FILTER(WHERE release_id=selected_rollout.candidate_release_id),0),
    count(*) FILTER(WHERE release_id=selected_rollout.candidate_release_id
      AND completion_code IN('SENSITIVE_LEAK_DETECTED','SECURITY_POLICY_FAILED','SECURITY_REGRESSION'))::integer
  INTO control_count,candidate_count,control_answered,candidate_answered,
    control_clarification,candidate_clarification,control_blocked,candidate_blocked,
    control_p95_ms,candidate_p95_ms,control_average_cost,candidate_average_cost,
    security_failure_count
  FROM run_cost;

  IF security_failure_count>0 THEN stop_codes:=array_append(stop_codes,'CANARY_SECURITY_REGRESSION'); END IF;
  IF selected_rollout.stage<>'SHADOW' AND candidate_count>=10 AND control_count>=10 THEN
    IF control_answered::numeric/control_count-candidate_answered::numeric/candidate_count>.10 THEN
      stop_codes:=array_append(stop_codes,'CANARY_ANSWER_RATE_REGRESSION');
    END IF;
    IF candidate_clarification::numeric/candidate_count-control_clarification::numeric/control_count>.15 THEN
      stop_codes:=array_append(stop_codes,'CANARY_CLARIFICATION_REGRESSION');
    END IF;
    IF control_p95_ms>0 AND candidate_p95_ms::numeric/control_p95_ms>2 THEN
      stop_codes:=array_append(stop_codes,'CANARY_LATENCY_REGRESSION');
    END IF;
    IF control_average_cost>0 AND candidate_average_cost/control_average_cost>2 THEN
      stop_codes:=array_append(stop_codes,'CANARY_COST_REGRESSION');
    END IF;
  END IF;
  stop_required := cardinality(stop_codes)>0;

  IF selected_rollout.state<>'RUNNING' THEN advance_codes:=array_append(advance_codes,'ROLLOUT_NOT_RUNNING'); END IF;
  IF NOT gate_passed THEN advance_codes:=array_append(advance_codes,'OFFLINE_GATE_REQUIRED'); END IF;
  IF stage_elapsed_seconds<minimum_duration_seconds THEN advance_codes:=array_append(advance_codes,'MINIMUM_STAGE_DURATION_REQUIRED'); END IF;
  IF selected_rollout.stage='SHADOW' THEN
    IF control_count<minimum_samples THEN advance_codes:=array_append(advance_codes,'SHADOW_CONTROL_OBSERVATIONS_REQUIRED'); END IF;
  ELSE
    IF control_count<minimum_samples THEN advance_codes:=array_append(advance_codes,'CANARY_CONTROL_SAMPLES_REQUIRED'); END IF;
    IF candidate_count<minimum_samples THEN advance_codes:=array_append(advance_codes,'CANARY_CANDIDATE_SAMPLES_REQUIRED'); END IF;
  END IF;
  advance_codes := advance_codes||stop_codes;
  advance_allowed := cardinality(advance_codes)=0;

  RETURN jsonb_build_object(
    'stage',selected_rollout.stage,'state',selected_rollout.state,
    'stageElapsedSeconds',stage_elapsed_seconds,
    'minimumDurationSeconds',minimum_duration_seconds,'minimumSamples',minimum_samples,
    'gatePassed',gate_passed,'controlSamples',control_count,'candidateSamples',candidate_count,
    'controlAnswered',control_answered,'candidateAnswered',candidate_answered,
    'controlClarifications',control_clarification,'candidateClarifications',candidate_clarification,
    'controlBlocked',control_blocked,'candidateBlocked',candidate_blocked,
    'controlP95LatencyMs',control_p95_ms,'candidateP95LatencyMs',candidate_p95_ms,
    'controlAverageCostCents',round(control_average_cost,2),
    'candidateAverageCostCents',round(candidate_average_cost,2),
    'stopRequired',stop_required,'stopCodes',to_jsonb(stop_codes),
    'advanceAllowed',advance_allowed,'advanceBlockedCodes',to_jsonb(advance_codes)
  );
END
$$;

CREATE OR REPLACE FUNCTION askdata.release_rollout_observability(selected_rollout_id uuid)
RETURNS jsonb
LANGUAGE plpgsql STABLE SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE selected_tenant_id uuid;
DECLARE selected_domain_id uuid;
BEGIN
  SELECT tenant_id,domain_id INTO selected_tenant_id,selected_domain_id
  FROM askdata.release_rollouts WHERE id=selected_rollout_id;
  IF selected_tenant_id IS NULL OR NOT askdata.evaluation_control_can_access(selected_tenant_id,selected_domain_id) THEN
    RETURN NULL;
  END IF;
  RETURN askdata.release_rollout_observability_internal(selected_rollout_id);
END
$$;

CREATE OR REPLACE FUNCTION askdata.auto_stop_release_rollout_from_run()
RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE selected_rollout askdata.release_rollouts%ROWTYPE;
DECLARE evidence jsonb;
DECLARE auto_reason_hash text;
BEGIN
  IF NEW.current_state NOT IN('ANSWERED','BLOCKED','CLARIFICATION_REQUIRED','CLARIFICATION_EXPIRED','OUT_OF_SCOPE')
    OR (TG_OP='UPDATE' AND NEW.current_state IS NOT DISTINCT FROM OLD.current_state) THEN RETURN NEW; END IF;
  SELECT * INTO selected_rollout FROM askdata.release_rollouts AS rollout
  WHERE rollout.tenant_id=NEW.tenant_id AND rollout.domain_id=NEW.domain_id
    AND NEW.release_id IN(rollout.control_release_id,rollout.candidate_release_id)
    AND rollout.stage<>'SHADOW' AND rollout.state='RUNNING'
  ORDER BY rollout.updated_at DESC,rollout.id DESC LIMIT 1 FOR UPDATE;
  IF selected_rollout.id IS NULL THEN RETURN NEW; END IF;
  evidence:=askdata.release_rollout_observability_internal(selected_rollout.id);
  IF COALESCE((evidence->>'stopRequired')::boolean,false) THEN
    auto_reason_hash:=encode(public.digest('release-rollout-auto-stop-v1:'||selected_rollout.id::text||':'||(evidence->'stopCodes')::text,'sha256'),'hex');
    UPDATE askdata.release_rollouts SET state='STOPPED',reason_hash=auto_reason_hash,
      stopped_at=clock_timestamp(),updated_at=clock_timestamp(),updated_by=selected_rollout.updated_by,
      version=version+1 WHERE id=selected_rollout.id AND state='RUNNING';
    INSERT INTO askdata.release_rollout_events(
      tenant_id,domain_id,rollout_id,candidate_release_id,event_type,from_stage,to_stage,
      actor_id,reason_hash,detail
    ) VALUES(selected_rollout.tenant_id,selected_rollout.domain_id,selected_rollout.id,
      selected_rollout.candidate_release_id,'AUTO_STOPPED',selected_rollout.stage,selected_rollout.stage,
      selected_rollout.updated_by,auto_reason_hash,jsonb_build_object(
        'automatic',true,'stopCodes',evidence->'stopCodes','evidence',evidence
      ));
  END IF;
  RETURN NEW;
END
$$;

CREATE TRIGGER askdata_question_runs_rollout_auto_stop
AFTER INSERT OR UPDATE OF current_state ON askdata.question_runs
FOR EACH ROW EXECUTE FUNCTION askdata.auto_stop_release_rollout_from_run();

REVOKE ALL ON FUNCTION
  askdata.release_rollout_observability_internal(uuid),
  askdata.release_rollout_observability(uuid),
  askdata.auto_stop_release_rollout_from_run()
FROM PUBLIC;

COMMIT;
