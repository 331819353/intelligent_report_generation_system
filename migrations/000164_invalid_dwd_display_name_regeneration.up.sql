-- DWD 业务显示名只能是中文业务语义。英文名或夹带数仓层级标记的历史产物
-- 不应被计入当前批次的完整产物数，以便沿用最新已发布 DIM 合同增量重建。
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
          AND (',
    '          AND dwd.status<>''DISABLED''
          AND dwd.name !~ ''[A-Za-z]''
          AND ('
  );

  IF definition=original
     OR position('dwd.name !~ ''[A-Za-z]''' IN definition)=0 THEN
    RAISE EXCEPTION '无法启用异常 DWD 中文显示名增量重建';
  END IF;
  EXECUTE definition;
END
$migration$;

COMMENT ON FUNCTION platform.trigger_manual_dwd_modeling(uuid) IS
  '仅选择当前领域最新成功 DIM 批次；缺失、删除或显示名含英文层级字符的 DWD 基于当前最新已发布 DIM 增量重建';
