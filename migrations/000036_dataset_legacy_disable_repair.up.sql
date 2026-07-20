-- 000035 只能从旧主状态 DEPRECATED 推断“曾经不可用”，但旧系统还会在
-- VERSION_DEPRECATED 生命周期迁移中合法产生真正的永久废弃数据集。仅当审计
-- 轨迹能证明最近一次相关生命周期动作是 DISABLE 时，才保留为可恢复停用；
-- 无证据或之后又发生永久废弃的数据集恢复为 DEPRECATED，避免误开放恢复入口。
WITH legacy_disabled AS (
  SELECT dataset.id,
         (SELECT max(log.occurred_at)
            FROM platform.audit_logs AS log
           WHERE log.resource_type='DATASET'
             AND log.resource_id=dataset.id::text
             AND log.action='DISABLE') AS last_disabled_at,
         (SELECT max(log.occurred_at)
            FROM platform.audit_logs AS log
           WHERE log.resource_type='DATASET'
             AND log.resource_id=dataset.id::text
             AND log.action='VERSION_DEPRECATED') AS last_deprecated_at
    FROM platform.datasets AS dataset
   WHERE dataset.status='DISABLED'
     AND dataset.deleted_at IS NULL
     AND dataset.disabled_from_status IS NULL
     AND dataset.disabled_published_version_id IS NULL
)
UPDATE platform.datasets AS dataset
   SET status='DEPRECATED'
  FROM legacy_disabled AS legacy
 WHERE dataset.id=legacy.id
   AND (
     legacy.last_disabled_at IS NULL
     OR legacy.last_deprecated_at IS NOT NULL
        AND legacy.last_deprecated_at > legacy.last_disabled_at
   );

COMMENT ON CONSTRAINT datasets_disabled_snapshot_shape_check ON platform.datasets IS
  '新停用必须携带停用快照；仅审计可证明由旧 DISABLE 产生的兼容记录允许空快照';
