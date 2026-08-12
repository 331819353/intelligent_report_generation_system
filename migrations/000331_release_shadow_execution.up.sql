-- Execute a real hidden candidate run for every terminal control run observed
-- during the SHADOW stage. Evidence is derived from paired immutable runs;
-- browser input cannot create, alter or approve a shadow observation.
BEGIN;

ALTER TABLE askdata.question_runs
  ADD COLUMN execution_mode text NOT NULL DEFAULT 'USER',
  ADD COLUMN source_run_id uuid,
  ADD CONSTRAINT askdata_question_runs_execution_mode_check
    CHECK(execution_mode IN('USER','SHADOW')),
  ADD CONSTRAINT askdata_question_runs_execution_shape_check CHECK(
    (execution_mode='USER' AND source_run_id IS NULL)
    OR (execution_mode='SHADOW' AND source_run_id IS NOT NULL
      AND parent_run_id IS NULL AND conversation_id IS NOT NULL)
  ),
  ADD CONSTRAINT askdata_question_runs_source_fk
    FOREIGN KEY(source_run_id,actor_id,tenant_id)
    REFERENCES askdata.question_runs(id,actor_id,tenant_id) ON DELETE RESTRICT;

CREATE INDEX askdata_question_runs_shadow_source_idx
  ON askdata.question_runs(tenant_id,source_run_id,created_at,id)
  WHERE execution_mode='SHADOW';

CREATE TABLE askdata.release_shadow_jobs(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  rollout_id uuid NOT NULL,
  source_run_id uuid NOT NULL,
  candidate_release_id uuid NOT NULL,
  shadow_run_id uuid,
  state text NOT NULL DEFAULT 'PENDING' CHECK(state IN(
    'PENDING','RUNNING','DISPATCHED','COMPLETED','FAILED'
  )),
  attempt integer NOT NULL DEFAULT 0 CHECK(attempt BETWEEN 0 AND 5),
  lease_owner text NOT NULL DEFAULT '' CHECK(
    length(lease_owner)<=128 AND lease_owner !~ '[[:cntrl:]]'
  ),
  lease_token uuid,
  lease_expires_at timestamptz,
  last_error_code text NOT NULL DEFAULT '' CHECK(
    last_error_code='' OR last_error_code ~ '^[A-Z][A-Z0-9_]{0,127}$'
  ),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_release_shadow_jobs_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_release_shadow_jobs_rollout_source_key UNIQUE(tenant_id,rollout_id,source_run_id),
  CONSTRAINT askdata_release_shadow_jobs_rollout_fk FOREIGN KEY(rollout_id,tenant_id)
    REFERENCES askdata.release_rollouts(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_release_shadow_jobs_source_fk FOREIGN KEY(source_run_id,tenant_id)
    REFERENCES askdata.question_runs(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_release_shadow_jobs_shadow_fk FOREIGN KEY(shadow_run_id,tenant_id)
    REFERENCES askdata.question_runs(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_release_shadow_jobs_candidate_fk
    FOREIGN KEY(candidate_release_id,domain_id,tenant_id)
    REFERENCES askdata.releases(id,domain_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_release_shadow_jobs_lease_shape_check CHECK(
    (state='RUNNING' AND lease_owner<>'' AND lease_token IS NOT NULL AND lease_expires_at IS NOT NULL)
    OR (state<>'RUNNING' AND lease_owner='' AND lease_token IS NULL AND lease_expires_at IS NULL)
  ),
  CONSTRAINT askdata_release_shadow_jobs_run_shape_check CHECK(
    (state IN('PENDING','RUNNING','FAILED') AND shadow_run_id IS NULL)
    OR (state IN('DISPATCHED','COMPLETED') AND shadow_run_id IS NOT NULL)
  )
);

CREATE INDEX askdata_release_shadow_jobs_claim_idx
  ON askdata.release_shadow_jobs(tenant_id,state,lease_expires_at,created_at,id)
  WHERE state IN('PENDING','RUNNING');

CREATE TABLE askdata.release_shadow_observations(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  rollout_id uuid NOT NULL,
  job_id uuid NOT NULL,
  source_run_id uuid NOT NULL,
  shadow_run_id uuid NOT NULL,
  control_release_id uuid NOT NULL,
  candidate_release_id uuid NOT NULL,
  aligned boolean NOT NULL,
  security_passed boolean NOT NULL,
  sensitive_leak boolean NOT NULL,
  control_state text NOT NULL,
  candidate_state text NOT NULL,
  control_completion_code text NOT NULL,
  candidate_completion_code text NOT NULL,
  control_result_hash text,
  candidate_result_hash text,
  control_latency_ms bigint NOT NULL CHECK(control_latency_ms>=0),
  candidate_latency_ms bigint NOT NULL CHECK(candidate_latency_ms>=0),
  control_cost_cents bigint NOT NULL CHECK(control_cost_cents>=0),
  candidate_cost_cents bigint NOT NULL CHECK(candidate_cost_cents>=0),
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_release_shadow_observations_job_key UNIQUE(tenant_id,job_id),
  CONSTRAINT askdata_release_shadow_observations_pair_key UNIQUE(tenant_id,source_run_id,shadow_run_id),
  CONSTRAINT askdata_release_shadow_observations_rollout_fk FOREIGN KEY(rollout_id,tenant_id)
    REFERENCES askdata.release_rollouts(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_release_shadow_observations_job_fk FOREIGN KEY(job_id,tenant_id)
    REFERENCES askdata.release_shadow_jobs(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_release_shadow_observations_source_fk FOREIGN KEY(source_run_id,tenant_id)
    REFERENCES askdata.question_runs(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_release_shadow_observations_shadow_fk FOREIGN KEY(shadow_run_id,tenant_id)
    REFERENCES askdata.question_runs(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_release_shadow_observations_control_fk
    FOREIGN KEY(control_release_id,domain_id,tenant_id)
    REFERENCES askdata.releases(id,domain_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_release_shadow_observations_candidate_fk
    FOREIGN KEY(candidate_release_id,domain_id,tenant_id)
    REFERENCES askdata.releases(id,domain_id,tenant_id) ON DELETE RESTRICT
);

CREATE INDEX askdata_release_shadow_observations_rollout_idx
  ON askdata.release_shadow_observations(tenant_id,rollout_id,created_at,id);

ALTER TABLE askdata.release_shadow_jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE askdata.release_shadow_jobs FORCE ROW LEVEL SECURITY;
CREATE POLICY askdata_release_shadow_jobs_management_isolation
  ON askdata.release_shadow_jobs
  USING(askdata.evaluation_control_can_access(tenant_id,domain_id));
ALTER TABLE askdata.release_shadow_observations ENABLE ROW LEVEL SECURITY;
ALTER TABLE askdata.release_shadow_observations FORCE ROW LEVEL SECURITY;
CREATE POLICY askdata_release_shadow_observations_management_isolation
  ON askdata.release_shadow_observations
  USING(askdata.evaluation_control_can_access(tenant_id,domain_id));

CREATE OR REPLACE FUNCTION askdata.enforce_question_run_execution_mode()
RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE source askdata.question_runs%ROWTYPE;
DECLARE expected_shadow_source text;
BEGIN
  IF TG_OP='UPDATE' THEN
    IF NEW.execution_mode IS DISTINCT FROM OLD.execution_mode
      OR NEW.source_run_id IS DISTINCT FROM OLD.source_run_id THEN
      RAISE EXCEPTION 'question run execution identity is immutable' USING ERRCODE='55000';
    END IF;
    RETURN NEW;
  END IF;
  IF NEW.execution_mode='USER' THEN
    IF NEW.source_run_id IS NOT NULL THEN
      RAISE EXCEPTION 'user question run cannot bind a shadow source' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
  END IF;
  expected_shadow_source:=current_setting('askdata.shadow_source_run_id',true);
  SELECT * INTO source FROM askdata.question_runs
  WHERE id=NEW.source_run_id AND tenant_id=NEW.tenant_id
    AND domain_id=NEW.domain_id AND actor_id=NEW.actor_id FOR SHARE;
  IF NEW.execution_mode<>'SHADOW' OR expected_shadow_source IS NULL
    OR expected_shadow_source<>NEW.source_run_id::text
    OR source.id IS NULL OR source.execution_mode<>'USER'
    OR source.conversation_id IS DISTINCT FROM NEW.conversation_id
    OR source.question_hash<>NEW.question_hash
    OR source.current_state NOT IN(
      'ANSWERED','BLOCKED','CLARIFICATION_REQUIRED','CLARIFICATION_EXPIRED','OUT_OF_SCOPE'
    ) OR NEW.parent_run_id IS NOT NULL
    OR NOT EXISTS(
      SELECT 1 FROM askdata.release_shadow_jobs AS job
      JOIN askdata.release_rollouts AS rollout
        ON rollout.id=job.rollout_id AND rollout.tenant_id=job.tenant_id
      WHERE job.tenant_id=NEW.tenant_id AND job.domain_id=NEW.domain_id
        AND job.source_run_id=source.id AND job.candidate_release_id=NEW.release_id
        AND job.state='RUNNING' AND job.lease_token IS NOT NULL
        AND rollout.stage='SHADOW' AND rollout.state='RUNNING'
        AND rollout.control_release_id=source.release_id
        AND rollout.candidate_release_id=NEW.release_id
    ) THEN
    RAISE EXCEPTION 'shadow question run is not dispatcher-authorized' USING ERRCODE='42501';
  END IF;
  RETURN NEW;
END
$$;

CREATE TRIGGER askdata_question_runs_00_execution_mode
BEFORE INSERT OR UPDATE ON askdata.question_runs
FOR EACH ROW EXECUTE FUNCTION askdata.enforce_question_run_execution_mode();

-- The normal lock still governs every user-facing run. A READY candidate is
-- admitted only when the transaction carries the exact claimed shadow source.
CREATE OR REPLACE FUNCTION askdata.lock_active_question_release(
  selected_tenant_id uuid, selected_domain_id uuid,
  selected_release_id uuid, selected_content_hash text
)
RETURNS boolean
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE release_status text;
DECLARE release_valid boolean := false;
DECLARE routed_release_id uuid;
DECLARE routed_content_hash text;
DECLARE shadow_source_id uuid;
BEGIN
  IF NOT askdata.tenant_matches(selected_tenant_id)
    OR NOT askdata.domain_can_access(selected_domain_id)
    OR (NOT askdata.system_access() AND askdata.current_actor_id() IS NULL) THEN
    RETURN false;
  END IF;
  SELECT status INTO release_status FROM askdata.releases AS release
  WHERE release.id=selected_release_id AND release.domain_id=selected_domain_id
    AND release.tenant_id=selected_tenant_id AND release.content_hash=selected_content_hash
  FOR SHARE OF release;
  IF release_status='ACTIVE' THEN RETURN true; END IF;

  BEGIN
    shadow_source_id:=NULLIF(current_setting('askdata.shadow_source_run_id',true),'')::uuid;
  EXCEPTION WHEN invalid_text_representation THEN
    shadow_source_id:=NULL;
  END;
  IF release_status='READY' AND shadow_source_id IS NOT NULL AND EXISTS(
    SELECT 1 FROM askdata.release_shadow_jobs AS job
    JOIN askdata.release_rollouts AS rollout
      ON rollout.id=job.rollout_id AND rollout.tenant_id=job.tenant_id
    JOIN askdata.question_runs AS source
      ON source.id=job.source_run_id AND source.tenant_id=job.tenant_id
    WHERE job.tenant_id=selected_tenant_id AND job.domain_id=selected_domain_id
      AND job.source_run_id=shadow_source_id AND job.candidate_release_id=selected_release_id
      AND job.state='RUNNING' AND rollout.stage='SHADOW' AND rollout.state='RUNNING'
      AND source.actor_id=askdata.current_actor_id()
      AND source.release_id=rollout.control_release_id
  ) THEN RETURN true; END IF;

  SELECT route.release_id,route.content_hash INTO routed_release_id,routed_content_hash
  FROM askdata.resolve_question_release(
    selected_tenant_id,selected_domain_id,askdata.current_actor_id()
  ) AS route;
  IF routed_release_id=selected_release_id AND routed_content_hash=selected_content_hash THEN
    RETURN true;
  END IF;
  IF release_status NOT IN ('SUPERSEDED','RETAINED') THEN RETURN false; END IF;
  SELECT true INTO release_valid FROM askdata.conversations AS conversation
  WHERE conversation.tenant_id=selected_tenant_id
    AND conversation.domain_id=selected_domain_id
    AND conversation.actor_id=askdata.current_actor_id()
    AND conversation.pinned_release_id=selected_release_id
    AND conversation.pin_source IN ('REPORT_COMPONENT','SAVED_QUESTION')
  LIMIT 1;
  RETURN COALESCE(release_valid,false);
END
$$;

CREATE OR REPLACE FUNCTION askdata.schedule_release_shadow_job()
RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
BEGIN
  IF NEW.execution_mode<>'USER'
    OR NEW.current_state NOT IN(
      'ANSWERED','BLOCKED','CLARIFICATION_REQUIRED','CLARIFICATION_EXPIRED','OUT_OF_SCOPE'
    ) OR (TG_OP='UPDATE' AND NEW.current_state IS NOT DISTINCT FROM OLD.current_state) THEN
    RETURN NEW;
  END IF;
  INSERT INTO askdata.release_shadow_jobs(
    tenant_id,domain_id,rollout_id,source_run_id,candidate_release_id
  )
  SELECT rollout.tenant_id,rollout.domain_id,rollout.id,NEW.id,rollout.candidate_release_id
  FROM askdata.release_rollouts AS rollout
  WHERE rollout.tenant_id=NEW.tenant_id AND rollout.domain_id=NEW.domain_id
    AND rollout.control_release_id=NEW.release_id
    AND rollout.stage='SHADOW' AND rollout.state='RUNNING'
    AND NEW.created_at>=rollout.stage_started_at
    AND NEW.conversation_id IS NOT NULL
  ON CONFLICT(tenant_id,rollout_id,source_run_id) DO NOTHING;
  RETURN NEW;
END
$$;

CREATE TRIGGER askdata_question_runs_schedule_shadow
AFTER INSERT OR UPDATE OF current_state ON askdata.question_runs
FOR EACH ROW EXECUTE FUNCTION askdata.schedule_release_shadow_job();

CREATE OR REPLACE FUNCTION askdata.list_release_shadow_job_tenants()
RETURNS TABLE(tenant_id uuid)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
  SELECT DISTINCT job.tenant_id
  FROM askdata.release_shadow_jobs AS job
  JOIN askdata.release_rollouts AS rollout
    ON rollout.id=job.rollout_id AND rollout.tenant_id=job.tenant_id
  WHERE rollout.stage='SHADOW' AND rollout.state='RUNNING'
    AND (job.state='PENDING'
      OR (job.state='RUNNING' AND job.lease_expires_at<=clock_timestamp()))
    AND job.attempt<5
  ORDER BY job.tenant_id
$$;

CREATE OR REPLACE FUNCTION askdata.claim_release_shadow_job(
  selected_tenant_id uuid,
  selected_worker_id text,
  selected_lease_seconds integer
)
RETURNS TABLE(
  claimed_job_id uuid,
  claimed_domain_id uuid,
  claimed_rollout_id uuid,
  claimed_actor_id uuid,
  claimed_conversation_id uuid,
  claimed_source_run_id uuid,
  claimed_source_release_id uuid,
  claimed_source_release_content_hash text,
  claimed_candidate_release_id uuid,
  claimed_candidate_content_hash text,
  claimed_question_hash text,
  claimed_max_steps integer,
  claimed_max_llm_calls integer,
  claimed_max_tool_calls integer,
  claimed_max_formal_queries integer,
  claimed_max_validation_queries integer,
  claimed_max_duration_ms integer,
  claimed_lease_token uuid
)
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
BEGIN
  IF selected_tenant_id IS NULL
    OR length(btrim(selected_worker_id)) NOT BETWEEN 1 AND 128
    OR selected_worker_id ~ '[[:cntrl:]]'
    OR selected_lease_seconds NOT BETWEEN 30 AND 600 THEN
    RAISE EXCEPTION 'invalid release shadow job lease parameters' USING ERRCODE='22023';
  END IF;
  RETURN QUERY
  WITH candidate AS(
    SELECT job.id
    FROM askdata.release_shadow_jobs AS job
    JOIN askdata.release_rollouts AS rollout
      ON rollout.id=job.rollout_id AND rollout.tenant_id=job.tenant_id
    WHERE job.tenant_id=selected_tenant_id
      AND rollout.stage='SHADOW' AND rollout.state='RUNNING'
      AND (job.state='PENDING'
        OR (job.state='RUNNING' AND job.lease_expires_at<=clock_timestamp()))
      AND job.attempt<5
    ORDER BY job.created_at,job.id
    FOR UPDATE OF job SKIP LOCKED LIMIT 1
  ), claimed AS(
    UPDATE askdata.release_shadow_jobs AS job SET
      state='RUNNING',lease_owner=btrim(selected_worker_id),lease_token=gen_random_uuid(),
      lease_expires_at=clock_timestamp()+make_interval(secs=>selected_lease_seconds),
      attempt=job.attempt+1,last_error_code='',updated_at=clock_timestamp()
    FROM candidate WHERE job.id=candidate.id
    RETURNING job.*
  )
  SELECT claimed.id,claimed.domain_id,claimed.rollout_id,source.actor_id,
    source.conversation_id,source.id,source.release_id,source.release_content_hash,
    candidate_release.id,candidate_release.content_hash,source.question_hash,
    source.max_steps,source.max_llm_calls,source.max_tool_calls,
    source.max_formal_queries,source.max_validation_queries,source.max_duration_ms,
    claimed.lease_token
  FROM claimed
  JOIN askdata.question_runs AS source
    ON source.id=claimed.source_run_id AND source.tenant_id=claimed.tenant_id
  JOIN askdata.releases AS candidate_release
    ON candidate_release.id=claimed.candidate_release_id
   AND candidate_release.tenant_id=claimed.tenant_id
   AND candidate_release.domain_id=claimed.domain_id
  WHERE source.execution_mode='USER' AND candidate_release.status='READY';
END
$$;

CREATE OR REPLACE FUNCTION askdata.complete_release_shadow_job(
  selected_job_id uuid,
  selected_lease_token uuid,
  selected_shadow_run_id uuid,
  selected_error_code text
)
RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE selected_job askdata.release_shadow_jobs%ROWTYPE;
DECLARE selected_shadow askdata.question_runs%ROWTYPE;
DECLARE changed integer := 0;
BEGIN
  IF selected_job_id IS NULL OR selected_lease_token IS NULL
    OR (selected_error_code<>'' AND selected_error_code !~ '^[A-Z][A-Z0-9_]{0,127}$') THEN
    RAISE EXCEPTION 'invalid release shadow completion' USING ERRCODE='22023';
  END IF;
  SELECT * INTO selected_job FROM askdata.release_shadow_jobs
  WHERE id=selected_job_id AND state='RUNNING' AND lease_token=selected_lease_token
    AND lease_expires_at>clock_timestamp() FOR UPDATE;
  IF selected_job.id IS NULL THEN RETURN false; END IF;
  IF selected_error_code='' THEN
    SELECT * INTO selected_shadow FROM askdata.question_runs
    WHERE id=selected_shadow_run_id AND tenant_id=selected_job.tenant_id
      AND domain_id=selected_job.domain_id AND source_run_id=selected_job.source_run_id
      AND release_id=selected_job.candidate_release_id AND execution_mode='SHADOW';
    IF selected_shadow.id IS NULL THEN
      RAISE EXCEPTION 'shadow run does not match the claimed job' USING ERRCODE='23514';
    END IF;
    UPDATE askdata.release_shadow_jobs SET state='DISPATCHED',shadow_run_id=selected_shadow.id,
      lease_owner='',lease_token=NULL,lease_expires_at=NULL,last_error_code='',updated_at=clock_timestamp()
    WHERE id=selected_job.id;
  ELSE
    -- A created terminal Shadow run still produces an observation; otherwise
    -- the job is failed without inventing a sample.
    IF selected_shadow_run_id IS NOT NULL THEN
      SELECT * INTO selected_shadow FROM askdata.question_runs
      WHERE id=selected_shadow_run_id AND tenant_id=selected_job.tenant_id
        AND source_run_id=selected_job.source_run_id AND execution_mode='SHADOW';
    END IF;
    IF selected_shadow.id IS NOT NULL THEN
      UPDATE askdata.release_shadow_jobs SET state='DISPATCHED',shadow_run_id=selected_shadow.id,
        lease_owner='',lease_token=NULL,lease_expires_at=NULL,last_error_code=selected_error_code,
        updated_at=clock_timestamp() WHERE id=selected_job.id;
    ELSE
      UPDATE askdata.release_shadow_jobs SET state='FAILED',shadow_run_id=NULL,
        lease_owner='',lease_token=NULL,lease_expires_at=NULL,last_error_code=selected_error_code,
        updated_at=clock_timestamp() WHERE id=selected_job.id;
    END IF;
  END IF;
  GET DIAGNOSTICS changed=ROW_COUNT;
  RETURN changed=1;
END
$$;

-- Claim includes execution identity and refuses a Shadow row until its fresh
-- envelope is present and the dispatch job is durable.
DROP FUNCTION askdata.claim_question_run(uuid,text,integer);
CREATE OR REPLACE FUNCTION askdata.claim_question_run(
  selected_tenant_id uuid,
  selected_worker_id text,
  selected_lease_seconds integer
)
RETURNS TABLE(
  claimed_run_id uuid,
  claimed_domain_id uuid,
  claimed_actor_id uuid,
  claimed_execution_mode text,
  claimed_source_run_id uuid,
  claimed_release_id uuid,
  claimed_release_content_hash text,
  claimed_current_state text,
  claimed_record_version bigint,
  claimed_lease_token uuid,
  claimed_attempt integer,
  claimed_resume_mode text
)
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
BEGIN
  IF selected_tenant_id IS NULL
    OR length(btrim(selected_worker_id)) NOT BETWEEN 1 AND 128
    OR selected_worker_id ~ '[[:cntrl:]]'
    OR selected_lease_seconds NOT BETWEEN 30 AND 600 THEN
    RAISE EXCEPTION 'invalid question run lease parameters' USING ERRCODE='22023';
  END IF;
  RETURN QUERY
  WITH candidate AS(
    SELECT run.id,CASE WHEN run.current_state='RECEIVED' THEN 'FRESH' ELSE 'ABANDONED' END AS mode
    FROM askdata.question_runs AS run
    LEFT JOIN askdata.question_run_leases AS lease
      ON lease.run_id=run.id AND lease.tenant_id=run.tenant_id
    WHERE run.tenant_id=selected_tenant_id
      AND run.current_state NOT IN(
        'CLARIFICATION_REQUIRED','CLARIFICATION_EXPIRED','OUT_OF_SCOPE','ANSWERED','BLOCKED'
      )
      AND (lease.run_id IS NULL OR lease.lease_owner='' OR lease.lease_expires_at<=now())
      AND COALESCE(lease.attempt,0)<3
      AND (run.execution_mode='USER' OR EXISTS(
        SELECT 1 FROM askdata.release_shadow_jobs AS job
        JOIN askdata.question_envelopes AS envelope
          ON envelope.tenant_id=run.tenant_id AND envelope.run_id=run.id
        WHERE job.tenant_id=run.tenant_id AND job.shadow_run_id=run.id
          AND job.state='DISPATCHED' AND envelope.expires_at>clock_timestamp()
      ))
    ORDER BY run.created_at,run.id
    FOR UPDATE OF run SKIP LOCKED LIMIT 1
  ), leased AS(
    INSERT INTO askdata.question_run_leases AS lease(
      run_id,tenant_id,domain_id,lease_owner,lease_token,lease_expires_at,attempt
    )
    SELECT run.id,run.tenant_id,run.domain_id,btrim(selected_worker_id),gen_random_uuid(),
      now()+make_interval(secs=>selected_lease_seconds),1
    FROM candidate JOIN askdata.question_runs AS run ON run.id=candidate.id
    ON CONFLICT(run_id) DO UPDATE SET
      lease_owner=btrim(selected_worker_id),lease_token=gen_random_uuid(),
      lease_expires_at=now()+make_interval(secs=>selected_lease_seconds),attempt=lease.attempt+1
    RETURNING lease.run_id,lease.lease_token,lease.attempt
  )
  SELECT run.id,run.domain_id,run.actor_id,run.execution_mode,run.source_run_id,
    run.release_id,run.release_content_hash,run.current_state,run.record_version,
    leased.lease_token,leased.attempt,candidate.mode
  FROM leased JOIN askdata.question_runs AS run ON run.id=leased.run_id
  JOIN candidate ON candidate.id=leased.run_id;
END
$$;

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

CREATE TRIGGER askdata_question_runs_record_shadow_observation
AFTER INSERT OR UPDATE OF current_state ON askdata.question_runs
FOR EACH ROW EXECUTE FUNCTION askdata.record_release_shadow_observation();

CREATE TRIGGER askdata_release_shadow_observations_immutable
BEFORE UPDATE OR DELETE ON askdata.release_shadow_observations
FOR EACH ROW EXECUTE FUNCTION askdata.reject_immutable_mutation();

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
DECLARE shadow_aligned_count integer := 0;
DECLARE shadow_pending_count integer := 0;
DECLARE stop_codes text[] := ARRAY[]::text[];
DECLARE advance_codes text[] := ARRAY[]::text[];
DECLARE stop_required boolean := false;
DECLARE advance_allowed boolean := false;
BEGIN
  SELECT * INTO selected_rollout FROM askdata.release_rollouts WHERE id=selected_rollout_id;
  IF selected_rollout.id IS NULL THEN RETURN NULL; END IF;
  stage_elapsed_seconds:=GREATEST(0,extract(epoch FROM(clock_timestamp()-selected_rollout.stage_started_at))::bigint);
  minimum_samples:=CASE WHEN selected_rollout.stage='SHADOW' THEN 5 ELSE 20 END;
  SELECT EXISTS(SELECT 1 FROM askdata.release_evaluation_gate_receipts AS receipt
    WHERE receipt.tenant_id=selected_rollout.tenant_id
      AND receipt.domain_id=selected_rollout.domain_id
      AND receipt.release_id=selected_rollout.candidate_release_id AND receipt.passed)
  INTO gate_passed;

  IF selected_rollout.stage='SHADOW' THEN
    SELECT count(*)::integer,
      count(*) FILTER(WHERE observation.aligned)::integer,
      count(*) FILTER(WHERE NOT observation.security_passed OR observation.sensitive_leak)::integer,
      count(*) FILTER(WHERE observation.control_state='ANSWERED')::integer,
      count(*) FILTER(WHERE observation.candidate_state='ANSWERED')::integer,
      count(*) FILTER(WHERE observation.control_state IN('CLARIFICATION_REQUIRED','CLARIFICATION_EXPIRED'))::integer,
      count(*) FILTER(WHERE observation.candidate_state IN('CLARIFICATION_REQUIRED','CLARIFICATION_EXPIRED'))::integer,
      count(*) FILTER(WHERE observation.control_state IN('BLOCKED','OUT_OF_SCOPE'))::integer,
      count(*) FILTER(WHERE observation.candidate_state IN('BLOCKED','OUT_OF_SCOPE'))::integer,
      COALESCE(percentile_disc(.95) WITHIN GROUP(ORDER BY observation.control_latency_ms),0)::bigint,
      COALESCE(percentile_disc(.95) WITHIN GROUP(ORDER BY observation.candidate_latency_ms),0)::bigint,
      COALESCE(avg(observation.control_cost_cents),0),
      COALESCE(avg(observation.candidate_cost_cents),0)
    INTO control_count,shadow_aligned_count,security_failure_count,
      control_answered,candidate_answered,control_clarification,candidate_clarification,
      control_blocked,candidate_blocked,control_p95_ms,candidate_p95_ms,
      control_average_cost,candidate_average_cost
    FROM askdata.release_shadow_observations AS observation
    WHERE observation.tenant_id=selected_rollout.tenant_id
      AND observation.rollout_id=selected_rollout.id
      AND observation.created_at>=selected_rollout.stage_started_at;
    candidate_count:=control_count;
    SELECT count(*)::integer INTO shadow_pending_count
    FROM askdata.release_shadow_jobs AS job
    WHERE job.tenant_id=selected_rollout.tenant_id AND job.rollout_id=selected_rollout.id
      AND job.state IN('PENDING','RUNNING','DISPATCHED');
  ELSE
    WITH run_cost AS(
      SELECT run.id,run.release_id,run.current_state,run.disposition,run.completion_code,
        run.elapsed_ms,COALESCE(sum(cost.cost_cents),0)::numeric AS cost_cents
      FROM askdata.question_runs AS run
      LEFT JOIN askdata.cost_records AS cost ON cost.tenant_id=run.tenant_id AND cost.run_id=run.id
      WHERE run.tenant_id=selected_rollout.tenant_id AND run.domain_id=selected_rollout.domain_id
        AND run.execution_mode='USER'
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
      control_p95_ms,candidate_p95_ms,control_average_cost,candidate_average_cost,security_failure_count
    FROM run_cost;
  END IF;

  IF security_failure_count>0 THEN
    stop_codes:=array_append(stop_codes,CASE WHEN selected_rollout.stage='SHADOW'
      THEN 'SHADOW_SECURITY_REGRESSION' ELSE 'CANARY_SECURITY_REGRESSION' END);
  END IF;
  IF selected_rollout.stage='SHADOW' THEN
    IF control_count>=minimum_samples AND shadow_aligned_count<control_count THEN
      stop_codes:=array_append(stop_codes,'SHADOW_ALIGNMENT_REGRESSION');
    END IF;
  ELSIF candidate_count>=10 AND control_count>=10 THEN
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
  stop_required:=cardinality(stop_codes)>0;
  IF selected_rollout.state<>'RUNNING' THEN advance_codes:=array_append(advance_codes,'ROLLOUT_NOT_RUNNING'); END IF;
  IF NOT gate_passed THEN advance_codes:=array_append(advance_codes,'OFFLINE_GATE_REQUIRED'); END IF;
  IF stage_elapsed_seconds<minimum_duration_seconds THEN advance_codes:=array_append(advance_codes,'MINIMUM_STAGE_DURATION_REQUIRED'); END IF;
  IF selected_rollout.stage='SHADOW' THEN
    IF control_count<minimum_samples THEN advance_codes:=array_append(advance_codes,'SHADOW_PAIRED_OBSERVATIONS_REQUIRED'); END IF;
    IF shadow_pending_count>0 THEN advance_codes:=array_append(advance_codes,'SHADOW_RUNS_PENDING'); END IF;
    IF control_count>0 AND shadow_aligned_count<control_count THEN advance_codes:=array_append(advance_codes,'SHADOW_ALIGNMENT_REQUIRED'); END IF;
  ELSE
    IF control_count<minimum_samples THEN advance_codes:=array_append(advance_codes,'CANARY_CONTROL_SAMPLES_REQUIRED'); END IF;
    IF candidate_count<minimum_samples THEN advance_codes:=array_append(advance_codes,'CANARY_CANDIDATE_SAMPLES_REQUIRED'); END IF;
  END IF;
  advance_codes:=advance_codes||stop_codes;
  advance_allowed:=cardinality(advance_codes)=0;
  RETURN jsonb_build_object(
    'stage',selected_rollout.stage,'state',selected_rollout.state,
    'stageElapsedSeconds',stage_elapsed_seconds,'minimumDurationSeconds',minimum_duration_seconds,
    'minimumSamples',minimum_samples,'gatePassed',gate_passed,
    'controlSamples',control_count,'candidateSamples',candidate_count,
    'controlAnswered',control_answered,'candidateAnswered',candidate_answered,
    'controlClarifications',control_clarification,'candidateClarifications',candidate_clarification,
    'controlBlocked',control_blocked,'candidateBlocked',candidate_blocked,
    'controlP95LatencyMs',control_p95_ms,'candidateP95LatencyMs',candidate_p95_ms,
    'controlAverageCostCents',round(control_average_cost,2),
    'candidateAverageCostCents',round(candidate_average_cost,2),
    'shadowAlignedSamples',shadow_aligned_count,'shadowPendingSamples',shadow_pending_count,
    'shadowSecurityFailures',security_failure_count,
    'stopRequired',stop_required,'stopCodes',to_jsonb(stop_codes),
    'advanceAllowed',advance_allowed,'advanceBlockedCodes',to_jsonb(advance_codes)
  );
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
  IF NEW.execution_mode<>'USER'
    OR NEW.current_state NOT IN('ANSWERED','BLOCKED','CLARIFICATION_REQUIRED','CLARIFICATION_EXPIRED','OUT_OF_SCOPE')
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

CREATE OR REPLACE FUNCTION askdata.auto_stop_release_shadow_from_observation()
RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE selected_rollout askdata.release_rollouts%ROWTYPE;
DECLARE evidence jsonb;
DECLARE auto_reason_hash text;
BEGIN
  SELECT * INTO selected_rollout FROM askdata.release_rollouts
  WHERE id=NEW.rollout_id AND tenant_id=NEW.tenant_id
    AND stage='SHADOW' AND state='RUNNING' FOR UPDATE;
  IF selected_rollout.id IS NULL THEN RETURN NEW; END IF;
  evidence:=askdata.release_rollout_observability_internal(selected_rollout.id);
  IF COALESCE((evidence->>'stopRequired')::boolean,false) THEN
    auto_reason_hash:=encode(public.digest('release-shadow-auto-stop-v1:'||selected_rollout.id::text||':'||(evidence->'stopCodes')::text,'sha256'),'hex');
    UPDATE askdata.release_rollouts SET state='STOPPED',reason_hash=auto_reason_hash,
      stopped_at=clock_timestamp(),updated_at=clock_timestamp(),updated_by=selected_rollout.updated_by,
      version=version+1 WHERE id=selected_rollout.id AND state='RUNNING';
    INSERT INTO askdata.release_rollout_events(
      tenant_id,domain_id,rollout_id,candidate_release_id,event_type,from_stage,to_stage,
      actor_id,reason_hash,detail
    ) VALUES(selected_rollout.tenant_id,selected_rollout.domain_id,selected_rollout.id,
      selected_rollout.candidate_release_id,'AUTO_STOPPED','SHADOW','SHADOW',selected_rollout.updated_by,
      auto_reason_hash,jsonb_build_object('automatic',true,'stopCodes',evidence->'stopCodes','evidence',evidence));
  END IF;
  RETURN NEW;
END
$$;

CREATE TRIGGER askdata_release_shadow_observations_auto_stop
AFTER INSERT ON askdata.release_shadow_observations
FOR EACH ROW EXECUTE FUNCTION askdata.auto_stop_release_shadow_from_observation();

-- Hidden work is accounted for in the immutable cost ledger, but it cannot
-- consume the user's product quota. USER and canary runs remain chargeable.
CREATE OR REPLACE FUNCTION askdata.load_quota_usage_snapshots(
  selected_domain_id uuid,
  selected_actor_id uuid,
  selected_run_id uuid,
  selected_at timestamptz
)
RETURNS TABLE(
  scope_type text,scope_id uuid,period text,llm_token_limit bigint,run_limit bigint,
  cost_limit_cents bigint,llm_tokens_used bigint,runs_used bigint,cost_cents_used bigint,
  reset_at timestamptz
)
LANGUAGE plpgsql STABLE SECURITY DEFINER
SET search_path=pg_catalog,askdata,platform
AS $$
DECLARE selected_tenant_id uuid:=askdata.current_tenant_id();
BEGIN
  IF selected_tenant_id IS NULL OR selected_domain_id IS NULL OR selected_actor_id IS NULL
    OR selected_run_id IS NULL OR selected_at IS NULL
    OR selected_actor_id<>askdata.current_actor_id() OR selected_domain_id<>askdata.current_domain_id()
    OR EXISTS(SELECT 1 FROM askdata.question_runs AS foreign_run
      WHERE foreign_run.tenant_id=selected_tenant_id AND foreign_run.id=selected_run_id
        AND(foreign_run.domain_id<>selected_domain_id OR foreign_run.actor_id<>selected_actor_id)) THEN
    RAISE EXCEPTION 'ASKDATA_QUOTA_SCOPE_INVALID' USING ERRCODE='42501';
  END IF;
  RETURN QUERY
  WITH applicable AS(
    SELECT quota.*,
      CASE quota.period WHEN 'DAY' THEN date_trunc('day',selected_at AT TIME ZONE'UTC')AT TIME ZONE'UTC'
        WHEN 'MONTH' THEN date_trunc('month',selected_at AT TIME ZONE'UTC')AT TIME ZONE'UTC'
        ELSE COALESCE(question_run.created_at,selected_at) END AS period_start,
      CASE quota.period WHEN 'DAY' THEN(date_trunc('day',selected_at AT TIME ZONE'UTC')+interval'1 day')AT TIME ZONE'UTC'
        WHEN 'MONTH' THEN(date_trunc('month',selected_at AT TIME ZONE'UTC')+interval'1 month')AT TIME ZONE'UTC'
        ELSE selected_at+interval'10 minutes' END AS period_end
    FROM askdata.quotas AS quota LEFT JOIN askdata.question_runs AS question_run
      ON question_run.tenant_id=selected_tenant_id AND question_run.id=selected_run_id
    WHERE quota.tenant_id=selected_tenant_id AND (
      (quota.scope_type='TENANT' AND quota.scope_id=selected_tenant_id)
      OR (quota.scope_type='DOMAIN' AND quota.scope_id=selected_domain_id)
      OR (quota.scope_type='USER' AND quota.scope_id=selected_actor_id)
      OR (quota.scope_type='RUN' AND (
        quota.scope_id=selected_run_id
        OR (
          quota.scope_id=selected_tenant_id
          AND NOT EXISTS(
            SELECT 1 FROM askdata.quotas AS exact_run_quota
            WHERE exact_run_quota.tenant_id=selected_tenant_id
              AND exact_run_quota.scope_type='RUN'
              AND exact_run_quota.scope_id=selected_run_id
          )
        )
      ))
    )
  )
  SELECT applicable.scope_type,
    CASE WHEN applicable.scope_type='RUN' THEN selected_run_id ELSE applicable.scope_id END,
    applicable.period,applicable.llm_token_limit,applicable.run_limit,applicable.cost_limit_cents,
    COALESCE(cost_usage.llm_tokens_used,0),COALESCE(run_usage.runs_used,0),
    COALESCE(cost_usage.cost_cents_used,0),applicable.period_end
  FROM applicable
  LEFT JOIN LATERAL(
    SELECT COALESCE(sum(cost_record.prompt_tokens+cost_record.completion_tokens),0)::bigint AS llm_tokens_used,
      COALESCE(sum(cost_record.cost_cents),0)::bigint AS cost_cents_used
    FROM askdata.cost_records AS cost_record JOIN askdata.question_runs AS cost_run
      ON cost_run.tenant_id=cost_record.tenant_id AND cost_run.id=cost_record.run_id
    WHERE cost_record.tenant_id=selected_tenant_id AND cost_run.execution_mode='USER'
      AND cost_record.created_at>=applicable.period_start AND cost_record.created_at<applicable.period_end
      AND(applicable.scope_type<>'DOMAIN' OR cost_record.domain_id=selected_domain_id)
      AND(applicable.scope_type<>'USER' OR cost_record.actor_id=selected_actor_id)
      AND(applicable.scope_type<>'RUN' OR cost_record.run_id=selected_run_id)
  )AS cost_usage ON true
  LEFT JOIN LATERAL(
    SELECT count(*)::bigint AS runs_used FROM askdata.question_runs AS governed_run
    WHERE governed_run.tenant_id=selected_tenant_id AND governed_run.execution_mode='USER'
      AND governed_run.created_at>=applicable.period_start AND governed_run.created_at<applicable.period_end
      AND(applicable.scope_type<>'DOMAIN' OR governed_run.domain_id=selected_domain_id)
      AND(applicable.scope_type<>'USER' OR governed_run.actor_id=selected_actor_id)
      AND(applicable.scope_type<>'RUN' OR governed_run.id=selected_run_id)
  )AS run_usage ON true
  ORDER BY CASE applicable.scope_type WHEN'TENANT'THEN 1 WHEN'DOMAIN'THEN 2 WHEN'USER'THEN 3 ELSE 4 END;
END
$$;

-- Existing side-effect surfaces may reference only user-visible runs.
CREATE OR REPLACE FUNCTION askdata.reject_shadow_run_side_effect()
RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE selected_run_id uuid;
DECLARE new_row jsonb;
BEGIN
  new_row:=to_jsonb(NEW);
  selected_run_id:=NULLIF(
    COALESCE(new_row->>'source_question_run_id',new_row->>'question_run_id'),''
  )::uuid;
  IF selected_run_id IS NOT NULL AND EXISTS(
    SELECT 1 FROM askdata.question_runs
    WHERE tenant_id=NEW.tenant_id AND id=selected_run_id AND execution_mode='SHADOW'
  ) THEN
    RAISE EXCEPTION 'shadow question run cannot create user-visible side effects' USING ERRCODE='42501';
  END IF;
  RETURN NEW;
END
$$;

CREATE TRIGGER askdata_query_feedback_00_user_run
BEFORE INSERT OR UPDATE ON askdata.query_feedback
FOR EACH ROW EXECUTE FUNCTION askdata.reject_shadow_run_side_effect();
CREATE TRIGGER askdata_saved_questions_00_user_run
BEFORE INSERT OR UPDATE ON askdata.saved_questions
FOR EACH ROW EXECUTE FUNCTION askdata.reject_shadow_run_side_effect();
CREATE TRIGGER askdata_feedback_tickets_00_user_run
BEFORE INSERT OR UPDATE ON askdata.feedback_tickets
FOR EACH ROW EXECUTE FUNCTION askdata.reject_shadow_run_side_effect();
CREATE TRIGGER askdata_add_to_report_intents_00_user_run
BEFORE INSERT OR UPDATE ON askdata.add_to_report_intents
FOR EACH ROW EXECUTE FUNCTION askdata.reject_shadow_run_side_effect();

REVOKE ALL ON FUNCTION
  askdata.enforce_question_run_execution_mode(),askdata.schedule_release_shadow_job(),
  askdata.list_release_shadow_job_tenants(),askdata.claim_release_shadow_job(uuid,text,integer),
  askdata.complete_release_shadow_job(uuid,uuid,uuid,text),
  askdata.record_release_shadow_observation(),askdata.auto_stop_release_shadow_from_observation(),
  askdata.reject_shadow_run_side_effect()
FROM PUBLIC;

COMMIT;
