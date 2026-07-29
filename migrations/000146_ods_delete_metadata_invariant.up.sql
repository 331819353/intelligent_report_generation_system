-- ODS 删除与来源表元数据状态必须是一个数据库级不变量，不能只依赖某个
-- HTTP/仓储调用方记得同步。软删除和受控物理清理都统一停用表及字段资产；
-- 触发器不会连接源库，也不会执行任何源表 DDL/DML。
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

DROP TRIGGER IF EXISTS datasets_deactivate_deleted_ods_metadata_update
  ON platform.datasets;
CREATE TRIGGER datasets_deactivate_deleted_ods_metadata_update
BEFORE UPDATE OF deleted_at ON platform.datasets
FOR EACH ROW
EXECUTE FUNCTION platform.deactivate_deleted_ods_metadata();

DROP TRIGGER IF EXISTS datasets_deactivate_deleted_ods_metadata_delete
  ON platform.datasets;
CREATE TRIGGER datasets_deactivate_deleted_ods_metadata_delete
BEFORE DELETE ON platform.datasets
FOR EACH ROW
EXECUTE FUNCTION platform.deactivate_deleted_ods_metadata();

-- 幂等修复仍保留软删除 ODS 的历史状态；没有 ODS 身份记录的孤儿表不能
-- 自动推断为“已删除”，避免误伤用户主动停用但仍保留的表资产。
UPDATE platform.metadata_columns AS metadata_column
SET asset_status='INACTIVE'
WHERE metadata_column.asset_status='ACTIVE'
  AND EXISTS(
    SELECT 1
    FROM platform.datasets AS dataset
    WHERE dataset.tenant_id=metadata_column.tenant_id
      AND dataset.origin_table_id=metadata_column.table_id
      AND dataset.layer='ODS'
      AND dataset.deleted_at IS NOT NULL
  );

UPDATE platform.metadata_tables AS metadata_table
SET asset_status='INACTIVE',
    management_status='DISABLED'
WHERE metadata_table.asset_status='ACTIVE'
  AND EXISTS(
    SELECT 1
    FROM platform.datasets AS dataset
    WHERE dataset.tenant_id=metadata_table.tenant_id
      AND dataset.origin_table_id=metadata_table.id
      AND dataset.layer='ODS'
      AND dataset.deleted_at IS NOT NULL
  );

-- 删除整个数据源同样不应留下仍可见的活动元数据。此回填与运行时代码使用
-- 相同的软停用语义，不删除历史表/字段记录。
UPDATE platform.metadata_columns AS metadata_column
SET asset_status='INACTIVE'
WHERE metadata_column.asset_status='ACTIVE'
  AND EXISTS(
    SELECT 1
    FROM platform.metadata_tables AS metadata_table
    JOIN platform.data_sources AS data_source
      ON data_source.id=metadata_table.data_source_id
     AND data_source.tenant_id=metadata_table.tenant_id
    WHERE metadata_table.id=metadata_column.table_id
      AND metadata_table.tenant_id=metadata_column.tenant_id
      AND data_source.deleted_at IS NOT NULL
  );

UPDATE platform.metadata_tables AS metadata_table
SET asset_status='INACTIVE',
    management_status='DISABLED'
WHERE (metadata_table.asset_status='ACTIVE'
       OR metadata_table.management_status<>'DISABLED')
  AND EXISTS(
    SELECT 1
    FROM platform.data_sources AS data_source
    WHERE data_source.id=metadata_table.data_source_id
      AND data_source.tenant_id=metadata_table.tenant_id
      AND data_source.deleted_at IS NOT NULL
  );

COMMENT ON FUNCTION platform.deactivate_deleted_ods_metadata() IS
  '数据库级保证 ODS 删除同步停用来源表及字段控制面元数据，不影响外部源表';
