-- 000153 已把任务和输出的 domain_key 统一为 business_domains.name，
-- 但明细建模入口仍用旧的“领域:<名称>”查找最新 DIM 批次，导致已发布
-- 全部 DIM 后仍误报 DIM_MODELING_REQUIRED。
DO $migration$
DECLARE
  definition text;
  original text;
BEGIN
  SELECT pg_get_functiondef(
    'platform.trigger_manual_dwd_modeling(uuid)'::regprocedure
  ) INTO definition;
  original := definition;
  definition := replace(
    definition,
    '''领域:''||domain.name AS domain_key',
    'domain.name AS domain_key'
  );
  IF definition=original
     OR position('''领域:''||domain.name AS domain_key' IN definition)>0
     OR position('domain.name AS domain_key' IN definition)=0 THEN
    RAISE EXCEPTION '无法把明细建模入口升级为普通业务领域键';
  END IF;
  EXECUTE definition;
END
$migration$;

COMMENT ON FUNCTION platform.trigger_manual_dwd_modeling(uuid) IS
  '使用当前用户业务领域的普通名称恢复最新成功 DIM 批次；全部关联 DIM 发布后人工触发事实 DWD 建模';
