-- Published data-source versions are immutable for business configuration, but
-- credential destruction is a security lifecycle operation. Permit exactly one
-- irreversible mutation: replacing a database credential reference with the
-- source-specific revoked tombstone after the root source enters retirement.
BEGIN;

CREATE OR REPLACE FUNCTION platform.reject_data_source_version_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF TG_OP='UPDATE'
     AND OLD.secret_ref IS DISTINCT FROM NEW.secret_ref
     AND NEW.secret_ref='revoked://data-source/'||OLD.data_source_id::text
     AND NEW.id IS NOT DISTINCT FROM OLD.id
     AND NEW.tenant_id IS NOT DISTINCT FROM OLD.tenant_id
     AND NEW.data_source_id IS NOT DISTINCT FROM OLD.data_source_id
     AND NEW.version_no IS NOT DISTINCT FROM OLD.version_no
     AND NEW.source_type IS NOT DISTINCT FROM OLD.source_type
     AND NEW.config IS NOT DISTINCT FROM OLD.config
     AND NEW.file_asset_id IS NOT DISTINCT FROM OLD.file_asset_id
     AND NEW.file_version_id IS NOT DISTINCT FROM OLD.file_version_id
     AND NEW.config_hash IS NOT DISTINCT FROM OLD.config_hash
     AND NEW.created_by IS NOT DISTINCT FROM OLD.created_by
     AND NEW.created_at IS NOT DISTINCT FROM OLD.created_at
     AND EXISTS(
       SELECT 1
       FROM platform.data_sources AS source
       WHERE source.id=OLD.data_source_id
         AND source.tenant_id=OLD.tenant_id
         AND source.status IN ('DELETING','DELETED')
     )
  THEN
    RETURN NEW;
  END IF;
  RAISE EXCEPTION 'data source versions are immutable';
END
$$;

-- Repair sources retired before this guard was introduced. This is intentionally
-- irreversible and applies only to database-backed versions whose root is gone.
UPDATE platform.data_source_versions AS version
SET secret_ref='revoked://data-source/'||version.data_source_id::text
FROM platform.data_sources AS source
WHERE source.id=version.data_source_id
  AND source.tenant_id=version.tenant_id
  AND source.status='DELETED'
  AND source.source_type IN ('MYSQL','ORACLE')
  AND version.secret_ref NOT LIKE 'revoked://%';

REVOKE ALL ON FUNCTION platform.reject_data_source_version_mutation() FROM PUBLIC;

COMMIT;
