-- 保存结构化 Join 风险告警，便于预览结果与后续审计使用同一诊断事实。
ALTER TABLE platform.query_runs
  ADD COLUMN warnings_json jsonb NOT NULL DEFAULT '[]'::jsonb
  CHECK(jsonb_typeof(warnings_json)='array');

COMMENT ON COLUMN platform.query_runs.warnings_json IS '不含业务键值的查询语义与性能风险告警';
