-- 跨源查询按节点记录来源版本、水位和子查询标识，便于追溯与取消。
CREATE TABLE platform.query_run_sources(
  query_run_id uuid NOT NULL,
  tenant_id uuid NOT NULL,
  node_id text NOT NULL CHECK(node_id ~ '^[A-Za-z][A-Za-z0-9_]{0,127}$'),
  data_source_id uuid NOT NULL,
  subquery_id uuid NOT NULL,
  source_version bigint NOT NULL CHECK(source_version>0),
  source_watermark text NOT NULL DEFAULT '',
  file_version_id uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(query_run_id,tenant_id,node_id),
  FOREIGN KEY(query_run_id,tenant_id) REFERENCES platform.query_runs(id,tenant_id) ON DELETE CASCADE,
  FOREIGN KEY(data_source_id,tenant_id) REFERENCES platform.data_sources(id,tenant_id),
  FOREIGN KEY(file_version_id,tenant_id) REFERENCES platform.file_asset_versions(id,tenant_id)
);

CREATE INDEX query_run_sources_source_idx ON platform.query_run_sources(tenant_id,data_source_id,created_at DESC);
CREATE UNIQUE INDEX query_run_sources_subquery_idx ON platform.query_run_sources(tenant_id,subquery_id);

ALTER TABLE platform.query_run_sources ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.query_run_sources FORCE ROW LEVEL SECURITY;
CREATE POLICY query_run_sources_tenant_isolation ON platform.query_run_sources
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

COMMENT ON TABLE platform.query_run_sources IS '跨源查询节点的固定来源版本、水位和取消标识';
