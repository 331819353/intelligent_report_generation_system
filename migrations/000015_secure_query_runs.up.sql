-- 查询审计只保存结构摘要和运行指标，不保存 SQL 参数明文或结果样本。
CREATE TABLE platform.query_runs(
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  dataset_id uuid NOT NULL,
  dataset_version_id uuid NOT NULL,
  actor_user_id uuid,
  data_source_id uuid NOT NULL,
  run_type text NOT NULL DEFAULT 'PREVIEW' CHECK(run_type IN ('PREVIEW','ONLINE','EXPORT','SCHEDULED')),
  sql_hash text NOT NULL CHECK(length(sql_hash)=64),
  parameter_hash text NOT NULL CHECK(length(parameter_hash)=64),
  status text NOT NULL CHECK(status IN ('RUNNING','SUCCEEDED','FAILED','TIMEOUT','CANCELLED')),
  row_count integer NOT NULL DEFAULT 0 CHECK(row_count>=0),
  duration_ms bigint NOT NULL DEFAULT 0 CHECK(duration_ms>=0),
  error_code text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz,
  FOREIGN KEY(dataset_id,tenant_id) REFERENCES platform.datasets(id,tenant_id),
  FOREIGN KEY(dataset_version_id,tenant_id) REFERENCES platform.dataset_versions(id,tenant_id),
  FOREIGN KEY(actor_user_id,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE SET NULL (actor_user_id),
  FOREIGN KEY(data_source_id,tenant_id) REFERENCES platform.data_sources(id,tenant_id),
  UNIQUE(id,tenant_id)
);
CREATE INDEX query_runs_dataset_time_idx ON platform.query_runs(tenant_id,dataset_id,created_at DESC);
CREATE INDEX query_runs_status_time_idx ON platform.query_runs(tenant_id,status,created_at DESC);
ALTER TABLE platform.query_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.query_runs FORCE ROW LEVEL SECURITY;
CREATE POLICY query_runs_tenant_isolation ON platform.query_runs USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
COMMENT ON TABLE platform.query_runs IS '不包含参数明文和结果样本的租户查询审计';
