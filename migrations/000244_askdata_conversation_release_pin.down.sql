DO $$
BEGIN
  IF EXISTS(SELECT 1 FROM askdata.question_runs WHERE current_state='CLARIFICATION_EXPIRED') THEN
    RAISE EXCEPTION 'cannot roll back conversation release pins while expired clarifications exist';
  END IF;
END
$$;

DROP TRIGGER IF EXISTS askdata_conversations_release_pin ON askdata.conversations;
DROP FUNCTION IF EXISTS askdata.enforce_conversation_release_pin();
DROP TABLE askdata.conversations;

ALTER TABLE askdata.question_run_events
  DROP CONSTRAINT askdata_question_run_events_type_shape_check,
  ADD CONSTRAINT askdata_question_run_events_type_shape_check CHECK(
    (
      event_type='STATE_TRANSITION' AND tool_call_id=''
      AND ((ai_request_id IS NULL AND action_hash IS NULL)
        OR (ai_request_id IS NOT NULL AND action_hash IS NOT NULL))
      AND (artifact_hash IS NULL OR state IN ('CLARIFICATION_REQUIRED','ANSWERED','BLOCKED'))
    ) OR (
      event_type='LLM_DECISION' AND tool_call_id=''
      AND ai_request_id IS NOT NULL AND action_hash IS NOT NULL AND artifact_hash IS NULL
    ) OR (
      event_type='TOOL_RESULT' AND tool_call_id<>''
      AND ai_request_id IS NULL AND action_hash IS NULL AND artifact_hash IS NULL
    ) OR (
      event_type='ARTIFACT_RECORDED' AND tool_call_id=''
      AND ai_request_id IS NULL AND action_hash IS NULL AND artifact_hash IS NOT NULL
    ) OR (
      event_type IN ('BUDGET_UPDATED','CORRECTION','ERROR','PROGRESS')
      AND tool_call_id='' AND artifact_hash IS NULL
    )
  );

ALTER TABLE askdata.question_run_events
  DROP CONSTRAINT askdata_question_run_events_state_check,
  ADD CONSTRAINT question_run_events_state_check CHECK(state IN (
    'RECEIVED','AUTHORIZED','CONTEXT_READY','UNDERSTANDING','RETRIEVING',
    'BINDING','GRAPH_VALIDATING','IR_READY','PLAN_VALIDATING','EXECUTING',
    'RESULT_VERIFYING','CLARIFICATION_REQUIRED','ANSWERED','BLOCKED'
  ));
ALTER TABLE askdata.tool_calls
  DROP CONSTRAINT askdata_tool_calls_state_check,
  ADD CONSTRAINT tool_calls_state_check CHECK(state IN (
    'RECEIVED','AUTHORIZED','CONTEXT_READY','UNDERSTANDING','RETRIEVING',
    'BINDING','GRAPH_VALIDATING','IR_READY','PLAN_VALIDATING','EXECUTING',
    'RESULT_VERIFYING','CLARIFICATION_REQUIRED','ANSWERED','BLOCKED'
  ));

ALTER TABLE askdata.question_runs
  DROP CONSTRAINT askdata_question_runs_state_check,
  DROP CONSTRAINT askdata_question_runs_budget_terminal_check,
  DROP CONSTRAINT askdata_question_runs_clarification_budget_check,
  DROP CONSTRAINT askdata_question_runs_completion_shape_check,
  DROP COLUMN budget_consumed_json,
  DROP COLUMN budget_frozen_at,
  DROP COLUMN clarification_deadline,
  ADD CONSTRAINT question_runs_current_state_check CHECK(current_state IN (
    'RECEIVED','AUTHORIZED','CONTEXT_READY','UNDERSTANDING','RETRIEVING',
    'BINDING','GRAPH_VALIDATING','IR_READY','PLAN_VALIDATING','EXECUTING',
    'RESULT_VERIFYING','CLARIFICATION_REQUIRED','ANSWERED','BLOCKED'
  )),
  ADD CONSTRAINT askdata_question_runs_budget_terminal_check CHECK(
    NOT budget_exhausted OR current_state IN ('CLARIFICATION_REQUIRED','ANSWERED','BLOCKED')
  ),
  ADD CONSTRAINT askdata_question_runs_completion_shape_check CHECK(
    (current_state NOT IN ('CLARIFICATION_REQUIRED','ANSWERED','BLOCKED')
      AND disposition='PENDING' AND completion_code=''
      AND completion_artifact_hash IS NULL AND completed_at IS NULL)
    OR (current_state='ANSWERED' AND disposition='DIRECT' AND completion_code<>''
      AND completion_artifact_hash IS NOT NULL AND completed_at IS NOT NULL
      AND understanding_hash IS NOT NULL AND binding_bundle_hash IS NOT NULL
      AND graph_plan_hash IS NOT NULL AND semantic_ir_hash IS NOT NULL
      AND query_plan_hash IS NOT NULL AND result_hash IS NOT NULL)
    OR (current_state='CLARIFICATION_REQUIRED' AND disposition='CLARIFY'
      AND completion_code<>'' AND completion_artifact_hash IS NOT NULL AND completed_at IS NOT NULL)
    OR (current_state='BLOCKED' AND disposition='REFUSE' AND completion_code<>''
      AND completion_artifact_hash IS NOT NULL AND completed_at IS NOT NULL)
  );

CREATE OR REPLACE FUNCTION askdata.valid_question_run_transition(previous_state text,next_state text)
RETURNS boolean LANGUAGE sql IMMUTABLE STRICT
AS $$
  SELECT previous_state NOT IN ('CLARIFICATION_REQUIRED','ANSWERED','BLOCKED')
    AND (previous_state=next_state OR CASE previous_state
      WHEN 'RECEIVED' THEN next_state IN ('AUTHORIZED','BLOCKED')
      WHEN 'AUTHORIZED' THEN next_state IN ('CONTEXT_READY','BLOCKED')
      WHEN 'CONTEXT_READY' THEN next_state IN ('UNDERSTANDING','BLOCKED')
      WHEN 'UNDERSTANDING' THEN next_state IN ('RETRIEVING','CLARIFICATION_REQUIRED','BLOCKED')
      WHEN 'RETRIEVING' THEN next_state IN ('BINDING','CLARIFICATION_REQUIRED','BLOCKED')
      WHEN 'BINDING' THEN next_state IN ('GRAPH_VALIDATING','CLARIFICATION_REQUIRED','BLOCKED')
      WHEN 'GRAPH_VALIDATING' THEN next_state IN ('IR_READY','CLARIFICATION_REQUIRED','BLOCKED')
      WHEN 'IR_READY' THEN next_state IN ('PLAN_VALIDATING','BLOCKED')
      WHEN 'PLAN_VALIDATING' THEN next_state IN ('EXECUTING','BINDING','CLARIFICATION_REQUIRED','BLOCKED')
      WHEN 'EXECUTING' THEN next_state IN ('RESULT_VERIFYING','BLOCKED')
      WHEN 'RESULT_VERIFYING' THEN next_state IN ('ANSWERED','BINDING','CLARIFICATION_REQUIRED','BLOCKED')
      ELSE false
    END)
$$;

CREATE OR REPLACE FUNCTION askdata.enforce_question_run_lifecycle()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE release_valid boolean := false;
DECLARE expected_artifact_type text;
DECLARE actual_artifact_type text;
DECLARE semantic_correction boolean := false;
BEGIN
  IF TG_OP='DELETE' THEN
    RAISE EXCEPTION 'question run audit cannot be deleted' USING ERRCODE='55000';
  END IF;
  IF TG_OP='INSERT' THEN
    release_valid := askdata.lock_active_question_release(NEW.tenant_id,NEW.domain_id,NEW.release_id,NEW.release_content_hash);
    IF NOT release_valid THEN
      RAISE EXCEPTION 'question run requires the matching ACTIVE release' USING ERRCODE='23514';
    END IF;
    IF NEW.current_state<>'RECEIVED' OR NEW.record_version<>1
      OR NEW.step_count<>0 OR NEW.llm_calls_used<>0 OR NEW.tool_calls_used<>0
      OR NEW.formal_queries_used<>0 OR NEW.validation_queries_used<>0 OR NEW.elapsed_ms<>0
      OR NEW.budget_exhausted OR NEW.understanding_hash IS NOT NULL
      OR NEW.binding_bundle_hash IS NOT NULL OR NEW.graph_plan_hash IS NOT NULL
      OR NEW.semantic_ir_hash IS NOT NULL OR NEW.query_plan_hash IS NOT NULL OR NEW.result_hash IS NOT NULL THEN
      RAISE EXCEPTION 'question run initial shape is invalid' USING ERRCODE='23514';
    END IF;
    NEW.created_at=clock_timestamp(); NEW.updated_at=NEW.created_at; RETURN NEW;
  END IF;
  IF OLD.current_state IN ('CLARIFICATION_REQUIRED','ANSWERED','BLOCKED') THEN
    RAISE EXCEPTION 'question run terminal state is immutable' USING ERRCODE='55000';
  END IF;
  IF NEW.id IS DISTINCT FROM OLD.id OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.domain_id IS DISTINCT FROM OLD.domain_id OR NEW.actor_id IS DISTINCT FROM OLD.actor_id
    OR NEW.conversation_id IS DISTINCT FROM OLD.conversation_id OR NEW.parent_run_id IS DISTINCT FROM OLD.parent_run_id
    OR NEW.trace_id IS DISTINCT FROM OLD.trace_id OR NEW.idempotency_key_hash IS DISTINCT FROM OLD.idempotency_key_hash
    OR NEW.question_hash IS DISTINCT FROM OLD.question_hash OR NEW.policy_scope_hash IS DISTINCT FROM OLD.policy_scope_hash
    OR NEW.release_id IS DISTINCT FROM OLD.release_id OR NEW.release_content_hash IS DISTINCT FROM OLD.release_content_hash
    OR NEW.max_steps IS DISTINCT FROM OLD.max_steps OR NEW.max_llm_calls IS DISTINCT FROM OLD.max_llm_calls
    OR NEW.max_tool_calls IS DISTINCT FROM OLD.max_tool_calls OR NEW.max_formal_queries IS DISTINCT FROM OLD.max_formal_queries
    OR NEW.max_validation_queries IS DISTINCT FROM OLD.max_validation_queries
    OR NEW.max_duration_ms IS DISTINCT FROM OLD.max_duration_ms OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION 'question run identity, release pin and budget are immutable' USING ERRCODE='55000';
  END IF;
  IF NEW.record_version<>OLD.record_version+1 THEN
    RAISE EXCEPTION 'question run record_version must increase by exactly one' USING ERRCODE='40001';
  END IF;
  IF NOT askdata.valid_question_run_transition(OLD.current_state,NEW.current_state) THEN
    RAISE EXCEPTION 'illegal question run state transition' USING ERRCODE='23514';
  END IF;
  IF NEW.step_count<OLD.step_count OR NEW.llm_calls_used<OLD.llm_calls_used
    OR NEW.tool_calls_used<OLD.tool_calls_used OR NEW.formal_queries_used<OLD.formal_queries_used
    OR NEW.validation_queries_used<OLD.validation_queries_used OR NEW.elapsed_ms<OLD.elapsed_ms
    OR (OLD.budget_exhausted AND NOT NEW.budget_exhausted) THEN
    RAISE EXCEPTION 'question run budget usage cannot decrease' USING ERRCODE='23514';
  END IF;
  semantic_correction := NEW.current_state='BINDING' AND OLD.current_state IN ('PLAN_VALIDATING','RESULT_VERIFYING');
  IF OLD.understanding_hash IS NOT NULL AND NEW.understanding_hash IS DISTINCT FROM OLD.understanding_hash THEN
    RAISE EXCEPTION 'understanding hash cannot be cleared or overwritten' USING ERRCODE='23514';
  END IF;
  IF NOT semantic_correction AND (
    (OLD.binding_bundle_hash IS NOT NULL AND NEW.binding_bundle_hash IS DISTINCT FROM OLD.binding_bundle_hash)
    OR (OLD.graph_plan_hash IS NOT NULL AND NEW.graph_plan_hash IS DISTINCT FROM OLD.graph_plan_hash)
    OR (OLD.semantic_ir_hash IS NOT NULL AND NEW.semantic_ir_hash IS DISTINCT FROM OLD.semantic_ir_hash)
    OR (OLD.query_plan_hash IS NOT NULL AND NEW.query_plan_hash IS DISTINCT FROM OLD.query_plan_hash)
    OR (OLD.result_hash IS NOT NULL AND NEW.result_hash IS DISTINCT FROM OLD.result_hash)) THEN
    RAISE EXCEPTION 'governed run hashes cannot be cleared or overwritten' USING ERRCODE='23514';
  END IF;
  IF semantic_correction AND (NEW.binding_bundle_hash IS NOT NULL OR NEW.graph_plan_hash IS NOT NULL
    OR NEW.semantic_ir_hash IS NOT NULL OR NEW.query_plan_hash IS NOT NULL OR NEW.result_hash IS NOT NULL) THEN
    RAISE EXCEPTION 'semantic correction must clear stale downstream hashes' USING ERRCODE='23514';
  END IF;
  IF OLD.understanding_hash IS NULL AND NEW.understanding_hash IS NOT NULL
    AND OLD.current_state<>'UNDERSTANDING' AND NEW.current_state<>'UNDERSTANDING' THEN
    RAISE EXCEPTION 'understanding hash appeared outside UNDERSTANDING' USING ERRCODE='23514';
  END IF;
  IF OLD.binding_bundle_hash IS NULL AND NEW.binding_bundle_hash IS NOT NULL
    AND OLD.current_state<>'BINDING' AND NEW.current_state<>'BINDING' THEN
    RAISE EXCEPTION 'binding hash appeared outside BINDING' USING ERRCODE='23514';
  END IF;
  IF OLD.graph_plan_hash IS NULL AND NEW.graph_plan_hash IS NOT NULL
    AND OLD.current_state<>'GRAPH_VALIDATING' AND NEW.current_state<>'GRAPH_VALIDATING' THEN
    RAISE EXCEPTION 'graph plan hash appeared outside GRAPH_VALIDATING' USING ERRCODE='23514';
  END IF;
  IF OLD.semantic_ir_hash IS NULL AND NEW.semantic_ir_hash IS NOT NULL
    AND OLD.current_state<>'IR_READY' AND NEW.current_state<>'IR_READY' THEN
    RAISE EXCEPTION 'semantic IR hash appeared outside IR_READY' USING ERRCODE='23514';
  END IF;
  IF OLD.query_plan_hash IS NULL AND NEW.query_plan_hash IS NOT NULL
    AND OLD.current_state<>'PLAN_VALIDATING' AND NEW.current_state<>'PLAN_VALIDATING' THEN
    RAISE EXCEPTION 'query plan hash appeared outside PLAN_VALIDATING' USING ERRCODE='23514';
  END IF;
  IF OLD.result_hash IS NULL AND NEW.result_hash IS NOT NULL
    AND OLD.current_state<>'RESULT_VERIFYING' AND NEW.current_state<>'RESULT_VERIFYING' THEN
    RAISE EXCEPTION 'result hash appeared outside RESULT_VERIFYING' USING ERRCODE='23514';
  END IF;
  IF (NEW.binding_bundle_hash IS NOT NULL AND NEW.understanding_hash IS NULL)
    OR (NEW.graph_plan_hash IS NOT NULL AND NEW.binding_bundle_hash IS NULL)
    OR (NEW.semantic_ir_hash IS NOT NULL AND NEW.graph_plan_hash IS NULL)
    OR (NEW.query_plan_hash IS NOT NULL AND NEW.semantic_ir_hash IS NULL)
    OR (NEW.result_hash IS NOT NULL AND NEW.query_plan_hash IS NULL) THEN
    RAISE EXCEPTION 'run hashes must form a contiguous governed chain' USING ERRCODE='23514';
  END IF;
  IF NEW.current_state IN ('CLARIFICATION_REQUIRED','ANSWERED','BLOCKED') THEN
    expected_artifact_type := CASE NEW.current_state WHEN 'CLARIFICATION_REQUIRED' THEN 'CLARIFICATION'
      WHEN 'ANSWERED' THEN 'ANSWER' ELSE 'BLOCK' END;
    SELECT artifact_type INTO actual_artifact_type FROM askdata.question_artifacts
    WHERE tenant_id=NEW.tenant_id AND question_run_id=NEW.id AND artifact_hash=NEW.completion_artifact_hash;
    IF actual_artifact_type IS DISTINCT FROM expected_artifact_type THEN
      RAISE EXCEPTION 'terminal state requires a matching completion artifact' USING ERRCODE='23514';
    END IF;
    NEW.completed_at=clock_timestamp();
  ELSE NEW.completed_at=NULL;
  END IF;
  NEW.updated_at=clock_timestamp(); RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION askdata.stamp_question_runtime_fact()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE current_run_version bigint;
DECLARE current_run_state text;
DECLARE current_completion_artifact_hash text;
DECLARE current_completion_code text;
BEGIN
  SELECT record_version,current_state,completion_artifact_hash,completion_code
    INTO current_run_version,current_run_state,current_completion_artifact_hash,current_completion_code
  FROM askdata.question_runs WHERE id=NEW.question_run_id AND actor_id=NEW.actor_id
    AND release_id=NEW.release_id AND release_content_hash=NEW.release_content_hash
    AND policy_scope_hash=NEW.policy_scope_hash AND domain_id=NEW.domain_id AND tenant_id=NEW.tenant_id FOR SHARE;
  IF current_run_version IS NULL OR NEW.run_version<>current_run_version THEN
    RAISE EXCEPTION 'question runtime fact must bind the current run version' USING ERRCODE='23514';
  END IF;
  IF TG_TABLE_NAME IN ('question_run_events','tool_calls') THEN
    IF NEW.state<>current_run_state THEN
      RAISE EXCEPTION 'question runtime fact state must match the current run state' USING ERRCODE='23514';
    END IF;
  END IF;
  IF current_run_state IN ('CLARIFICATION_REQUIRED','ANSWERED','BLOCKED') THEN
    IF TG_TABLE_NAME<>'question_run_events' THEN
      RAISE EXCEPTION 'terminal question run accepts no additional facts' USING ERRCODE='55000';
    END IF;
    IF NEW.event_type<>'STATE_TRANSITION' OR NEW.stage<>current_run_state OR NEW.code<>current_completion_code
      OR NEW.artifact_hash IS DISTINCT FROM current_completion_artifact_hash
      OR NEW.status<>(CASE WHEN current_run_state='ANSWERED' THEN 'SUCCEEDED' ELSE 'BLOCKED' END)
      OR EXISTS(SELECT 1 FROM askdata.question_run_events WHERE tenant_id=NEW.tenant_id
        AND question_run_id=NEW.question_run_id AND run_version=current_run_version) THEN
      RAISE EXCEPTION 'terminal question run accepts only its unique completion event' USING ERRCODE='55000';
    END IF;
  END IF;
  IF TG_TABLE_NAME='question_run_events' THEN
    IF NEW.event_type='TOOL_RESULT' AND NOT EXISTS(SELECT 1 FROM askdata.tool_calls AS call
      WHERE call.tenant_id=NEW.tenant_id AND call.question_run_id=NEW.question_run_id
        AND call.tool_call_id=NEW.tool_call_id AND call.run_version=NEW.run_version AND call.state=NEW.state
        AND call.status=NEW.status AND call.actor_id=NEW.actor_id AND call.release_id=NEW.release_id
        AND call.release_content_hash=NEW.release_content_hash AND call.policy_scope_hash=NEW.policy_scope_hash
        AND call.domain_id=NEW.domain_id) THEN
      RAISE EXCEPTION 'tool result event requires its exact tool outcome' USING ERRCODE='23514';
    END IF;
    IF NEW.event_type='ARTIFACT_RECORDED' AND NOT EXISTS(SELECT 1 FROM askdata.question_artifacts AS artifact
      WHERE artifact.tenant_id=NEW.tenant_id AND artifact.question_run_id=NEW.question_run_id
        AND artifact.artifact_hash=NEW.artifact_hash AND artifact.run_version=NEW.run_version
        AND artifact.actor_id=NEW.actor_id AND artifact.release_id=NEW.release_id
        AND artifact.release_content_hash=NEW.release_content_hash AND artifact.policy_scope_hash=NEW.policy_scope_hash
        AND artifact.domain_id=NEW.domain_id) THEN
      RAISE EXCEPTION 'artifact event requires its exact persisted artifact' USING ERRCODE='23514';
    END IF;
    IF NEW.ai_request_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM platform.ai_requests AS request
      WHERE request.id=NEW.ai_request_id AND request.tenant_id=NEW.tenant_id
        AND request.actor_user_id=NEW.actor_id AND request.purpose='SEMANTIC_QUESTION') THEN
      RAISE EXCEPTION 'question event AI request must match actor and purpose' USING ERRCODE='23514';
    END IF;
  END IF;
  NEW.created_at=clock_timestamp(); RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION askdata.valid_question_run_transition(text,text),
  askdata.enforce_question_run_lifecycle(),askdata.stamp_question_runtime_fact() FROM PUBLIC;
