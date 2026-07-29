-- 无度量事件事实同样属于 FACT。升级 DIM 建模入口及重试审计合同，
-- 确保新任务和失败阶段恢复都使用包含事件粒度硬约束的版本。
DO $migration$
DECLARE
  definition text;
  original text;
BEGIN
  SELECT pg_get_functiondef(
    'platform.trigger_manual_dim_modeling(uuid)'::regprocedure
  ) INTO definition;
  original := definition;
  definition := replace(
    definition,
    'warehouse-classification-v5',
    'warehouse-classification-v6'
  );
  definition := replace(
    definition,
    'warehouse-dimension-design-v3',
    'warehouse-dimension-design-v4'
  );
  IF definition=original
     OR position('warehouse-classification-v6' IN definition)=0
     OR position('warehouse-dimension-design-v4' IN definition)=0
     OR position('warehouse-classification-v5' IN definition)>0
     OR position('warehouse-dimension-design-v3' IN definition)>0 THEN
    RAISE EXCEPTION '无法升级 DIM 建模入口的事件事实合同';
  END IF;
  EXECUTE definition;
END
$migration$;

DO $migration$
DECLARE
  definition text;
BEGIN
  SELECT pg_get_functiondef(
    'platform.retry_dwd_modeling_stage_task(uuid,uuid)'::regprocedure
  ) INTO definition;
  definition := replace(
    definition,
    'warehouse-classification-v5',
    'warehouse-classification-v6'
  );
  IF position('warehouse-classification-v6' IN definition)=0
     OR position('warehouse-classification-v5' IN definition)>0 THEN
    RAISE EXCEPTION '无法升级建模阶段重试函数到 classification-v6';
  END IF;
  EXECUTE definition;
END
$migration$;

-- 只把会在新 worker 下继续执行的阶段，以及已经存在新版本检查点的
-- 阶段审计升级到实际合同；不改写已完成的真实历史旧版本运行。
UPDATE platform.dwd_modeling_stage_jobs AS stage
SET prompt_version='warehouse-classification-v6',updated_at=now()
WHERE stage.stage='DOMAIN_CLASSIFICATION'
  AND stage.prompt_version='warehouse-classification-v5'
  AND (
    stage.status IN (
      'PENDING','WAITING_DEPENDENCY','RUNNING','PARTIAL','FAILED','SKIPPED'
    )
    OR EXISTS(
      SELECT 1
      FROM platform.dwd_modeling_checkpoints AS checkpoint
      WHERE checkpoint.job_id=stage.workflow_job_id
        AND checkpoint.checkpoint_kind='CLASSIFICATION'
        AND checkpoint.prompt_version='warehouse-classification-v6'
    )
  );

UPDATE platform.dwd_modeling_stage_jobs AS stage
SET prompt_version='warehouse-dimension-design-v4',updated_at=now()
WHERE stage.stage='DIMENSION_MODELING'
  AND stage.prompt_version='warehouse-dimension-design-v3'
  AND (
    stage.status IN (
      'PENDING','WAITING_DEPENDENCY','RUNNING','PARTIAL','FAILED','SKIPPED'
    )
    OR EXISTS(
      SELECT 1
      FROM platform.dwd_modeling_checkpoints AS checkpoint
      WHERE checkpoint.job_id=stage.workflow_job_id
        AND checkpoint.checkpoint_kind='DIM_DESIGN'
        AND checkpoint.prompt_version='warehouse-dimension-design-v4'
    )
  );

COMMENT ON FUNCTION platform.trigger_manual_dim_modeling(uuid) IS
  '按用户当前业务领域触发 DIM 建模；classification-v6 拒绝把无度量事件事实设计为 DIM';

COMMENT ON FUNCTION platform.retry_dwd_modeling_stage_task(uuid,uuid) IS
  '重试指定建模阶段；classification-v6 保留逐 ODS 检查点并恢复所有受影响的依赖阶段';
