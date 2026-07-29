-- 回滚函数入口版本；阶段审计可能已经真实执行过新合同，因此不伪造回退。
DO $migration$
DECLARE
  definition text;
BEGIN
  SELECT pg_get_functiondef(
    'platform.trigger_manual_dim_modeling(uuid)'::regprocedure
  ) INTO definition;
  definition := replace(
    definition,
    'warehouse-classification-v6',
    'warehouse-classification-v5'
  );
  definition := replace(
    definition,
    'warehouse-dimension-design-v4',
    'warehouse-dimension-design-v3'
  );
  IF position('warehouse-classification-v5' IN definition)=0
     OR position('warehouse-dimension-design-v3' IN definition)=0 THEN
    RAISE EXCEPTION '无法回滚 DIM 建模入口的事件事实合同';
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
    'warehouse-classification-v6',
    'warehouse-classification-v5'
  );
  IF position('warehouse-classification-v5' IN definition)=0 THEN
    RAISE EXCEPTION '无法回滚建模阶段重试函数到 classification-v5';
  END IF;
  EXECUTE definition;
END
$migration$;

COMMENT ON FUNCTION platform.trigger_manual_dim_modeling(uuid) IS
  '按用户当前业务领域触发 DIM 建模并创建独立持久阶段任务';

COMMENT ON FUNCTION platform.retry_dwd_modeling_stage_task(uuid,uuid) IS
  '重试指定建模阶段；classification-v5 保留逐 ODS 检查点并恢复所有受影响的依赖阶段';
