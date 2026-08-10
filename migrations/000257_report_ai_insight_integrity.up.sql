CREATE OR REPLACE FUNCTION platform.guard_report_ai_summary()
RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,platform AS $$
DECLARE summary_key text;
BEGIN
  FOR summary_key IN SELECT jsonb_object_keys(NEW.request_summary_json) LOOP
    IF summary_key NOT IN ('intent','selectionIds','availableFields') THEN
      RAISE EXCEPTION 'request summary contains unsupported field %',summary_key USING ERRCODE='23514';
    END IF;
  END LOOP;
  IF jsonb_typeof(COALESCE(NEW.request_summary_json->'selectionIds','[]'::jsonb)) <> 'array'
    OR jsonb_typeof(COALESCE(NEW.request_summary_json->'availableFields','[]'::jsonb)) <> 'array'
    OR jsonb_typeof(COALESCE(NEW.request_summary_json->'intent','""'::jsonb)) <> 'string' THEN
    RAISE EXCEPTION 'request summary field types are invalid' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION platform.guard_report_insight_artifact()
RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,platform AS $$
BEGIN
  IF TG_OP='DELETE' THEN
    RAISE EXCEPTION 'report insight artifacts are append-only' USING ERRCODE='55000';
  END IF;
  IF OLD.id<>NEW.id OR OLD.tenant_id<>NEW.tenant_id OR OLD.report_id<>NEW.report_id
    OR OLD.component_id<>NEW.component_id OR OLD.evidence_id<>NEW.evidence_id
    OR OLD.evidence_hash<>NEW.evidence_hash OR OLD.human_edited<>NEW.human_edited
    OR OLD.human_edited_by IS DISTINCT FROM NEW.human_edited_by
    OR OLD.human_edited_at IS DISTINCT FROM NEW.human_edited_at
    OR OLD.created_at<>NEW.created_at OR OLD.status<>'CURRENT' OR NEW.status<>'STALE'
    OR (OLD.artifact_json - 'status') IS DISTINCT FROM (NEW.artifact_json - 'status')
    OR NEW.artifact_json->>'status'<>'STALE' THEN
    RAISE EXCEPTION 'report insight artifacts are immutable except CURRENT to STALE' USING ERRCODE='55000';
  END IF;
  RETURN NEW;
END
$$;

CREATE TRIGGER report_insight_artifact_guard
BEFORE UPDATE OR DELETE ON platform.report_insight_artifacts
FOR EACH ROW EXECUTE FUNCTION platform.guard_report_insight_artifact();

CREATE OR REPLACE FUNCTION platform.guard_report_evidence_artifact()
RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,platform AS $$
BEGIN
  IF TG_OP='DELETE' THEN
    RAISE EXCEPTION 'report evidence artifacts are append-only' USING ERRCODE='55000';
  END IF;
  IF OLD IS DISTINCT FROM NEW THEN
    RAISE EXCEPTION 'report evidence artifacts are immutable' USING ERRCODE='55000';
  END IF;
  RETURN NEW;
END
$$;

CREATE TRIGGER report_evidence_artifact_guard
BEFORE UPDATE OR DELETE ON platform.report_evidence_artifacts
FOR EACH ROW EXECUTE FUNCTION platform.guard_report_evidence_artifact();

REVOKE ALL ON FUNCTION platform.guard_report_ai_summary() FROM PUBLIC;
REVOKE ALL ON FUNCTION platform.guard_report_insight_artifact() FROM PUBLIC;
REVOKE ALL ON FUNCTION platform.guard_report_evidence_artifact() FROM PUBLIC;
