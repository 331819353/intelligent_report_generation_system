BEGIN;

-- 回退 IR_READY / ANSWER_VERIFYING 的澄清出口，恢复 000247 的状态图。
--
-- 注意：回退后 PLAN_SELECTION 与 RESULT_VERIFICATION 阶段允许的 CLARIFY
-- 将再次无法表达，调用方必须回到失败关闭（降级为 BLOCKED）。
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

COMMENT ON FUNCTION askdata.valid_question_run_transition(text,text) IS
  'Governed lifecycle including mandatory answer verification and explicit out-of-scope completion';

COMMIT;
