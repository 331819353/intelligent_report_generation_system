DROP TRIGGER IF EXISTS dataset_materializations_00_enqueue_dimension_profiles
  ON platform.dataset_materializations;

ALTER TABLE platform.dimension_members
  DROP CONSTRAINT IF EXISTS dimension_members_reserved_default_inactive_check;

DROP FUNCTION IF EXISTS platform.is_reserved_dimension_default(text);

-- 历史默认成员已被安全停用，down 不自动恢复为 ACTIVE。
