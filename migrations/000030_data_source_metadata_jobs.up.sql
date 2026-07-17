-- 数据源表采样与 LLM 完善改为持久化后台批任务，支持真实进度、租约恢复和增量刷新。
CREATE TABLE platform.data_source_metadata_jobs(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  data_source_id uuid NOT NULL,
  requested_by uuid,
  kind text NOT NULL CHECK(kind IN ('IMPORT','REFRESH')),
  refresh_mode text NOT NULL CHECK(refresh_mode IN ('FULL','INCREMENTAL')),
  source_config_hash text NOT NULL CHECK(length(source_config_hash)=64),
  status text NOT NULL DEFAULT 'QUEUED' CHECK(status IN ('QUEUED','RUNNING','SUCCEEDED','PARTIAL','FAILED')),
  stage text NOT NULL DEFAULT 'QUEUED' CHECK(stage IN ('QUEUED','DISCOVERY','DIFF','SAMPLE','PERSISTENCE','LLM','COMPLETE','FAILED')),
  total integer NOT NULL CHECK(total>=0),
  error_code text NOT NULL DEFAULT '',
  error_message text NOT NULL DEFAULT '',
  lease_owner text NOT NULL DEFAULT '',
  lease_expires_at timestamptz,
  heartbeat_at timestamptz,
  attempt integer NOT NULL DEFAULT 0 CHECK(attempt>=0),
  created_at timestamptz NOT NULL DEFAULT now(),
  started_at timestamptz,
  completed_at timestamptz,
  FOREIGN KEY(data_source_id,tenant_id) REFERENCES platform.data_sources(id,tenant_id),
  FOREIGN KEY(requested_by,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE SET NULL (requested_by),
  UNIQUE(id,tenant_id)
);

CREATE TABLE platform.data_source_metadata_job_items(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  job_id uuid NOT NULL,
  catalog_name text NOT NULL DEFAULT '',
  schema_name text NOT NULL,
  table_name text NOT NULL,
  table_id uuid,
  previous_structure_hash text NOT NULL DEFAULT '',
  previous_enrichment_status text NOT NULL DEFAULT '',
  status text NOT NULL DEFAULT 'QUEUED' CHECK(status IN ('QUEUED','RUNNING','SUCCEEDED','SKIPPED','FAILED')),
  stage text NOT NULL DEFAULT 'QUEUED' CHECK(stage IN ('QUEUED','DISCOVERY','DIFF','SAMPLE','PERSISTENCE','LLM','COMPLETE','FAILED')),
  error_code text NOT NULL DEFAULT '',
  error_message text NOT NULL DEFAULT '',
  started_at timestamptz,
  completed_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY(job_id,tenant_id) REFERENCES platform.data_source_metadata_jobs(id,tenant_id) ON DELETE CASCADE,
  FOREIGN KEY(table_id,tenant_id) REFERENCES platform.metadata_tables(id,tenant_id) ON DELETE SET NULL (table_id),
  UNIQUE(tenant_id,job_id,catalog_name,schema_name,table_name)
);

CREATE UNIQUE INDEX data_source_metadata_jobs_one_active_idx
  ON platform.data_source_metadata_jobs(tenant_id,data_source_id)
  WHERE status IN ('QUEUED','RUNNING');
CREATE INDEX data_source_metadata_jobs_claim_idx
  ON platform.data_source_metadata_jobs(tenant_id,status,lease_expires_at,created_at);
CREATE INDEX data_source_metadata_jobs_source_time_idx
  ON platform.data_source_metadata_jobs(tenant_id,data_source_id,created_at DESC);
CREATE INDEX data_source_metadata_job_items_progress_idx
  ON platform.data_source_metadata_job_items(tenant_id,job_id,status);

ALTER TABLE platform.data_source_metadata_jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.data_source_metadata_jobs FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.data_source_metadata_job_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.data_source_metadata_job_items FORCE ROW LEVEL SECURITY;
CREATE POLICY data_source_metadata_jobs_tenant_isolation ON platform.data_source_metadata_jobs
  USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY data_source_metadata_job_items_tenant_isolation ON platform.data_source_metadata_job_items
  USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());

COMMENT ON TABLE platform.data_source_metadata_jobs IS 'Durable batches for source discovery, sampling and AI metadata completion';
COMMENT ON TABLE platform.data_source_metadata_job_items IS 'Per-table progress; sample rows and credentials are never persisted';
