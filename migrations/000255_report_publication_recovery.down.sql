DROP INDEX IF EXISTS platform.report_v2_artifact_retry_idx;
ALTER TABLE platform.report_versions
  DROP CONSTRAINT IF EXISTS report_v2_artifact_lease_shape_check,
  DROP COLUMN IF EXISTS artifact_error_code,
  DROP COLUMN IF EXISTS artifact_lease_expires_at,
  DROP COLUMN IF EXISTS artifact_lease_token,
  DROP COLUMN IF EXISTS artifact_next_attempt_at,
  DROP COLUMN IF EXISTS artifact_attempt;
CREATE INDEX report_v2_artifact_retry_idx ON platform.report_versions(artifact_state,published_at)
  WHERE artifact_state<>'READY';
