-- 资产列表需要在刷新期间逐表读取“运行中批任务”的实时进度。原有索引以
-- (tenant_id,job_id,status) 组织，按 table_id 反查会退化为全表扫描，因此为
-- 已关联表资产的任务项补一个 table_id 维度的索引。
BEGIN;

CREATE INDEX IF NOT EXISTS data_source_metadata_job_items_table_idx
  ON platform.data_source_metadata_job_items(tenant_id,table_id)
  WHERE table_id IS NOT NULL;

COMMIT;
