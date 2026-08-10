LOCK TABLE askdata.question_runs IN ACCESS EXCLUSIVE MODE;

ALTER TABLE askdata.question_runs
  DROP CONSTRAINT askdata_question_runs_clarification_budget_check,
  DROP CONSTRAINT question_runs_max_tool_calls_check,
  DROP CONSTRAINT question_runs_max_formal_queries_check,
  DROP CONSTRAINT question_runs_max_duration_ms_check,
  ADD CONSTRAINT askdata_question_runs_max_tool_calls_check
    CHECK(max_tool_calls BETWEEN 0 AND 10),
  ADD CONSTRAINT askdata_question_runs_max_formal_queries_check
    CHECK(max_formal_queries BETWEEN 0 AND 6),
  ADD CONSTRAINT askdata_question_runs_max_duration_ms_check
    CHECK(max_duration_ms BETWEEN 100 AND 30000);

ALTER TABLE askdata.question_runs DISABLE TRIGGER askdata_question_runs_lifecycle;

UPDATE askdata.question_runs SET budget_consumed_json=jsonb_build_object(
  'stepCount',step_count,
  'llmCallsUsed',llm_calls_used,
  'toolCallsUsed',tool_calls_used,
  'formalQueriesUsed',formal_queries_used,
  'validationQueriesUsed',validation_queries_used,
  'elapsedMs',elapsed_ms,
  'exhausted',budget_exhausted
);

ALTER TABLE askdata.question_runs ENABLE TRIGGER askdata_question_runs_lifecycle;

ALTER TABLE askdata.question_runs
  ADD CONSTRAINT askdata_question_runs_clarification_clock_check CHECK(
    (
      current_state IN ('CLARIFICATION_REQUIRED','CLARIFICATION_EXPIRED')
      AND clarification_deadline IS NOT NULL AND budget_frozen_at IS NOT NULL
      AND clarification_deadline>budget_frozen_at
    ) OR (
      current_state NOT IN ('CLARIFICATION_REQUIRED','CLARIFICATION_EXPIRED')
      AND clarification_deadline IS NULL AND budget_frozen_at IS NULL
    )
  ),
  ADD CONSTRAINT askdata_question_runs_budget_consumed_check CHECK(
    jsonb_typeof(budget_consumed_json)='object'
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
  );

CREATE OR REPLACE FUNCTION askdata.stamp_question_run_budget_consumption()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
BEGIN
  NEW.budget_consumed_json=jsonb_build_object(
    'stepCount',NEW.step_count,
    'llmCallsUsed',NEW.llm_calls_used,
    'toolCallsUsed',NEW.tool_calls_used,
    'formalQueriesUsed',NEW.formal_queries_used,
    'validationQueriesUsed',NEW.validation_queries_used,
    'elapsedMs',NEW.elapsed_ms,
    'exhausted',NEW.budget_exhausted
  );
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION askdata.stamp_question_run_budget_consumption() FROM PUBLIC;

-- PostgreSQL executes triggers for the same event in name order. The lifecycle
-- guard therefore validates identity, monotonicity and terminal invariants
-- first; this final trigger derives the non-forgeable consumption snapshot.
CREATE TRIGGER zz_askdata_question_runs_budget_consumption
BEFORE INSERT OR UPDATE ON askdata.question_runs
FOR EACH ROW EXECUTE FUNCTION askdata.stamp_question_run_budget_consumption();

COMMENT ON COLUMN askdata.question_runs.budget_consumed_json IS
  'Database-derived complete governed usage snapshot for cost attribution, error-budget analysis and clarification resume';
