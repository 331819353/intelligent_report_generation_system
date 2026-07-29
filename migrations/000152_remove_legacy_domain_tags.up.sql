-- 领域身份只存在于 business_domains/domain_memberships 和资产 domain_id。
-- 删除所有历史领域标签及其派生痕迹，避免任何旧标签参与后续提示、分组或审批。
UPDATE platform.metadata_tables
SET tags=COALESCE((
  SELECT array_agg(raw_tag ORDER BY ordinal)
  FROM unnest(tags) WITH ORDINALITY AS item(raw_tag,ordinal)
  WHERE replace(btrim(raw_tag),'：',':') NOT LIKE '领域:%'
),'{}'::text[])
WHERE EXISTS(
  SELECT 1 FROM unnest(tags) AS raw_tag
  WHERE replace(btrim(raw_tag),'：',':') LIKE '领域:%'
);

UPDATE platform.metadata_columns
SET tags=COALESCE((
  SELECT array_agg(raw_tag ORDER BY ordinal)
  FROM unnest(tags) WITH ORDINALITY AS item(raw_tag,ordinal)
  WHERE replace(btrim(raw_tag),'：',':') NOT LIKE '领域:%'
),'{}'::text[])
WHERE EXISTS(
  SELECT 1 FROM unnest(tags) AS raw_tag
  WHERE replace(btrim(raw_tag),'：',':') LIKE '领域:%'
);

UPDATE platform.metric_semantic_documents
SET tags=COALESCE((
  SELECT array_agg(raw_tag ORDER BY ordinal)
  FROM unnest(tags) WITH ORDINALITY AS item(raw_tag,ordinal)
  WHERE replace(btrim(raw_tag),'：',':') NOT LIKE '领域:%'
),'{}'::text[])
WHERE EXISTS(
  SELECT 1 FROM unnest(tags) AS raw_tag
  WHERE replace(btrim(raw_tag),'：',':') LIKE '领域:%'
);

DELETE FROM platform.dataset_tag_suggestion_items AS item
USING platform.semantic_tags AS tag
WHERE tag.id=item.tag_id
  AND tag.tenant_id=item.tenant_id
  AND (
    tag.category='BUSINESS_DOMAIN'
    OR replace(btrim(tag.name),'：',':') LIKE '领域:%'
  );

DELETE FROM platform.asset_tag_bindings AS binding
USING platform.semantic_tags AS tag
WHERE tag.id=binding.tag_id
  AND tag.tenant_id=binding.tenant_id
  AND (
    tag.category='BUSINESS_DOMAIN'
    OR replace(btrim(tag.name),'：',':') LIKE '领域:%'
  );

UPDATE platform.semantic_tags AS child
SET parent_tag_id=NULL,version=child.version+1,updated_at=now()
FROM platform.semantic_tags AS parent
WHERE parent.id=child.parent_tag_id
  AND parent.tenant_id=child.tenant_id
  AND (
    parent.category='BUSINESS_DOMAIN'
    OR replace(btrim(parent.name),'：',':') LIKE '领域:%'
  );

DELETE FROM platform.semantic_tags
WHERE category='BUSINESS_DOMAIN'
   OR replace(btrim(name),'：',':') LIKE '领域:%';

ALTER TABLE platform.semantic_tags
  DROP CONSTRAINT semantic_tags_category_check,
  ADD CONSTRAINT semantic_tags_category_check CHECK(category IN (
    'BUSINESS_ENTITY','TABLE_FUNCTION','USAGE_SCOPE',
    'DATA_GRAIN','JOIN_ROLE','SENSITIVITY','FREEFORM'
  ));

ALTER TABLE platform.dataset_tag_suggestion_items
  DROP CONSTRAINT dataset_tag_suggestion_items_category_check,
  ADD CONSTRAINT dataset_tag_suggestion_items_category_check CHECK(category IN (
    'BUSINESS_ENTITY','TABLE_FUNCTION','USAGE_SCOPE','DATA_GRAIN','JOIN_ROLE'
  ));

-- 清除按旧领域标签形成的建模快照；输出映射保留以便新流程原地重建既有
-- DIM/DWD 草稿，但其领域键改为资产真实归属，旧输入摘要不再可复用。
DELETE FROM platform.dwd_modeling_checkpoints AS checkpoint
USING platform.dwd_modeling_jobs AS job,
      platform.datasets AS dataset,
      platform.business_domains AS domain
WHERE job.id=checkpoint.job_id
  AND job.tenant_id=checkpoint.tenant_id
  AND dataset.id=job.trigger_dataset_id
  AND dataset.tenant_id=job.tenant_id
  AND domain.id=dataset.domain_id
  AND domain.tenant_id=dataset.tenant_id
  AND job.domain_key<>''
  AND job.domain_key<>domain.name;

UPDATE platform.dwd_modeling_stage_jobs AS stage
SET result_json='{}'::jsonb,ai_request_id=NULL,updated_at=now()
FROM platform.dwd_modeling_jobs AS job,
     platform.datasets AS dataset,
     platform.business_domains AS domain
WHERE job.id=stage.workflow_job_id
  AND job.tenant_id=stage.tenant_id
  AND dataset.id=job.trigger_dataset_id
  AND dataset.tenant_id=job.tenant_id
  AND domain.id=dataset.domain_id
  AND domain.tenant_id=dataset.tenant_id
  AND job.domain_key<>''
  AND job.domain_key<>domain.name;

UPDATE platform.dwd_modeling_jobs AS job
SET domain_key=domain.name,
    result_json='{}'::jsonb,ai_request_id=NULL,updated_at=now()
FROM platform.datasets AS dataset,
     platform.business_domains AS domain
WHERE dataset.id=job.trigger_dataset_id
  AND dataset.tenant_id=job.tenant_id
  AND domain.id=dataset.domain_id
  AND domain.tenant_id=dataset.tenant_id
  AND job.domain_key<>''
  AND job.domain_key<>domain.name;

UPDATE platform.dim_modeling_outputs AS output
SET domain_key=domain.name,
    last_input_hash=encode(public.digest(
      convert_to('domain-context-reset:'||source.id::text,'UTF8'),'sha256'
    ),'hex'),
    updated_at=now()
FROM platform.datasets AS source,
     platform.business_domains AS domain
WHERE source.id=output.source_dataset_id
  AND source.tenant_id=output.tenant_id
  AND domain.id=source.domain_id
  AND domain.tenant_id=source.tenant_id
  AND output.domain_key<>domain.name;

UPDATE platform.dwd_modeling_outputs AS output
SET domain_key=domain.name,
    last_input_hash=encode(public.digest(
      convert_to('domain-context-reset:'||source.id::text,'UTF8'),'sha256'
    ),'hex'),
    updated_at=now()
FROM platform.datasets AS source,
     platform.business_domains AS domain
WHERE source.id=output.fact_dataset_id
  AND source.tenant_id=output.tenant_id
  AND domain.id=source.domain_id
  AND domain.tenant_id=source.tenant_id
  AND output.domain_key<>domain.name;

-- 用户明确要求旧领域标签不作为审计兼容信息保留。迁移角色短暂关闭
-- append-only 防线，只移除这两个已废弃的标签派生键，随后立即恢复。
ALTER TABLE platform.audit_logs DISABLE TRIGGER audit_logs_immutable;

UPDATE platform.audit_logs
SET detail=detail-'domain'-'domainKey'
WHERE jsonb_typeof(detail)='object'
  AND (
    replace(btrim(COALESCE(detail->>'domain','')),'：',':') LIKE '领域:%'
    OR replace(btrim(COALESCE(detail->>'domainKey','')),'：',':') LIKE '领域:%'
  );

ALTER TABLE platform.audit_logs ENABLE TRIGGER audit_logs_immutable;

CREATE OR REPLACE FUNCTION platform.dataset_version_effective_domain(
  target_version_id uuid
)
RETURNS text
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
  SELECT domain.name
  FROM platform.dataset_versions AS version
  JOIN platform.datasets AS dataset
    ON dataset.tenant_id=version.tenant_id
   AND dataset.id=version.dataset_id
  JOIN platform.business_domains AS domain
    ON domain.tenant_id=dataset.tenant_id
   AND domain.id=dataset.domain_id
  WHERE version.tenant_id=platform.current_tenant_id()
    AND version.id=target_version_id
$$;

CREATE OR REPLACE FUNCTION platform.enforce_dataset_domain_lineage()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  target_domain_id uuid;
  dependency_count integer;
  mismatched_count integer;
BEGIN
  IF NEW.status NOT IN ('PUBLISHING','PUBLISHED') THEN
    RETURN NEW;
  END IF;

  SELECT dataset.domain_id
  INTO target_domain_id
  FROM platform.datasets AS dataset
  WHERE dataset.tenant_id=NEW.tenant_id
    AND dataset.id=NEW.dataset_id
    AND dataset.deleted_at IS NULL;
  IF target_domain_id IS NULL THEN
    RAISE EXCEPTION 'published dataset domain is required'
      USING ERRCODE='23514',CONSTRAINT='dataset_versions_domain_required';
  END IF;
  IF NEW.layer='ODS' THEN
    RETURN NEW;
  END IF;

  SELECT count(*)::integer,
    count(*) FILTER(
      WHERE upstream.id IS NULL
         OR upstream_dataset.id IS NULL
         OR upstream_dataset.domain_id<>target_domain_id
    )::integer
  INTO dependency_count,mismatched_count
  FROM jsonb_array_elements(
    COALESCE(NEW.dsl_json->'nodes','[]'::jsonb)
  ) AS node
  LEFT JOIN platform.dataset_versions AS upstream
    ON upstream.tenant_id=NEW.tenant_id
   AND upstream.id=(node->>'datasetVersionId')::uuid
   AND upstream.status='PUBLISHED'
  LEFT JOIN platform.datasets AS upstream_dataset
    ON upstream_dataset.tenant_id=upstream.tenant_id
   AND upstream_dataset.id=upstream.dataset_id
   AND upstream_dataset.deleted_at IS NULL
  WHERE node->>'type'='DATASET';

  IF dependency_count=0 OR mismatched_count>0 THEN
    RAISE EXCEPTION 'downstream dataset domain must equal every published upstream domain'
      USING ERRCODE='23514',CONSTRAINT='dataset_versions_domain_lineage_match';
  END IF;
  RETURN NEW;
END
$$;

COMMENT ON FUNCTION platform.dataset_version_effective_domain(uuid) IS
  '返回数据集版本所属资产 domain_id 对应的业务领域名称，不读取 DSL 或历史标签';
COMMENT ON FUNCTION platform.enforce_dataset_domain_lineage() IS
  '发布时按 datasets.domain_id 校验全部精确上游领域，不读取 DSL 或历史标签';
