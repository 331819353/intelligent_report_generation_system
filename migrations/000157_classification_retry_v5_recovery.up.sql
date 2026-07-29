-- 逐 ODS 分类已经升级到 v5，任务中心重试必须继续使用同一合同。
-- 旧函数仍把 v5 阶段审计降级为 v4，虽然 worker 实际执行 v5，
-- 但会破坏检查点身份和运行日志的一致性。
DO $migration$
DECLARE
  definition text;
BEGIN
  SELECT pg_get_functiondef(
    'platform.retry_dwd_modeling_stage_task(uuid,uuid)'::regprocedure
  ) INTO definition;
  IF position('warehouse-classification-v5' IN definition)>0
     AND position('warehouse-classification-v4' IN definition)=0 THEN
    RETURN;
  END IF;
  definition := replace(
    definition,
    'warehouse-classification-v4',
    'warehouse-classification-v5'
  );
  IF position('warehouse-classification-v5' IN definition)=0
     OR position('warehouse-classification-v4' IN definition)>0 THEN
    RAISE EXCEPTION '无法升级建模阶段重试函数到 classification-v5';
  END IF;
  EXECUTE definition;
END
$migration$;

-- 只修复已经证明属于 v5 运行的审计行，不改写真实历史 v4 记录。
UPDATE platform.dwd_modeling_stage_jobs AS stage
SET prompt_version='warehouse-classification-v5',updated_at=now()
WHERE stage.stage='DOMAIN_CLASSIFICATION'
  AND stage.prompt_version='warehouse-classification-v4'
  AND (
    EXISTS(
      SELECT 1
      FROM platform.dwd_modeling_checkpoints AS checkpoint
      WHERE checkpoint.job_id=stage.workflow_job_id
        AND checkpoint.checkpoint_kind='CLASSIFICATION'
        AND checkpoint.prompt_version='warehouse-classification-v5'
    )
    OR stage.requested_at>=(
      SELECT migration.applied_at
      FROM public.platform_schema_migrations AS migration
      WHERE migration.version='000154_per_dataset_parallel_warehouse_modeling'
    )
  );

COMMENT ON FUNCTION platform.retry_dwd_modeling_stage_task(uuid,uuid) IS
  '重试指定建模阶段；classification-v5 保留逐 ODS 检查点并恢复所有受影响的依赖阶段';
