-- 自然语言语义槽位解析只产生只读 QueryPlan，不执行 SQL，也不直接修改 DAG。
-- 与指标起草和标签建议一致，它受租户总 AI 开关、配额、脱敏和摘要审计保护，
-- 不扩张历史 allowed_purposes 数组。

ALTER TABLE platform.ai_requests
  DROP CONSTRAINT ai_requests_purpose_check;

ALTER TABLE platform.ai_requests
  ADD CONSTRAINT ai_requests_purpose_check CHECK(purpose IN (
    'METADATA_COMPLETION',
    'REPORT_GENERATION',
    'BLOCK_EDIT',
    'CONCLUSION_GENERATION',
    'DATASET_DAG_GENERATION',
    'METRIC_AUTHORING',
    'DATASET_TAG_SUGGESTION',
    'SEMANTIC_QUERY_PLANNING'
  ));
