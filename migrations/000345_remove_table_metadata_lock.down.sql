-- 已清除的历史表级锁无法可靠还原。回滚仅恢复旧字段说明，字段值继续保持 false。
COMMENT ON COLUMN platform.metadata_tables.manual_locked IS
  'Manual definitions are not overwritten automatically by AI suggestions';
