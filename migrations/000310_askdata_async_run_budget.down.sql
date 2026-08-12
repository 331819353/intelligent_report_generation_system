BEGIN;

DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM askdata.question_runs WHERE max_duration_ms > 120000
  ) THEN
    RAISE EXCEPTION 'cannot restore 120-second AskData budget while longer runs exist';
  END IF;
END $$;

ALTER TABLE askdata.question_runs
  DROP CONSTRAINT askdata_question_runs_max_duration_ms_check,
  ADD CONSTRAINT askdata_question_runs_max_duration_ms_check
    CHECK(max_duration_ms BETWEEN 100 AND 120000);

COMMIT;
