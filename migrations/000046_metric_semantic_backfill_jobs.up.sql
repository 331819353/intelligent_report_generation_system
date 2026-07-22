-- 复用 V1 候选指纹执行一次 V2 语义回填；ON CONFLICT 保证候选不会重复，
-- worker 只为缺失的候选补充 LLM 语义文档和向量任务。
INSERT INTO platform.metric_extraction_jobs(
  tenant_id,dataset_id,dataset_version_id,dsl_hash,requested_by,extractor_version
)
SELECT version.tenant_id,version.dataset_id,version.id,version.schema_hash,
  version.published_by,'metric-candidate-semantic-v2'
FROM platform.dataset_versions AS version
JOIN platform.datasets AS dataset
  ON dataset.id=version.dataset_id AND dataset.tenant_id=version.tenant_id
 AND dataset.current_published_version_id=version.id
WHERE version.status='PUBLISHED' AND dataset.status='PUBLISHED'
  AND dataset.deleted_at IS NULL
ON CONFLICT(tenant_id,dataset_version_id,extractor_version) DO NOTHING;

COMMENT ON COLUMN platform.metric_extraction_jobs.extractor_version IS
  '发布后工作流版本；候选本体指纹仍由稳定的规则提取器版本决定，语义回填不会复制候选';
