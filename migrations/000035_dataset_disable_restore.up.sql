-- 数据集停用是可恢复的目录级状态，不再反向改写不可变发布版本。
-- 保存停用前的稳定状态和精确发布指针，使恢复可以在行锁内还原原状态；
-- 迁移前的 DEPRECATED 同时承载“停用”和“永久废弃”两种含义；本迁移先引入
-- DISABLED 兼容状态，随后由 000036 依据审计轨迹把真正永久废弃的记录纠正回去。
ALTER TABLE platform.datasets
  DROP CONSTRAINT datasets_status_check,
  ADD CONSTRAINT datasets_status_check
    CHECK(status IN ('DRAFT','VALIDATING','PUBLISHED','STALE','DEPRECATED','DISABLED')),
  ADD COLUMN disabled_from_status text,
  ADD COLUMN disabled_published_version_id uuid;

ALTER TABLE platform.datasets
  ADD CONSTRAINT datasets_disabled_from_status_check
    CHECK(disabled_from_status IS NULL OR disabled_from_status IN ('DRAFT','PUBLISHED','STALE')),
  ADD CONSTRAINT datasets_disabled_published_version_fk
    FOREIGN KEY(disabled_published_version_id,id,tenant_id)
    REFERENCES platform.dataset_versions(id,dataset_id,tenant_id),
  ADD CONSTRAINT datasets_disabled_snapshot_shape_check CHECK(
    status='DISABLED'
      OR disabled_from_status IS NULL AND disabled_published_version_id IS NULL
  );

-- 旧实现把停用和永久废弃都记录为 DEPRECATED，且已清除当前发布指针。
-- 先标记为候选停用记录；000036 只保留审计能够证明由 DISABLE 产生的记录。
UPDATE platform.datasets
SET status='DISABLED'
WHERE status='DEPRECATED' AND deleted_at IS NULL;

COMMENT ON COLUMN platform.datasets.disabled_from_status IS
  '停用前的数据集稳定状态；仅用于 DISABLED 到原状态的受控恢复';
COMMENT ON COLUMN platform.datasets.disabled_published_version_id IS
  '停用前的精确当前发布版本；恢复 PUBLISHED 状态时重新挂接，不改写版本快照';
