-- 为只读指标创建提案增加独立 AI 计量与审计分类。
-- METRIC_AUTHORING 不是租户用途白名单项；只要通用 AI 开关启用即可调用。

ALTER TABLE platform.ai_requests
  DROP CONSTRAINT ai_requests_purpose_check;

ALTER TABLE platform.ai_requests
  ADD CONSTRAINT ai_requests_purpose_check CHECK(purpose IN (
    'METADATA_COMPLETION',
    'REPORT_GENERATION',
    'BLOCK_EDIT',
    'CONCLUSION_GENERATION',
    'DATASET_DAG_GENERATION',
    'METRIC_AUTHORING'
  ));

-- 本迁移不修改租户开关、allowed_purposes 或配额。调用仍受通用
-- enabled 开关、操作者状态、配额预留和 ai_requests 审计约束。
