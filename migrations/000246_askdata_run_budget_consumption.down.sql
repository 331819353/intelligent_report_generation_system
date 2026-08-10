LOCK TABLE askdata.question_runs IN ACCESS EXCLUSIVE MODE;

DO $$
BEGIN
  IF EXISTS(
    SELECT 1 FROM askdata.question_runs
    WHERE max_tool_calls>8 OR max_formal_queries>2 OR max_duration_ms>25000
  ) THEN
    RAISE EXCEPTION 'cannot remove revised run budgets while runs use the new governed limits'
      USING ERRCODE='55000';
  END IF;
END
$$;

DROP TRIGGER zz_askdata_question_runs_budget_consumption ON askdata.question_runs;
DROP FUNCTION askdata.stamp_question_run_budget_consumption();

ALTER TABLE askdata.question_runs
  DROP CONSTRAINT askdata_question_runs_clarification_clock_check,
  DROP CONSTRAINT askdata_question_runs_budget_consumed_check,
  DROP CONSTRAINT askdata_question_runs_max_tool_calls_check,
  DROP CONSTRAINT askdata_question_runs_max_formal_queries_check,
  DROP CONSTRAINT askdata_question_runs_max_duration_ms_check;

ALTER TABLE askdata.question_runs DISABLE TRIGGER askdata_question_runs_lifecycle;

UPDATE askdata.question_runs SET budget_consumed_json=NULL
WHERE current_state NOT IN ('CLARIFICATION_REQUIRED','CLARIFICATION_EXPIRED');

ALTER TABLE askdata.question_runs ENABLE TRIGGER askdata_question_runs_lifecycle;

ALTER TABLE askdata.question_runs
  ADD CONSTRAINT question_runs_max_tool_calls_check
    CHECK(max_tool_calls BETWEEN 0 AND 8),
  ADD CONSTRAINT question_runs_max_formal_queries_check
    CHECK(max_formal_queries BETWEEN 0 AND 2),
  ADD CONSTRAINT question_runs_max_duration_ms_check
    CHECK(max_duration_ms BETWEEN 100 AND 25000),
  ADD CONSTRAINT askdata_question_runs_clarification_budget_check CHECK(
    (
      current_state IN ('CLARIFICATION_REQUIRED','CLARIFICATION_EXPIRED')
      AND clarification_deadline IS NOT NULL AND budget_frozen_at IS NOT NULL
      AND clarification_deadline>budget_frozen_at
      AND jsonb_typeof(budget_consumed_json)='object'
      AND pg_column_size(budget_consumed_json)<=16384
      AND askdata.question_audit_json_is_safe(budget_consumed_json)
      AND budget_consumed_json=jsonb_build_object(
        'stepCount',step_count,
        'llmCallsUsed',llm_calls_used,
        'toolCallsUsed',tool_calls_used,
        'formalQueriesUsed',formal_queries_used,
        'validationQueriesUsed',validation_queries_used,
        'elapsedMs',elapsed_ms,
        'exhausted',budget_exhausted
      )
    ) OR (
      current_state NOT IN ('CLARIFICATION_REQUIRED','CLARIFICATION_EXPIRED')
      AND clarification_deadline IS NULL AND budget_frozen_at IS NULL
      AND budget_consumed_json IS NULL
    )
  );

COMMENT ON COLUMN askdata.question_runs.budget_consumed_json IS
  'Exact governed usage snapshot resumed by the clarification child without charging waiting time';
