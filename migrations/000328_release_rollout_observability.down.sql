BEGIN;

DROP TRIGGER askdata_question_runs_rollout_auto_stop ON askdata.question_runs;
DROP FUNCTION askdata.auto_stop_release_rollout_from_run();
DROP FUNCTION askdata.release_rollout_observability(uuid);
DROP FUNCTION askdata.release_rollout_observability_internal(uuid);

ALTER TABLE askdata.release_rollout_events
  DROP CONSTRAINT release_rollout_events_event_type_check;
ALTER TABLE askdata.release_rollout_events
  ADD CONSTRAINT release_rollout_events_event_type_check CHECK(event_type IN (
    'STARTED','ADVANCED','PAUSED','RESUMED','STOPPED',
    'ACCEPTED','ACTIVATED','ROLLED_BACK'
  ));

COMMIT;
