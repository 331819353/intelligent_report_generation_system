DROP INDEX IF EXISTS platform.data_sources_domain_connection_identity_active_key;
ALTER TABLE platform.data_sources DROP COLUMN IF EXISTS connection_identity;
