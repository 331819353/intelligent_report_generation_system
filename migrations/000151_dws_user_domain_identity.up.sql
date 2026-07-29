-- 000150 已部署的环境需要把当前领域 UUID 固化进 DWS source_scope，worker
-- 才能在后续独立事务中恢复同一访问上下文，而不是退回租户默认领域。
DO $migration$
DECLARE
  definition text;
  original text;
BEGIN
  SELECT pg_get_functiondef(
    'platform.trigger_manual_dws_modeling(uuid)'::regprocedure
  ) INTO definition;
  IF position('''domainId'',grouped.domain_id::text' IN definition)>0 THEN
    RETURN;
  END IF;
  original := definition;
  definition := replace(
    definition,
    'domain.code AS domain_code,domain.name AS domain_name',
    'domain.id AS domain_id,domain.code AS domain_code,domain.name AS domain_name'
  );
  definition := replace(
    definition,
    'member.tenant_id,member.group_key,'||E'\n      '||
      'min(member.domain_code) AS domain_code,',
    'member.tenant_id,member.group_key,'||E'\n      '||
      'min(member.domain_id) AS domain_id,'||E'\n      '||
      'min(member.domain_code) AS domain_code,'
  );
  definition := replace(
    definition,
    '''groupKey'',grouped.group_key,'||E'\n        '||
      '''domainCode'',grouped.domain_code,',
    '''groupKey'',grouped.group_key,'||E'\n        '||
      '''domainId'',grouped.domain_id::text,'||E'\n        '||
      '''domainCode'',grouped.domain_code,'
  );
  IF definition=original
     OR position('domain.id AS domain_id' IN definition)=0
     OR position('''domainId'',grouped.domain_id::text' IN definition)=0 THEN
    RAISE EXCEPTION '无法升级 DWS 当前领域身份合同';
  END IF;
  EXECUTE definition;
END
$migration$;

COMMENT ON FUNCTION platform.trigger_manual_dws_modeling(uuid) IS
  '只用当前用户所属领域的 DIM/DWD；source_scope 固化领域 UUID 供 worker 恢复访问上下文';
