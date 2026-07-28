BEGIN;

DROP INDEX IF EXISTS platform.auth_sessions_domain_active_idx;
ALTER TABLE platform.auth_sessions
  DROP CONSTRAINT IF EXISTS auth_sessions_business_domain_fk,
  DROP COLUMN IF EXISTS business_domain_id;

COMMIT;
