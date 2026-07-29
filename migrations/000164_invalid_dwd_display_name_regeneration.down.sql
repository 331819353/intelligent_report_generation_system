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
    '          AND dwd.status<>''DISABLED''
          AND dwd.name !~ ''[A-Za-z]''
          AND (',
    '          AND dwd.status<>''DISABLED''
          AND ('
  );

  IF definition=original
     OR position('dwd.name !~ ''[A-Za-z]''' IN definition)>0 THEN
    RAISE EXCEPTION '无法回滚异常 DWD 中文显示名增量重建';
  END IF;
  EXECUTE definition;
END
$migration$;

COMMENT ON FUNCTION platform.trigger_manual_dwd_modeling(uuid) IS
  '仅选择当前领域最新成功 DIM 批次；DWD 完整时幂等返回，产物被删除时基于当前最新已发布 DIM 增量重建';
