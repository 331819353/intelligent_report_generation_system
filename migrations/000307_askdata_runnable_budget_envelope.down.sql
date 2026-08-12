BEGIN;

LOCK TABLE askdata.question_runs IN ACCESS EXCLUSIVE MODE;

DO $$
BEGIN
  IF EXISTS(
    SELECT 1 FROM askdata.question_runs
    WHERE max_steps>32 OR max_llm_calls>4 OR max_tool_calls>10 OR max_duration_ms>30000
  ) THEN
    RAISE EXCEPTION 'cannot restore the historical budget envelope while widened runs exist'
      USING ERRCODE='55000';
  END IF;
END
$$;

ALTER TABLE askdata.question_runs
  DROP CONSTRAINT askdata_question_runs_max_steps_check,
  DROP CONSTRAINT askdata_question_runs_max_llm_calls_check,
  DROP CONSTRAINT askdata_question_runs_max_tool_calls_check,
  DROP CONSTRAINT askdata_question_runs_max_duration_ms_check,
  ADD CONSTRAINT question_runs_max_steps_check
    CHECK(max_steps BETWEEN 1 AND 32),
  ADD CONSTRAINT question_runs_max_llm_calls_check
    CHECK(max_llm_calls BETWEEN 1 AND 4),
  ADD CONSTRAINT askdata_question_runs_max_tool_calls_check
    CHECK(max_tool_calls BETWEEN 0 AND 10),
  ADD CONSTRAINT askdata_question_runs_max_duration_ms_check
    CHECK(max_duration_ms BETWEEN 100 AND 30000);

COMMENT ON COLUMN askdata.question_runs.max_llm_calls IS NULL;

COMMIT;
