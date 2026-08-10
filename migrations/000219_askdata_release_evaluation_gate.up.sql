-- Database-recomputed semantic release evaluation gate. Every decision is
-- derived from sealed case/review facts and immutable per-case run rows; API
-- supplied summaries are deliberately absent from the function signature.

ALTER TABLE askdata.evaluation_cases
  ADD COLUMN shard_id smallint NOT NULL DEFAULT 1 CHECK(shard_id BETWEEN 1 AND 4),
  ADD COLUMN usage_count integer NOT NULL DEFAULT 0 CHECK(usage_count>=0),
  ADD COLUMN exposed_at timestamptz,
  ADD COLUMN retired_at timestamptz,
  ADD COLUMN retire_reason text CHECK(retire_reason IN ('USAGE_LIMIT','EXPOSED','SUPERSEDED')),
  ADD CONSTRAINT askdata_evaluation_cases_shard_retirement_shape_check CHECK(
    (retired_at IS NULL AND retire_reason IS NULL)
    OR (retired_at IS NOT NULL AND retire_reason IS NOT NULL)
  );

CREATE INDEX askdata_evaluation_cases_shard_idx
  ON askdata.evaluation_cases(
    tenant_id,evaluation_set_id,shard_id,retired_at,case_key,id
  );

-- Runtime rotation is separated from immutable sealed case content. The
-- columns above are the seal-time shard facts; this table is the mutable,
-- database-owned usage/exposure ledger.
CREATE TABLE askdata.evaluation_shard_states(
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  evaluation_set_id uuid NOT NULL,
  shard_id smallint NOT NULL CHECK(shard_id BETWEEN 1 AND 4),
  usage_count integer NOT NULL DEFAULT 0 CHECK(usage_count>=0),
  exposed_at timestamptz,
  retired_at timestamptz,
  retire_reason text CHECK(retire_reason IN ('USAGE_LIMIT','EXPOSED','SUPERSEDED')),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(tenant_id,evaluation_set_id,shard_id),
  CONSTRAINT askdata_evaluation_shard_states_set_fk FOREIGN KEY(
    evaluation_set_id,domain_id,tenant_id
  ) REFERENCES askdata.evaluation_sets(id,domain_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_evaluation_shard_states_retirement_shape_check CHECK(
    (retired_at IS NULL AND retire_reason IS NULL)
    OR (retired_at IS NOT NULL AND retire_reason IS NOT NULL)
  )
);

CREATE TABLE askdata.evaluation_shard_rotations(
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  evaluation_set_id uuid NOT NULL,
  next_shard_id smallint NOT NULL DEFAULT 1 CHECK(next_shard_id BETWEEN 1 AND 4),
  run_count bigint NOT NULL DEFAULT 0 CHECK(run_count>=0),
  version bigint NOT NULL DEFAULT 1 CHECK(version>0),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(tenant_id,evaluation_set_id),
  CONSTRAINT askdata_evaluation_shard_rotations_set_fk FOREIGN KEY(
    evaluation_set_id,domain_id,tenant_id
  ) REFERENCES askdata.evaluation_sets(id,domain_id,tenant_id) ON DELETE RESTRICT
);

CREATE TABLE askdata.evaluation_batch_plans(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  evaluation_set_id uuid NOT NULL,
  evaluation_batch_id uuid NOT NULL,
  run_kind text NOT NULL CHECK(run_kind IN (
    'REGULAR_RELEASE','FIRST_95_CLAIM','MAJOR_ARCHITECTURE_CHANGE','ANNUAL_REVIEW'
  )),
  shard_ids smallint[] NOT NULL CHECK(
    cardinality(shard_ids) BETWEEN 1 AND 4
    AND array_position(shard_ids,NULL) IS NULL
  ),
  can_issue_95_percent boolean NOT NULL,
  planned_by uuid NOT NULL,
  planned_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_evaluation_batch_plans_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_evaluation_batch_plans_batch_key UNIQUE(tenant_id,evaluation_batch_id),
  CONSTRAINT askdata_evaluation_batch_plans_set_fk FOREIGN KEY(
    evaluation_set_id,domain_id,tenant_id
  ) REFERENCES askdata.evaluation_sets(id,domain_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_evaluation_batch_plans_actor_fk FOREIGN KEY(planned_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_evaluation_batch_plans_conclusion_shape_check CHECK(
    can_issue_95_percent=(shard_ids=ARRAY[1,2,3,4]::smallint[])
  )
);

ALTER TABLE askdata.releases
  ADD CONSTRAINT askdata_releases_gate_pin_key UNIQUE(id,content_hash,domain_id,tenant_id);

-- Narrative verification is a separately append-only evaluation fact. It is
-- not inferred from prose or from the general runtime failure sampler.
CREATE TABLE askdata.evaluation_narrative_results(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  evaluation_set_id uuid NOT NULL,
  evaluation_batch_id uuid NOT NULL,
  evaluation_case_id uuid NOT NULL,
  release_id uuid NOT NULL,
  release_content_hash text NOT NULL CHECK(release_content_hash ~ '^[0-9a-f]{64}$'),
  passed boolean NOT NULL,
  evidence_hash text NOT NULL CHECK(evidence_hash ~ '^[0-9a-f]{64}$'),
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_evaluation_narrative_results_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_evaluation_narrative_results_batch_case_key UNIQUE(
    tenant_id,evaluation_batch_id,evaluation_case_id
  ),
  CONSTRAINT askdata_evaluation_narrative_results_case_fk FOREIGN KEY(
    evaluation_case_id,evaluation_set_id,domain_id,tenant_id
  ) REFERENCES askdata.evaluation_cases(
    id,evaluation_set_id,domain_id,tenant_id
  ) ON DELETE RESTRICT,
  CONSTRAINT askdata_evaluation_narrative_results_release_fk FOREIGN KEY(
    release_id,release_content_hash,domain_id,tenant_id
  ) REFERENCES askdata.releases(
    id,content_hash,domain_id,tenant_id
  ) ON DELETE RESTRICT
);

CREATE TABLE askdata.release_error_budget_receipts(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  release_id uuid NOT NULL,
  release_content_hash text NOT NULL CHECK(release_content_hash ~ '^[0-9a-f]{64}$'),
  evaluation_set_id uuid NOT NULL,
  evaluation_set_content_hash text NOT NULL CHECK(evaluation_set_content_hash ~ '^[0-9a-f]{64}$'),
  evaluation_batch_id uuid NOT NULL,
  report_json jsonb NOT NULL CHECK(
    jsonb_typeof(report_json)='object'
    AND pg_column_size(report_json)<=131072
    AND askdata.json_is_safe(report_json)
  ),
  residual_target double precision NOT NULL CHECK(residual_target>0 AND residual_target<=0.038),
  total_residual double precision NOT NULL CHECK(total_residual>=0 AND total_residual<=1),
  passed boolean NOT NULL,
  report_hash text NOT NULL CHECK(report_hash ~ '^[0-9a-f]{64}$'),
  recorded_by uuid NOT NULL,
  recorded_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_release_error_budget_receipts_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_release_error_budget_receipts_hash_key UNIQUE(
    tenant_id,release_id,evaluation_set_id,evaluation_batch_id,report_hash
  ),
  CONSTRAINT askdata_release_error_budget_receipts_release_fk FOREIGN KEY(
    release_id,release_content_hash,domain_id,tenant_id
  ) REFERENCES askdata.releases(id,content_hash,domain_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_release_error_budget_receipts_set_fk FOREIGN KEY(
    evaluation_set_id,domain_id,tenant_id
  ) REFERENCES askdata.evaluation_sets(id,domain_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_release_error_budget_receipts_actor_fk FOREIGN KEY(recorded_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_release_error_budget_receipts_pass_shape_check CHECK(
    passed=(total_residual<=residual_target)
  )
);

CREATE TABLE askdata.release_evaluation_gate_receipts(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  release_id uuid NOT NULL,
  release_content_hash text NOT NULL CHECK(release_content_hash ~ '^[0-9a-f]{64}$'),
  evaluation_set_id uuid NOT NULL,
  evaluation_set_content_hash text NOT NULL CHECK(evaluation_set_content_hash ~ '^[0-9a-f]{64}$'),
  evaluation_batch_id uuid NOT NULL,
  warehouse_snapshot_hash text CHECK(
    warehouse_snapshot_hash IS NULL OR warehouse_snapshot_hash ~ '^[0-9a-f]{64}$'
  ),
  case_count integer NOT NULL CHECK(case_count>=0),
  reviewed_case_count integer NOT NULL CHECK(reviewed_case_count>=0),
  strict_correct_count integer NOT NULL CHECK(strict_correct_count>=0),
  direct_expected_count integer NOT NULL CHECK(direct_expected_count>=0),
  direct_answer_count integer NOT NULL CHECK(direct_answer_count>=0),
  decision_expected_count integer NOT NULL CHECK(decision_expected_count>=0),
  decision_correct_count integer NOT NULL CHECK(decision_correct_count>=0),
  p0_case_count integer NOT NULL CHECK(p0_case_count>=0),
  p0_correct_count integer NOT NULL CHECK(p0_correct_count>=0),
  security_case_count integer NOT NULL CHECK(security_case_count>=0),
  security_passed_count integer NOT NULL CHECK(security_passed_count>=0),
  sensitive_leak_count integer NOT NULL CHECK(sensitive_leak_count>=0),
  narrative_case_count integer NOT NULL CHECK(narrative_case_count>=0),
  narrative_failure_count integer NOT NULL CHECK(narrative_failure_count>=0),
  sealed_shard_count smallint NOT NULL CHECK(sealed_shard_count BETWEEN 0 AND 4),
  strict_accuracy double precision NOT NULL CHECK(strict_accuracy BETWEEN 0 AND 1),
  wilson_lower_bound double precision NOT NULL CHECK(wilson_lower_bound BETWEEN 0 AND 1),
  direct_coverage double precision NOT NULL CHECK(direct_coverage BETWEEN 0 AND 1),
  decision_accuracy double precision NOT NULL CHECK(decision_accuracy BETWEEN 0 AND 1),
  narrative_failure_rate double precision NOT NULL CHECK(narrative_failure_rate BETWEEN 0 AND 1),
  error_budget_report_hash text CHECK(
    error_budget_report_hash IS NULL OR error_budget_report_hash ~ '^[0-9a-f]{64}$'
  ),
  passed boolean NOT NULL,
  failure_codes text[] NOT NULL DEFAULT '{}'::text[] CHECK(
    cardinality(failure_codes)<=32 AND array_position(failure_codes,NULL) IS NULL
  ),
  facts_json jsonb NOT NULL CHECK(
    jsonb_typeof(facts_json)='object'
    AND pg_column_size(facts_json)<=131072
    AND askdata.json_is_safe(facts_json)
  ),
  facts_hash text NOT NULL CHECK(facts_hash ~ '^[0-9a-f]{64}$'),
  receipt_hash text NOT NULL CHECK(receipt_hash ~ '^[0-9a-f]{64}$'),
  recomputed_by uuid NOT NULL,
  recomputed_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_release_evaluation_gate_receipts_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_release_evaluation_gate_receipts_hash_key UNIQUE(tenant_id,receipt_hash),
  CONSTRAINT askdata_release_evaluation_gate_receipts_release_fk FOREIGN KEY(
    release_id,release_content_hash,domain_id,tenant_id
  ) REFERENCES askdata.releases(id,content_hash,domain_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_release_evaluation_gate_receipts_set_fk FOREIGN KEY(
    evaluation_set_id,domain_id,tenant_id
  ) REFERENCES askdata.evaluation_sets(id,domain_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_release_evaluation_gate_receipts_actor_fk FOREIGN KEY(recomputed_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_release_evaluation_gate_receipts_pass_shape_check CHECK(
    passed=(cardinality(failure_codes)=0)
  )
);

CREATE INDEX askdata_release_evaluation_gate_receipts_subject_idx
  ON askdata.release_evaluation_gate_receipts(
    tenant_id,domain_id,release_id,evaluation_set_id,evaluation_batch_id,recomputed_at DESC,id
  );

CREATE OR REPLACE FUNCTION askdata.wilson_lower_bound(
  successes bigint,
  total bigint
)
RETURNS double precision
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
AS $$
  SELECT CASE WHEN total<=0 OR successes<0 OR successes>total THEN 0::double precision ELSE
    (
      successes::double precision/total
      + power(1.959963984540054,2)/(2*total)
      - 1.959963984540054*sqrt(
          (successes::double precision/total)*(1-successes::double precision/total)/total
          + power(1.959963984540054,2)/(4*power(total,2))
        )
    )/(1+power(1.959963984540054,2)/total)
  END
$$;

CREATE OR REPLACE FUNCTION askdata.record_release_error_budget(
  selected_release_id uuid,
  selected_evaluation_set_id uuid,
  selected_evaluation_batch_id uuid,
  selected_report jsonb,
  selected_actor_id uuid
)
RETURNS text
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata,platform
AS $$
DECLARE selected_release askdata.releases%ROWTYPE;
DECLARE selected_set askdata.evaluation_sets%ROWTYPE;
DECLARE selected_target double precision;
DECLARE selected_total double precision;
DECLARE selected_line_count integer;
DECLARE selected_stage_count integer;
DECLARE selected_invalid_count integer;
DECLARE selected_report_hash text;
BEGIN
  IF selected_actor_id IS NULL OR selected_actor_id<>askdata.current_actor_id()
    OR selected_evaluation_batch_id IS NULL
    OR selected_report IS NULL OR jsonb_typeof(selected_report)<>'object'
    OR pg_column_size(selected_report)>131072 OR NOT askdata.json_is_safe(selected_report) THEN
    RAISE EXCEPTION 'EVAL_ERROR_BUDGET_INVALID' USING ERRCODE='22023';
  END IF;
  SELECT * INTO selected_release FROM askdata.releases
  WHERE tenant_id=askdata.current_tenant_id() AND domain_id=askdata.current_domain_id()
    AND id=selected_release_id FOR SHARE;
  SELECT * INTO selected_set FROM askdata.evaluation_sets
  WHERE tenant_id=askdata.current_tenant_id() AND domain_id=askdata.current_domain_id()
    AND id=selected_evaluation_set_id FOR SHARE;
  IF selected_release.id IS NULL OR selected_set.id IS NULL
    OR selected_set.status<>'SEALED'
    OR selected_set.target_release_id<>selected_release.id
    OR selected_set.target_release_content_hash<>selected_release.content_hash
    OR askdata.evaluation_control_can_access(selected_release.tenant_id,selected_release.domain_id) IS DISTINCT FROM true THEN
    RAISE EXCEPTION 'EVAL_ERROR_BUDGET_SCOPE' USING ERRCODE='42501';
  END IF;
  BEGIN
    selected_target := (selected_report->>'residualTarget')::double precision;
  EXCEPTION WHEN OTHERS THEN
    RAISE EXCEPTION 'EVAL_ERROR_BUDGET_INVALID' USING ERRCODE='22023';
  END;
  IF selected_target<=0 OR selected_target>0.038
    OR jsonb_typeof(selected_report->'lines')<>'array' THEN
    RAISE EXCEPTION 'EVAL_ERROR_BUDGET_INVALID' USING ERRCODE='22023';
  END IF;
  BEGIN
    SELECT count(*),count(DISTINCT line->>'stage'),count(*) FILTER(WHERE
        line->>'stage' NOT IN ('INTENT','RECALL','BINDING','GRAPH','IR','PLAN','EXECUTION','VALIDATION','NARRATIVE','SECURITY')
        OR (line->>'errorRate')::double precision NOT BETWEEN 0 AND 1
        OR (line->>'budget')::double precision NOT BETWEEN 0 AND 1
        OR (line->>'recoveryRate')::double precision NOT BETWEEN 0 AND 1
        OR (COALESCE((line->>'recoveryMeasured')::boolean,false)=false AND (line->>'recoveryRate')::double precision<>0)
      ),sum(
        (line->>'errorRate')::double precision
        *(1-CASE WHEN COALESCE((line->>'recoveryMeasured')::boolean,false)
          THEN (line->>'recoveryRate')::double precision ELSE 0 END)
      )
    INTO selected_line_count,selected_stage_count,selected_invalid_count,selected_total
    FROM jsonb_array_elements(selected_report->'lines') AS line;
  EXCEPTION WHEN OTHERS THEN
    RAISE EXCEPTION 'EVAL_ERROR_BUDGET_INVALID' USING ERRCODE='22023';
  END;
  IF selected_line_count NOT BETWEEN 1 AND 32 OR selected_stage_count<>selected_line_count
    OR selected_invalid_count<>0 OR selected_total IS NULL OR selected_total NOT BETWEEN 0 AND 1 THEN
    RAISE EXCEPTION 'EVAL_ERROR_BUDGET_INVALID' USING ERRCODE='22023';
  END IF;
  selected_report_hash := encode(public.digest(selected_report::text,'sha256'),'hex');
  INSERT INTO askdata.release_error_budget_receipts(
    tenant_id,domain_id,release_id,release_content_hash,evaluation_set_id,
    evaluation_set_content_hash,evaluation_batch_id,report_json,residual_target,
    total_residual,passed,report_hash,recorded_by
  ) VALUES(
    selected_release.tenant_id,selected_release.domain_id,selected_release.id,
    selected_release.content_hash,selected_set.id,selected_set.sealed_content_hash,
    selected_evaluation_batch_id,selected_report,selected_target,selected_total,
    selected_total<=selected_target,selected_report_hash,selected_actor_id
  ) ON CONFLICT(tenant_id,release_id,evaluation_set_id,evaluation_batch_id,report_hash) DO NOTHING;
  RETURN selected_report_hash;
END
$$;

CREATE OR REPLACE FUNCTION askdata.plan_evaluation_batch(
  selected_evaluation_set_id uuid,
  selected_evaluation_batch_id uuid,
  selected_run_kind text,
  selected_actor_id uuid
)
RETURNS smallint[]
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata,platform
AS $$
DECLARE selected_set askdata.evaluation_sets%ROWTYPE;
DECLARE selected_rotation askdata.evaluation_shard_rotations%ROWTYPE;
DECLARE selected_shards smallint[];
DECLARE available_count integer;
BEGIN
  IF selected_actor_id IS NULL OR selected_actor_id<>askdata.current_actor_id()
    OR selected_evaluation_batch_id IS NULL
    OR selected_run_kind NOT IN ('REGULAR_RELEASE','FIRST_95_CLAIM','MAJOR_ARCHITECTURE_CHANGE','ANNUAL_REVIEW') THEN
    RAISE EXCEPTION 'EVAL_SHARD_PLAN_INVALID' USING ERRCODE='22023';
  END IF;
  SELECT * INTO selected_set FROM askdata.evaluation_sets
  WHERE tenant_id=askdata.current_tenant_id() AND domain_id=askdata.current_domain_id()
    AND id=selected_evaluation_set_id FOR SHARE;
  IF selected_set.id IS NULL OR selected_set.status<>'SEALED'
    OR selected_set.dataset_split<>'SEALED'
    OR askdata.evaluation_control_can_access(selected_set.tenant_id,selected_set.domain_id) IS DISTINCT FROM true THEN
    RAISE EXCEPTION 'EVAL_SHARD_PLAN_SCOPE' USING ERRCODE='42501';
  END IF;
  INSERT INTO askdata.evaluation_shard_states(tenant_id,domain_id,evaluation_set_id,shard_id)
  SELECT selected_set.tenant_id,selected_set.domain_id,selected_set.id,shard_id
  FROM generate_series(1,4) AS shard_id
  ON CONFLICT(tenant_id,evaluation_set_id,shard_id) DO NOTHING;
  INSERT INTO askdata.evaluation_shard_rotations(tenant_id,domain_id,evaluation_set_id)
  VALUES(selected_set.tenant_id,selected_set.domain_id,selected_set.id)
  ON CONFLICT(tenant_id,evaluation_set_id) DO NOTHING;
  SELECT * INTO selected_rotation FROM askdata.evaluation_shard_rotations
  WHERE tenant_id=selected_set.tenant_id AND evaluation_set_id=selected_set.id FOR UPDATE;
  SELECT count(DISTINCT state.shard_id) INTO available_count
  FROM askdata.evaluation_shard_states AS state
  WHERE state.tenant_id=selected_set.tenant_id
    AND state.evaluation_set_id=selected_set.id AND state.retired_at IS NULL
    AND EXISTS(
      SELECT 1 FROM askdata.evaluation_cases AS evaluation_case
      WHERE evaluation_case.tenant_id=state.tenant_id
        AND evaluation_case.evaluation_set_id=state.evaluation_set_id
        AND evaluation_case.shard_id=state.shard_id
    );
  IF available_count<>4 THEN
    RAISE EXCEPTION 'EVAL_FOUR_SHARDS_REQUIRED' USING ERRCODE='55000';
  END IF;
  IF selected_run_kind='REGULAR_RELEASE' THEN
    selected_shards := ARRAY[selected_rotation.next_shard_id]::smallint[];
  ELSE
    selected_shards := ARRAY[1,2,3,4]::smallint[];
  END IF;
  INSERT INTO askdata.evaluation_batch_plans(
    tenant_id,domain_id,evaluation_set_id,evaluation_batch_id,run_kind,
    shard_ids,can_issue_95_percent,planned_by
  ) VALUES(
    selected_set.tenant_id,selected_set.domain_id,selected_set.id,
    selected_evaluation_batch_id,selected_run_kind,selected_shards,
    selected_shards=ARRAY[1,2,3,4]::smallint[],selected_actor_id
  );
  UPDATE askdata.evaluation_shard_states SET
    usage_count=usage_count+1,
    retired_at=CASE WHEN usage_count+1>6 THEN clock_timestamp() ELSE retired_at END,
    retire_reason=CASE WHEN usage_count+1>6 THEN 'USAGE_LIMIT' ELSE retire_reason END,
    updated_at=clock_timestamp()
  WHERE tenant_id=selected_set.tenant_id AND evaluation_set_id=selected_set.id
    AND shard_id=ANY(selected_shards) AND retired_at IS NULL;
  UPDATE askdata.evaluation_shard_rotations SET
    next_shard_id=CASE WHEN selected_run_kind='REGULAR_RELEASE'
      THEN selected_rotation.next_shard_id%4+1 ELSE next_shard_id END,
    run_count=run_count+1,version=version+1,updated_at=clock_timestamp()
  WHERE tenant_id=selected_set.tenant_id AND evaluation_set_id=selected_set.id;
  RETURN selected_shards;
END
$$;

CREATE OR REPLACE FUNCTION askdata.expose_evaluation_shard(
  selected_evaluation_set_id uuid,
  selected_shard_id smallint,
  selected_actor_id uuid
)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata,platform
AS $$
DECLARE changed integer;
BEGIN
  IF selected_actor_id IS NULL OR selected_actor_id<>askdata.current_actor_id()
    OR selected_shard_id NOT BETWEEN 1 AND 4
    OR askdata.evaluation_control_can_access(askdata.current_tenant_id(),askdata.current_domain_id()) IS DISTINCT FROM true THEN
    RAISE EXCEPTION 'EVAL_SHARD_EXPOSURE_FORBIDDEN' USING ERRCODE='42501';
  END IF;
  INSERT INTO askdata.evaluation_shard_states(
    tenant_id,domain_id,evaluation_set_id,shard_id
  ) SELECT evaluation_set.tenant_id,evaluation_set.domain_id,evaluation_set.id,selected_shard_id
  FROM askdata.evaluation_sets AS evaluation_set
  WHERE evaluation_set.tenant_id=askdata.current_tenant_id()
    AND evaluation_set.domain_id=askdata.current_domain_id()
    AND evaluation_set.id=selected_evaluation_set_id
    AND evaluation_set.status='SEALED'
    AND EXISTS(
      SELECT 1 FROM askdata.evaluation_cases AS evaluation_case
      WHERE evaluation_case.tenant_id=evaluation_set.tenant_id
        AND evaluation_case.evaluation_set_id=evaluation_set.id
        AND evaluation_case.shard_id=selected_shard_id
    )
  ON CONFLICT(tenant_id,evaluation_set_id,shard_id) DO NOTHING;
  UPDATE askdata.evaluation_shard_states SET
    exposed_at=clock_timestamp(),retired_at=clock_timestamp(),
    retire_reason='EXPOSED',updated_at=clock_timestamp()
  WHERE tenant_id=askdata.current_tenant_id() AND domain_id=askdata.current_domain_id()
    AND evaluation_set_id=selected_evaluation_set_id AND shard_id=selected_shard_id
    AND retired_at IS NULL;
  GET DIAGNOSTICS changed=ROW_COUNT;
  RETURN changed=1;
END
$$;

CREATE OR REPLACE FUNCTION askdata.recompute_release_evaluation_gate(
  selected_release_id uuid,
  selected_evaluation_set_id uuid,
  selected_evaluation_batch_id uuid,
  selected_actor_id uuid
)
RETURNS TABLE(
  gate_passed boolean,
  gate_receipt_hash text,
  gate_failure_codes text[],
  gate_facts jsonb
)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata,platform
AS $$
DECLARE selected_release askdata.releases%ROWTYPE;
DECLARE selected_set askdata.evaluation_sets%ROWTYPE;
DECLARE selected_plan askdata.evaluation_batch_plans%ROWTYPE;
DECLARE selected_budget askdata.release_error_budget_receipts%ROWTYPE;
DECLARE run_count integer := 0;
DECLARE case_count integer := 0;
DECLARE reviewed_count integer := 0;
DECLARE strict_count integer := 0;
DECLARE direct_expected integer := 0;
DECLARE direct_actual integer := 0;
DECLARE decision_expected integer := 0;
DECLARE decision_correct integer := 0;
DECLARE p0_count integer := 0;
DECLARE p0_correct integer := 0;
DECLARE security_count integer := 0;
DECLARE security_correct integer := 0;
DECLARE leak_count integer := 0;
DECLARE narrative_count integer := 0;
DECLARE narrative_failures integer := 0;
DECLARE shard_count integer := 0;
DECLARE pin_count integer := 0;
DECLARE warehouse_count integer := 0;
DECLARE projection_count integer := 0;
DECLARE selected_warehouse_hash text;
DECLARE strict_rate double precision := 0;
DECLARE wilson_rate double precision := 0;
DECLARE direct_rate double precision := 0;
DECLARE decision_rate double precision := 0;
DECLARE narrative_rate double precision := 0;
DECLARE failures text[] := '{}'::text[];
DECLARE facts jsonb;
DECLARE facts_hash text;
DECLARE receipt_hash text;
BEGIN
  IF selected_actor_id IS NULL OR selected_actor_id<>askdata.current_actor_id()
    OR selected_evaluation_batch_id IS NULL THEN
    RAISE EXCEPTION 'EVAL_GATE_INVALID_IDENTITY' USING ERRCODE='22023';
  END IF;
  SELECT * INTO selected_release FROM askdata.releases
  WHERE tenant_id=askdata.current_tenant_id() AND domain_id=askdata.current_domain_id()
    AND id=selected_release_id FOR UPDATE;
  SELECT * INTO selected_set FROM askdata.evaluation_sets
  WHERE tenant_id=askdata.current_tenant_id() AND domain_id=askdata.current_domain_id()
    AND id=selected_evaluation_set_id FOR SHARE;
  IF selected_release.id IS NULL OR selected_set.id IS NULL
    OR askdata.evaluation_control_can_access(selected_release.tenant_id,selected_release.domain_id) IS DISTINCT FROM true THEN
    RAISE EXCEPTION 'EVAL_GATE_SCOPE' USING ERRCODE='42501';
  END IF;

  SELECT * INTO selected_plan FROM askdata.evaluation_batch_plans
  WHERE tenant_id=selected_release.tenant_id
    AND evaluation_set_id=selected_set.id
    AND evaluation_batch_id=selected_evaluation_batch_id;
  SELECT * INTO selected_budget FROM askdata.release_error_budget_receipts
  WHERE tenant_id=selected_release.tenant_id AND release_id=selected_release.id
    AND evaluation_set_id=selected_set.id AND evaluation_batch_id=selected_evaluation_batch_id
  ORDER BY recorded_at DESC,id DESC LIMIT 1;
  SELECT count(*)::integer INTO projection_count
  FROM askdata.release_projections AS projection
  WHERE projection.tenant_id=selected_release.tenant_id
    AND projection.domain_id=selected_release.domain_id
    AND projection.release_id=selected_release.id
    AND projection.status='READY'
    AND projection.expected_content_hash=selected_release.content_hash
    AND projection.applied_content_hash=selected_release.content_hash
    AND projection.resource_version<>'';

  WITH current_reviews AS (
    SELECT evaluation_case.id,count(DISTINCT review.reviewer_id) AS approval_count
    FROM askdata.evaluation_cases AS evaluation_case
    LEFT JOIN askdata.evaluation_case_reviews AS review
      ON review.tenant_id=evaluation_case.tenant_id
     AND review.evaluation_case_id=evaluation_case.id
     AND review.decision='APPROVED'
     AND review.reviewed_case_content_hash=evaluation_case.content_hash
     AND review.reviewer_id<>evaluation_case.created_by
     AND review.reviewer_id<>evaluation_case.content_updated_by
    WHERE evaluation_case.tenant_id=selected_release.tenant_id
      AND evaluation_case.evaluation_set_id=selected_set.id
    GROUP BY evaluation_case.id
  ), latest_runs AS (
    SELECT DISTINCT ON (run.evaluation_case_id) run.*
    FROM askdata.evaluation_runs AS run
    WHERE run.tenant_id=selected_release.tenant_id
      AND run.domain_id=selected_release.domain_id
      AND run.evaluation_set_id=selected_set.id
      AND run.evaluation_batch_id=selected_evaluation_batch_id
    ORDER BY run.evaluation_case_id,run.created_at DESC,run.id DESC
  )
  SELECT
    count(*)::integer,
    count(run.id)::integer,
    count(*) FILTER(WHERE current_reviews.approval_count=2)::integer,
    count(*) FILTER(WHERE run.strict_correct)::integer,
    count(*) FILTER(WHERE evaluation_case.expected_disposition='DIRECT')::integer,
    count(*) FILTER(WHERE evaluation_case.expected_disposition='DIRECT' AND run.actual_disposition='DIRECT')::integer,
    count(*) FILTER(WHERE evaluation_case.expected_disposition IN ('CLARIFY','REFUSE'))::integer,
    count(*) FILTER(WHERE evaluation_case.expected_disposition IN ('CLARIFY','REFUSE') AND run.actual_disposition=evaluation_case.expected_disposition)::integer,
    count(*) FILTER(WHERE evaluation_case.priority='P0')::integer,
    count(*) FILTER(WHERE evaluation_case.priority='P0' AND run.strict_correct)::integer,
    count(*) FILTER(WHERE evaluation_case.security_expectation<>'NONE')::integer,
    count(*) FILTER(WHERE evaluation_case.security_expectation<>'NONE' AND run.security_passed AND run.actual_disposition=evaluation_case.expected_disposition)::integer,
    count(*) FILTER(WHERE run.sensitive_leak_detected)::integer,
    count(DISTINCT evaluation_case.shard_id)::integer,
    count(*) FILTER(WHERE
      run.release_id=selected_release.id
      AND run.release_content_hash=selected_release.content_hash
      AND run.evaluation_set_content_hash=selected_set.sealed_content_hash
      AND run.case_content_hash=evaluation_case.content_hash
      AND run.warehouse_snapshot_hash IS NOT NULL
      AND run.warehouse_freshness_at IS NOT NULL
    )::integer,
    count(DISTINCT (run.warehouse_snapshot_hash,run.warehouse_freshness_at)) FILTER(
      WHERE run.warehouse_snapshot_hash IS NOT NULL AND run.warehouse_freshness_at IS NOT NULL
    )::integer,
    min(run.warehouse_snapshot_hash)
  INTO case_count,run_count,reviewed_count,strict_count,
    direct_expected,direct_actual,decision_expected,decision_correct,
    p0_count,p0_correct,security_count,security_correct,leak_count,
    shard_count,pin_count,warehouse_count,selected_warehouse_hash
  FROM askdata.evaluation_cases AS evaluation_case
  JOIN current_reviews ON current_reviews.id=evaluation_case.id
  LEFT JOIN latest_runs AS run ON run.evaluation_case_id=evaluation_case.id
  WHERE evaluation_case.tenant_id=selected_release.tenant_id
    AND evaluation_case.evaluation_set_id=selected_set.id;

  SELECT count(*)::integer,count(*) FILTER(WHERE NOT narrative.passed)::integer
  INTO narrative_count,narrative_failures
  FROM askdata.evaluation_narrative_results AS narrative
  WHERE narrative.tenant_id=selected_release.tenant_id
    AND narrative.domain_id=selected_release.domain_id
    AND narrative.release_id=selected_release.id
    AND narrative.release_content_hash=selected_release.content_hash
    AND narrative.evaluation_set_id=selected_set.id
    AND narrative.evaluation_batch_id=selected_evaluation_batch_id;

  strict_rate := CASE WHEN case_count=0 THEN 0 ELSE strict_count::double precision/case_count END;
  wilson_rate := askdata.wilson_lower_bound(strict_count,case_count);
  direct_rate := CASE WHEN direct_expected=0 THEN 0 ELSE direct_actual::double precision/direct_expected END;
  decision_rate := CASE WHEN decision_expected=0 THEN 0 ELSE decision_correct::double precision/decision_expected END;
  narrative_rate := CASE WHEN narrative_count=0 THEN 0 ELSE narrative_failures::double precision/narrative_count END;

  IF selected_release.status NOT IN ('READY','SUPERSEDED')
    OR selected_set.status<>'SEALED' OR selected_set.dataset_split<>'SEALED'
    OR selected_set.evaluation_mode<>'END_TO_END_RESULT_EQUIVALENCE'
    OR selected_set.target_release_id<>selected_release.id
    OR selected_set.target_release_content_hash<>selected_release.content_hash
    OR selected_set.sealed_content_hash IS NULL THEN failures:=array_append(failures,'EVAL_RELEASE_PIN'); END IF;
  IF projection_count<>4 THEN failures:=array_append(failures,'RELEASE_PROJECTION_MISMATCH'); END IF;
  IF case_count<2000 THEN failures:=array_append(failures,'EVAL_CASE_COUNT'); END IF;
  IF reviewed_count<>case_count THEN failures:=array_append(failures,'EVAL_INDEPENDENT_REVIEW'); END IF;
  IF run_count<>case_count OR pin_count<>case_count THEN failures:=array_append(failures,'EVAL_RUN_FACTS_INCOMPLETE'); END IF;
  IF warehouse_count<>1 THEN failures:=array_append(failures,'EVAL_WAREHOUSE_PIN'); END IF;
  IF strict_rate<0.96 THEN failures:=array_append(failures,'EVAL_STRICT_ACCURACY'); END IF;
  IF wilson_rate<0.95 THEN failures:=array_append(failures,'EVAL_WILSON_LOWER_BOUND'); END IF;
  IF direct_expected=0 OR direct_rate<0.85 THEN failures:=array_append(failures,'EVAL_DIRECT_COVERAGE'); END IF;
  IF decision_expected=0 OR decision_rate<0.95 THEN failures:=array_append(failures,'EVAL_CLARIFY_REFUSE_ACCURACY'); END IF;
  IF p0_count=0 OR p0_correct<>p0_count THEN failures:=array_append(failures,'EVAL_P0_ACCURACY'); END IF;
  IF security_count=0 OR security_correct<>security_count THEN failures:=array_append(failures,'EVAL_SECURITY'); END IF;
  IF leak_count<>0 THEN failures:=array_append(failures,'EVAL_SENSITIVE_LEAK'); END IF;
  IF narrative_count<>case_count OR narrative_rate>0.02 THEN failures:=array_append(failures,'EVAL_NARRATIVE_FAILURE'); END IF;
  IF selected_plan.id IS NULL OR NOT selected_plan.can_issue_95_percent
    OR selected_plan.shard_ids<>ARRAY[1,2,3,4]::smallint[] OR shard_count<>4 THEN
    failures:=array_append(failures,'EVAL_FOUR_SHARDS_REQUIRED');
  END IF;
  IF selected_budget.id IS NULL OR NOT selected_budget.passed THEN failures:=array_append(failures,'EVAL_ERROR_BUDGET'); END IF;
  SELECT COALESCE(array_agg(code ORDER BY code),'{}'::text[]) INTO failures
  FROM (SELECT DISTINCT unnest(failures) AS code) AS stable_failures;

  facts := jsonb_build_object(
    'databaseRecomputed',true,'releaseId',selected_release.id,
    'releaseContentHash',selected_release.content_hash,'releaseVersion',selected_release.version,
    'evaluationSetId',selected_set.id,
    'evaluationSetHash',selected_set.sealed_content_hash,'evaluationBatchId',selected_evaluation_batch_id,
    'warehouseSnapshotHash',selected_warehouse_hash,'caseCount',case_count,'runCount',run_count,
    'readyProjectionCount',projection_count,
    'reviewedCaseCount',reviewed_count,'strictCorrectCount',strict_count,
    'directExpectedCount',direct_expected,'directAnswerCount',direct_actual,
    'decisionExpectedCount',decision_expected,'decisionCorrectCount',decision_correct,
    'p0CaseCount',p0_count,'p0CorrectCount',p0_correct,
    'securityCaseCount',security_count,'securityPassedCount',security_correct,
    'sensitiveLeakCount',leak_count,'narrativeCaseCount',narrative_count,
    'narrativeFailureCount',narrative_failures,'sealedShardCount',shard_count,
    'errorBudgetReportHash',selected_budget.report_hash,'failureCodes',to_jsonb(failures)
  );
  facts_hash := encode(public.digest(facts::text,'sha256'),'hex');
  receipt_hash := encode(public.digest(
    'askdata-release-evaluation-gate-v1:'||facts_hash,'sha256'
  ),'hex');
  INSERT INTO askdata.release_evaluation_gate_receipts(
    tenant_id,domain_id,release_id,release_content_hash,evaluation_set_id,
    evaluation_set_content_hash,evaluation_batch_id,warehouse_snapshot_hash,
    case_count,reviewed_case_count,strict_correct_count,direct_expected_count,
    direct_answer_count,decision_expected_count,decision_correct_count,p0_case_count,
    p0_correct_count,security_case_count,security_passed_count,sensitive_leak_count,
    narrative_case_count,narrative_failure_count,sealed_shard_count,strict_accuracy,
    wilson_lower_bound,direct_coverage,decision_accuracy,narrative_failure_rate,
    error_budget_report_hash,passed,failure_codes,facts_json,facts_hash,receipt_hash,recomputed_by
  ) VALUES(
    selected_release.tenant_id,selected_release.domain_id,selected_release.id,
    selected_release.content_hash,selected_set.id,selected_set.sealed_content_hash,
    selected_evaluation_batch_id,selected_warehouse_hash,case_count,reviewed_count,
    strict_count,direct_expected,direct_actual,decision_expected,decision_correct,
    p0_count,p0_correct,security_count,security_correct,leak_count,narrative_count,
    narrative_failures,shard_count,strict_rate,wilson_rate,direct_rate,decision_rate,
    narrative_rate,selected_budget.report_hash,cardinality(failures)=0,failures,facts,
    facts_hash,receipt_hash,selected_actor_id
  ) ON CONFLICT ON CONSTRAINT askdata_release_evaluation_gate_receipts_hash_key DO NOTHING;
  RETURN QUERY SELECT cardinality(failures)=0,receipt_hash,failures,facts;
END
$$;

CREATE TRIGGER askdata_evaluation_batch_plans_immutable
BEFORE UPDATE OR DELETE ON askdata.evaluation_batch_plans
FOR EACH ROW EXECUTE FUNCTION askdata.reject_immutable_mutation();
CREATE TRIGGER askdata_evaluation_narrative_results_immutable
BEFORE UPDATE OR DELETE ON askdata.evaluation_narrative_results
FOR EACH ROW EXECUTE FUNCTION askdata.reject_immutable_mutation();
CREATE TRIGGER askdata_release_error_budget_receipts_immutable
BEFORE UPDATE OR DELETE ON askdata.release_error_budget_receipts
FOR EACH ROW EXECUTE FUNCTION askdata.reject_immutable_mutation();
CREATE TRIGGER askdata_release_evaluation_gate_receipts_immutable
BEFORE UPDATE OR DELETE ON askdata.release_evaluation_gate_receipts
FOR EACH ROW EXECUTE FUNCTION askdata.reject_immutable_mutation();

DO $rls$
DECLARE relation_name text;
BEGIN
  FOREACH relation_name IN ARRAY ARRAY[
    'evaluation_shard_states','evaluation_shard_rotations','evaluation_batch_plans',
    'evaluation_narrative_results','release_error_budget_receipts',
    'release_evaluation_gate_receipts'
  ] LOOP
    EXECUTE format('ALTER TABLE askdata.%I ENABLE ROW LEVEL SECURITY',relation_name);
    EXECUTE format('ALTER TABLE askdata.%I FORCE ROW LEVEL SECURITY',relation_name);
    EXECUTE format(
      'CREATE POLICY %I ON askdata.%I USING(askdata.evaluation_control_can_access(tenant_id,domain_id)) WITH CHECK(askdata.evaluation_control_can_access(tenant_id,domain_id))',
      'askdata_'||relation_name||'_management_isolation',relation_name
    );
  END LOOP;
END
$rls$;

REVOKE ALL ON FUNCTION
  askdata.wilson_lower_bound(bigint,bigint),
  askdata.record_release_error_budget(uuid,uuid,uuid,jsonb,uuid),
  askdata.plan_evaluation_batch(uuid,uuid,text,uuid),
  askdata.expose_evaluation_shard(uuid,smallint,uuid),
  askdata.recompute_release_evaluation_gate(uuid,uuid,uuid,uuid)
FROM PUBLIC;
REVOKE ALL ON TABLE
  askdata.evaluation_shard_states,askdata.evaluation_shard_rotations,
  askdata.evaluation_batch_plans,askdata.evaluation_narrative_results,
  askdata.release_error_budget_receipts,askdata.release_evaluation_gate_receipts
FROM PUBLIC;

COMMENT ON TABLE askdata.release_evaluation_gate_receipts IS
  'Append-only release gate receipts recomputed exclusively from sealed review, run, narrative, shard and error-budget facts';
COMMENT ON FUNCTION askdata.recompute_release_evaluation_gate(uuid,uuid,uuid,uuid) IS
  'Recomputes every 95 percent release gate from database facts; accepts no summary metrics';
