-- 不删除注册设置和历史 AI 审计记录，回滚只撤销新的运行时授权。
ALTER TABLE platform.ai_tenant_policies
  DROP CONSTRAINT ai_tenant_policies_purposes_check;

UPDATE platform.ai_tenant_policies
SET allowed_purposes=array_remove(allowed_purposes,'DATA_SOURCE_CONFIGURATION');

UPDATE platform.ai_tenant_policies
SET allowed_purposes=ARRAY['METADATA_COMPLETION']::text[]
WHERE cardinality(allowed_purposes)=0;

ALTER TABLE platform.ai_tenant_policies
  ADD CONSTRAINT ai_tenant_policies_purposes_check CHECK(
    cardinality(allowed_purposes) BETWEEN 1 AND 4
    AND array_position(allowed_purposes,NULL) IS NULL
    AND allowed_purposes <@ ARRAY[
      'METADATA_COMPLETION','DATASET_DAG_GENERATION',
      'DATASET_TAG_SUGGESTION','DATASET_SEMANTIC_NAMING'
    ]::text[]
  );

COMMENT ON COLUMN platform.ai_tenant_policies.allowed_purposes IS
  '租户显式授权的 AI 用途；模型输出不能代替人工治理与发布审批';
