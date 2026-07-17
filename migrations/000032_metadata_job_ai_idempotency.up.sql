-- 将 AI 完善成功与具体后台表任务绑定，worker 租约恢复时可精确收口而不重复调用模型。
ALTER TABLE platform.ai_metadata_jobs
  ADD COLUMN data_source_metadata_job_item_id uuid;

ALTER TABLE platform.data_source_metadata_job_items
  ADD CONSTRAINT data_source_metadata_job_items_id_tenant_key UNIQUE(id,tenant_id);

ALTER TABLE platform.ai_metadata_jobs
  ADD CONSTRAINT ai_metadata_jobs_processing_item_fk
  FOREIGN KEY(data_source_metadata_job_item_id,tenant_id)
  REFERENCES platform.data_source_metadata_job_items(id,tenant_id)
  ON DELETE SET NULL (data_source_metadata_job_item_id);

CREATE UNIQUE INDEX ai_metadata_jobs_item_structure_success_idx
  ON platform.ai_metadata_jobs(tenant_id,data_source_metadata_job_item_id,metadata_structure_hash)
  WHERE status='SUCCEEDED' AND data_source_metadata_job_item_id IS NOT NULL AND metadata_structure_hash<>'';

COMMENT ON COLUMN platform.ai_metadata_jobs.data_source_metadata_job_item_id IS 'Optional durable table-task identity used to close worker retries after AI success';
