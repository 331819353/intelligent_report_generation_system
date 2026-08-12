BEGIN;

ALTER TABLE askdata.add_to_report_intents
  DROP CONSTRAINT add_to_report_intents_operation_bundle_json_check;
ALTER TABLE askdata.add_to_report_intents
  ADD CONSTRAINT add_to_report_intents_operation_bundle_json_check CHECK(
    jsonb_typeof(operation_bundle_json)='object'
    AND askdata.json_is_safe(operation_bundle_json)
  );

DROP FUNCTION askdata.report_operation_json_is_safe(jsonb);

COMMIT;
