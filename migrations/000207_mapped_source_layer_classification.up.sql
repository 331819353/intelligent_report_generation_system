-- Physical source tables are mapped by the platform into one of two virtual
-- source layers: row-level/detail tables remain ODS, while tables that already
-- carry a trusted aggregate grain are exposed as PRE_AGGREGATED DWS. Keep the
-- system-publication proof narrow: arbitrary modeled DWS drafts are not mapped
-- sources and still require their normal dependency contract.
CREATE OR REPLACE FUNCTION platform.system_mapped_source_layer_allowed(
  candidate platform.dataset_versions
)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
SET search_path=pg_catalog,platform
AS $$
  SELECT candidate.layer='ODS'
    OR (
      candidate.layer='DWS'
      AND candidate.dsl_json#>>'{dataset,sourceMode}'='PRE_AGGREGATED'
    )
$$;

REVOKE ALL ON FUNCTION
  platform.system_mapped_source_layer_allowed(platform.dataset_versions)
FROM PUBLIC;

DO $migration$
DECLARE
  definition text;
  occurrence_count integer;
BEGIN
  SELECT pg_get_functiondef(
    'platform.dataset_publication_origin_facts_match(platform.dataset_versions)'::regprocedure
  ) INTO definition;
  SELECT count(*) INTO occurrence_count
  FROM regexp_matches(definition, 'candidate\.layer\s*=\s*''ODS''', 'g');
  IF occurrence_count<>3 THEN
    RAISE EXCEPTION 'expected three mapped ODS publication guards, found %', occurrence_count;
  END IF;
  definition := regexp_replace(
    definition,
    'candidate\.layer\s*=\s*''ODS''',
    'platform.system_mapped_source_layer_allowed(candidate)',
    'g'
  );
  EXECUTE definition;
END
$migration$;

COMMENT ON FUNCTION
  platform.dataset_publication_origin_facts_match(platform.dataset_versions)
IS '以关系事实验证数据集发布来源；允许系统映射的 ODS 与源端已汇总 DWS 自动发布';

-- The domain-lineage guard normally requires DATASET nodes for every modeled
-- layer. A pre-aggregated source mapping has a TABLE node and owns its domain
-- directly, just like ODS, so it must exit before dependency counting.
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
  IF NEW.layer='ODS'
     OR (
       NEW.layer='DWS'
       AND NEW.dsl_json#>>'{dataset,sourceMode}'='PRE_AGGREGATED'
     ) THEN
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
  '发布时按 datasets.domain_id 校验精确上游领域；源端已汇总 DWS 按物理来源直接继承资产领域';

-- Deleting any mapped source dataset must deactivate its source metadata. The
-- invariant follows origin_table_id rather than a specific classified layer.
CREATE OR REPLACE FUNCTION platform.deactivate_deleted_ods_metadata()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
SET row_security=off
AS $$
BEGIN
  IF OLD.origin_table_id IS NULL THEN
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

UPDATE platform.metadata_columns AS metadata_column
SET asset_status='INACTIVE'
WHERE metadata_column.asset_status='ACTIVE'
  AND EXISTS(
    SELECT 1
    FROM platform.datasets AS dataset
    WHERE dataset.tenant_id=metadata_column.tenant_id
      AND dataset.origin_table_id=metadata_column.table_id
      AND dataset.deleted_at IS NOT NULL
  );

UPDATE platform.metadata_tables AS metadata_table
SET asset_status='INACTIVE',
    management_status='DISABLED'
WHERE (metadata_table.asset_status='ACTIVE'
       OR metadata_table.management_status<>'DISABLED')
  AND EXISTS(
    SELECT 1
    FROM platform.datasets AS dataset
    WHERE dataset.tenant_id=metadata_table.tenant_id
      AND dataset.origin_table_id=metadata_table.id
      AND dataset.deleted_at IS NOT NULL
  );

COMMENT ON FUNCTION platform.deactivate_deleted_ods_metadata() IS
  '数据库级保证映射源数据集删除同步停用来源表及字段控制面元数据，不影响外部源表';
