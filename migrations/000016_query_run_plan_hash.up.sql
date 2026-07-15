-- 文件预览没有 SQL 文本，统一以执行计划摘要记录数据库与文件查询。
ALTER TABLE platform.query_runs RENAME COLUMN sql_hash TO plan_hash;

COMMENT ON COLUMN platform.query_runs.plan_hash IS 'SQL 或固定文件 DSL 执行计划的 SHA-256 摘要，不含查询正文';
