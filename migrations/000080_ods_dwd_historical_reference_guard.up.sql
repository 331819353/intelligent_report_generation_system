-- 只要下游 DWD 数据集仍存在，就保留其全部版本的历史血缘并阻止删除 ODS。
-- 若确需删除 ODS，必须先删除下游 DWD 数据集，而不是仅废弃某个 DWD 版本。
CREATE OR REPLACE FUNCTION platform.prevent_referenced_ods_soft_delete()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
BEGIN
  IF OLD.layer='ODS'
     AND OLD.deleted_at IS NULL
     AND NEW.deleted_at IS NOT NULL
     AND EXISTS(
       SELECT 1
       FROM platform.dataset_versions AS source_version
       JOIN platform.dataset_dependencies AS dependency
         ON dependency.tenant_id=source_version.tenant_id
        AND dependency.source_type='DATASET_VERSION'
        AND dependency.source_id=source_version.id::text
       JOIN platform.dataset_versions AS downstream_version
         ON downstream_version.id=dependency.dataset_version_id
        AND downstream_version.tenant_id=dependency.tenant_id
        AND downstream_version.layer='DWD'
       JOIN platform.datasets AS downstream_dataset
         ON downstream_dataset.id=downstream_version.dataset_id
        AND downstream_dataset.tenant_id=downstream_version.tenant_id
        AND downstream_dataset.deleted_at IS NULL
       WHERE source_version.tenant_id=OLD.tenant_id
         AND source_version.dataset_id=OLD.id
     ) THEN
    RAISE EXCEPTION 'ODS 数据集仍被 DWD 数据集引用'
      USING ERRCODE='23503',
        CONSTRAINT='datasets_ods_dwd_reference_guard';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.prevent_referenced_ods_soft_delete() FROM PUBLIC;
