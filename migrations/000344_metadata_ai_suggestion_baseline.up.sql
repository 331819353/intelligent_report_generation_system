-- 待确认建议此前用 expected_business_version 精确匹配作为唯一并发栅栏，存在两个缺陷：
--   1. 建议入库时写的是"构建模型输入那一刻"的版本快照，而不是在同一事务里加锁读到的
--      当前版本。一旦模型思考期间目标被改写，建议会以 VERSION_CHANGED 落为待确认，
--      而它记录的期望版本恰好就是已经过期的那个值，导致该建议从写入起就永远无法被接受。
--   2. business_version 是一个任何写入都会自增的计数器（人工保存、后续 AI 轮次应用、
--      手工完成资产化），与"这条建议要覆盖的内容是否被人改过"没有直接关系。
--
-- baseline_value 记录建议生成时目标的业务字段快照，接受时用内容比对代替计数器比对：
-- 内容没变就可以安全应用，内容变了才是真正的冲突，并能准确告诉用户变了哪些字段。
BEGIN;

ALTER TABLE platform.ai_metadata_suggestions
  ADD COLUMN IF NOT EXISTS baseline_value jsonb NOT NULL DEFAULT '{}'::jsonb;

COMMENT ON COLUMN platform.ai_metadata_suggestions.baseline_value IS
  'Business-field snapshot of the target when the suggestion was generated; empty object means legacy row with unknown baseline';
COMMENT ON COLUMN platform.ai_metadata_suggestions.expected_business_version IS
  'Target business version observed under lock when the suggestion was written; auditing and diagnostics only, no longer an apply gate';

COMMIT;
