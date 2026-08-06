-- Governed AskData question runtime and append-only replay audit. The control
-- plane stores hashes and bounded sanitized contracts, never raw questions,
-- prompts, hidden reasoning, SQL, parameter values or result rows.

CREATE OR REPLACE FUNCTION askdata.question_audit_json_is_safe(document jsonb)
RETURNS boolean
LANGUAGE plpgsql
IMMUTABLE
STRICT
AS $$
DECLARE
  item record;
  normalized_key text;
BEGIN
  IF NOT askdata.json_is_safe(document) THEN
    RETURN false;
  END IF;
  IF jsonb_typeof(document)='object' THEN
    FOR item IN SELECT key,value FROM jsonb_each(document)
    LOOP
      normalized_key := regexp_replace(lower(item.key),'[^a-z0-9]','','g');
      IF normalized_key IN (
        'question','questiontext','questionsummary','rawquestion',
        'prompt','prompts','systemprompt','developerprompt','userprompt',
        'messages','messagehistory','reasoning','reasoningcontent',
        'chainofthought','thought','thoughts','cot','analysis','modelanalysis',
        'parameters','params','parametervalues','bindparameters','bindvalues',
        'arguments','toolarguments','queryparameters','queryparams',
        'sqlparameters','sqlparams','sqltext','sqlquery','querytext',
        'rawquery','statementtext','resultrows','resultset','resultdata',
        'datarows','recordset','rawresult','rawresultdata','rawresponse',
        'response','responsebody','completion','completionbody',
        'modeloutput','modelresponse','requestbody','requestpayload',
        'resultpayload',
        'answer','answertext','naturalanswer'
      ) THEN
        RETURN false;
      END IF;
      IF NOT askdata.question_audit_json_is_safe(item.value) THEN
        RETURN false;
      END IF;
    END LOOP;
  ELSIF jsonb_typeof(document)='array' THEN
    FOR item IN SELECT value FROM jsonb_array_elements(document)
    LOOP
      IF NOT askdata.question_audit_json_is_safe(item.value) THEN
        RETURN false;
      END IF;
    END LOOP;
  END IF;
  RETURN true;
END
$$;

CREATE OR REPLACE FUNCTION askdata.question_runtime_can_access(
  selected_tenant_id uuid,
  selected_domain_id uuid,
  selected_actor_id uuid
)
RETURNS boolean
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
  SELECT askdata.tenant_matches(selected_tenant_id)
    AND askdata.domain_can_access(selected_domain_id)
    AND (
      askdata.system_access()
      OR (
        selected_actor_id IS NOT NULL
        AND selected_actor_id=askdata.current_actor_id()
      )
    )
$$;

-- Take a transaction-scoped lock on the exact ACTIVE release pin without
-- granting the application role UPDATE on governed release rows. This closes
-- the status-change race between validating a pin and creating its run.
CREATE OR REPLACE FUNCTION askdata.lock_active_question_release(
  selected_tenant_id uuid,
  selected_domain_id uuid,
  selected_release_id uuid,
  selected_content_hash text
)
RETURNS boolean
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE
  release_valid boolean := false;
BEGIN
  IF NOT askdata.tenant_matches(selected_tenant_id)
    OR NOT askdata.domain_can_access(selected_domain_id)
    OR (
      NOT askdata.system_access()
      AND askdata.current_actor_id() IS NULL
    ) THEN
    RETURN false;
  END IF;

  SELECT true INTO release_valid
  FROM askdata.releases AS release
  WHERE release.id=selected_release_id
    AND release.domain_id=selected_domain_id
    AND release.tenant_id=selected_tenant_id
    AND release.content_hash=selected_content_hash
    AND release.status='ACTIVE'
  FOR SHARE OF release;

  RETURN COALESCE(release_valid,false);
END
$$;

CREATE TABLE askdata.question_runs(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  actor_id uuid NOT NULL,
  conversation_id uuid,
  parent_run_id uuid,
  trace_id uuid NOT NULL DEFAULT gen_random_uuid(),
  idempotency_key_hash text NOT NULL CHECK(idempotency_key_hash ~ '^[0-9a-f]{64}$'),
  question_hash text NOT NULL CHECK(question_hash ~ '^[0-9a-f]{64}$'),
  policy_scope_hash text NOT NULL CHECK(policy_scope_hash ~ '^[0-9a-f]{64}$'),
  release_id uuid NOT NULL,
  release_content_hash text NOT NULL CHECK(release_content_hash ~ '^[0-9a-f]{64}$'),
  current_state text NOT NULL DEFAULT 'RECEIVED' CHECK(current_state IN (
    'RECEIVED','AUTHORIZED','CONTEXT_READY','UNDERSTANDING','RETRIEVING',
    'BINDING','GRAPH_VALIDATING','IR_READY','PLAN_VALIDATING','EXECUTING',
    'RESULT_VERIFYING','CLARIFICATION_REQUIRED','ANSWERED','BLOCKED'
  )),
  disposition text NOT NULL DEFAULT 'PENDING' CHECK(disposition IN (
    'PENDING','DIRECT','CLARIFY','REFUSE'
  )),
  completion_code text NOT NULL DEFAULT '' CHECK(
    completion_code='' OR completion_code ~ '^[A-Z][A-Z0-9_]{0,127}$'
  ),
  completion_artifact_hash text CHECK(
    completion_artifact_hash IS NULL
    OR completion_artifact_hash ~ '^[0-9a-f]{64}$'
  ),
  understanding_hash text CHECK(
    understanding_hash IS NULL OR understanding_hash ~ '^[0-9a-f]{64}$'
  ),
  binding_bundle_hash text CHECK(
    binding_bundle_hash IS NULL OR binding_bundle_hash ~ '^[0-9a-f]{64}$'
  ),
  graph_plan_hash text CHECK(
    graph_plan_hash IS NULL OR graph_plan_hash ~ '^[0-9a-f]{64}$'
  ),
  semantic_ir_hash text CHECK(
    semantic_ir_hash IS NULL OR semantic_ir_hash ~ '^[0-9a-f]{64}$'
  ),
  query_plan_hash text CHECK(
    query_plan_hash IS NULL OR query_plan_hash ~ '^[0-9a-f]{64}$'
  ),
  result_hash text CHECK(
    result_hash IS NULL OR result_hash ~ '^[0-9a-f]{64}$'
  ),
  max_steps integer NOT NULL DEFAULT 16 CHECK(max_steps BETWEEN 1 AND 32),
  max_llm_calls integer NOT NULL DEFAULT 4 CHECK(max_llm_calls BETWEEN 1 AND 4),
  max_tool_calls integer NOT NULL DEFAULT 8 CHECK(max_tool_calls BETWEEN 0 AND 8),
  max_formal_queries integer NOT NULL DEFAULT 2 CHECK(max_formal_queries BETWEEN 0 AND 2),
  max_validation_queries integer NOT NULL DEFAULT 3 CHECK(max_validation_queries BETWEEN 0 AND 3),
  max_duration_ms integer NOT NULL DEFAULT 25000 CHECK(max_duration_ms BETWEEN 100 AND 25000),
  step_count integer NOT NULL DEFAULT 0 CHECK(step_count BETWEEN 0 AND max_steps),
  llm_calls_used integer NOT NULL DEFAULT 0 CHECK(llm_calls_used BETWEEN 0 AND max_llm_calls),
  tool_calls_used integer NOT NULL DEFAULT 0 CHECK(tool_calls_used BETWEEN 0 AND max_tool_calls),
  formal_queries_used integer NOT NULL DEFAULT 0 CHECK(formal_queries_used BETWEEN 0 AND max_formal_queries),
  validation_queries_used integer NOT NULL DEFAULT 0 CHECK(validation_queries_used BETWEEN 0 AND max_validation_queries),
  elapsed_ms bigint NOT NULL DEFAULT 0 CHECK(elapsed_ms BETWEEN 0 AND 600000),
  budget_exhausted boolean NOT NULL DEFAULT false,
  record_version bigint NOT NULL DEFAULT 1 CHECK(record_version>0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz,
  CONSTRAINT askdata_question_runs_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_question_runs_identity_domain_tenant_key
    UNIQUE(id,domain_id,tenant_id),
  CONSTRAINT askdata_question_runs_identity_actor_tenant_key
    UNIQUE(id,actor_id,tenant_id),
  CONSTRAINT askdata_question_runs_audit_identity_key UNIQUE(
    id,actor_id,release_id,release_content_hash,policy_scope_hash,domain_id,tenant_id
  ),
  CONSTRAINT askdata_question_runs_idempotency_key UNIQUE(
    tenant_id,actor_id,idempotency_key_hash
  ),
  CONSTRAINT askdata_question_runs_domain_fk
    FOREIGN KEY(domain_id,tenant_id)
    REFERENCES askdata.domains(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_question_runs_actor_fk
    FOREIGN KEY(actor_id,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_question_runs_release_fk
    FOREIGN KEY(release_id,domain_id,tenant_id)
    REFERENCES askdata.releases(id,domain_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_question_runs_parent_fk
    FOREIGN KEY(parent_run_id,actor_id,tenant_id)
    REFERENCES askdata.question_runs(id,actor_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_question_runs_budget_terminal_check CHECK(
    NOT budget_exhausted
    OR current_state IN ('CLARIFICATION_REQUIRED','ANSWERED','BLOCKED')
  ),
  CONSTRAINT askdata_question_runs_completion_shape_check CHECK(
    (
      current_state NOT IN ('CLARIFICATION_REQUIRED','ANSWERED','BLOCKED')
      AND disposition='PENDING' AND completion_code=''
      AND completion_artifact_hash IS NULL AND completed_at IS NULL
    ) OR (
      current_state='ANSWERED' AND disposition='DIRECT'
      AND completion_code<>'' AND completion_artifact_hash IS NOT NULL
      AND completed_at IS NOT NULL AND understanding_hash IS NOT NULL
      AND binding_bundle_hash IS NOT NULL AND graph_plan_hash IS NOT NULL
      AND semantic_ir_hash IS NOT NULL AND query_plan_hash IS NOT NULL
      AND result_hash IS NOT NULL
    ) OR (
      current_state='CLARIFICATION_REQUIRED' AND disposition='CLARIFY'
      AND completion_code<>'' AND completion_artifact_hash IS NOT NULL
      AND completed_at IS NOT NULL
    ) OR (
      current_state='BLOCKED' AND disposition='REFUSE'
      AND completion_code<>'' AND completion_artifact_hash IS NOT NULL
      AND completed_at IS NOT NULL
    )
  )
);

CREATE INDEX askdata_question_runs_actor_recent_idx
  ON askdata.question_runs(tenant_id,domain_id,actor_id,created_at DESC,id);
CREATE INDEX askdata_question_runs_conversation_idx
  ON askdata.question_runs(
    tenant_id,actor_id,conversation_id,created_at DESC,id
  ) WHERE conversation_id IS NOT NULL;
CREATE INDEX askdata_question_runs_release_idx
  ON askdata.question_runs(
    tenant_id,domain_id,release_id,release_content_hash,created_at DESC,id
  );

CREATE TABLE askdata.question_run_events(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  actor_id uuid NOT NULL,
  question_run_id uuid NOT NULL,
  release_id uuid NOT NULL,
  release_content_hash text NOT NULL CHECK(release_content_hash ~ '^[0-9a-f]{64}$'),
  policy_scope_hash text NOT NULL CHECK(policy_scope_hash ~ '^[0-9a-f]{64}$'),
  event_index integer NOT NULL CHECK(event_index BETWEEN 1 AND 1000000),
  run_version bigint NOT NULL CHECK(run_version>0),
  state text NOT NULL CHECK(state IN (
    'RECEIVED','AUTHORIZED','CONTEXT_READY','UNDERSTANDING','RETRIEVING',
    'BINDING','GRAPH_VALIDATING','IR_READY','PLAN_VALIDATING','EXECUTING',
    'RESULT_VERIFYING','CLARIFICATION_REQUIRED','ANSWERED','BLOCKED'
  )),
  event_type text NOT NULL CHECK(event_type IN (
    'STATE_TRANSITION','LLM_DECISION','TOOL_RESULT','ARTIFACT_RECORDED',
    'BUDGET_UPDATED','CORRECTION','ERROR','PROGRESS'
  )),
  stage text NOT NULL DEFAULT '' CHECK(
    stage='' OR stage ~ '^[A-Z][A-Z0-9_]{0,63}$'
  ),
  status text NOT NULL CHECK(status IN (
    'STARTED','SUCCEEDED','BLOCKED','FAILED','CANCELED'
  )),
  code text NOT NULL DEFAULT '' CHECK(
    code='' OR code ~ '^[A-Z][A-Z0-9_]{0,127}$'
  ),
  tool_call_id text NOT NULL DEFAULT '' CHECK(
    length(tool_call_id)<=128 AND tool_call_id=btrim(tool_call_id)
    AND tool_call_id !~ '[[:cntrl:]]'
  ),
  ai_request_id uuid,
  action_hash text CHECK(action_hash IS NULL OR action_hash ~ '^[0-9a-f]{64}$'),
  artifact_hash text CHECK(artifact_hash IS NULL OR artifact_hash ~ '^[0-9a-f]{64}$'),
  evidence_ids text[] NOT NULL DEFAULT '{}'::text[] CHECK(
    cardinality(evidence_ids)<=64
    AND array_position(evidence_ids,NULL) IS NULL
    AND pg_column_size(evidence_ids)<=16384
    AND array_to_string(evidence_ids,',') !~ '[[:cntrl:]]'
  ),
  summary_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK(
    jsonb_typeof(summary_json)='object'
    AND pg_column_size(summary_json)<=65536
    AND askdata.question_audit_json_is_safe(summary_json)
  ),
  event_hash text NOT NULL CHECK(event_hash ~ '^[0-9a-f]{64}$'),
  duration_ms bigint CHECK(duration_ms IS NULL OR duration_ms BETWEEN 0 AND 600000),
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_question_run_events_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_question_run_events_index_key
    UNIQUE(tenant_id,question_run_id,event_index),
  CONSTRAINT askdata_question_run_events_type_shape_check CHECK(
    (
      event_type='STATE_TRANSITION' AND tool_call_id=''
      AND ((ai_request_id IS NULL AND action_hash IS NULL)
        OR (ai_request_id IS NOT NULL AND action_hash IS NOT NULL))
      AND (artifact_hash IS NULL
        OR state IN ('CLARIFICATION_REQUIRED','ANSWERED','BLOCKED'))
    ) OR (
      event_type='LLM_DECISION' AND tool_call_id=''
      AND ai_request_id IS NOT NULL AND action_hash IS NOT NULL
      AND artifact_hash IS NULL
    ) OR (
      event_type='TOOL_RESULT' AND tool_call_id<>''
      AND ai_request_id IS NULL AND action_hash IS NULL
      AND artifact_hash IS NULL
    ) OR (
      event_type='ARTIFACT_RECORDED' AND tool_call_id=''
      AND ai_request_id IS NULL AND action_hash IS NULL
      AND artifact_hash IS NOT NULL
    ) OR (
      event_type IN ('BUDGET_UPDATED','CORRECTION','ERROR','PROGRESS')
      AND tool_call_id='' AND artifact_hash IS NULL
    )
  ),
  CONSTRAINT askdata_question_run_events_run_fk FOREIGN KEY(
    question_run_id,actor_id,release_id,release_content_hash,
    policy_scope_hash,domain_id,tenant_id
  ) REFERENCES askdata.question_runs(
    id,actor_id,release_id,release_content_hash,
    policy_scope_hash,domain_id,tenant_id
  ) ON DELETE RESTRICT,
  CONSTRAINT askdata_question_run_events_ai_request_fk
    FOREIGN KEY(ai_request_id,tenant_id)
    REFERENCES platform.ai_requests(id,tenant_id) ON DELETE RESTRICT
);

CREATE INDEX askdata_question_run_events_lookup_idx
  ON askdata.question_run_events(
    tenant_id,domain_id,actor_id,question_run_id,event_index
  );

CREATE TABLE askdata.question_artifacts(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  actor_id uuid NOT NULL,
  question_run_id uuid NOT NULL,
  release_id uuid NOT NULL,
  release_content_hash text NOT NULL CHECK(release_content_hash ~ '^[0-9a-f]{64}$'),
  policy_scope_hash text NOT NULL CHECK(policy_scope_hash ~ '^[0-9a-f]{64}$'),
  artifact_index integer NOT NULL CHECK(artifact_index BETWEEN 1 AND 1000000),
  run_version bigint NOT NULL CHECK(run_version>0),
  artifact_type text NOT NULL CHECK(artifact_type IN (
    'UNDERSTANDING','CANDIDATE_SET','BINDING_BUNDLE','GRAPH_PLAN',
    'SEMANTIC_IR','QUERY_PLAN','RESULT_SUMMARY','RESULT_VERIFICATION',
    'EVIDENCE','ANSWER','CLARIFICATION','BLOCK'
  )),
  schema_version text NOT NULL CHECK(
    length(schema_version) BETWEEN 1 AND 128
    AND schema_version=btrim(schema_version)
    AND schema_version ~ '^[A-Za-z0-9][A-Za-z0-9._:@/-]{0,127}$'
  ),
  artifact_hash text NOT NULL CHECK(artifact_hash ~ '^[0-9a-f]{64}$'),
  evidence_ids text[] NOT NULL DEFAULT '{}'::text[] CHECK(
    cardinality(evidence_ids)<=64
    AND array_position(evidence_ids,NULL) IS NULL
    AND pg_column_size(evidence_ids)<=16384
    AND array_to_string(evidence_ids,',') !~ '[[:cntrl:]]'
  ),
  payload_json jsonb NOT NULL CHECK(
    jsonb_typeof(payload_json)='object'
    AND pg_column_size(payload_json)<=262144
    AND askdata.question_audit_json_is_safe(payload_json)
  ),
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_question_artifacts_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_question_artifacts_index_key
    UNIQUE(tenant_id,question_run_id,artifact_index),
  CONSTRAINT askdata_question_artifacts_hash_key
    UNIQUE(tenant_id,question_run_id,artifact_hash),
  CONSTRAINT askdata_question_artifacts_run_fk FOREIGN KEY(
    question_run_id,actor_id,release_id,release_content_hash,
    policy_scope_hash,domain_id,tenant_id
  ) REFERENCES askdata.question_runs(
    id,actor_id,release_id,release_content_hash,
    policy_scope_hash,domain_id,tenant_id
  ) ON DELETE RESTRICT
);

CREATE INDEX askdata_question_artifacts_type_idx
  ON askdata.question_artifacts(
    tenant_id,domain_id,actor_id,question_run_id,artifact_type,artifact_index
  );

ALTER TABLE askdata.question_runs
  ADD CONSTRAINT askdata_question_runs_completion_artifact_fk
  FOREIGN KEY(tenant_id,id,completion_artifact_hash)
  REFERENCES askdata.question_artifacts(
    tenant_id,question_run_id,artifact_hash
  ) ON DELETE RESTRICT;

CREATE TABLE askdata.tool_calls(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  actor_id uuid NOT NULL,
  question_run_id uuid NOT NULL,
  release_id uuid NOT NULL,
  release_content_hash text NOT NULL CHECK(release_content_hash ~ '^[0-9a-f]{64}$'),
  policy_scope_hash text NOT NULL CHECK(policy_scope_hash ~ '^[0-9a-f]{64}$'),
  run_version bigint NOT NULL CHECK(run_version>0),
  tool_call_id text NOT NULL CHECK(
    length(tool_call_id) BETWEEN 1 AND 128
    AND tool_call_id=btrim(tool_call_id)
    AND tool_call_id !~ '[[:cntrl:]]'
  ),
  tool_name text NOT NULL CHECK(tool_name IN (
    'search_semantic_objects','get_semantic_contracts',
    'lookup_dimension_values','get_certified_examples','resolve_graph_plan',
    'validate_semantic_bundle','get_data_quality_status',
    'compile_semantic_query','validate_query_plan','probe_join_cardinality',
    'execute_query_plan','execute_validation_query',
    'compare_candidate_results','request_clarification'
  )),
  state text NOT NULL CHECK(state IN (
    'RECEIVED','AUTHORIZED','CONTEXT_READY','UNDERSTANDING','RETRIEVING',
    'BINDING','GRAPH_VALIDATING','IR_READY','PLAN_VALIDATING','EXECUTING',
    'RESULT_VERIFYING','CLARIFICATION_REQUIRED','ANSWERED','BLOCKED'
  )),
  status text NOT NULL CHECK(status IN (
    'SUCCEEDED','BLOCKED','FAILED','CANCELED'
  )),
  request_hash text NOT NULL CHECK(request_hash ~ '^[0-9a-f]{64}$'),
  result_hash text NOT NULL CHECK(result_hash ~ '^[0-9a-f]{64}$'),
  call_hash text NOT NULL CHECK(call_hash ~ '^[0-9a-f]{64}$'),
  evidence_ids text[] NOT NULL DEFAULT '{}'::text[] CHECK(
    cardinality(evidence_ids)<=64
    AND array_position(evidence_ids,NULL) IS NULL
    AND pg_column_size(evidence_ids)<=16384
    AND array_to_string(evidence_ids,',') !~ '[[:cntrl:]]'
  ),
  budget_json jsonb NOT NULL CHECK(
    jsonb_typeof(budget_json)='object'
    AND pg_column_size(budget_json)<=16384
    AND askdata.question_audit_json_is_safe(budget_json)
  ),
  duration_ms bigint NOT NULL CHECK(duration_ms BETWEEN 0 AND 600000),
  error_code text NOT NULL DEFAULT '' CHECK(
    error_code='' OR error_code ~ '^[A-Z][A-Z0-9_]{0,127}$'
  ),
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_tool_calls_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_tool_calls_question_call_key
    UNIQUE(tenant_id,question_run_id,tool_call_id),
  CONSTRAINT askdata_tool_calls_status_shape_check CHECK(
    (status='SUCCEEDED' AND error_code='')
    OR (status<>'SUCCEEDED' AND error_code<>'')
  ),
  CONSTRAINT askdata_tool_calls_run_fk FOREIGN KEY(
    question_run_id,actor_id,release_id,release_content_hash,
    policy_scope_hash,domain_id,tenant_id
  ) REFERENCES askdata.question_runs(
    id,actor_id,release_id,release_content_hash,
    policy_scope_hash,domain_id,tenant_id
  ) ON DELETE RESTRICT
);

CREATE INDEX askdata_tool_calls_lookup_idx
  ON askdata.tool_calls(
    tenant_id,domain_id,actor_id,question_run_id,created_at,id
  );
CREATE INDEX askdata_tool_calls_release_tool_idx
  ON askdata.tool_calls(
    tenant_id,domain_id,release_id,tool_name,status,created_at DESC,id
  );

CREATE OR REPLACE FUNCTION askdata.valid_question_run_transition(
  previous_state text,
  next_state text
)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
STRICT
AS $$
  SELECT previous_state NOT IN (
    'CLARIFICATION_REQUIRED','ANSWERED','BLOCKED'
  ) AND (previous_state=next_state OR CASE previous_state
    WHEN 'RECEIVED' THEN next_state IN ('AUTHORIZED','BLOCKED')
    WHEN 'AUTHORIZED' THEN next_state IN ('CONTEXT_READY','BLOCKED')
    WHEN 'CONTEXT_READY' THEN next_state IN ('UNDERSTANDING','BLOCKED')
    WHEN 'UNDERSTANDING' THEN next_state IN (
      'RETRIEVING','CLARIFICATION_REQUIRED','BLOCKED'
    )
    WHEN 'RETRIEVING' THEN next_state IN (
      'BINDING','CLARIFICATION_REQUIRED','BLOCKED'
    )
    WHEN 'BINDING' THEN next_state IN (
      'GRAPH_VALIDATING','CLARIFICATION_REQUIRED','BLOCKED'
    )
    WHEN 'GRAPH_VALIDATING' THEN next_state IN (
      'IR_READY','CLARIFICATION_REQUIRED','BLOCKED'
    )
    WHEN 'IR_READY' THEN next_state IN ('PLAN_VALIDATING','BLOCKED')
    WHEN 'PLAN_VALIDATING' THEN next_state IN (
      'EXECUTING','BINDING','CLARIFICATION_REQUIRED','BLOCKED'
    )
    WHEN 'EXECUTING' THEN next_state IN ('RESULT_VERIFYING','BLOCKED')
    WHEN 'RESULT_VERIFYING' THEN next_state IN (
      'ANSWERED','BINDING','CLARIFICATION_REQUIRED','BLOCKED'
    )
    ELSE false
  END)
$$;

CREATE OR REPLACE FUNCTION askdata.enforce_question_run_lifecycle()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE
  release_valid boolean := false;
  expected_artifact_type text;
  actual_artifact_type text;
  semantic_correction boolean := false;
BEGIN
  IF TG_OP='DELETE' THEN
    RAISE EXCEPTION 'question run audit cannot be deleted' USING ERRCODE='55000';
  END IF;

  IF TG_OP='INSERT' THEN
    release_valid := askdata.lock_active_question_release(
      NEW.tenant_id,
      NEW.domain_id,
      NEW.release_id,
      NEW.release_content_hash
    );
    IF NOT release_valid THEN
      RAISE EXCEPTION 'question run requires the matching ACTIVE release'
        USING ERRCODE='23514';
    END IF;
    IF NEW.current_state<>'RECEIVED' OR NEW.record_version<>1
      OR NEW.step_count<>0 OR NEW.llm_calls_used<>0 OR NEW.tool_calls_used<>0
      OR NEW.formal_queries_used<>0 OR NEW.validation_queries_used<>0
      OR NEW.elapsed_ms<>0 OR NEW.budget_exhausted
      OR NEW.understanding_hash IS NOT NULL
      OR NEW.binding_bundle_hash IS NOT NULL OR NEW.graph_plan_hash IS NOT NULL
      OR NEW.semantic_ir_hash IS NOT NULL OR NEW.query_plan_hash IS NOT NULL
      OR NEW.result_hash IS NOT NULL THEN
      RAISE EXCEPTION 'question run initial shape is invalid' USING ERRCODE='23514';
    END IF;
    NEW.created_at=clock_timestamp();
    NEW.updated_at=NEW.created_at;
    RETURN NEW;
  END IF;

  IF OLD.current_state IN ('CLARIFICATION_REQUIRED','ANSWERED','BLOCKED') THEN
    RAISE EXCEPTION 'question run terminal state is immutable' USING ERRCODE='55000';
  END IF;
  IF NEW.id IS DISTINCT FROM OLD.id
    OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.domain_id IS DISTINCT FROM OLD.domain_id
    OR NEW.actor_id IS DISTINCT FROM OLD.actor_id
    OR NEW.conversation_id IS DISTINCT FROM OLD.conversation_id
    OR NEW.parent_run_id IS DISTINCT FROM OLD.parent_run_id
    OR NEW.trace_id IS DISTINCT FROM OLD.trace_id
    OR NEW.idempotency_key_hash IS DISTINCT FROM OLD.idempotency_key_hash
    OR NEW.question_hash IS DISTINCT FROM OLD.question_hash
    OR NEW.policy_scope_hash IS DISTINCT FROM OLD.policy_scope_hash
    OR NEW.release_id IS DISTINCT FROM OLD.release_id
    OR NEW.release_content_hash IS DISTINCT FROM OLD.release_content_hash
    OR NEW.max_steps IS DISTINCT FROM OLD.max_steps
    OR NEW.max_llm_calls IS DISTINCT FROM OLD.max_llm_calls
    OR NEW.max_tool_calls IS DISTINCT FROM OLD.max_tool_calls
    OR NEW.max_formal_queries IS DISTINCT FROM OLD.max_formal_queries
    OR NEW.max_validation_queries IS DISTINCT FROM OLD.max_validation_queries
    OR NEW.max_duration_ms IS DISTINCT FROM OLD.max_duration_ms
    OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION 'question run identity, release pin and budget are immutable'
      USING ERRCODE='55000';
  END IF;
  IF NEW.record_version<>OLD.record_version+1 THEN
    RAISE EXCEPTION 'question run record_version must increase by exactly one'
      USING ERRCODE='40001';
  END IF;
  IF NOT askdata.valid_question_run_transition(
    OLD.current_state,NEW.current_state
  ) THEN
    RAISE EXCEPTION 'illegal question run state transition'
      USING ERRCODE='23514';
  END IF;
  semantic_correction := NEW.current_state='BINDING'
    AND OLD.current_state IN ('PLAN_VALIDATING','RESULT_VERIFYING');
  IF NEW.step_count<OLD.step_count
    OR NEW.llm_calls_used<OLD.llm_calls_used
    OR NEW.tool_calls_used<OLD.tool_calls_used
    OR NEW.formal_queries_used<OLD.formal_queries_used
    OR NEW.validation_queries_used<OLD.validation_queries_used
    OR NEW.elapsed_ms<OLD.elapsed_ms
    OR (OLD.budget_exhausted AND NOT NEW.budget_exhausted) THEN
    RAISE EXCEPTION 'question run budget usage cannot decrease'
      USING ERRCODE='23514';
  END IF;
  IF OLD.understanding_hash IS NOT NULL
    AND NEW.understanding_hash IS DISTINCT FROM OLD.understanding_hash THEN
    RAISE EXCEPTION 'understanding hash cannot be cleared or overwritten'
      USING ERRCODE='23514';
  END IF;
  IF NOT semantic_correction AND (
    (OLD.binding_bundle_hash IS NOT NULL
      AND NEW.binding_bundle_hash IS DISTINCT FROM OLD.binding_bundle_hash)
    OR (OLD.graph_plan_hash IS NOT NULL
      AND NEW.graph_plan_hash IS DISTINCT FROM OLD.graph_plan_hash)
    OR (OLD.semantic_ir_hash IS NOT NULL
      AND NEW.semantic_ir_hash IS DISTINCT FROM OLD.semantic_ir_hash)
    OR (OLD.query_plan_hash IS NOT NULL
      AND NEW.query_plan_hash IS DISTINCT FROM OLD.query_plan_hash)
    OR (OLD.result_hash IS NOT NULL
      AND NEW.result_hash IS DISTINCT FROM OLD.result_hash)
  ) THEN
    RAISE EXCEPTION 'governed run hashes cannot be cleared or overwritten'
      USING ERRCODE='23514';
  END IF;
  IF semantic_correction AND (
      NEW.binding_bundle_hash IS NOT NULL OR NEW.graph_plan_hash IS NOT NULL
      OR NEW.semantic_ir_hash IS NOT NULL OR NEW.query_plan_hash IS NOT NULL
      OR NEW.result_hash IS NOT NULL
  ) THEN
    RAISE EXCEPTION 'semantic correction must clear stale downstream hashes'
      USING ERRCODE='23514';
  END IF;
  IF OLD.understanding_hash IS NULL AND NEW.understanding_hash IS NOT NULL
    AND OLD.current_state<>'UNDERSTANDING'
    AND NEW.current_state<>'UNDERSTANDING' THEN
    RAISE EXCEPTION 'understanding hash appeared outside UNDERSTANDING'
      USING ERRCODE='23514';
  END IF;
  IF OLD.binding_bundle_hash IS NULL AND NEW.binding_bundle_hash IS NOT NULL
    AND OLD.current_state<>'BINDING' AND NEW.current_state<>'BINDING' THEN
    RAISE EXCEPTION 'binding hash appeared outside BINDING'
      USING ERRCODE='23514';
  END IF;
  IF OLD.graph_plan_hash IS NULL AND NEW.graph_plan_hash IS NOT NULL
    AND OLD.current_state<>'GRAPH_VALIDATING'
    AND NEW.current_state<>'GRAPH_VALIDATING' THEN
    RAISE EXCEPTION 'graph plan hash appeared outside GRAPH_VALIDATING'
      USING ERRCODE='23514';
  END IF;
  IF OLD.semantic_ir_hash IS NULL AND NEW.semantic_ir_hash IS NOT NULL
    AND OLD.current_state<>'IR_READY' AND NEW.current_state<>'IR_READY' THEN
    RAISE EXCEPTION 'semantic IR hash appeared outside IR_READY'
      USING ERRCODE='23514';
  END IF;
  IF OLD.query_plan_hash IS NULL AND NEW.query_plan_hash IS NOT NULL
    AND OLD.current_state<>'PLAN_VALIDATING'
    AND NEW.current_state<>'PLAN_VALIDATING' THEN
    RAISE EXCEPTION 'query plan hash appeared outside PLAN_VALIDATING'
      USING ERRCODE='23514';
  END IF;
  IF OLD.result_hash IS NULL AND NEW.result_hash IS NOT NULL
    AND OLD.current_state<>'RESULT_VERIFYING'
    AND NEW.current_state<>'RESULT_VERIFYING' THEN
    RAISE EXCEPTION 'result hash appeared outside RESULT_VERIFYING'
      USING ERRCODE='23514';
  END IF;
  IF (NEW.binding_bundle_hash IS NOT NULL AND NEW.understanding_hash IS NULL)
    OR (NEW.graph_plan_hash IS NOT NULL AND NEW.binding_bundle_hash IS NULL)
    OR (NEW.semantic_ir_hash IS NOT NULL AND NEW.graph_plan_hash IS NULL)
    OR (NEW.query_plan_hash IS NOT NULL AND NEW.semantic_ir_hash IS NULL)
    OR (NEW.result_hash IS NOT NULL AND NEW.query_plan_hash IS NULL) THEN
    RAISE EXCEPTION 'run hashes must form a contiguous governed chain'
      USING ERRCODE='23514';
  END IF;

  IF NEW.current_state IN ('CLARIFICATION_REQUIRED','ANSWERED','BLOCKED') THEN
    expected_artifact_type := CASE NEW.current_state
      WHEN 'CLARIFICATION_REQUIRED' THEN 'CLARIFICATION'
      WHEN 'ANSWERED' THEN 'ANSWER'
      ELSE 'BLOCK'
    END;
    SELECT artifact.artifact_type INTO actual_artifact_type
    FROM askdata.question_artifacts AS artifact
    WHERE artifact.tenant_id=NEW.tenant_id
      AND artifact.question_run_id=NEW.id
      AND artifact.artifact_hash=NEW.completion_artifact_hash;
    IF actual_artifact_type IS DISTINCT FROM expected_artifact_type THEN
      RAISE EXCEPTION 'terminal state requires a matching completion artifact'
        USING ERRCODE='23514';
    END IF;
    NEW.completed_at=clock_timestamp();
  ELSE
    NEW.completed_at=NULL;
  END IF;
  NEW.updated_at=clock_timestamp();
  RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION askdata.stamp_question_runtime_fact()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE
  current_run_version bigint;
  current_run_state text;
  current_completion_artifact_hash text;
  current_completion_code text;
BEGIN
  SELECT run.record_version,run.current_state,run.completion_artifact_hash,
    run.completion_code
    INTO current_run_version,current_run_state,current_completion_artifact_hash,
      current_completion_code
  FROM askdata.question_runs AS run
  WHERE run.id=NEW.question_run_id
    AND run.actor_id=NEW.actor_id
    AND run.release_id=NEW.release_id
    AND run.release_content_hash=NEW.release_content_hash
    AND run.policy_scope_hash=NEW.policy_scope_hash
    AND run.domain_id=NEW.domain_id
    AND run.tenant_id=NEW.tenant_id
  FOR SHARE OF run;
  IF current_run_version IS NULL OR NEW.run_version<>current_run_version THEN
    RAISE EXCEPTION 'question runtime fact must bind the current run version'
      USING ERRCODE='23514';
  END IF;
  IF TG_TABLE_NAME IN ('question_run_events','tool_calls') THEN
    IF NEW.state<>current_run_state THEN
      RAISE EXCEPTION 'question runtime fact state must match the current run state'
        USING ERRCODE='23514';
    END IF;
  END IF;
  IF current_run_state IN (
    'CLARIFICATION_REQUIRED','ANSWERED','BLOCKED'
  ) THEN
    IF TG_TABLE_NAME<>'question_run_events' THEN
      RAISE EXCEPTION 'terminal question run accepts no additional facts'
        USING ERRCODE='55000';
    END IF;
    IF NEW.event_type<>'STATE_TRANSITION'
      OR NEW.stage<>current_run_state
      OR NEW.code<>current_completion_code
      OR NEW.artifact_hash IS DISTINCT FROM current_completion_artifact_hash
      OR NEW.status<>(CASE
        WHEN current_run_state='ANSWERED' THEN 'SUCCEEDED'
        ELSE 'BLOCKED'
      END)
      OR EXISTS(
        SELECT 1 FROM askdata.question_run_events AS terminal_event
        WHERE terminal_event.tenant_id=NEW.tenant_id
          AND terminal_event.question_run_id=NEW.question_run_id
          AND terminal_event.run_version=current_run_version
      ) THEN
      RAISE EXCEPTION 'terminal question run accepts only its unique completion event'
        USING ERRCODE='55000';
    END IF;
  END IF;
  IF TG_TABLE_NAME='question_run_events' THEN
    IF NEW.event_type='TOOL_RESULT' AND NOT EXISTS(
      SELECT 1 FROM askdata.tool_calls AS call
      WHERE call.tenant_id=NEW.tenant_id
        AND call.question_run_id=NEW.question_run_id
        AND call.tool_call_id=NEW.tool_call_id
        AND call.run_version=NEW.run_version
        AND call.state=NEW.state
        AND call.status=NEW.status
        AND call.actor_id=NEW.actor_id
        AND call.release_id=NEW.release_id
        AND call.release_content_hash=NEW.release_content_hash
        AND call.policy_scope_hash=NEW.policy_scope_hash
        AND call.domain_id=NEW.domain_id
    ) THEN
      RAISE EXCEPTION 'tool result event requires its exact tool outcome'
        USING ERRCODE='23514';
    END IF;
    IF NEW.event_type='ARTIFACT_RECORDED' AND NOT EXISTS(
      SELECT 1 FROM askdata.question_artifacts AS artifact
      WHERE artifact.tenant_id=NEW.tenant_id
        AND artifact.question_run_id=NEW.question_run_id
        AND artifact.artifact_hash=NEW.artifact_hash
        AND artifact.run_version=NEW.run_version
        AND artifact.actor_id=NEW.actor_id
        AND artifact.release_id=NEW.release_id
        AND artifact.release_content_hash=NEW.release_content_hash
        AND artifact.policy_scope_hash=NEW.policy_scope_hash
        AND artifact.domain_id=NEW.domain_id
    ) THEN
      RAISE EXCEPTION 'artifact event requires its exact persisted artifact'
        USING ERRCODE='23514';
    END IF;
    IF NEW.ai_request_id IS NOT NULL THEN
      IF NOT EXISTS(
        SELECT 1 FROM platform.ai_requests AS request
        WHERE request.id=NEW.ai_request_id
          AND request.tenant_id=NEW.tenant_id
          AND request.actor_user_id=NEW.actor_id
          AND request.purpose='SEMANTIC_QUESTION'
      ) THEN
        RAISE EXCEPTION 'question event AI request must match actor and purpose'
          USING ERRCODE='23514';
      END IF;
    END IF;
  END IF;
  NEW.created_at=clock_timestamp();
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION
  askdata.question_audit_json_is_safe(jsonb),
  askdata.question_runtime_can_access(uuid,uuid,uuid),
  askdata.lock_active_question_release(uuid,uuid,uuid,text),
  askdata.valid_question_run_transition(text,text),
  askdata.enforce_question_run_lifecycle(),
  askdata.stamp_question_runtime_fact()
FROM PUBLIC;

CREATE TRIGGER askdata_question_runs_lifecycle
BEFORE INSERT OR UPDATE OR DELETE ON askdata.question_runs
FOR EACH ROW EXECUTE FUNCTION askdata.enforce_question_run_lifecycle();

CREATE TRIGGER askdata_question_run_events_stamp
BEFORE INSERT ON askdata.question_run_events
FOR EACH ROW EXECUTE FUNCTION askdata.stamp_question_runtime_fact();
CREATE TRIGGER askdata_question_artifacts_stamp
BEFORE INSERT ON askdata.question_artifacts
FOR EACH ROW EXECUTE FUNCTION askdata.stamp_question_runtime_fact();
CREATE TRIGGER askdata_tool_calls_stamp
BEFORE INSERT ON askdata.tool_calls
FOR EACH ROW EXECUTE FUNCTION askdata.stamp_question_runtime_fact();

CREATE TRIGGER askdata_question_run_events_immutable
BEFORE UPDATE OR DELETE ON askdata.question_run_events
FOR EACH ROW EXECUTE FUNCTION askdata.reject_immutable_mutation();
CREATE TRIGGER askdata_question_artifacts_immutable
BEFORE UPDATE OR DELETE ON askdata.question_artifacts
FOR EACH ROW EXECUTE FUNCTION askdata.reject_immutable_mutation();
CREATE TRIGGER askdata_tool_calls_immutable
BEFORE UPDATE OR DELETE ON askdata.tool_calls
FOR EACH ROW EXECUTE FUNCTION askdata.reject_immutable_mutation();

DO $rls$
DECLARE relation_name text;
BEGIN
  FOREACH relation_name IN ARRAY ARRAY[
    'question_runs','question_run_events','question_artifacts','tool_calls'
  ] LOOP
    EXECUTE format(
      'ALTER TABLE askdata.%I ENABLE ROW LEVEL SECURITY',relation_name
    );
    EXECUTE format(
      'ALTER TABLE askdata.%I FORCE ROW LEVEL SECURITY',relation_name
    );
    EXECUTE format(
      'CREATE POLICY %I ON askdata.%I USING(askdata.question_runtime_can_access(tenant_id,domain_id,actor_id)) WITH CHECK(askdata.question_runtime_can_access(tenant_id,domain_id,actor_id))',
      'askdata_'||relation_name||'_actor_domain_isolation',relation_name
    );
  END LOOP;
END
$rls$;

COMMENT ON TABLE askdata.question_runs IS
  'Mutable nonterminal orchestrator state pinned to actor, policy and ACTIVE release; terminal rows are immutable and contain hashes only';
COMMENT ON TABLE askdata.question_run_events IS
  'Append-only bounded state/action/evidence ledger without raw question, prompt, reasoning, SQL, parameter values or result rows';
COMMENT ON TABLE askdata.question_artifacts IS
  'Append-only hash-addressed sanitized replay contracts; terminal runs reference a matching ANSWER, CLARIFICATION or BLOCK artifact';
COMMENT ON TABLE askdata.tool_calls IS
  'Append-only idempotent Tool Host outcomes keyed by run and tool_call_id; arguments and results are represented only by hashes';
