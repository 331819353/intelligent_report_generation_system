-- 回滚仅恢复标签建议 prompt 身份。历史业务领域标签不会被删除；建模函数
-- 保留更严格的用户领域隔离，避免回滚代码时重新引入跨领域访问。
DO $migration$
DECLARE
  definition text;
BEGIN
  SELECT pg_get_functiondef(
    'platform.enqueue_dataset_tag_suggestion()'::regprocedure
  ) INTO definition;
  definition := replace(
    definition,
    'dataset-tag-suggestion-v6',
    'dataset-tag-suggestion-v5'
  );
  EXECUTE definition;
END
$migration$;
