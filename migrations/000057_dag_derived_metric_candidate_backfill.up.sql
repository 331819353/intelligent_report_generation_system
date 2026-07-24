-- v4 识别数据集 DAG 已经完成的聚合输出，并以 DERIVED + NONE 生成候选，
-- 避免在指标层再次 SUM/COUNT。为当前可用发布版本补一次新规则任务；旧候选
-- 作为审计事实保留，但目录接口不再展示旧版“聚合数据集不支持”阻塞项。
INSERT INTO platform.metric_extraction_jobs(
  tenant_id,dataset_id,dataset_version_id,dsl_hash,requested_by,extractor_version
)
SELECT version.tenant_id,version.dataset_id,version.id,version.schema_hash,
       version.published_by,'metric-candidate-semantic-v4'
FROM platform.dataset_versions AS version
JOIN platform.datasets AS dataset
  ON dataset.id=version.dataset_id AND dataset.tenant_id=version.tenant_id
 AND dataset.current_published_version_id=version.id
WHERE version.status='PUBLISHED'
  AND dataset.status='PUBLISHED'
  AND dataset.deleted_at IS NULL
  AND dataset.origin_table_id IS NULL
ON CONFLICT(tenant_id,dataset_version_id,extractor_version) DO NOTHING;
