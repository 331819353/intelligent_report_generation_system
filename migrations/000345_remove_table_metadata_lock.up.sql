-- 表级人工锁没有有效的保护边界；刷新保护统一收口到字段级锁。
UPDATE platform.metadata_tables
SET manual_locked = false
WHERE manual_locked;

COMMENT ON COLUMN platform.metadata_tables.manual_locked IS
  'Deprecated compatibility column; table-level metadata is always refreshable and new writes keep this false';
