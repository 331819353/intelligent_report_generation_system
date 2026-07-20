-- 为数据集自然语言 DAG 配置增加独立的 AI 审计用途。
ALTER TABLE platform.ai_tenant_policies
  DROP CONSTRAINT ai_tenant_policies_purposes_check;

ALTER TABLE platform.ai_tenant_policies
  ADD CONSTRAINT ai_tenant_policies_purposes_check CHECK(
    cardinality(allowed_purposes) BETWEEN 1 AND 5
    AND array_position(allowed_purposes,NULL) IS NULL
    AND allowed_purposes <@ ARRAY[
      'METADATA_COMPLETION',
      'REPORT_GENERATION',
      'BLOCK_EDIT',
      'CONCLUSION_GENERATION',
      'DATASET_DAG_GENERATION'
    ]::text[]
  );

ALTER TABLE platform.ai_requests
  DROP CONSTRAINT ai_requests_purpose_check;

ALTER TABLE platform.ai_requests
  ADD CONSTRAINT ai_requests_purpose_check CHECK(purpose IN (
    'METADATA_COMPLETION',
    'REPORT_GENERATION',
    'BLOCK_EDIT',
    'CONCLUSION_GENERATION',
    'DATASET_DAG_GENERATION'
  ));

-- 迁移只扩展允许的用途枚举，不修改新租户默认值，也不为已启用 AI 的存量租户自动授权。
-- DATASET_DAG_GENERATION 必须由受信管理流程显式加入 allowed_purposes。

COMMENT ON COLUMN platform.ai_tenant_policies.allowed_purposes IS
  '租户显式授权的 AI 用途；DATASET_DAG_GENERATION 只生成待用户确认的画布提案，不直接持久化或发布';

COMMENT ON TABLE platform.ai_tenant_policies IS
  '租户级 AI 授权用途和日/月配额策略；新租户默认禁用且仅预置元数据补全用途，其他用途必须由受信管理流程显式授权';
