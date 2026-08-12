BEGIN;

-- Report operation bundles legitimately contain layout.rows. Reusing the
-- materialization JSON guard made every generated report block impossible to
-- persist because that guard treats any key named rows as warehouse data.
-- Keep the report boundary strict against SQL, credentials and embedded data,
-- while allowing the bounded integer grid field validated by Report V2.
CREATE OR REPLACE FUNCTION askdata.report_operation_json_is_safe(document jsonb)
RETURNS boolean
LANGUAGE plpgsql
IMMUTABLE
STRICT
AS $$
DECLARE
  item record;
  normalized_key text;
BEGIN
  IF jsonb_typeof(document)='object' THEN
    FOR item IN SELECT key,value FROM jsonb_each(document)
    LOOP
      normalized_key := regexp_replace(lower(item.key),'[_-]','','g');
      IF normalized_key IN (
        'sql','rawsql','query','statement','password','secret','credentials',
        'samplerows','rawdata'
      ) THEN
        RETURN false;
      END IF;
      IF NOT askdata.report_operation_json_is_safe(item.value) THEN
        RETURN false;
      END IF;
    END LOOP;
  ELSIF jsonb_typeof(document)='array' THEN
    FOR item IN SELECT value FROM jsonb_array_elements(document)
    LOOP
      IF NOT askdata.report_operation_json_is_safe(item.value) THEN
        RETURN false;
      END IF;
    END LOOP;
  END IF;
  RETURN true;
END
$$;

ALTER TABLE askdata.add_to_report_intents
  DROP CONSTRAINT add_to_report_intents_operation_bundle_json_check;
ALTER TABLE askdata.add_to_report_intents
  ADD CONSTRAINT add_to_report_intents_operation_bundle_json_check CHECK(
    jsonb_typeof(operation_bundle_json)='object'
    AND askdata.report_operation_json_is_safe(operation_bundle_json)
  );

COMMENT ON FUNCTION askdata.report_operation_json_is_safe(jsonb) IS
  'Report operation safety guard: permits validated layout rows but rejects SQL, credentials and embedded business data';

GRANT EXECUTE ON FUNCTION askdata.report_operation_json_is_safe(jsonb)
  TO report_app,report_worker;

COMMIT;
