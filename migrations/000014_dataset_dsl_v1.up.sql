-- 数据集 DSL 是设计态唯一事实来源；逻辑计划和索引均可由 DSL 重新派生。
CREATE TABLE platform.datasets(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  code citext NOT NULL,
  name text NOT NULL CHECK(btrim(name)<>''),
  description text NOT NULL DEFAULT '',
  dataset_type text NOT NULL CHECK(dataset_type IN ('SINGLE_SOURCE','CROSS_SOURCE')),
  status text NOT NULL DEFAULT 'DRAFT' CHECK(status IN ('DRAFT','VALIDATING','PUBLISHED','STALE','DEPRECATED')),
  current_draft_version_id uuid,
  current_published_version_id uuid,
  version bigint NOT NULL DEFAULT 1 CHECK(version>0),
  created_by uuid,
  updated_by uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  deleted_at timestamptz,
  FOREIGN KEY(created_by,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE SET NULL (created_by),
  FOREIGN KEY(updated_by,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE SET NULL (updated_by),
  UNIQUE(tenant_id,code),
  UNIQUE(id,tenant_id)
);

CREATE TABLE platform.dataset_versions(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  dataset_id uuid NOT NULL,
  version_no integer NOT NULL CHECK(version_no>0),
  status text NOT NULL DEFAULT 'DRAFT' CHECK(status IN ('DRAFT','PUBLISHED','STALE','DEPRECATED')),
  dsl_version text NOT NULL CHECK(dsl_version='1.0'),
  dsl_json jsonb NOT NULL CHECK(jsonb_typeof(dsl_json)='object'),
  schema_hash text NOT NULL CHECK(length(schema_hash)=64),
  logical_plan_json jsonb NOT NULL CHECK(jsonb_typeof(logical_plan_json)='object'),
  plan_hash text NOT NULL CHECK(length(plan_hash)=64),
  record_version bigint NOT NULL DEFAULT 1 CHECK(record_version>0),
  created_by uuid,
  updated_by uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY(dataset_id,tenant_id) REFERENCES platform.datasets(id,tenant_id),
  FOREIGN KEY(created_by,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE SET NULL (created_by),
  FOREIGN KEY(updated_by,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE SET NULL (updated_by),
  UNIQUE(tenant_id,dataset_id,version_no),
  UNIQUE(id,tenant_id)
);

ALTER TABLE platform.datasets ADD CONSTRAINT datasets_current_draft_fk
  FOREIGN KEY(current_draft_version_id,tenant_id) REFERENCES platform.dataset_versions(id,tenant_id);
ALTER TABLE platform.datasets ADD CONSTRAINT datasets_current_published_fk
  FOREIGN KEY(current_published_version_id,tenant_id) REFERENCES platform.dataset_versions(id,tenant_id);

-- 字段、参数和依赖表是检索/血缘索引，不是第二份事实来源。
CREATE TABLE platform.dataset_fields(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  dataset_version_id uuid NOT NULL,
  field_id text NOT NULL,
  field_code citext NOT NULL,
  field_name text NOT NULL,
  expression_json jsonb NOT NULL,
  canonical_type text NOT NULL,
  semantic_type text NOT NULL DEFAULT '',
  field_role text NOT NULL,
  aggregation text NOT NULL DEFAULT '',
  nullable boolean NOT NULL DEFAULT false,
  visible boolean NOT NULL DEFAULT true,
  ordinal_position integer NOT NULL CHECK(ordinal_position>0),
  FOREIGN KEY(dataset_version_id,tenant_id) REFERENCES platform.dataset_versions(id,tenant_id) ON DELETE CASCADE,
  UNIQUE(tenant_id,dataset_version_id,field_id),
  UNIQUE(tenant_id,dataset_version_id,field_code)
);

CREATE TABLE platform.dataset_parameters(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  dataset_version_id uuid NOT NULL,
  code citext NOT NULL,
  name text NOT NULL,
  data_type text NOT NULL,
  multi_value boolean NOT NULL DEFAULT false,
  required boolean NOT NULL DEFAULT false,
  default_value jsonb,
  ordinal_position integer NOT NULL CHECK(ordinal_position>0),
  FOREIGN KEY(dataset_version_id,tenant_id) REFERENCES platform.dataset_versions(id,tenant_id) ON DELETE CASCADE,
  UNIQUE(tenant_id,dataset_version_id,code)
);

CREATE TABLE platform.dataset_dependencies(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  dataset_version_id uuid NOT NULL,
  source_type text NOT NULL CHECK(source_type IN ('TABLE','FILE_VERSION','DATASET_VERSION')),
  source_id text NOT NULL CHECK(btrim(source_id)<>''),
  created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY(dataset_version_id,tenant_id) REFERENCES platform.dataset_versions(id,tenant_id) ON DELETE CASCADE,
  UNIQUE(tenant_id,dataset_version_id,source_type,source_id)
);

CREATE UNIQUE INDEX dataset_versions_one_draft_idx ON platform.dataset_versions(tenant_id,dataset_id) WHERE status='DRAFT';
CREATE INDEX datasets_tenant_status_idx ON platform.datasets(tenant_id,status,updated_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX dataset_fields_version_order_idx ON platform.dataset_fields(tenant_id,dataset_version_id,ordinal_position);
CREATE INDEX dataset_parameters_version_order_idx ON platform.dataset_parameters(tenant_id,dataset_version_id,ordinal_position);
CREATE INDEX dataset_dependencies_source_idx ON platform.dataset_dependencies(tenant_id,source_type,source_id);
CREATE TRIGGER datasets_set_updated_at BEFORE UPDATE ON platform.datasets FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();
CREATE TRIGGER dataset_versions_set_updated_at BEFORE UPDATE ON platform.dataset_versions FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();

ALTER TABLE platform.datasets ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.datasets FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.dataset_versions ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.dataset_versions FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.dataset_fields ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.dataset_fields FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.dataset_parameters ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.dataset_parameters FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.dataset_dependencies ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.dataset_dependencies FORCE ROW LEVEL SECURITY;
CREATE POLICY datasets_tenant_isolation ON platform.datasets USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY dataset_versions_tenant_isolation ON platform.dataset_versions USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY dataset_fields_tenant_isolation ON platform.dataset_fields USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY dataset_parameters_tenant_isolation ON platform.dataset_parameters USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY dataset_dependencies_tenant_isolation ON platform.dataset_dependencies USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());

COMMENT ON TABLE platform.datasets IS '租户内数据集主对象及可变草稿指针';
COMMENT ON TABLE platform.dataset_versions IS '版本化 DSL 事实来源及可重建逻辑计划';
COMMENT ON TABLE platform.dataset_fields IS '由 dataset_versions.dsl_json 可重建的字段索引';
COMMENT ON TABLE platform.dataset_parameters IS '由 dataset_versions.dsl_json 可重建的参数索引';
COMMENT ON TABLE platform.dataset_dependencies IS '用于血缘和影响分析的可重建上游依赖索引';
