-- 新建画布尚无 dataset/version 外键身份；使用独立审计表记录组件候选预览。
CREATE TABLE platform.query_candidate_runs(
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  candidate_code text NOT NULL CHECK(candidate_code ~ '^[A-Za-z][A-Za-z0-9_]{0,127}$'),
  actor_user_id uuid,
  data_source_id uuid NOT NULL,
  run_type text NOT NULL CHECK(run_type='COMPONENT_PREVIEW'),
  plan_hash text NOT NULL CHECK(length(plan_hash)=64),
  parameter_hash text NOT NULL CHECK(length(parameter_hash)=64),
  status text NOT NULL CHECK(status IN ('RUNNING','SUCCEEDED','FAILED','TIMEOUT','CANCELLED')),
  row_count integer NOT NULL DEFAULT 0 CHECK(row_count>=0),
  duration_ms bigint NOT NULL DEFAULT 0 CHECK(duration_ms>=0),
  error_code text NOT NULL DEFAULT '',
  warnings_json jsonb NOT NULL DEFAULT '[]'::jsonb CHECK(jsonb_typeof(warnings_json)='array'),
  created_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz,
  FOREIGN KEY(actor_user_id,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE SET NULL (actor_user_id),
  FOREIGN KEY(data_source_id,tenant_id) REFERENCES platform.data_sources(id,tenant_id),
  UNIQUE(id,tenant_id)
);

CREATE TABLE platform.query_candidate_run_sources(
  query_run_id uuid NOT NULL,
  tenant_id uuid NOT NULL,
  node_id text NOT NULL CHECK(node_id ~ '^[A-Za-z][A-Za-z0-9_]{0,127}$'),
  data_source_id uuid NOT NULL,
  subquery_id uuid NOT NULL,
  source_version bigint NOT NULL CHECK(source_version>0),
  source_watermark text NOT NULL DEFAULT '',
  file_version_id uuid,
  status text NOT NULL DEFAULT 'RUNNING' CHECK(status IN ('RUNNING','SUCCEEDED','FAILED','TIMEOUT','CANCELLED')),
  row_count integer NOT NULL DEFAULT 0 CHECK(row_count>=0),
  duration_ms bigint NOT NULL DEFAULT 0 CHECK(duration_ms>=0),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(query_run_id,tenant_id,node_id),
  FOREIGN KEY(query_run_id,tenant_id) REFERENCES platform.query_candidate_runs(id,tenant_id) ON DELETE CASCADE,
  FOREIGN KEY(data_source_id,tenant_id) REFERENCES platform.data_sources(id,tenant_id),
  FOREIGN KEY(file_version_id,tenant_id) REFERENCES platform.file_asset_versions(id,tenant_id)
);

CREATE INDEX query_candidate_runs_tenant_time_idx ON platform.query_candidate_runs(tenant_id,created_at DESC);
CREATE UNIQUE INDEX query_candidate_run_sources_subquery_idx ON platform.query_candidate_run_sources(tenant_id,subquery_id);

ALTER TABLE platform.query_candidate_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.query_candidate_runs FORCE ROW LEVEL SECURITY;
CREATE POLICY query_candidate_runs_tenant_isolation ON platform.query_candidate_runs
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

ALTER TABLE platform.query_candidate_run_sources ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.query_candidate_run_sources FORCE ROW LEVEL SECURITY;
CREATE POLICY query_candidate_run_sources_tenant_isolation ON platform.query_candidate_run_sources
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

COMMENT ON TABLE platform.query_candidate_runs IS '未保存数据集组件预览的安全查询审计，不保存 SQL、参数明文或结果样本';
