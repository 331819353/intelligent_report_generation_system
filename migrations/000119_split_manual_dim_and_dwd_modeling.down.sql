DROP FUNCTION IF EXISTS platform.trigger_manual_dim_modeling(uuid);

-- 回滚只恢复 000114 的组合触发合同；历史工作流阶段保持可领取。
UPDATE platform.dwd_modeling_stage_jobs
SET manual_enabled=true
WHERE NOT manual_enabled;

ALTER TABLE platform.dwd_modeling_stage_jobs
  DROP COLUMN manual_enabled;

-- 完整的旧函数由 000114_manual_modeling_run_identity.up.sql 定义。数据库迁移
-- 工具只向前执行；需要降级时应在事务中重放该定义，避免这里复制后产生漂移。
