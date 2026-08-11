ALTER TABLE platform.report_ai_runs
  DROP CONSTRAINT report_ai_runs_kind_check;

ALTER TABLE platform.report_ai_runs
  ADD CONSTRAINT report_ai_runs_kind_check
  CHECK(kind IN ('PLAN','GENERATE_DRAFT','SCOPED_EDIT','INSIGHT','PUBLISH_REVIEW'));

ALTER TABLE platform.report_asset_events
  DROP CONSTRAINT report_asset_events_event_type_check;

ALTER TABLE platform.report_asset_events
  ADD CONSTRAINT report_asset_events_event_type_check
  CHECK(event_type IN (
    'CREATED','OWNER_CHANGED','PUBLISHED','ROLLED_BACK',
    'PERMISSION_GRANTED','PERMISSION_REVOKED',
    'ARCHIVED','RESTORED','SHARE_CREATED','SHARE_REVOKED','PUBLISH_REVIEWED'
  ));

COMMENT ON CONSTRAINT report_ai_runs_kind_check ON platform.report_ai_runs IS
  'PUBLISH_REVIEW records the LLM recommendation over deterministic report publication gates.';
