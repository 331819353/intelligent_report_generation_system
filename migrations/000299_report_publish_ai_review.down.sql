DELETE FROM platform.report_asset_events WHERE event_type='PUBLISH_REVIEWED';
DELETE FROM platform.report_ai_operations
 WHERE ai_run_id IN (SELECT id FROM platform.report_ai_runs WHERE kind='PUBLISH_REVIEW');
DELETE FROM platform.report_ai_runs WHERE kind='PUBLISH_REVIEW';

ALTER TABLE platform.report_asset_events
  DROP CONSTRAINT report_asset_events_event_type_check;

ALTER TABLE platform.report_asset_events
  ADD CONSTRAINT report_asset_events_event_type_check
  CHECK(event_type IN (
    'CREATED','OWNER_CHANGED','PUBLISHED','ROLLED_BACK',
    'PERMISSION_GRANTED','PERMISSION_REVOKED',
    'ARCHIVED','RESTORED','SHARE_CREATED','SHARE_REVOKED'
  ));

ALTER TABLE platform.report_ai_runs
  DROP CONSTRAINT report_ai_runs_kind_check;

ALTER TABLE platform.report_ai_runs
  ADD CONSTRAINT report_ai_runs_kind_check
  CHECK(kind IN ('PLAN','GENERATE_DRAFT','SCOPED_EDIT','INSIGHT'));
