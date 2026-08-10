ALTER TABLE askdata.quotas
  ADD CONSTRAINT askdata_quotas_scope_period_check CHECK(
    (scope_type='RUN' AND period='RUN')
    OR (scope_type<>'RUN' AND period IN ('DAY','MONTH'))
  );

CREATE INDEX askdata_cost_records_run_time_idx
  ON askdata.cost_records(tenant_id,run_id,created_at);

CREATE OR REPLACE FUNCTION askdata.load_quota_usage_snapshots(
  selected_domain_id uuid,
  selected_actor_id uuid,
  selected_run_id uuid,
  selected_at timestamptz
)
RETURNS TABLE(
  scope_type text,
  scope_id uuid,
  period text,
  llm_token_limit bigint,
  run_limit bigint,
  cost_limit_cents bigint,
  llm_tokens_used bigint,
  runs_used bigint,
  cost_cents_used bigint,
  reset_at timestamptz
)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path=pg_catalog,askdata,platform
AS $$
DECLARE selected_tenant_id uuid := askdata.current_tenant_id();
BEGIN
  IF selected_tenant_id IS NULL OR selected_domain_id IS NULL
    OR selected_actor_id IS NULL OR selected_run_id IS NULL OR selected_at IS NULL
    OR selected_actor_id<>askdata.current_actor_id()
    OR selected_domain_id<>askdata.current_domain_id()
    OR EXISTS(
      SELECT 1 FROM askdata.question_runs AS foreign_run
      WHERE foreign_run.tenant_id=selected_tenant_id AND foreign_run.id=selected_run_id
        AND (foreign_run.domain_id<>selected_domain_id OR foreign_run.actor_id<>selected_actor_id)
    ) THEN
    RAISE EXCEPTION 'ASKDATA_QUOTA_SCOPE_INVALID' USING ERRCODE='42501';
  END IF;

  RETURN QUERY
  WITH applicable AS (
    SELECT quota.*,
      CASE quota.period
        WHEN 'DAY' THEN date_trunc('day',selected_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'
        WHEN 'MONTH' THEN date_trunc('month',selected_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'
        ELSE COALESCE(question_run.created_at,selected_at)
      END AS period_start,
      CASE quota.period
        WHEN 'DAY' THEN (date_trunc('day',selected_at AT TIME ZONE 'UTC')+interval '1 day') AT TIME ZONE 'UTC'
        WHEN 'MONTH' THEN (date_trunc('month',selected_at AT TIME ZONE 'UTC')+interval '1 month') AT TIME ZONE 'UTC'
        ELSE selected_at+interval '10 minutes'
      END AS period_end
    FROM askdata.quotas AS quota
    LEFT JOIN askdata.question_runs AS question_run
      ON question_run.tenant_id=selected_tenant_id AND question_run.id=selected_run_id
    WHERE quota.tenant_id=selected_tenant_id AND (
      (quota.scope_type='TENANT' AND quota.scope_id=selected_tenant_id)
      OR (quota.scope_type='DOMAIN' AND quota.scope_id=selected_domain_id)
      OR (quota.scope_type='USER' AND quota.scope_id=selected_actor_id)
      OR (quota.scope_type='RUN' AND (
        quota.scope_id=selected_run_id OR (
          quota.scope_id=selected_tenant_id AND NOT EXISTS(
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
    applicable.period,
    applicable.llm_token_limit,applicable.run_limit,applicable.cost_limit_cents,
    COALESCE(cost_usage.llm_tokens_used,0),
    COALESCE(run_usage.runs_used,0),
    COALESCE(cost_usage.cost_cents_used,0),
    applicable.period_end
  FROM applicable
  LEFT JOIN LATERAL (
    SELECT
      COALESCE(sum(cost_record.prompt_tokens+cost_record.completion_tokens),0)::bigint AS llm_tokens_used,
      COALESCE(sum(cost_record.cost_cents),0)::bigint AS cost_cents_used
    FROM askdata.cost_records AS cost_record
    WHERE cost_record.tenant_id=selected_tenant_id
      AND cost_record.created_at>=applicable.period_start
      AND cost_record.created_at<applicable.period_end
      AND (applicable.scope_type<>'DOMAIN' OR cost_record.domain_id=selected_domain_id)
      AND (applicable.scope_type<>'USER' OR cost_record.actor_id=selected_actor_id)
      AND (applicable.scope_type<>'RUN' OR cost_record.run_id=selected_run_id)
  ) AS cost_usage ON true
  LEFT JOIN LATERAL (
    SELECT count(*)::bigint AS runs_used
    FROM askdata.question_runs AS governed_run
    WHERE governed_run.tenant_id=selected_tenant_id
      AND governed_run.created_at>=applicable.period_start
      AND governed_run.created_at<applicable.period_end
      AND (applicable.scope_type<>'DOMAIN' OR governed_run.domain_id=selected_domain_id)
      AND (applicable.scope_type<>'USER' OR governed_run.actor_id=selected_actor_id)
      AND (applicable.scope_type<>'RUN' OR governed_run.id=selected_run_id)
  ) AS run_usage ON true
  ORDER BY CASE applicable.scope_type
    WHEN 'TENANT' THEN 1 WHEN 'DOMAIN' THEN 2 WHEN 'USER' THEN 3 ELSE 4 END;
END
$$;

CREATE OR REPLACE FUNCTION askdata.record_cost_usage(
  selected_record_id uuid,
  selected_run_id uuid,
  selected_domain_id uuid,
  selected_actor_id uuid,
  selected_question_type text,
  selected_provider text,
  selected_model text,
  selected_prompt_tokens bigint,
  selected_completion_tokens bigint,
  selected_cost_cents bigint,
  selected_query_scan_bytes bigint
)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata,platform
AS $$
DECLARE selected_tenant_id uuid := askdata.current_tenant_id();
DECLARE inserted_count integer;
DECLARE existing askdata.cost_records%ROWTYPE;
BEGIN
  IF selected_tenant_id IS NULL OR selected_record_id IS NULL OR selected_run_id IS NULL
    OR selected_domain_id IS NULL OR selected_actor_id IS NULL
    OR (NOT askdata.system_access() AND (
      selected_actor_id<>askdata.current_actor_id()
      OR selected_domain_id<>askdata.current_domain_id()
    ))
    OR selected_question_type !~ '^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$'
    OR selected_provider !~ '^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$'
    OR selected_model !~ '^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$'
    OR selected_prompt_tokens<0 OR selected_completion_tokens<0
    OR selected_cost_cents<0 OR selected_query_scan_bytes<0
    OR (selected_prompt_tokens=0 AND selected_completion_tokens=0 AND selected_query_scan_bytes=0)
    OR NOT EXISTS(
      SELECT 1 FROM askdata.question_runs AS question_run
      WHERE question_run.tenant_id=selected_tenant_id
        AND question_run.domain_id=selected_domain_id
        AND question_run.actor_id=selected_actor_id
        AND question_run.id=selected_run_id
    ) THEN
    RAISE EXCEPTION 'ASKDATA_COST_USAGE_INVALID' USING ERRCODE='22023';
  END IF;

  INSERT INTO askdata.cost_records(
    id,run_id,tenant_id,domain_id,actor_id,question_type,provider,model,
    prompt_tokens,completion_tokens,cost_cents,query_scan_bytes,created_at
  ) VALUES(
    selected_record_id,selected_run_id,selected_tenant_id,selected_domain_id,
    selected_actor_id,selected_question_type,selected_provider,selected_model,
    selected_prompt_tokens,selected_completion_tokens,selected_cost_cents,
    selected_query_scan_bytes,clock_timestamp()
  ) ON CONFLICT(id) DO NOTHING;
  GET DIAGNOSTICS inserted_count=ROW_COUNT;
  IF inserted_count=1 THEN
    RETURN true;
  END IF;

  SELECT * INTO existing FROM askdata.cost_records
  WHERE tenant_id=selected_tenant_id AND id=selected_record_id;
  IF existing.id IS NULL OR existing.run_id<>selected_run_id
    OR existing.domain_id<>selected_domain_id OR existing.actor_id<>selected_actor_id
    OR existing.question_type<>selected_question_type OR existing.provider<>selected_provider
    OR existing.model<>selected_model OR existing.prompt_tokens<>selected_prompt_tokens
    OR existing.completion_tokens<>selected_completion_tokens
    OR existing.cost_cents<>selected_cost_cents
    OR existing.query_scan_bytes<>selected_query_scan_bytes THEN
    RAISE EXCEPTION 'ASKDATA_COST_IDEMPOTENCY_CONFLICT' USING ERRCODE='23505';
  END IF;
  RETURN false;
END
$$;

REVOKE ALL ON FUNCTION
  askdata.load_quota_usage_snapshots(uuid,uuid,uuid,timestamptz),
  askdata.record_cost_usage(uuid,uuid,uuid,uuid,text,text,text,bigint,bigint,bigint,bigint)
FROM PUBLIC;

COMMENT ON FUNCTION askdata.load_quota_usage_snapshots(uuid,uuid,uuid,timestamptz) IS
  'Returns aggregate-only tenant/domain/user/run quota usage without exposing another actor cost rows';
COMMENT ON FUNCTION askdata.record_cost_usage(uuid,uuid,uuid,uuid,text,text,text,bigint,bigint,bigint,bigint) IS
  'Idempotently records bounded AskData LLM/query cost facts for an exact governed question run';
