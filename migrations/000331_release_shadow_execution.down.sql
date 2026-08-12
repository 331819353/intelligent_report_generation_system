BEGIN;

DROP TRIGGER IF EXISTS askdata_add_to_report_intents_00_user_run ON askdata.add_to_report_intents;
DROP TRIGGER IF EXISTS askdata_feedback_tickets_00_user_run ON askdata.feedback_tickets;
DROP TRIGGER IF EXISTS askdata_saved_questions_00_user_run ON askdata.saved_questions;
DROP TRIGGER IF EXISTS askdata_query_feedback_00_user_run ON askdata.query_feedback;
DROP FUNCTION IF EXISTS askdata.reject_shadow_run_side_effect();
DROP TRIGGER IF EXISTS askdata_release_shadow_observations_auto_stop ON askdata.release_shadow_observations;
DROP FUNCTION IF EXISTS askdata.auto_stop_release_shadow_from_observation();
DROP TRIGGER IF EXISTS askdata_question_runs_record_shadow_observation ON askdata.question_runs;
DROP FUNCTION IF EXISTS askdata.record_release_shadow_observation();
DROP TRIGGER IF EXISTS askdata_question_runs_schedule_shadow ON askdata.question_runs;
DROP FUNCTION IF EXISTS askdata.schedule_release_shadow_job();
DROP TRIGGER IF EXISTS askdata_question_runs_00_execution_mode ON askdata.question_runs;
DROP FUNCTION IF EXISTS askdata.enforce_question_run_execution_mode();
DROP FUNCTION IF EXISTS askdata.complete_release_shadow_job(uuid,uuid,uuid,text);
DROP FUNCTION IF EXISTS askdata.claim_release_shadow_job(uuid,text,integer);
DROP FUNCTION IF EXISTS askdata.list_release_shadow_job_tenants();
DROP TABLE IF EXISTS askdata.release_shadow_observations;
DROP TABLE IF EXISTS askdata.release_shadow_jobs;
ALTER TABLE askdata.question_runs
  DROP CONSTRAINT IF EXISTS askdata_question_runs_source_fk,
  DROP CONSTRAINT IF EXISTS askdata_question_runs_execution_shape_check,
  DROP CONSTRAINT IF EXISTS askdata_question_runs_execution_mode_check,
  DROP COLUMN IF EXISTS source_run_id,
  DROP COLUMN IF EXISTS execution_mode;

COMMIT;
