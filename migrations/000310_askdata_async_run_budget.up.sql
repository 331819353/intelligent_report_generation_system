BEGIN;

-- Complex governed questions are asynchronous multi-stage workflows. The
-- former 120-second ceiling could not accommodate the mandatory understanding,
-- retrieval, binding, planning and verification rounds when providers were
-- healthy but moderately loaded. The run remains bounded by step, model, tool,
-- query and ten-minute absolute ceilings.
ALTER TABLE askdata.question_runs
  DROP CONSTRAINT askdata_question_runs_max_duration_ms_check,
  ADD CONSTRAINT askdata_question_runs_max_duration_ms_check
    CHECK(max_duration_ms BETWEEN 100 AND 600000);

COMMIT;
