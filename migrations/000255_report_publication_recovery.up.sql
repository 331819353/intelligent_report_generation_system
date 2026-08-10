ALTER TABLE platform.report_versions
  ADD COLUMN artifact_attempt integer NOT NULL DEFAULT 0 CHECK(artifact_attempt BETWEEN 0 AND 20),
  ADD COLUMN artifact_next_attempt_at timestamptz NOT NULL DEFAULT now(),
  ADD COLUMN artifact_lease_token uuid,
  ADD COLUMN artifact_lease_expires_at timestamptz,
  ADD COLUMN artifact_error_code text NOT NULL DEFAULT '' CHECK(length(artifact_error_code)<=128),
  ADD CONSTRAINT report_v2_artifact_lease_shape_check CHECK(
    (artifact_lease_token IS NULL AND artifact_lease_expires_at IS NULL)
    OR (artifact_state='RETRY' AND artifact_lease_token IS NOT NULL AND artifact_lease_expires_at IS NOT NULL)
  );

DROP INDEX IF EXISTS platform.report_v2_artifact_retry_idx;
CREATE INDEX report_v2_artifact_retry_idx
  ON platform.report_versions(tenant_id,artifact_state,artifact_next_attempt_at,published_at,id)
  WHERE artifact_state<>'READY';

CREATE OR REPLACE FUNCTION platform.guard_report_v2_version_mutation()
RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,platform AS $$
BEGIN
  IF TG_OP='DELETE' THEN
    RAISE EXCEPTION 'report v2 immutable artifact cannot be deleted' USING ERRCODE='55000';
  END IF;
  IF ROW(OLD.id,OLD.tenant_id,OLD.report_id,OLD.version_no,OLD.source_revision_no,
         OLD.definition_json,OLD.definition_bytes,OLD.definition_hash,OLD.schema_version,
         OLD.object_uri,OLD.published_by,OLD.published_at,OLD.rollback_of_version_no,
         OLD.rollback_reason,OLD.stale_insights_acknowledged)
     IS DISTINCT FROM
     ROW(NEW.id,NEW.tenant_id,NEW.report_id,NEW.version_no,NEW.source_revision_no,
         NEW.definition_json,NEW.definition_bytes,NEW.definition_hash,NEW.schema_version,
         NEW.object_uri,NEW.published_by,NEW.published_at,NEW.rollback_of_version_no,
         NEW.rollback_reason,NEW.stale_insights_acknowledged) THEN
    RAISE EXCEPTION 'report v2 immutable artifact content cannot be changed' USING ERRCODE='55000';
  END IF;
  IF OLD.artifact_state='READY' OR
     (OLD.artifact_state='PENDING' AND NEW.artifact_state NOT IN('PENDING','READY','RETRY')) OR
     (OLD.artifact_state='RETRY' AND NEW.artifact_state NOT IN('RETRY','READY')) THEN
    RAISE EXCEPTION 'invalid report artifact state transition' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.guard_report_v2_version_mutation() FROM PUBLIC;
