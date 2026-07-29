-- 回滚函数身份，但不伪造已经真实执行过 v5 的阶段审计。
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
    'warehouse-classification-v4'
  );
  IF position('warehouse-classification-v4' IN definition)=0 THEN
    RAISE EXCEPTION '无法回滚建模阶段重试函数到 classification-v4';
  END IF;
  EXECUTE definition;
END
$migration$;

COMMENT ON FUNCTION platform.retry_dwd_modeling_stage_task(uuid,uuid) IS
  '重试指定建模阶段；旧分类合同自动升级并从领域识别阶段重新执行';
