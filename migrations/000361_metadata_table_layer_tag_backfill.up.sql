-- 元数据清洗从 metadata-completion-v16 起为每张表判定数仓层级并写入恰好一个
-- “层级:” 受控标签（ODS/DIM/DWD/DWS/ADS），人工可在资产页修改；该标签决定该表
-- 默认映射数据集进入数仓的层级。这里为已经完成清洗、且已生成映射数据集的历史
-- 表补上与其现有映射数据集一致的层级标签，让人工立即可见、可改；尚无映射数据集
-- 的历史表留待下一次清洗补齐（缺少层级标签会让 tags 重新列为待补全属性）。
BEGIN;

UPDATE platform.metadata_tables AS metadata_table
SET tags=array_append(COALESCE(metadata_table.tags,'{}'::text[]),'层级:'||dataset.layer),
    business_version=metadata_table.business_version+1
FROM platform.datasets AS dataset
WHERE dataset.tenant_id=metadata_table.tenant_id
  AND dataset.origin_table_id=metadata_table.id
  AND dataset.deleted_at IS NULL
  AND dataset.layer IN ('ODS','DIM','DWD','DWS','ADS')
  AND metadata_table.asset_status='ACTIVE'
  AND NOT EXISTS(
    SELECT 1 FROM unnest(COALESCE(metadata_table.tags,'{}'::text[])) AS tag
    WHERE tag LIKE '层级:%'
  );

COMMIT;
