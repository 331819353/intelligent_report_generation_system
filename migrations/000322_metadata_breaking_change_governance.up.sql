-- 将物理结构差异升级为可执行治理事件。破坏性变更会在同一事务内使当前
-- 发布数据集及其发布下游失效，避免查询继续消费已不满足固定依赖摘要的版本。
BEGIN;

CREATE OR REPLACE FUNCTION platform.metadata_diff_is_breaking(
  selected_object_type text,
  selected_change_type platform.metadata_change_type,
  selected_before jsonb,
  selected_after jsonb
)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
AS $function$
  SELECT CASE
    WHEN selected_change_type='REMOVED' THEN true
    WHEN selected_change_type IN ('ADDED','REACTIVATED') THEN false
    WHEN selected_change_type<>'CHANGED' THEN false
    WHEN selected_object_type='TABLE' THEN
      COALESCE(selected_before->>'table_type',selected_before->>'type','')
        IS DISTINCT FROM COALESCE(selected_after->>'table_type',selected_after->>'type','')
      OR COALESCE(selected_before->'primary_key_columns',selected_before->'primaryKeyColumns','[]'::jsonb)
        IS DISTINCT FROM COALESCE(selected_after->'primary_key_columns',selected_after->'primaryKeyColumns','[]'::jsonb)
      OR COALESCE(selected_before->'constraints_json',selected_before->'constraints','[]'::jsonb)
        IS DISTINCT FROM COALESCE(selected_after->'constraints_json',selected_after->'constraints','[]'::jsonb)
    WHEN selected_object_type='COLUMN' THEN
      COALESCE(selected_before->>'canonical_type',selected_before->>'canonicalType','')
        IS DISTINCT FROM COALESCE(selected_after->>'canonical_type',selected_after->>'canonicalType','')
      OR COALESCE(selected_before->>'native_type',selected_before->>'nativeType','')
        IS DISTINCT FROM COALESCE(selected_after->>'native_type',selected_after->>'nativeType','')
      OR (
        COALESCE((selected_before->>'nullable')::boolean,true)
        AND NOT COALESCE((selected_after->>'nullable')::boolean,true)
      )
      OR (
        NULLIF(COALESCE(selected_before->>'length',''),'') IS NOT NULL
        AND NULLIF(COALESCE(selected_after->>'length',''),'') IS NOT NULL
        AND (selected_after->>'length')::bigint < (selected_before->>'length')::bigint
      )
      OR (
        NULLIF(COALESCE(selected_before->>'numeric_precision',selected_before->>'precision',''),'') IS NOT NULL
        AND NULLIF(COALESCE(selected_after->>'numeric_precision',selected_after->>'precision',''),'') IS NOT NULL
        AND COALESCE(selected_after->>'numeric_precision',selected_after->>'precision')::integer
          < COALESCE(selected_before->>'numeric_precision',selected_before->>'precision')::integer
      )
      OR (
        NULLIF(COALESCE(selected_before->>'numeric_scale',selected_before->>'scale',''),'') IS NOT NULL
        AND NULLIF(COALESCE(selected_after->>'numeric_scale',selected_after->>'scale',''),'') IS NOT NULL
        AND COALESCE(selected_after->>'numeric_scale',selected_after->>'scale')::integer
          < COALESCE(selected_before->>'numeric_scale',selected_before->>'scale')::integer
      )
      OR COALESCE((COALESCE(selected_before->>'is_primary_key',selected_before->>'primaryKey','false'))::boolean,false)
        IS DISTINCT FROM COALESCE((COALESCE(selected_after->>'is_primary_key',selected_after->>'primaryKey','false'))::boolean,false)
      OR COALESCE((COALESCE(selected_before->>'is_foreign_key',selected_before->>'foreignKey','false'))::boolean,false)
        IS DISTINCT FROM COALESCE((COALESCE(selected_after->>'is_foreign_key',selected_after->>'foreignKey','false'))::boolean,false)
      OR COALESCE((COALESCE(selected_before->>'is_unique',selected_before->>'unique','false'))::boolean,false)
        IS DISTINCT FROM COALESCE((COALESCE(selected_after->>'is_unique',selected_after->>'unique','false'))::boolean,false)
    ELSE false
  END
$function$;

REVOKE ALL ON FUNCTION platform.metadata_diff_is_breaking(text,platform.metadata_change_type,jsonb,jsonb)
  FROM PUBLIC;
GRANT EXECUTE ON FUNCTION platform.metadata_diff_is_breaking(text,platform.metadata_change_type,jsonb,jsonb)
  TO report_app;

CREATE OR REPLACE FUNCTION platform.propagate_breaking_metadata_diff()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
SET row_security=off
AS $function$
DECLARE
  impacted_version_ids uuid[] := '{}'::uuid[];
  impacted_dataset_ids uuid[] := '{}'::uuid[];
BEGIN
  IF NOT platform.metadata_diff_is_breaking(
    NEW.object_type,NEW.change_type,NEW.before_json,NEW.after_json
  ) THEN
    RETURN NEW;
  END IF;

  WITH RECURSIVE upstream_tables AS (
    SELECT table_asset.id
    FROM platform.metadata_tables AS table_asset
    WHERE table_asset.tenant_id=NEW.tenant_id
      AND table_asset.data_source_id=NEW.data_source_id
      AND (
        (NEW.object_type='TABLE' AND (
          table_asset.id::text=COALESCE(NEW.before_json->>'id','')
          OR table_asset.catalog_name||'.'||table_asset.schema_name||'.'||table_asset.table_name=NEW.object_key
          OR table_asset.schema_name||'.'||table_asset.table_name=NEW.object_key
        ))
        OR (NEW.object_type='COLUMN' AND (
          table_asset.id::text=COALESCE(NEW.before_json->>'table_id','')
          OR EXISTS(
            SELECT 1 FROM platform.metadata_columns AS column_asset
            WHERE column_asset.tenant_id=NEW.tenant_id
              AND column_asset.table_id=table_asset.id
              AND (
                column_asset.id::text=COALESCE(NEW.before_json->>'id','')
                OR table_asset.catalog_name||'.'||table_asset.schema_name||'.'||table_asset.table_name||'.'||column_asset.column_name=NEW.object_key
                OR table_asset.schema_name||'.'||table_asset.table_name||'.'||column_asset.column_name=NEW.object_key
              )
          )
        ))
      )
  ), directly_impacted AS (
    SELECT DISTINCT version.id,version.dataset_id
    FROM upstream_tables AS upstream
    JOIN platform.dataset_dependencies AS dependency
      ON dependency.tenant_id=NEW.tenant_id
     AND dependency.source_type='TABLE'
     AND dependency.source_id=upstream.id::text
    JOIN platform.dataset_versions AS version
      ON version.tenant_id=dependency.tenant_id
     AND version.id=dependency.dataset_version_id
     AND version.status='PUBLISHED'
    JOIN platform.datasets AS dataset
      ON dataset.tenant_id=version.tenant_id
     AND dataset.id=version.dataset_id
     AND dataset.current_published_version_id=version.id
     AND dataset.status='PUBLISHED'
     AND dataset.deleted_at IS NULL
  ), impacted AS (
    SELECT id,dataset_id FROM directly_impacted
    UNION
    SELECT downstream_version.id,downstream_version.dataset_id
    FROM impacted AS upstream_version
    JOIN platform.dataset_dependencies AS dependency
      ON dependency.tenant_id=NEW.tenant_id
     AND dependency.source_type='DATASET_VERSION'
     AND dependency.source_id=upstream_version.id::text
    JOIN platform.dataset_versions AS downstream_version
      ON downstream_version.tenant_id=dependency.tenant_id
     AND downstream_version.id=dependency.dataset_version_id
     AND downstream_version.status='PUBLISHED'
    JOIN platform.datasets AS downstream_dataset
      ON downstream_dataset.tenant_id=downstream_version.tenant_id
     AND downstream_dataset.id=downstream_version.dataset_id
     AND downstream_dataset.current_published_version_id=downstream_version.id
     AND downstream_dataset.status='PUBLISHED'
     AND downstream_dataset.deleted_at IS NULL
  )
  SELECT COALESCE(array_agg(id),'{}'::uuid[]),
         COALESCE(array_agg(DISTINCT dataset_id),'{}'::uuid[])
  INTO impacted_version_ids,impacted_dataset_ids
  FROM impacted;

  IF cardinality(impacted_version_ids)=0 THEN
    RETURN NEW;
  END IF;

  UPDATE platform.dataset_versions
  SET status='STALE'
  WHERE tenant_id=NEW.tenant_id
    AND id=ANY(impacted_version_ids)
    AND status='PUBLISHED';

  UPDATE platform.datasets
  SET status='STALE',current_published_version_id=NULL,
      version=version+1,updated_at=clock_timestamp()
  WHERE tenant_id=NEW.tenant_id
    AND id=ANY(impacted_dataset_ids)
    AND status='PUBLISHED';

  INSERT INTO platform.audit_logs(
    tenant_id,action,resource_type,resource_id,detail
  )
  SELECT NEW.tenant_id,'MARK_DATASET_STALE','DATASET',dataset_id::text,
    jsonb_build_object(
      'reason','BREAKING_METADATA_DIFF','metadataDiffId',NEW.id::text,
      'dataSourceId',NEW.data_source_id::text,'objectType',NEW.object_type,
      'objectKey',NEW.object_key,'changeType',NEW.change_type::text
    )
  FROM unnest(impacted_dataset_ids) AS dataset_id;

  RETURN NEW;
END
$function$;

REVOKE ALL ON FUNCTION platform.propagate_breaking_metadata_diff() FROM PUBLIC;

CREATE TRIGGER metadata_diffs_propagate_breaking_change
AFTER INSERT ON platform.metadata_diffs
FOR EACH ROW EXECUTE FUNCTION platform.propagate_breaking_metadata_diff();

COMMENT ON FUNCTION platform.metadata_diff_is_breaking(text,platform.metadata_change_type,jsonb,jsonb) IS
  'Deterministic schema compatibility classification used by metadata impact governance';
COMMENT ON FUNCTION platform.propagate_breaking_metadata_diff() IS
  'Atomically marks directly and transitively dependent current published dataset versions STALE';

COMMIT;
