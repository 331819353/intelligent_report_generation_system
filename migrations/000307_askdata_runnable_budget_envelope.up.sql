-- The staged Tool Host protocol needs one cognition round to request each
-- governed tool and another to consume its result. The original 4-call/16-step
-- envelope could not complete the mandatory state graph, so every real run
-- exhausted before execution. Widen only the immutable per-run maxima; all
-- existing cost, timeout, lease and query-count guards remain in force.

BEGIN;

LOCK TABLE askdata.question_runs IN ACCESS EXCLUSIVE MODE;

ALTER TABLE askdata.question_runs
  DROP CONSTRAINT question_runs_max_steps_check,
  DROP CONSTRAINT question_runs_max_llm_calls_check,
  DROP CONSTRAINT askdata_question_runs_max_tool_calls_check,
  DROP CONSTRAINT askdata_question_runs_max_duration_ms_check,
  ADD CONSTRAINT askdata_question_runs_max_steps_check
    CHECK(max_steps BETWEEN 1 AND 48),
  ADD CONSTRAINT askdata_question_runs_max_llm_calls_check
    CHECK(max_llm_calls BETWEEN 1 AND 16),
  ADD CONSTRAINT askdata_question_runs_max_tool_calls_check
    CHECK(max_tool_calls BETWEEN 0 AND 16),
  ADD CONSTRAINT askdata_question_runs_max_duration_ms_check
    CHECK(max_duration_ms BETWEEN 100 AND 120000);

COMMENT ON COLUMN askdata.question_runs.max_llm_calls IS
  'Immutable per-run cognition-round ceiling sized for the mandatory staged Tool Host protocol';

COMMIT;
