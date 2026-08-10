DROP TRIGGER IF EXISTS users_guard_responsibility ON platform.users;
DROP FUNCTION IF EXISTS platform.guard_user_disable_responsibility();
DROP FUNCTION IF EXISTS platform.user_has_open_responsibility(uuid);
DROP TABLE IF EXISTS platform.user_lifecycle_events;
DROP TABLE IF EXISTS platform.user_lifecycle_batch_items;
DROP TABLE IF EXISTS platform.user_lifecycle_batches;
DROP TRIGGER IF EXISTS datasets_default_owner ON platform.datasets;
DROP FUNCTION IF EXISTS platform.default_dataset_owner();
ALTER TABLE platform.datasets DROP COLUMN IF EXISTS owner_user_id;
