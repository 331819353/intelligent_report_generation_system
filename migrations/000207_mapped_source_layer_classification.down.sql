DO $migration$
DECLARE
  definition text;
  occurrence_count integer;
BEGIN
  SELECT pg_get_functiondef(
    'platform.dataset_publication_origin_facts_match(platform.dataset_versions)'::regprocedure
  ) INTO definition;
  SELECT count(*) INTO occurrence_count
  FROM regexp_matches(
    definition,
    'platform\.system_mapped_source_layer_allowed\(candidate\)',
    'g'
  );
  IF occurrence_count<>3 THEN
    RAISE EXCEPTION 'expected three mapped source publication guards, found %', occurrence_count;
  END IF;
  definition := regexp_replace(
    definition,
    'platform\.system_mapped_source_layer_allowed\(candidate\)',
    'candidate.layer=''ODS''',
    'g'
  );
  EXECUTE definition;
END
$migration$;

DROP FUNCTION IF EXISTS
  platform.system_mapped_source_layer_allowed(platform.dataset_versions);

COMMENT ON FUNCTION
  platform.dataset_publication_origin_facts_match(platform.dataset_versions)
IS '以关系事实验证数据集发布来源；允许零人工操作的系统 ODS 草稿在重新映射后首次发布';

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

COMMENT ON FUNCTION platform.enforce_dataset_domain_lineage() IS
  '发布时按 datasets.domain_id 校验全部精确上游领域，不读取 DSL 或历史标签';

CREATE OR REPLACE FUNCTION platform.deactivate_deleted_ods_metadata()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
SET row_security=off
AS $$
BEGIN
  IF OLD.layer<>'ODS' OR OLD.origin_table_id IS NULL THEN
    IF TG_OP='DELETE' THEN
      RETURN OLD;
    END IF;
    RETURN NEW;
  END IF;

  IF TG_OP='UPDATE' THEN
    IF NOT (OLD.deleted_at IS NULL AND NEW.deleted_at IS NOT NULL) THEN
      RETURN NEW;
    END IF;
  END IF;

  UPDATE platform.metadata_columns
  SET asset_status='INACTIVE'
  WHERE tenant_id=OLD.tenant_id
    AND table_id=OLD.origin_table_id
    AND asset_status='ACTIVE';

  UPDATE platform.metadata_tables
  SET asset_status='INACTIVE',
      management_status='DISABLED'
  WHERE tenant_id=OLD.tenant_id
    AND id=OLD.origin_table_id
    AND (asset_status='ACTIVE' OR management_status<>'DISABLED');

  IF TG_OP='DELETE' THEN
    RETURN OLD;
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.deactivate_deleted_ods_metadata() FROM PUBLIC;

COMMENT ON FUNCTION platform.deactivate_deleted_ods_metadata() IS
  '数据库级保证 ODS 删除同步停用来源表及字段控制面元数据，不影响外部源表';
