-- 修复历史软删除的映射 ODS：只停用控制面元数据记录，使源表重新出现在
-- “新增数据表”候选中。这里不会连接源库，也不会删除任何物理源表。
UPDATE platform.metadata_columns AS metadata_column
SET asset_status='INACTIVE'
WHERE metadata_column.asset_status='ACTIVE'
  AND EXISTS (
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
  AND EXISTS (
    SELECT 1
    FROM platform.datasets AS dataset
    WHERE dataset.tenant_id=metadata_table.tenant_id
      AND dataset.origin_table_id=metadata_table.id
      AND dataset.layer='ODS'
      AND dataset.deleted_at IS NOT NULL
  );
