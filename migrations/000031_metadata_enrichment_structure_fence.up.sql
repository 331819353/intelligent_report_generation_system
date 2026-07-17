-- 将“已完善”状态绑定到精确技术结构，避免旧结构成功记录导致增量刷新误跳过。
ALTER TABLE platform.metadata_tables
  ADD COLUMN last_enriched_structure_hash text NOT NULL DEFAULT ''
  CHECK(last_enriched_structure_hash='' OR length(last_enriched_structure_hash)=64);

ALTER TABLE platform.ai_metadata_jobs
  ADD COLUMN metadata_structure_hash text NOT NULL DEFAULT ''
  CHECK(metadata_structure_hash='' OR length(metadata_structure_hash)=64);

CREATE INDEX ai_metadata_jobs_structure_success_idx
  ON platform.ai_metadata_jobs(tenant_id,table_id,metadata_structure_hash,completed_at DESC)
  WHERE status='SUCCEEDED' AND metadata_structure_hash<>'';

COMMENT ON COLUMN platform.metadata_tables.last_enriched_structure_hash IS 'Exact technical structure hash most recently completed by metadata AI';
COMMENT ON COLUMN platform.ai_metadata_jobs.metadata_structure_hash IS 'Technical structure hash fenced by this AI completion; legacy jobs remain blank';
