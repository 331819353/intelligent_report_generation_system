BEGIN;

ALTER TABLE askdata.release_events
  DROP CONSTRAINT askdata_release_events_event_type_check;
ALTER TABLE askdata.release_events
  ADD CONSTRAINT askdata_release_events_event_type_check CHECK(event_type IN (
    'CREATED','VALIDATING','PROJECTING','PROJECTION_READY','PROJECTION_FAILED',
    'PROJECTION_RETRIED','READY','ACTIVATED','SUPERSEDED','RETAINED','RETIRED','BLOCKED'
  ));

COMMIT;
