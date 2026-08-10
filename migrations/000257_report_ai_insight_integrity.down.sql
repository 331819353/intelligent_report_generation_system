DROP TRIGGER IF EXISTS report_evidence_artifact_guard ON platform.report_evidence_artifacts;
DROP FUNCTION IF EXISTS platform.guard_report_evidence_artifact();
DROP TRIGGER IF EXISTS report_insight_artifact_guard ON platform.report_insight_artifacts;
DROP FUNCTION IF EXISTS platform.guard_report_insight_artifact();

CREATE OR REPLACE FUNCTION platform.guard_report_ai_summary()
RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,platform AS $$
DECLARE forbidden text;
BEGIN
  FOREACH forbidden IN ARRAY ARRAY['prompt','rawPrompt','sampleRows','rawData','resultRows'] LOOP
    IF NEW.request_summary_json ? forbidden THEN
      RAISE EXCEPTION 'request summary contains forbidden field %',forbidden USING ERRCODE='23514';
    END IF;
  END LOOP;
  RETURN NEW;
END
$$;
REVOKE ALL ON FUNCTION platform.guard_report_ai_summary() FROM PUBLIC;
