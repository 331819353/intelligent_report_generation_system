LOCK TABLE askdata.question_runs, askdata.question_run_events, askdata.tool_calls
  IN ACCESS EXCLUSIVE MODE;

ALTER TABLE askdata.question_runs
  DROP CONSTRAINT askdata_question_runs_state_check,
  DROP CONSTRAINT askdata_question_runs_budget_terminal_check,
  DROP CONSTRAINT askdata_question_runs_completion_shape_check,
  ADD CONSTRAINT askdata_question_runs_state_check CHECK(current_state IN (
    'RECEIVED','AUTHORIZED','CONTEXT_READY','UNDERSTANDING','RETRIEVING',
    'BINDING','GRAPH_VALIDATING','IR_READY','PLAN_VALIDATING','EXECUTING',
    'RESULT_VERIFYING','ANSWER_VERIFYING','CLARIFICATION_REQUIRED',
    'CLARIFICATION_EXPIRED','OUT_OF_SCOPE','ANSWERED','BLOCKED'
  )),
  ADD CONSTRAINT askdata_question_runs_budget_terminal_check CHECK(
    NOT budget_exhausted OR current_state IN (
      'CLARIFICATION_REQUIRED','CLARIFICATION_EXPIRED','OUT_OF_SCOPE','ANSWERED','BLOCKED'
    )
  ),
  ADD CONSTRAINT askdata_question_runs_completion_shape_check CHECK(
    (
      current_state NOT IN (
        'CLARIFICATION_REQUIRED','CLARIFICATION_EXPIRED','OUT_OF_SCOPE','ANSWERED','BLOCKED'
      )
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
      current_state IN ('CLARIFICATION_REQUIRED','CLARIFICATION_EXPIRED')
      AND disposition='CLARIFY' AND completion_code<>''
      AND completion_artifact_hash IS NOT NULL AND completed_at IS NOT NULL
    ) OR (
      current_state IN ('OUT_OF_SCOPE','BLOCKED') AND disposition='REFUSE'
      AND completion_code<>'' AND completion_artifact_hash IS NOT NULL
      AND completed_at IS NOT NULL
    )
  );

ALTER TABLE askdata.question_run_events
  DROP CONSTRAINT askdata_question_run_events_state_check,
  DROP CONSTRAINT askdata_question_run_events_type_shape_check,
  ADD CONSTRAINT askdata_question_run_events_state_check CHECK(state IN (
    'RECEIVED','AUTHORIZED','CONTEXT_READY','UNDERSTANDING','RETRIEVING',
    'BINDING','GRAPH_VALIDATING','IR_READY','PLAN_VALIDATING','EXECUTING',
    'RESULT_VERIFYING','ANSWER_VERIFYING','CLARIFICATION_REQUIRED',
    'CLARIFICATION_EXPIRED','OUT_OF_SCOPE','ANSWERED','BLOCKED'
  )),
  ADD CONSTRAINT askdata_question_run_events_type_shape_check CHECK(
    (
      event_type='STATE_TRANSITION' AND tool_call_id=''
      AND ((ai_request_id IS NULL AND action_hash IS NULL)
        OR (ai_request_id IS NOT NULL AND action_hash IS NOT NULL))
      AND (artifact_hash IS NULL OR state IN (
        'CLARIFICATION_REQUIRED','CLARIFICATION_EXPIRED','OUT_OF_SCOPE','ANSWERED','BLOCKED'
      ))
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

ALTER TABLE askdata.tool_calls
  DROP CONSTRAINT askdata_tool_calls_state_check,
  ADD CONSTRAINT askdata_tool_calls_state_check CHECK(state IN (
    'RECEIVED','AUTHORIZED','CONTEXT_READY','UNDERSTANDING','RETRIEVING',
    'BINDING','GRAPH_VALIDATING','IR_READY','PLAN_VALIDATING','EXECUTING',
    'RESULT_VERIFYING','ANSWER_VERIFYING','CLARIFICATION_REQUIRED',
    'CLARIFICATION_EXPIRED','OUT_OF_SCOPE','ANSWERED','BLOCKED'
  ));

CREATE OR REPLACE FUNCTION askdata.valid_question_run_transition(previous_state text,next_state text)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
STRICT
AS $$
  SELECT (previous_state='CLARIFICATION_REQUIRED' AND next_state='CLARIFICATION_EXPIRED')
    OR (previous_state NOT IN (
      'CLARIFICATION_REQUIRED','CLARIFICATION_EXPIRED','OUT_OF_SCOPE','ANSWERED','BLOCKED'
    ) AND (previous_state=next_state OR CASE previous_state
      WHEN 'RECEIVED' THEN next_state IN ('AUTHORIZED','BLOCKED')
      WHEN 'AUTHORIZED' THEN next_state IN ('CONTEXT_READY','BLOCKED')
      WHEN 'CONTEXT_READY' THEN next_state IN ('UNDERSTANDING','BLOCKED')
      WHEN 'UNDERSTANDING' THEN next_state IN (
        'RETRIEVING','CLARIFICATION_REQUIRED','OUT_OF_SCOPE','BLOCKED'
      )
      WHEN 'RETRIEVING' THEN next_state IN ('BINDING','CLARIFICATION_REQUIRED','BLOCKED')
      WHEN 'BINDING' THEN next_state IN (
        'GRAPH_VALIDATING','CLARIFICATION_REQUIRED','OUT_OF_SCOPE','BLOCKED'
      )
      WHEN 'GRAPH_VALIDATING' THEN next_state IN ('IR_READY','CLARIFICATION_REQUIRED','BLOCKED')
      WHEN 'IR_READY' THEN next_state IN ('PLAN_VALIDATING','BLOCKED')
      WHEN 'PLAN_VALIDATING' THEN next_state IN ('EXECUTING','BINDING','CLARIFICATION_REQUIRED','BLOCKED')
      WHEN 'EXECUTING' THEN next_state IN ('RESULT_VERIFYING','BLOCKED')
      WHEN 'RESULT_VERIFYING' THEN next_state IN (
        'ANSWER_VERIFYING','BINDING','CLARIFICATION_REQUIRED','BLOCKED'
      )
      WHEN 'ANSWER_VERIFYING' THEN next_state IN ('ANSWERED','BLOCKED')
      ELSE false
    END))
$$;

-- The v244 lifecycle functions contain the complete immutable audit fence.
-- Preserve that reviewed implementation and extend only its terminal-state
-- set. regexp_replace is applied to pg_get_functiondef so later formatting
-- changes cannot make this migration silently miss a literal replacement.
DO $$
DECLARE definition text;
BEGIN
  SELECT pg_get_functiondef('askdata.enforce_question_run_lifecycle()'::regprocedure)
    INTO definition;
  definition := regexp_replace(
    definition,
    '''CLARIFICATION_EXPIRED''[[:space:]]*,[[:space:]]*''ANSWERED''[[:space:]]*,[[:space:]]*''BLOCKED''',
    '''CLARIFICATION_EXPIRED'',''OUT_OF_SCOPE'',''ANSWERED'',''BLOCKED''',
    'g'
  );
  IF position('OUT_OF_SCOPE' in definition)=0 THEN
    RAISE EXCEPTION 'failed to extend question lifecycle terminal states';
  END IF;
  EXECUTE definition;

  SELECT pg_get_functiondef('askdata.stamp_question_runtime_fact()'::regprocedure)
    INTO definition;
  definition := regexp_replace(
    definition,
    '''CLARIFICATION_EXPIRED''[[:space:]]*,[[:space:]]*''ANSWERED''[[:space:]]*,[[:space:]]*''BLOCKED''',
    '''CLARIFICATION_EXPIRED'',''OUT_OF_SCOPE'',''ANSWERED'',''BLOCKED''',
    'g'
  );
  IF position('OUT_OF_SCOPE' in definition)=0 THEN
    RAISE EXCEPTION 'failed to extend question runtime fact terminal states';
  END IF;
  EXECUTE definition;
END
$$;

REVOKE ALL ON FUNCTION
  askdata.valid_question_run_transition(text,text),
  askdata.enforce_question_run_lifecycle(),
  askdata.stamp_question_runtime_fact()
FROM PUBLIC;

COMMENT ON FUNCTION askdata.valid_question_run_transition(text,text) IS
  'Governed lifecycle including mandatory answer verification and explicit out-of-scope completion';
