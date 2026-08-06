ALTER TABLE IF EXISTS askdata.question_runs
  DROP CONSTRAINT IF EXISTS askdata_question_runs_completion_artifact_fk;

DROP TABLE IF EXISTS askdata.tool_calls;
DROP TABLE IF EXISTS askdata.question_run_events;
DROP TABLE IF EXISTS askdata.question_artifacts;
DROP TABLE IF EXISTS askdata.question_runs;

DROP FUNCTION IF EXISTS askdata.stamp_question_runtime_fact();
DROP FUNCTION IF EXISTS askdata.enforce_question_run_lifecycle();
DROP FUNCTION IF EXISTS askdata.valid_question_run_transition(text,text);
DROP FUNCTION IF EXISTS askdata.lock_active_question_release(uuid,uuid,uuid,text);
DROP FUNCTION IF EXISTS askdata.question_runtime_can_access(uuid,uuid,uuid);
DROP FUNCTION IF EXISTS askdata.question_audit_json_is_safe(jsonb);
