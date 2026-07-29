-- 移除 source_scope 中的领域 UUID；仅用于回滚 000151。
DO $migration$
DECLARE
  definition text;
BEGIN
  SELECT pg_get_functiondef(
    'platform.trigger_manual_dws_modeling(uuid)'::regprocedure
  ) INTO definition;
  definition := replace(
    definition,
    'domain.id AS domain_id,domain.code AS domain_code,domain.name AS domain_name',
    'domain.code AS domain_code,domain.name AS domain_name'
  );
  definition := replace(
    definition,
    'member.tenant_id,member.group_key,'||E'\n      '||
      'min(member.domain_id) AS domain_id,'||E'\n      ',
    'member.tenant_id,member.group_key,'||E'\n      '
  );
  definition := replace(
    definition,
    '''groupKey'',grouped.group_key,'||E'\n        '||
      '''domainId'',grouped.domain_id::text,'||E'\n        ',
    '''groupKey'',grouped.group_key,'||E'\n        '
  );
  EXECUTE definition;
END
$migration$;
