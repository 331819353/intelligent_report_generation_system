-- Governed PostgreSQL preview/validation path for DATASET nodes and DWS
-- metrics. Source execution keeps its data_source_id identity; a pure
-- warehouse execution instead records exact ACTIVE materialization snapshots.
ALTER TABLE platform.query_runs
  ADD COLUMN execution_engine text NOT NULL DEFAULT 'SOURCE'
    CHECK(execution_engine IN ('SOURCE','POSTGRES')),
  ALTER COLUMN data_source_id DROP NOT NULL,
  ADD CONSTRAINT query_runs_execution_identity_check CHECK(
    (execution_engine='SOURCE' AND data_source_id IS NOT NULL)
    OR
    (execution_engine='POSTGRES' AND data_source_id IS NULL)
  );

ALTER TABLE platform.query_candidate_runs
  ADD COLUMN execution_engine text NOT NULL DEFAULT 'SOURCE'
    CHECK(execution_engine IN ('SOURCE','POSTGRES')),
  ALTER COLUMN data_source_id DROP NOT NULL,
  ADD CONSTRAINT query_candidate_runs_execution_identity_check CHECK(
    (execution_engine='SOURCE' AND data_source_id IS NOT NULL)
    OR
    (execution_engine='POSTGRES' AND data_source_id IS NULL)
  );

CREATE TABLE platform.query_run_materializations(
  query_run_id uuid NOT NULL,
  tenant_id uuid NOT NULL,
  node_id text NOT NULL CHECK(node_id ~ '^[A-Za-z][A-Za-z0-9_]{0,127}$'),
  dataset_id uuid NOT NULL,
  dataset_version_id uuid NOT NULL,
  materialization_id uuid NOT NULL,
  layer text NOT NULL CHECK(layer IN ('ODS','DWD','DWS')),
  published_schema text NOT NULL CHECK(published_schema='warehouse_published'),
  published_name text NOT NULL CHECK(
    published_name ~ '^(ods|dwd|dws)_t[0-9a-f]{12}_d[0-9a-f]{12}$'
  ),
  schema_hash text NOT NULL CHECK(schema_hash ~ '^[0-9a-f]{64}$'),
  snapshot_hash text NOT NULL CHECK(snapshot_hash ~ '^[0-9a-f]{64}$'),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(query_run_id,tenant_id,node_id),
  FOREIGN KEY(query_run_id,tenant_id)
    REFERENCES platform.query_runs(id,tenant_id) ON DELETE CASCADE,
  FOREIGN KEY(materialization_id,dataset_id,dataset_version_id,tenant_id)
    REFERENCES platform.dataset_materializations(
      id,dataset_id,dataset_version_id,tenant_id
    ) ON DELETE RESTRICT
);

CREATE TABLE platform.query_candidate_run_materializations(
  query_run_id uuid NOT NULL,
  tenant_id uuid NOT NULL,
  node_id text NOT NULL CHECK(node_id ~ '^[A-Za-z][A-Za-z0-9_]{0,127}$'),
  dataset_id uuid NOT NULL,
  dataset_version_id uuid NOT NULL,
  materialization_id uuid NOT NULL,
  layer text NOT NULL CHECK(layer IN ('ODS','DWD','DWS')),
  published_schema text NOT NULL CHECK(published_schema='warehouse_published'),
  published_name text NOT NULL CHECK(
    published_name ~ '^(ods|dwd|dws)_t[0-9a-f]{12}_d[0-9a-f]{12}$'
  ),
  schema_hash text NOT NULL CHECK(schema_hash ~ '^[0-9a-f]{64}$'),
  snapshot_hash text NOT NULL CHECK(snapshot_hash ~ '^[0-9a-f]{64}$'),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(query_run_id,tenant_id,node_id),
  FOREIGN KEY(query_run_id,tenant_id)
    REFERENCES platform.query_candidate_runs(id,tenant_id) ON DELETE CASCADE,
  FOREIGN KEY(materialization_id,dataset_id,dataset_version_id,tenant_id)
    REFERENCES platform.dataset_materializations(
      id,dataset_id,dataset_version_id,tenant_id
    ) ON DELETE RESTRICT
);

CREATE INDEX query_run_materializations_version_idx
  ON platform.query_run_materializations(
    tenant_id,dataset_version_id,created_at DESC
  );
CREATE INDEX query_candidate_run_materializations_version_idx
  ON platform.query_candidate_run_materializations(
    tenant_id,dataset_version_id,created_at DESC
  );

CREATE OR REPLACE FUNCTION platform.reject_query_materialization_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  RAISE EXCEPTION '查询物化快照不可修改或删除' USING ERRCODE='23514';
END
$$;

REVOKE ALL ON FUNCTION platform.reject_query_materialization_mutation()
  FROM PUBLIC;

CREATE TRIGGER query_run_materializations_immutable
BEFORE UPDATE OR DELETE ON platform.query_run_materializations
FOR EACH ROW EXECUTE FUNCTION platform.reject_query_materialization_mutation();

CREATE TRIGGER query_candidate_run_materializations_immutable
BEFORE UPDATE OR DELETE ON platform.query_candidate_run_materializations
FOR EACH ROW EXECUTE FUNCTION platform.reject_query_materialization_mutation();

ALTER TABLE platform.query_run_materializations ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.query_run_materializations FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.query_candidate_run_materializations ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.query_candidate_run_materializations FORCE ROW LEVEL SECURITY;

CREATE POLICY query_run_materializations_tenant_isolation
  ON platform.query_run_materializations
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY query_candidate_run_materializations_tenant_isolation
  ON platform.query_candidate_run_materializations
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

COMMENT ON COLUMN platform.query_runs.execution_engine IS
  'SOURCE 使用外部 Connector；POSTGRES 只读取受治理的 warehouse_published 视图';
COMMENT ON TABLE platform.query_run_materializations IS
  '已保存数据集或指标查询开始时冻结的 ACTIVE 物化身份，不保存 SQL、参数或结果样本';
COMMENT ON TABLE platform.query_candidate_run_materializations IS
  '未保存数据集候选查询开始时冻结的 ACTIVE 物化身份';
