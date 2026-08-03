-- 把报告草稿编译为不可变 JSON 版本，并让在线运行时只读取精确发布制品。
CREATE TABLE platform.report_versions(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  report_id uuid NOT NULL,
  version_no integer NOT NULL CHECK(version_no>0),
  source_revision_no bigint NOT NULL CHECK(source_revision_no>0),
  source_definition_hash text NOT NULL CHECK(source_definition_hash ~ '^[0-9a-f]{64}$'),
  schema_version text NOT NULL CHECK(schema_version='1.0'),
  definition_json jsonb NOT NULL CHECK(jsonb_typeof(definition_json)='object'),
  definition_bytes bytea NOT NULL,
  definition_hash text NOT NULL CHECK(definition_hash ~ '^[0-9a-f]{64}$'),
  size_bytes bigint NOT NULL CHECK(size_bytes>0 AND size_bytes<=2097152 AND size_bytes=octet_length(definition_bytes)),
  publish_comment text NOT NULL DEFAULT '' CHECK(octet_length(publish_comment)<=1000),
  published_by uuid NOT NULL,
  published_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT report_versions_report_fk
    FOREIGN KEY(report_id,tenant_id) REFERENCES platform.reports(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT report_versions_published_by_fk
    FOREIGN KEY(published_by,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT report_versions_tenant_number_key UNIQUE(tenant_id,report_id,version_no),
  CONSTRAINT report_versions_source_revision_key UNIQUE(tenant_id,report_id,source_revision_no,source_definition_hash),
  CONSTRAINT report_versions_identity_report_tenant_key UNIQUE(id,report_id,tenant_id)
);

CREATE TABLE platform.report_version_component_indexes(
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  report_version_id uuid NOT NULL,
  report_id uuid NOT NULL,
  page_id text NOT NULL,
  block_id text NOT NULL,
  component_id text NOT NULL,
  component_type text NOT NULL,
  PRIMARY KEY(tenant_id,report_version_id,component_id),
  CONSTRAINT report_version_component_indexes_version_fk
    FOREIGN KEY(report_version_id,report_id,tenant_id)
    REFERENCES platform.report_versions(id,report_id,tenant_id) ON DELETE RESTRICT
);

CREATE TABLE platform.report_version_dependencies(
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  report_version_id uuid NOT NULL,
  report_id uuid NOT NULL,
  dependency_type text NOT NULL CHECK(dependency_type IN ('DATASET_VERSION','METRIC_VERSION','SOURCE_TRACE')),
  dependency_id text NOT NULL CHECK(btrim(dependency_id)<>''),
  referenced_version_id text NOT NULL DEFAULT '',
  json_path text NOT NULL CHECK(btrim(json_path)<>''),
  PRIMARY KEY(tenant_id,report_version_id,dependency_type,dependency_id,json_path),
  CONSTRAINT report_version_dependencies_version_fk
    FOREIGN KEY(report_version_id,report_id,tenant_id)
    REFERENCES platform.report_versions(id,report_id,tenant_id) ON DELETE RESTRICT
);

CREATE TABLE platform.report_publication_idempotency(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  report_id uuid NOT NULL,
  actor_user_id uuid NOT NULL,
  operation text NOT NULL CHECK(operation IN ('PUBLISH','ROLLBACK')),
  idempotency_key text NOT NULL CHECK(length(idempotency_key) BETWEEN 1 AND 128),
  request_hash text NOT NULL CHECK(request_hash ~ '^[0-9a-f]{64}$'),
  response_json jsonb NOT NULL CHECK(jsonb_typeof(response_json)='object'),
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT report_publication_idempotency_report_fk
    FOREIGN KEY(report_id,tenant_id) REFERENCES platform.reports(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT report_publication_idempotency_actor_fk
    FOREIGN KEY(actor_user_id,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT report_publication_idempotency_key UNIQUE(tenant_id,report_id,operation,idempotency_key)
);

ALTER TABLE platform.reports
  ADD COLUMN current_published_version_id uuid,
  ADD CONSTRAINT reports_current_published_version_fk
    FOREIGN KEY(current_published_version_id,id,tenant_id)
    REFERENCES platform.report_versions(id,report_id,tenant_id) ON DELETE RESTRICT;

CREATE INDEX report_versions_latest_idx ON platform.report_versions(tenant_id,report_id,version_no DESC);
CREATE INDEX report_versions_hash_idx ON platform.report_versions(tenant_id,definition_hash);
CREATE INDEX report_version_dependencies_impact_idx
  ON platform.report_version_dependencies(tenant_id,dependency_type,dependency_id,referenced_version_id);

CREATE OR REPLACE FUNCTION platform.reject_report_version_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  RAISE EXCEPTION 'report versions are immutable';
END
$$;

CREATE TRIGGER report_versions_immutable
BEFORE UPDATE OR DELETE ON platform.report_versions
FOR EACH ROW EXECUTE FUNCTION platform.reject_report_version_mutation();

CREATE TRIGGER report_version_component_indexes_immutable
BEFORE UPDATE OR DELETE ON platform.report_version_component_indexes
FOR EACH ROW EXECUTE FUNCTION platform.reject_report_version_mutation();

CREATE TRIGGER report_version_dependencies_immutable
BEFORE UPDATE OR DELETE ON platform.report_version_dependencies
FOR EACH ROW EXECUTE FUNCTION platform.reject_report_version_mutation();

ALTER TABLE platform.report_versions ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.report_versions FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.report_version_component_indexes ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.report_version_component_indexes FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.report_version_dependencies ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.report_version_dependencies FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.report_publication_idempotency ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.report_publication_idempotency FORCE ROW LEVEL SECURITY;

CREATE POLICY report_versions_tenant_isolation ON platform.report_versions
  USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY report_version_component_indexes_tenant_isolation ON platform.report_version_component_indexes
  USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY report_version_dependencies_tenant_isolation ON platform.report_version_dependencies
  USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY report_publication_idempotency_tenant_isolation ON platform.report_publication_idempotency
  USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());

COMMENT ON TABLE platform.report_versions IS '从指定草稿修订编译出的不可变报告 JSON 制品';
COMMENT ON TABLE platform.report_version_dependencies IS '发布时固定的精确数据集和指标版本引用';
COMMENT ON COLUMN platform.reports.current_published_version_id IS '在线运行时唯一允许跟随的不可变报告版本指针';
