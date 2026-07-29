UPDATE platform.dwd_modeling_stage_jobs
SET prompt_version='warehouse-classification-v4',updated_at=now()
WHERE stage='DOMAIN_CLASSIFICATION'
  AND prompt_version='warehouse-classification-v5';

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
    'warehouse-classification-v4'
  );
  IF definition=original
     OR position('warehouse-classification-v4' IN definition)=0 THEN
    RAISE EXCEPTION '无法回滚逐 ODS 分类 prompt 合同';
  END IF;
  EXECUTE definition;
END
$migration$;

-- 回滚只恢复 prompt 身份。逐 DWD 的 DWS 范围比旧版按使用范围聚合更严格，
-- 保留该安全范围，避免回滚代码时把已经拆分的任务重新合并。
UPDATE platform.dws_modeling_jobs
SET prompt_version='dws-group-planning-v2',updated_at=now()
WHERE prompt_version='dws-single-fact-planning-v3'
  AND status IN ('PENDING','WAITING_DEPENDENCY');

COMMENT ON FUNCTION platform.trigger_manual_dws_modeling(uuid) IS
  '逐个当前发布 DWD 创建主题建模任务；回滚后仍保留单事实安全范围';
