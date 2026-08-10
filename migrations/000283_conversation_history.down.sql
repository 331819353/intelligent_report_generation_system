DROP INDEX IF EXISTS askdata.askdata_conversations_history_idx;
ALTER TABLE askdata.conversations
  DROP CONSTRAINT IF EXISTS askdata_conversations_archive_shape_check,
  DROP COLUMN IF EXISTS record_version,
  DROP COLUMN IF EXISTS archived_at,
  DROP COLUMN IF EXISTS is_pinned;
