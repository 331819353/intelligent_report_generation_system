BEGIN;

-- 允许 IR_READY → CLARIFICATION_REQUIRED。
--
-- 冲突背景：cognition.stageAllowsAction 允许 PLAN_SELECTION 阶段返回 CLARIFY，
-- 而 IR_READY 由该阶段驱动；但状态图此前只给 IR_READY 留了 PLAN_VALIDATING 与
-- BLOCKED 两个后继。两处既有契约互相矛盾，模型在计划选择阶段提出的澄清会在
-- enforce_question_run_lifecycle 上被拒绝，只能降级为 BLOCKED——用户失去了一次
-- 本可以澄清的机会。
--
-- 本迁移按「放宽状态图」的裁定对齐两者：计划选择阶段确实可能需要澄清
-- （例如同一绑定存在多个可比口径的计划），澄清是比阻断更好的出口。
--
-- 同一类冲突在 ANSWER_VERIFYING 上也存在：该状态由 RESULT_VERIFICATION 阶段驱动，
-- 阶段允许 CLARIFY，但状态图此前只给了 ANSWERED 与 BLOCKED。叙述核验阶段发现
-- 结果存在歧义时，回到用户澄清同样比直接阻断更合理，因此一并放开。
-- 该实例是协议表测试发现的，不是人工枚举出来的。
--
-- 只增加这两条边，其余迁移规则逐字保持 000247 的既有实现不变。
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
      WHEN 'IR_READY' THEN next_state IN ('PLAN_VALIDATING','CLARIFICATION_REQUIRED','BLOCKED')
      WHEN 'PLAN_VALIDATING' THEN next_state IN ('EXECUTING','BINDING','CLARIFICATION_REQUIRED','BLOCKED')
      WHEN 'EXECUTING' THEN next_state IN ('RESULT_VERIFYING','BLOCKED')
      WHEN 'RESULT_VERIFYING' THEN next_state IN (
        'ANSWER_VERIFYING','BINDING','CLARIFICATION_REQUIRED','BLOCKED'
      )
      WHEN 'ANSWER_VERIFYING' THEN next_state IN ('ANSWERED','CLARIFICATION_REQUIRED','BLOCKED')
      ELSE false
    END))
$$;

COMMENT ON FUNCTION askdata.valid_question_run_transition(text,text) IS
  'Governed lifecycle including mandatory answer verification, explicit out-of-scope completion and clarification from plan selection and answer verification';

COMMIT;
