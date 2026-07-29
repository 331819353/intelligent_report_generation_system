-- 已部署 000150/000152 的环境曾短暂使用“领域:<名称>”作为内部兼容键。
-- 彻底移除该前缀，任务和输出只保存 business_domains.name。
UPDATE platform.dwd_modeling_jobs AS job
SET domain_key=domain.name,updated_at=now()
FROM platform.datasets AS dataset,
     platform.business_domains AS domain
WHERE dataset.id=job.trigger_dataset_id
  AND dataset.tenant_id=job.tenant_id
  AND domain.id=dataset.domain_id
  AND domain.tenant_id=dataset.tenant_id
  AND job.domain_key<>domain.name;

UPDATE platform.dim_modeling_outputs AS output
SET domain_key=domain.name,updated_at=now()
FROM platform.datasets AS source,
     platform.business_domains AS domain
WHERE source.id=output.source_dataset_id
  AND source.tenant_id=output.tenant_id
  AND domain.id=source.domain_id
  AND domain.tenant_id=source.tenant_id
  AND output.domain_key<>domain.name;

UPDATE platform.dwd_modeling_outputs AS output
SET domain_key=domain.name,updated_at=now()
FROM platform.datasets AS source,
     platform.business_domains AS domain
WHERE source.id=output.fact_dataset_id
  AND source.tenant_id=output.tenant_id
  AND domain.id=source.domain_id
  AND domain.tenant_id=source.tenant_id
  AND output.domain_key<>domain.name;

COMMENT ON COLUMN platform.dwd_modeling_jobs.domain_key IS
  '当前用户所属 business_domains.name；不是标签，不带“领域:”前缀';
