-- V2 规则会从明细数据集补充记录数、标识符去重数和待复核数值属性。
-- 使用新的工作流版本为所有当前发布快照重新入队；精确版本和版本号唯一约束保证幂等。
INSERT INTO platform.metric_extraction_jobs(
  tenant_id,dataset_id,dataset_version_id,dsl_hash,requested_by,extractor_version
)
SELECT version.tenant_id,version.dataset_id,version.id,version.schema_hash,
  version.published_by,'metric-candidate-semantic-v3'
FROM platform.dataset_versions AS version
JOIN platform.datasets AS dataset
  ON dataset.id=version.dataset_id AND dataset.tenant_id=version.tenant_id
 AND dataset.current_published_version_id=version.id
WHERE version.status='PUBLISHED' AND dataset.status='PUBLISHED'
  AND dataset.deleted_at IS NULL
ON CONFLICT(tenant_id,dataset_version_id,extractor_version) DO NOTHING;

COMMENT ON COLUMN platform.metric_extraction_jobs.extractor_version IS
  '发布后工作流版本；V3 使用 metric-candidate-v2 规则覆盖记录数、标识符去重数和全部数值原子候选';
