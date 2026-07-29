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
    'domain.name AS domain_key',
    '''领域:''||domain.name AS domain_key'
  );
  IF definition=original
     OR position('''领域:''||domain.name AS domain_key' IN definition)=0 THEN
    RAISE EXCEPTION '无法回滚明细建模入口的旧领域键';
  END IF;
  EXECUTE definition;
END
$migration$;

COMMENT ON FUNCTION platform.trigger_manual_dwd_modeling(uuid) IS
  '全部关联 DIM 发布后，人工恢复当前用户业务领域最近一次成功维度批次的事实落地阶段';
