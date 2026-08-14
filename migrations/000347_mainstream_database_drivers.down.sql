-- PostgreSQL cannot safely remove individual enum values in place. A rollback
-- leaves unused labels available in storage; the previous application version
-- still rejects them and migration 000348 restores its row constraints.
COMMENT ON TYPE platform.data_source_type IS NULL;
