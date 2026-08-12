-- Repair historical extraction receipts produced before legacy report zone
-- ordering was normalized by the compiler. Rows whose source version no
-- longer exists are terminal; surviving READY versions are safe to retry.
BEGIN;

UPDATE askdata.report_asset_extraction_outbox AS outbox
SET error_code = 'REPORT_ASSET_SOURCE_GONE',
    updated_at = now()
WHERE outbox.state = 'FAILED'
  AND outbox.error_code = 'REPORT_ASSET_EXTRACTION_FAILED'
  AND NOT EXISTS (
    SELECT 1
    FROM platform.report_versions AS version
    WHERE version.id = outbox.report_version_id
      AND version.report_id = outbox.report_id
      AND version.tenant_id = outbox.tenant_id
  );

UPDATE askdata.report_asset_extraction_outbox AS outbox
SET state = 'PENDING',
    attempt = 0,
    next_attempt_at = now(),
    lease_token = NULL,
    lease_expires_at = NULL,
    error_code = '',
    updated_at = now()
WHERE outbox.state = 'FAILED'
  AND outbox.error_code = 'REPORT_ASSET_EXTRACTION_FAILED'
  AND EXISTS (
    SELECT 1
    FROM platform.report_versions AS version
    WHERE version.id = outbox.report_version_id
      AND version.report_id = outbox.report_id
      AND version.tenant_id = outbox.tenant_id
      AND version.artifact_state = 'READY'
  );

COMMIT;
