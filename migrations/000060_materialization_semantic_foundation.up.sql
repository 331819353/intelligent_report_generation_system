-- PostgreSQL 数据面物化、质量门和一等语义对象的控制面合同。
-- 真实业务数据只能由执行器写入独立 warehouse_* schema；platform 仅保存
-- 可审计的计划、快照、运行、物化定位和语义索引元数据。
CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE SCHEMA IF NOT EXISTS warehouse_staging;
CREATE SCHEMA IF NOT EXISTS warehouse_ods;
CREATE SCHEMA IF NOT EXISTS warehouse_dwd;
CREATE SCHEMA IF NOT EXISTS warehouse_dws;
CREATE SCHEMA IF NOT EXISTS warehouse_published;

REVOKE ALL ON SCHEMA warehouse_staging FROM PUBLIC;
REVOKE ALL ON SCHEMA warehouse_ods FROM PUBLIC;
REVOKE ALL ON SCHEMA warehouse_dwd FROM PUBLIC;
REVOKE ALL ON SCHEMA warehouse_dws FROM PUBLIC;
REVOKE ALL ON SCHEMA warehouse_published FROM PUBLIC;

COMMENT ON SCHEMA warehouse_staging IS
  '运行级临时落地区；只允许受控 warehouse 执行角色创建动态关系';
COMMENT ON SCHEMA warehouse_ods IS
  'ODS 物化数据面；不保存 platform 控制对象';
COMMENT ON SCHEMA warehouse_dwd IS
  'DWD 明细物化数据面；不保存 platform 控制对象';
COMMENT ON SCHEMA warehouse_dws IS
  'DWS 汇总物化数据面；不保存 platform 控制对象';
COMMENT ON SCHEMA warehouse_published IS
  '稳定发布视图数据面；由物化激活流程原子切换';

-- 字段说明属于字段检索文档的事实来源，不能只留在 DSL JSON 中。历史版本按
-- 稳定 field_id 回填；无法匹配或旧 DSL 未提供说明时保留空字符串。
ALTER TABLE platform.dataset_fields
  ADD COLUMN description text NOT NULL DEFAULT '';

UPDATE platform.dataset_fields AS dataset_field
SET description=COALESCE(field_document->>'description','')
FROM platform.dataset_versions AS dataset_version
CROSS JOIN LATERAL jsonb_array_elements(
  CASE
    WHEN jsonb_typeof(dataset_version.dsl_json->'fields')='array'
      THEN dataset_version.dsl_json->'fields'
    ELSE '[]'::jsonb
  END
) AS field_document
WHERE dataset_version.id=dataset_field.dataset_version_id
  AND dataset_version.tenant_id=dataset_field.tenant_id
  AND field_document->>'id'=dataset_field.field_id;

COMMENT ON COLUMN platform.dataset_fields.description IS
  '字段业务说明；由精确数据集版本 DSL 的 fields[].description 派生并供语义检索使用';

-- datasets.layer 是当前草稿层级的目录摘要，必须和指针所指版本保持一致。
-- 约束延迟到提交时执行，以允许“先建主对象、再建草稿、最后切指针”的事务写法。
CREATE OR REPLACE FUNCTION platform.enforce_dataset_draft_layer_consistency()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  target_dataset_id uuid;
  owner_layer text;
  owner_deleted_at timestamptz;
  draft_version_id uuid;
  draft_layer text;
BEGIN
  IF TG_TABLE_NAME='datasets' THEN
    target_dataset_id=COALESCE(NEW.id,OLD.id);
  ELSE
    target_dataset_id=COALESCE(NEW.dataset_id,OLD.dataset_id);
  END IF;

  SELECT layer,deleted_at,current_draft_version_id
  INTO owner_layer,owner_deleted_at,draft_version_id
  FROM platform.datasets
  WHERE id=target_dataset_id;
  IF NOT FOUND OR owner_deleted_at IS NOT NULL OR draft_version_id IS NULL THEN
    RETURN NULL;
  END IF;

  SELECT layer INTO draft_layer
  FROM platform.dataset_versions
  WHERE id=draft_version_id AND dataset_id=target_dataset_id;
  IF FOUND AND owner_layer IS DISTINCT FROM draft_layer THEN
    RAISE EXCEPTION '数据集目录层级必须与当前草稿版本层级一致'
      USING ERRCODE='23514';
  END IF;
  RETURN NULL;
END
$$;

REVOKE ALL ON FUNCTION platform.enforce_dataset_draft_layer_consistency() FROM PUBLIC;

CREATE CONSTRAINT TRIGGER datasets_draft_layer_consistency
AFTER INSERT OR UPDATE OR DELETE ON platform.datasets
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION platform.enforce_dataset_draft_layer_consistency();

CREATE CONSTRAINT TRIGGER dataset_versions_draft_layer_consistency
AFTER INSERT OR UPDATE OR DELETE ON platform.dataset_versions
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION platform.enforce_dataset_draft_layer_consistency();

-- 防御直接数据库写入绕过 Go 类型系统：控制面 JSON 不允许携带原始 SQL、
-- 凭据或业务数据行。执行器只能从已发布 DSL 和受限节点拓扑编译 SQL。
CREATE OR REPLACE FUNCTION platform.materialization_json_is_safe(document jsonb)
RETURNS boolean
LANGUAGE plpgsql
IMMUTABLE
STRICT
AS $$
DECLARE
  item record;
  normalized_key text;
BEGIN
  IF jsonb_typeof(document)='object' THEN
    FOR item IN SELECT key,value FROM jsonb_each(document)
    LOOP
      normalized_key := regexp_replace(lower(item.key),'[_-]','','g');
      IF normalized_key IN (
        'sql','rawsql','query','statement','password','secret','credentials',
        'rows','samplerows','rawdata'
      ) THEN
        RETURN false;
      END IF;
      IF NOT platform.materialization_json_is_safe(item.value) THEN
        RETURN false;
      END IF;
    END LOOP;
  ELSIF jsonb_typeof(document)='array' THEN
    FOR item IN SELECT value FROM jsonb_array_elements(document)
    LOOP
      IF NOT platform.materialization_json_is_safe(item.value) THEN
        RETURN false;
      END IF;
    END LOOP;
  END IF;
  RETURN true;
END
$$;

-- 该无副作用 IMMUTABLE helper 被下面的 CHECK 约束以写入者身份调用；保留
-- PostgreSQL 默认的 PUBLIC EXECUTE，否则普通应用角色无法插入构建计划或输入快照。
-- 它不读取表、不使用 SECURITY DEFINER，也不暴露任何控制面写能力。

CREATE TABLE platform.dataset_build_runs(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  dataset_id uuid NOT NULL,
  dataset_version_id uuid NOT NULL,
  layer text NOT NULL CHECK(layer IN ('ODS','DWD','DWS')),
  run_mode text NOT NULL CHECK(run_mode IN ('FULL','INCREMENTAL','BACKFILL')),
  status text NOT NULL DEFAULT 'QUEUED'
    CHECK(status IN ('QUEUED','RUNNING','SUCCEEDED','FAILED','CANCELLED')),
  plan_version text NOT NULL CHECK(plan_version='1.0'),
  plan_json jsonb NOT NULL
    CHECK(
      jsonb_typeof(plan_json)='object'
      AND pg_column_size(plan_json)<=1048576
      AND platform.materialization_json_is_safe(plan_json)
      AND plan_json->>'version'=plan_version
      AND plan_json->>'datasetId'=dataset_id::text
      AND plan_json->>'datasetVersionId'=dataset_version_id::text
      AND plan_json->>'layer'=layer
      AND plan_json->>'mode'=run_mode
    ),
  plan_hash text NOT NULL CHECK(plan_hash ~ '^[0-9a-f]{64}$'),
  input_snapshot_hash text NOT NULL CHECK(input_snapshot_hash ~ '^[0-9a-f]{64}$'),
  request_hash text NOT NULL CHECK(request_hash ~ '^[0-9a-f]{64}$'),
  idempotency_key text NOT NULL CHECK(idempotency_key ~ '^[0-9a-f]{64}$'),
  partition_key text NOT NULL DEFAULT '' CHECK(
    length(partition_key)<=256
    AND partition_key=btrim(partition_key)
    AND partition_key !~ '[[:cntrl:]]'
  ),
  requested_by uuid NOT NULL,
  attempt integer NOT NULL DEFAULT 0 CHECK(attempt>=0),
  max_attempts integer NOT NULL DEFAULT 3 CHECK(max_attempts BETWEEN 1 AND 10),
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  lease_owner text NOT NULL DEFAULT '' CHECK(length(lease_owner)<=128),
  lease_token uuid,
  lease_expires_at timestamptz,
  error_code text NOT NULL DEFAULT '' CHECK(length(error_code)<=128),
  error_message text NOT NULL DEFAULT '' CHECK(length(error_message)<=4096),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  started_at timestamptz,
  completed_at timestamptz,
  CONSTRAINT dataset_build_runs_dataset_version_fk
    FOREIGN KEY(dataset_version_id,dataset_id,tenant_id)
    REFERENCES platform.dataset_versions(id,dataset_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dataset_build_runs_requested_by_fk
    FOREIGN KEY(requested_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dataset_build_runs_attempt_budget_check CHECK(attempt<=max_attempts),
  CONSTRAINT dataset_build_runs_status_shape_check CHECK(
    (status='QUEUED' AND attempt=0 AND started_at IS NULL AND completed_at IS NULL
      AND lease_owner='' AND lease_token IS NULL AND lease_expires_at IS NULL
      AND error_code='' AND error_message='')
    OR
    (status='RUNNING' AND attempt>0 AND started_at IS NOT NULL AND completed_at IS NULL
      AND lease_owner<>'' AND lease_token IS NOT NULL AND lease_expires_at IS NOT NULL
      AND error_code='' AND error_message='')
    OR
    (status='SUCCEEDED' AND attempt>0 AND started_at IS NOT NULL
      AND completed_at IS NOT NULL AND completed_at>=started_at
      AND lease_owner='' AND lease_token IS NULL AND lease_expires_at IS NULL
      AND error_code='' AND error_message='')
    OR
    (status='FAILED' AND attempt>0 AND started_at IS NOT NULL
      AND completed_at IS NOT NULL AND completed_at>=started_at
      AND lease_owner='' AND lease_token IS NULL AND lease_expires_at IS NULL
      AND btrim(error_code)<>'')
    OR
    (status='CANCELLED' AND completed_at IS NOT NULL
      AND (started_at IS NULL OR completed_at>=started_at)
      AND lease_owner='' AND lease_token IS NULL AND lease_expires_at IS NULL)
  ),
  CONSTRAINT dataset_build_runs_idempotency_key
    UNIQUE(tenant_id,dataset_version_id,idempotency_key),
  CONSTRAINT dataset_build_runs_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT dataset_build_runs_identity_full_key
    UNIQUE(id,dataset_id,dataset_version_id,tenant_id)
);

CREATE TABLE platform.dataset_materializations(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  dataset_id uuid NOT NULL,
  dataset_version_id uuid NOT NULL,
  build_run_id uuid NOT NULL,
  layer text NOT NULL CHECK(layer IN ('ODS','DWD','DWS')),
  status text NOT NULL CHECK(status IN ('BUILDING','ACTIVE','RETIRED','FAILED')),
  relation_kind text NOT NULL DEFAULT 'TABLE'
    CHECK(relation_kind IN ('TABLE','PARTITIONED_TABLE')),
  refresh_mode text NOT NULL CHECK(refresh_mode IN ('FULL','INCREMENTAL','BACKFILL')),
  physical_schema text NOT NULL CHECK(
    physical_schema IN ('warehouse_ods','warehouse_dwd','warehouse_dws')
  ),
  physical_name text NOT NULL CHECK(
    physical_name ~ '^[a-z][a-z0-9_]{0,62}$'
  ),
  published_schema text NOT NULL DEFAULT 'warehouse_published'
    CHECK(published_schema='warehouse_published'),
  published_name text NOT NULL CHECK(
    published_name ~ '^[a-z][a-z0-9_]{0,62}$'
  ),
  schema_hash text NOT NULL CHECK(schema_hash ~ '^[0-9a-f]{64}$'),
  snapshot_hash text NOT NULL CHECK(snapshot_hash ~ '^[0-9a-f]{64}$'),
  row_count bigint CHECK(row_count>=0),
  size_bytes bigint CHECK(size_bytes>=0),
  watermark_json jsonb NOT NULL DEFAULT '{}'::jsonb
    CHECK(jsonb_typeof(watermark_json)='object' AND pg_column_size(watermark_json)<=65536),
  activated_at timestamptz,
  retired_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT dataset_materializations_dataset_version_fk
    FOREIGN KEY(dataset_version_id,dataset_id,tenant_id)
    REFERENCES platform.dataset_versions(id,dataset_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dataset_materializations_build_run_fk
    FOREIGN KEY(build_run_id,dataset_id,dataset_version_id,tenant_id)
    REFERENCES platform.dataset_build_runs(id,dataset_id,dataset_version_id,tenant_id)
    ON DELETE RESTRICT,
  CONSTRAINT dataset_materializations_status_shape_check CHECK(
    (status='BUILDING' AND activated_at IS NULL AND retired_at IS NULL)
    OR
    (status='ACTIVE' AND activated_at IS NOT NULL AND retired_at IS NULL
      AND row_count IS NOT NULL AND size_bytes IS NOT NULL)
    OR
    (status='RETIRED' AND activated_at IS NOT NULL AND retired_at IS NOT NULL
      AND retired_at>=activated_at AND row_count IS NOT NULL AND size_bytes IS NOT NULL)
    OR
    (status='FAILED' AND activated_at IS NULL AND retired_at IS NULL)
  ),
  CONSTRAINT dataset_materializations_layer_schema_check CHECK(
    physical_schema=CASE layer
      WHEN 'ODS' THEN 'warehouse_ods'
      WHEN 'DWD' THEN 'warehouse_dwd'
      WHEN 'DWS' THEN 'warehouse_dws'
    END
  ),
  CONSTRAINT dataset_materializations_build_run_key UNIQUE(tenant_id,build_run_id),
  CONSTRAINT dataset_materializations_physical_key UNIQUE(physical_schema,physical_name),
  CONSTRAINT dataset_materializations_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT dataset_materializations_identity_run_tenant_key
    UNIQUE(id,build_run_id,tenant_id)
);

CREATE TABLE platform.build_run_inputs(
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  build_run_id uuid NOT NULL,
  ordinal_position integer NOT NULL CHECK(ordinal_position>0),
  source_type text NOT NULL
    CHECK(source_type IN ('SOURCE_TABLE','FILE_VERSION','DATASET_VERSION','MATERIALIZATION')),
  input_layer text NOT NULL CHECK(input_layer IN ('SOURCE','ODS','DWD','DWS')),
  metadata_table_id uuid,
  file_version_id uuid,
  input_dataset_id uuid,
  input_dataset_version_id uuid,
  input_materialization_id uuid,
  source_version text NOT NULL CHECK(
    length(source_version) BETWEEN 1 AND 256
    AND source_version=btrim(source_version)
    AND source_version !~ '[[:cntrl:]]'
  ),
  schema_hash text NOT NULL CHECK(schema_hash ~ '^[0-9a-f]{64}$'),
  snapshot_hash text NOT NULL CHECK(snapshot_hash ~ '^[0-9a-f]{64}$'),
  snapshot_json jsonb NOT NULL DEFAULT '{}'::jsonb
    CHECK(
      jsonb_typeof(snapshot_json)='object'
      AND pg_column_size(snapshot_json)<=65536
      AND platform.materialization_json_is_safe(snapshot_json)
    ),
  row_count bigint CHECK(row_count>=0),
  captured_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(tenant_id,build_run_id,ordinal_position),
  CONSTRAINT build_run_inputs_build_run_fk
    FOREIGN KEY(build_run_id,tenant_id)
    REFERENCES platform.dataset_build_runs(id,tenant_id) ON DELETE CASCADE,
  CONSTRAINT build_run_inputs_metadata_table_fk
    FOREIGN KEY(metadata_table_id,tenant_id)
    REFERENCES platform.metadata_tables(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT build_run_inputs_file_version_fk
    FOREIGN KEY(file_version_id,tenant_id)
    REFERENCES platform.file_asset_versions(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT build_run_inputs_dataset_version_fk
    FOREIGN KEY(input_dataset_version_id,input_dataset_id,tenant_id)
    REFERENCES platform.dataset_versions(id,dataset_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT build_run_inputs_materialization_fk
    FOREIGN KEY(input_materialization_id,tenant_id)
    REFERENCES platform.dataset_materializations(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT build_run_inputs_source_shape_check CHECK(
    (source_type='SOURCE_TABLE' AND input_layer='SOURCE'
      AND metadata_table_id IS NOT NULL AND file_version_id IS NULL
      AND input_dataset_id IS NULL AND input_dataset_version_id IS NULL
      AND input_materialization_id IS NULL)
    OR
    (source_type='FILE_VERSION' AND input_layer='SOURCE'
      AND metadata_table_id IS NULL AND file_version_id IS NOT NULL
      AND input_dataset_id IS NULL AND input_dataset_version_id IS NULL
      AND input_materialization_id IS NULL)
    OR
    (source_type='DATASET_VERSION' AND input_layer IN ('ODS','DWD','DWS')
      AND metadata_table_id IS NULL AND file_version_id IS NULL
      AND input_dataset_id IS NOT NULL AND input_dataset_version_id IS NOT NULL
      AND input_materialization_id IS NULL)
    OR
    (source_type='MATERIALIZATION' AND input_layer IN ('ODS','DWD','DWS')
      AND metadata_table_id IS NULL AND file_version_id IS NULL
      AND input_dataset_id IS NULL AND input_dataset_version_id IS NULL
      AND input_materialization_id IS NOT NULL)
  )
);

CREATE TABLE platform.build_node_runs(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  build_run_id uuid NOT NULL,
  node_id text NOT NULL CHECK(node_id ~ '^[a-z][a-z0-9_-]{0,63}$'),
  node_kind text NOT NULL CHECK(
    node_kind IN ('EXTRACT','STAGE','PROJECT','FILTER','JOIN','AGGREGATE','MATERIALIZE')
  ),
  execution_engine text NOT NULL CHECK(execution_engine IN ('SOURCE_DB','POSTGRES')),
  status text NOT NULL DEFAULT 'PENDING'
    CHECK(status IN ('PENDING','RUNNING','SUCCEEDED','FAILED','SKIPPED')),
  attempt integer NOT NULL DEFAULT 0 CHECK(attempt>=0),
  input_row_count bigint CHECK(input_row_count>=0),
  output_row_count bigint CHECK(output_row_count>=0),
  output_size_bytes bigint CHECK(output_size_bytes>=0),
  error_code text NOT NULL DEFAULT '' CHECK(length(error_code)<=128),
  error_message text NOT NULL DEFAULT '' CHECK(length(error_message)<=4096),
  started_at timestamptz,
  completed_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT build_node_runs_build_run_fk
    FOREIGN KEY(build_run_id,tenant_id)
    REFERENCES platform.dataset_build_runs(id,tenant_id) ON DELETE CASCADE,
  CONSTRAINT build_node_runs_status_shape_check CHECK(
    (status='PENDING' AND attempt=0 AND started_at IS NULL AND completed_at IS NULL
      AND error_code='' AND error_message='')
    OR
    (status='RUNNING' AND attempt>0 AND started_at IS NOT NULL AND completed_at IS NULL
      AND error_code='' AND error_message='')
    OR
    (status IN ('SUCCEEDED','SKIPPED') AND attempt>0 AND started_at IS NOT NULL
      AND completed_at IS NOT NULL AND completed_at>=started_at AND error_code='')
    OR
    (status='FAILED' AND attempt>0 AND started_at IS NOT NULL
      AND completed_at IS NOT NULL AND completed_at>=started_at
      AND btrim(error_code)<>'')
  ),
  CONSTRAINT build_node_runs_run_node_key UNIQUE(tenant_id,build_run_id,node_id),
  CONSTRAINT build_node_runs_identity_run_tenant_key UNIQUE(id,build_run_id,tenant_id)
);

CREATE TABLE platform.data_quality_results(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  build_run_id uuid NOT NULL,
  build_node_run_id uuid,
  materialization_id uuid,
  rule_code text NOT NULL CHECK(
    length(rule_code) BETWEEN 1 AND 128
    AND rule_code=btrim(rule_code)
    AND rule_code ~ '^[A-Za-z][A-Za-z0-9_.-]*$'
  ),
  rule_version text NOT NULL CHECK(length(rule_version) BETWEEN 1 AND 64),
  rule_definition_hash text NOT NULL CHECK(rule_definition_hash ~ '^[0-9a-f]{64}$'),
  scope text NOT NULL CHECK(scope IN ('DATASET','FIELD','RELATIONSHIP')),
  field_id text NOT NULL DEFAULT '' CHECK(length(field_id)<=256),
  severity text NOT NULL CHECK(severity IN ('INFO','WARNING','ERROR')),
  status text NOT NULL CHECK(status IN ('PASSED','FAILED','SKIPPED')),
  expectation_json jsonb NOT NULL DEFAULT '{}'::jsonb
    CHECK(jsonb_typeof(expectation_json)='object' AND pg_column_size(expectation_json)<=65536),
  observed_json jsonb NOT NULL DEFAULT '{}'::jsonb
    CHECK(jsonb_typeof(observed_json)='object' AND pg_column_size(observed_json)<=65536),
  message text NOT NULL DEFAULT '' CHECK(length(message)<=4096),
  measured_at timestamptz NOT NULL DEFAULT now(),
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT data_quality_results_build_run_fk
    FOREIGN KEY(build_run_id,tenant_id)
    REFERENCES platform.dataset_build_runs(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT data_quality_results_node_run_fk
    FOREIGN KEY(build_node_run_id,build_run_id,tenant_id)
    REFERENCES platform.build_node_runs(id,build_run_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT data_quality_results_materialization_fk
    FOREIGN KEY(materialization_id,build_run_id,tenant_id)
    REFERENCES platform.dataset_materializations(id,build_run_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT data_quality_results_scope_shape_check CHECK(
    (scope='FIELD' AND btrim(field_id)<>'')
    OR (scope IN ('DATASET','RELATIONSHIP') AND field_id='')
  )
);

CREATE TABLE platform.semantic_tags(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  parent_tag_id uuid,
  code citext NOT NULL CHECK(
    length(code::text) BETWEEN 1 AND 128
    AND btrim(code::text)<>''
    AND code::text !~ '[[:cntrl:]]'
  ),
  name text NOT NULL CHECK(length(name) BETWEEN 1 AND 256 AND btrim(name)<>''),
  description text NOT NULL DEFAULT '' CHECK(length(description)<=4096),
  category text NOT NULL CHECK(category IN (
    'BUSINESS_DOMAIN','BUSINESS_ENTITY','TABLE_FUNCTION','USAGE_SCOPE',
    'DATA_GRAIN','JOIN_ROLE','SENSITIVITY','FREEFORM'
  )),
  governance text NOT NULL DEFAULT 'CONTROLLED'
    CHECK(governance IN ('CONTROLLED','FREEFORM')),
  status text NOT NULL DEFAULT 'DRAFT'
    CHECK(status IN ('DRAFT','ACTIVE','DEPRECATED')),
  version bigint NOT NULL DEFAULT 1 CHECK(version>0),
  created_by uuid NOT NULL,
  updated_by uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT semantic_tags_parent_fk
    FOREIGN KEY(parent_tag_id,tenant_id)
    REFERENCES platform.semantic_tags(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT semantic_tags_created_by_fk
    FOREIGN KEY(created_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT semantic_tags_updated_by_fk
    FOREIGN KEY(updated_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT semantic_tags_no_self_parent_check CHECK(parent_tag_id IS NULL OR parent_tag_id<>id),
  CONSTRAINT semantic_tags_tenant_code_key UNIQUE(tenant_id,code),
  CONSTRAINT semantic_tags_identity_tenant_key UNIQUE(id,tenant_id)
);

CREATE TABLE platform.semantic_tag_aliases(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  tag_id uuid NOT NULL,
  alias citext NOT NULL CHECK(
    length(alias::text) BETWEEN 1 AND 256
    AND btrim(alias::text)<>''
    AND alias::text !~ '[[:cntrl:]]'
  ),
  alias_type text NOT NULL DEFAULT 'BUSINESS'
    CHECK(alias_type IN ('BUSINESS','ABBREVIATION','LEGACY','LLM','USER')),
  language_code text NOT NULL DEFAULT 'zh-CN'
    CHECK(language_code ~ '^[a-z]{2}(-[A-Z]{2})?$'),
  created_by uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT semantic_tag_aliases_tag_fk
    FOREIGN KEY(tag_id,tenant_id)
    REFERENCES platform.semantic_tags(id,tenant_id) ON DELETE CASCADE,
  CONSTRAINT semantic_tag_aliases_created_by_fk
    FOREIGN KEY(created_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT semantic_tag_aliases_tenant_alias_key UNIQUE(tenant_id,alias),
  CONSTRAINT semantic_tag_aliases_identity_tenant_key UNIQUE(id,tenant_id)
);

-- 维度固定到一个不可变 DWS 数据集版本和逻辑字段；成员表只保存去重后的
-- 检索键、规范标签和有效期，不复制 DWS 的动态事实或度量列。
CREATE TABLE platform.semantic_dimensions(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  dataset_id uuid NOT NULL,
  dataset_version_id uuid NOT NULL,
  field_id text NOT NULL CHECK(length(field_id) BETWEEN 1 AND 256),
  code citext NOT NULL CHECK(
    length(code::text) BETWEEN 1 AND 128 AND btrim(code::text)<>''
  ),
  name text NOT NULL CHECK(length(name) BETWEEN 1 AND 256 AND btrim(name)<>''),
  description text NOT NULL DEFAULT '' CHECK(length(description)<=4096),
  dimension_type text NOT NULL CHECK(dimension_type IN (
    'STANDARD','TIME','GEOGRAPHY','ORGANIZATION','PRODUCT','CUSTOMER','OTHER'
  )),
  member_index_policy text NOT NULL DEFAULT 'FULL'
    CHECK(member_index_policy IN ('FULL','EXACT_ONLY','NONE')),
  high_cardinality boolean NOT NULL DEFAULT false,
  sensitive boolean NOT NULL DEFAULT false,
  status text NOT NULL DEFAULT 'DRAFT'
    CHECK(status IN ('DRAFT','PUBLISHED','DEPRECATED')),
  definition_hash text NOT NULL CHECK(definition_hash ~ '^[0-9a-f]{64}$'),
  version bigint NOT NULL DEFAULT 1 CHECK(version>0),
  created_by uuid NOT NULL,
  updated_by uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT semantic_dimensions_dataset_version_fk
    FOREIGN KEY(dataset_version_id,dataset_id,tenant_id)
    REFERENCES platform.dataset_versions(id,dataset_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT semantic_dimensions_dataset_field_fk
    FOREIGN KEY(tenant_id,dataset_version_id,field_id)
    REFERENCES platform.dataset_fields(tenant_id,dataset_version_id,field_id) ON DELETE RESTRICT,
  CONSTRAINT semantic_dimensions_created_by_fk
    FOREIGN KEY(created_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT semantic_dimensions_updated_by_fk
    FOREIGN KEY(updated_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT semantic_dimensions_tenant_version_code_key
    UNIQUE(tenant_id,dataset_version_id,code),
  CONSTRAINT semantic_dimensions_tenant_version_field_key
    UNIQUE(tenant_id,dataset_version_id,field_id),
  CONSTRAINT semantic_dimensions_identity_tenant_key UNIQUE(id,tenant_id)
);

CREATE TABLE platform.dimension_members(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  dimension_id uuid NOT NULL,
  member_key text NOT NULL CHECK(
    length(member_key) BETWEEN 1 AND 1024
    AND member_key=btrim(member_key)
    AND member_key !~ '[[:cntrl:]]'
  ),
  canonical_label text NOT NULL CHECK(
    length(canonical_label) BETWEEN 1 AND 1024 AND btrim(canonical_label)<>''
  ),
  normalized_value text NOT NULL CHECK(
    length(normalized_value) BETWEEN 1 AND 1024 AND btrim(normalized_value)<>''
  ),
  source_value_hash text NOT NULL CHECK(source_value_hash ~ '^[0-9a-f]{64}$'),
  status text NOT NULL DEFAULT 'ACTIVE' CHECK(status IN ('ACTIVE','DEPRECATED')),
  first_seen_at timestamptz NOT NULL DEFAULT now(),
  last_seen_at timestamptz NOT NULL DEFAULT now(),
  valid_from timestamptz,
  valid_to timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT dimension_members_dimension_fk
    FOREIGN KEY(dimension_id,tenant_id)
    REFERENCES platform.semantic_dimensions(id,tenant_id) ON DELETE CASCADE,
  CONSTRAINT dimension_members_seen_check CHECK(last_seen_at>=first_seen_at),
  CONSTRAINT dimension_members_validity_check CHECK(
    valid_to IS NULL OR (valid_from IS NOT NULL AND valid_to>valid_from)
  ),
  CONSTRAINT dimension_members_dimension_key_key
    UNIQUE(tenant_id,dimension_id,member_key),
  CONSTRAINT dimension_members_dimension_normalized_key
    UNIQUE(tenant_id,dimension_id,normalized_value),
  CONSTRAINT dimension_members_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT dimension_members_identity_dimension_tenant_key
    UNIQUE(id,dimension_id,tenant_id)
);

CREATE TABLE platform.dimension_member_aliases(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  dimension_id uuid NOT NULL,
  dimension_member_id uuid NOT NULL,
  alias text NOT NULL CHECK(
    length(alias) BETWEEN 1 AND 1024
    AND alias=btrim(alias)
    AND alias !~ '[[:cntrl:]]'
  ),
  normalized_alias text NOT NULL CHECK(
    length(normalized_alias) BETWEEN 1 AND 1024 AND btrim(normalized_alias)<>''
  ),
  alias_type text NOT NULL
    CHECK(alias_type IN ('CODE','BUSINESS','ABBREVIATION','LEGACY','LLM','USER')),
  valid_from timestamptz,
  valid_to timestamptz,
  created_by uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT dimension_member_aliases_member_fk
    FOREIGN KEY(dimension_member_id,dimension_id,tenant_id)
    REFERENCES platform.dimension_members(id,dimension_id,tenant_id) ON DELETE CASCADE,
  CONSTRAINT dimension_member_aliases_dimension_fk
    FOREIGN KEY(dimension_id,tenant_id)
    REFERENCES platform.semantic_dimensions(id,tenant_id) ON DELETE CASCADE,
  CONSTRAINT dimension_member_aliases_created_by_fk
    FOREIGN KEY(created_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE SET NULL (created_by),
  CONSTRAINT dimension_member_aliases_validity_check CHECK(
    valid_to IS NULL OR (valid_from IS NOT NULL AND valid_to>valid_from)
  ),
  CONSTRAINT dimension_member_aliases_dimension_alias_key
    UNIQUE(tenant_id,dimension_id,normalized_alias),
  CONSTRAINT dimension_member_aliases_identity_tenant_key UNIQUE(id,tenant_id)
);

CREATE TABLE platform.asset_tag_bindings(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  tag_id uuid NOT NULL,
  asset_type text NOT NULL CHECK(asset_type IN (
    'DATASET_VERSION','DATASET_FIELD','DIMENSION','DIMENSION_MEMBER','METRIC_VERSION'
  )),
  dataset_id uuid,
  dataset_version_id uuid,
  dataset_field_id text,
  dimension_id uuid,
  dimension_member_id uuid,
  metric_id uuid,
  metric_version_id uuid,
  metric_dataset_version_id uuid,
  origin text NOT NULL CHECK(origin IN ('USER','LLM','RULE','IMPORT')),
  status text NOT NULL DEFAULT 'SUGGESTED'
    CHECK(status IN ('SUGGESTED','APPROVED','REJECTED')),
  confidence numeric(5,4) CHECK(confidence IS NULL OR confidence BETWEEN 0 AND 1),
  evidence_json jsonb NOT NULL DEFAULT '{}'::jsonb
    CHECK(jsonb_typeof(evidence_json)='object' AND pg_column_size(evidence_json)<=65536),
  assigned_by uuid,
  approved_by uuid,
  approved_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT asset_tag_bindings_tag_fk
    FOREIGN KEY(tag_id,tenant_id)
    REFERENCES platform.semantic_tags(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT asset_tag_bindings_dataset_version_fk
    FOREIGN KEY(dataset_version_id,dataset_id,tenant_id)
    REFERENCES platform.dataset_versions(id,dataset_id,tenant_id) ON DELETE CASCADE,
  CONSTRAINT asset_tag_bindings_dataset_field_fk
    FOREIGN KEY(tenant_id,dataset_version_id,dataset_field_id)
    REFERENCES platform.dataset_fields(tenant_id,dataset_version_id,field_id) ON DELETE CASCADE,
  CONSTRAINT asset_tag_bindings_dimension_fk
    FOREIGN KEY(dimension_id,tenant_id)
    REFERENCES platform.semantic_dimensions(id,tenant_id) ON DELETE CASCADE,
  CONSTRAINT asset_tag_bindings_member_fk
    FOREIGN KEY(dimension_member_id,dimension_id,tenant_id)
    REFERENCES platform.dimension_members(id,dimension_id,tenant_id) ON DELETE CASCADE,
  CONSTRAINT asset_tag_bindings_metric_version_fk
    FOREIGN KEY(metric_version_id,metric_id,metric_dataset_version_id,tenant_id)
    REFERENCES platform.metric_versions(id,metric_id,dataset_version_id,tenant_id) ON DELETE CASCADE,
  CONSTRAINT asset_tag_bindings_assigned_by_fk
    FOREIGN KEY(assigned_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE SET NULL (assigned_by),
  CONSTRAINT asset_tag_bindings_approved_by_fk
    FOREIGN KEY(approved_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT asset_tag_bindings_subject_shape_check CHECK(
    (asset_type='DATASET_VERSION'
      AND dataset_id IS NOT NULL AND dataset_version_id IS NOT NULL
      AND dataset_field_id IS NULL AND dimension_id IS NULL AND dimension_member_id IS NULL
      AND metric_id IS NULL AND metric_version_id IS NULL AND metric_dataset_version_id IS NULL)
    OR
    (asset_type='DATASET_FIELD'
      AND dataset_id IS NOT NULL AND dataset_version_id IS NOT NULL
      AND dataset_field_id IS NOT NULL AND dimension_id IS NULL AND dimension_member_id IS NULL
      AND metric_id IS NULL AND metric_version_id IS NULL AND metric_dataset_version_id IS NULL)
    OR
    (asset_type='DIMENSION'
      AND dataset_id IS NULL AND dataset_version_id IS NULL AND dataset_field_id IS NULL
      AND dimension_id IS NOT NULL AND dimension_member_id IS NULL
      AND metric_id IS NULL AND metric_version_id IS NULL AND metric_dataset_version_id IS NULL)
    OR
    (asset_type='DIMENSION_MEMBER'
      AND dataset_id IS NULL AND dataset_version_id IS NULL AND dataset_field_id IS NULL
      AND dimension_id IS NOT NULL AND dimension_member_id IS NOT NULL
      AND metric_id IS NULL AND metric_version_id IS NULL AND metric_dataset_version_id IS NULL)
    OR
    (asset_type='METRIC_VERSION'
      AND dataset_id IS NULL AND dataset_version_id IS NULL AND dataset_field_id IS NULL
      AND dimension_id IS NULL AND dimension_member_id IS NULL
      AND metric_id IS NOT NULL AND metric_version_id IS NOT NULL
      AND metric_dataset_version_id IS NOT NULL)
  ),
  CONSTRAINT asset_tag_bindings_approval_shape_check CHECK(
    (status='APPROVED' AND approved_by IS NOT NULL AND approved_at IS NOT NULL)
    OR (status<>'APPROVED' AND approved_by IS NULL AND approved_at IS NULL)
  )
);

CREATE TABLE platform.semantic_documents(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  subject_type text NOT NULL CHECK(subject_type IN (
    'TAG','DATASET_VERSION','DATASET_FIELD','DIMENSION','DIMENSION_MEMBER','METRIC_VERSION'
  )),
  tag_id uuid,
  dataset_id uuid,
  dataset_version_id uuid,
  dataset_field_id text,
  dimension_id uuid,
  dimension_member_id uuid,
  metric_id uuid,
  metric_version_id uuid,
  metric_dataset_version_id uuid,
  document_version text NOT NULL CHECK(length(document_version) BETWEEN 1 AND 64),
  document text NOT NULL CHECK(length(document) BETWEEN 1 AND 262144),
  input_hash text NOT NULL CHECK(input_hash ~ '^[0-9a-f]{64}$'),
  semantic_source text NOT NULL CHECK(semantic_source IN ('RULE','LLM','HYBRID','USER')),
  embedding halfvec(2560),
  embedding_model text NOT NULL DEFAULT '',
  embedding_input_hash text NOT NULL DEFAULT ''
    CHECK(embedding_input_hash='' OR embedding_input_hash ~ '^[0-9a-f]{64}$'),
  embedding_status text NOT NULL DEFAULT 'PENDING'
    CHECK(embedding_status IN ('PENDING','SUCCEEDED','FAILED','SKIPPED')),
  embedding_error_code text NOT NULL DEFAULT '' CHECK(length(embedding_error_code)<=128),
  embedded_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT semantic_documents_tag_fk
    FOREIGN KEY(tag_id,tenant_id)
    REFERENCES platform.semantic_tags(id,tenant_id) ON DELETE CASCADE,
  CONSTRAINT semantic_documents_dataset_version_fk
    FOREIGN KEY(dataset_version_id,dataset_id,tenant_id)
    REFERENCES platform.dataset_versions(id,dataset_id,tenant_id) ON DELETE CASCADE,
  CONSTRAINT semantic_documents_dataset_field_fk
    FOREIGN KEY(tenant_id,dataset_version_id,dataset_field_id)
    REFERENCES platform.dataset_fields(tenant_id,dataset_version_id,field_id) ON DELETE CASCADE,
  CONSTRAINT semantic_documents_dimension_fk
    FOREIGN KEY(dimension_id,tenant_id)
    REFERENCES platform.semantic_dimensions(id,tenant_id) ON DELETE CASCADE,
  CONSTRAINT semantic_documents_member_fk
    FOREIGN KEY(dimension_member_id,dimension_id,tenant_id)
    REFERENCES platform.dimension_members(id,dimension_id,tenant_id) ON DELETE CASCADE,
  CONSTRAINT semantic_documents_metric_version_fk
    FOREIGN KEY(metric_version_id,metric_id,metric_dataset_version_id,tenant_id)
    REFERENCES platform.metric_versions(id,metric_id,dataset_version_id,tenant_id) ON DELETE CASCADE,
  CONSTRAINT semantic_documents_subject_shape_check CHECK(
    (subject_type='TAG' AND tag_id IS NOT NULL
      AND dataset_id IS NULL AND dataset_version_id IS NULL AND dataset_field_id IS NULL
      AND dimension_id IS NULL AND dimension_member_id IS NULL
      AND metric_id IS NULL AND metric_version_id IS NULL AND metric_dataset_version_id IS NULL)
    OR
    (subject_type='DATASET_VERSION' AND tag_id IS NULL
      AND dataset_id IS NOT NULL AND dataset_version_id IS NOT NULL AND dataset_field_id IS NULL
      AND dimension_id IS NULL AND dimension_member_id IS NULL
      AND metric_id IS NULL AND metric_version_id IS NULL AND metric_dataset_version_id IS NULL)
    OR
    (subject_type='DATASET_FIELD' AND tag_id IS NULL
      AND dataset_id IS NOT NULL AND dataset_version_id IS NOT NULL AND dataset_field_id IS NOT NULL
      AND dimension_id IS NULL AND dimension_member_id IS NULL
      AND metric_id IS NULL AND metric_version_id IS NULL AND metric_dataset_version_id IS NULL)
    OR
    (subject_type='DIMENSION' AND tag_id IS NULL
      AND dataset_id IS NULL AND dataset_version_id IS NULL AND dataset_field_id IS NULL
      AND dimension_id IS NOT NULL AND dimension_member_id IS NULL
      AND metric_id IS NULL AND metric_version_id IS NULL AND metric_dataset_version_id IS NULL)
    OR
    (subject_type='DIMENSION_MEMBER' AND tag_id IS NULL
      AND dataset_id IS NULL AND dataset_version_id IS NULL AND dataset_field_id IS NULL
      AND dimension_id IS NOT NULL AND dimension_member_id IS NOT NULL
      AND metric_id IS NULL AND metric_version_id IS NULL AND metric_dataset_version_id IS NULL)
    OR
    (subject_type='METRIC_VERSION' AND tag_id IS NULL
      AND dataset_id IS NULL AND dataset_version_id IS NULL AND dataset_field_id IS NULL
      AND dimension_id IS NULL AND dimension_member_id IS NULL
      AND metric_id IS NOT NULL AND metric_version_id IS NOT NULL
      AND metric_dataset_version_id IS NOT NULL)
  ),
  CONSTRAINT semantic_documents_embedding_shape_check CHECK(
    (embedding_status='SUCCEEDED' AND embedding IS NOT NULL
      AND btrim(embedding_model)<>'' AND embedding_input_hash=input_hash
      AND embedded_at IS NOT NULL AND embedding_error_code='')
    OR
    (embedding_status='PENDING' AND embedding IS NULL AND embedding_model=''
      AND embedding_input_hash='' AND embedded_at IS NULL AND embedding_error_code='')
    OR
    (embedding_status IN ('FAILED','SKIPPED') AND embedding IS NULL
      AND embedding_model='' AND embedding_input_hash='' AND embedded_at IS NULL
      AND btrim(embedding_error_code)<>'')
  ),
  CONSTRAINT semantic_documents_identity_tenant_key UNIQUE(id,tenant_id)
);

CREATE TABLE platform.semantic_change_outbox(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  subject_type text NOT NULL CHECK(subject_type IN (
    'TAG','DATASET_VERSION','DATASET_FIELD','DIMENSION','DIMENSION_MEMBER',
    'METRIC_VERSION','SEMANTIC_DOCUMENT'
  )),
  subject_ref text NOT NULL CHECK(
    length(subject_ref) BETWEEN 1 AND 1024
    AND subject_ref=btrim(subject_ref)
    AND subject_ref !~ '[[:cntrl:]]'
  ),
  event_kind text NOT NULL DEFAULT 'REBUILD' CHECK(event_kind IN ('REBUILD','DELETE')),
  event_version bigint NOT NULL DEFAULT 1 CHECK(event_version>0),
  status text NOT NULL DEFAULT 'PENDING'
    CHECK(status IN ('PENDING','RUNNING','SUCCEEDED','FAILED','SKIPPED')),
  attempt integer NOT NULL DEFAULT 0 CHECK(attempt>=0),
  max_attempts integer NOT NULL DEFAULT 3 CHECK(max_attempts BETWEEN 1 AND 10),
  error_code text NOT NULL DEFAULT '' CHECK(length(error_code)<=128),
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  lease_owner text NOT NULL DEFAULT '' CHECK(length(lease_owner)<=128),
  lease_token uuid,
  lease_expires_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz,
  CONSTRAINT semantic_change_outbox_attempt_budget_check CHECK(attempt<=max_attempts),
  CONSTRAINT semantic_change_outbox_lease_shape_check CHECK(
    (status='RUNNING' AND attempt>0 AND lease_owner<>'' AND lease_token IS NOT NULL
      AND lease_expires_at IS NOT NULL AND completed_at IS NULL AND error_code='')
    OR
    (status<>'RUNNING' AND lease_owner='' AND lease_token IS NULL AND lease_expires_at IS NULL)
  ),
  CONSTRAINT semantic_change_outbox_completion_shape_check CHECK(
    (status IN ('SUCCEEDED','SKIPPED') AND completed_at IS NOT NULL AND error_code='')
    OR (status='FAILED' AND completed_at IS NOT NULL AND btrim(error_code)<>'')
    OR (status IN ('PENDING','RUNNING') AND completed_at IS NULL)
  ),
  CONSTRAINT semantic_change_outbox_subject_key
    UNIQUE(tenant_id,subject_type,subject_ref),
  CONSTRAINT semantic_change_outbox_identity_tenant_key UNIQUE(id,tenant_id)
);

-- 只物化较小的“维度—指标兼容关系”，不复制“维度成员 × 指标”。
CREATE TABLE platform.dimension_metric_compatibility(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  dimension_id uuid NOT NULL,
  metric_id uuid NOT NULL,
  metric_version_id uuid NOT NULL,
  metric_dataset_version_id uuid NOT NULL,
  compatibility_type text NOT NULL
    CHECK(compatibility_type IN ('DIRECT','BRIDGE','DERIVED')),
  fanout_policy text NOT NULL CHECK(fanout_policy IN ('SAFE','DEDUPLICATE','UNSAFE')),
  join_path_json jsonb NOT NULL DEFAULT '[]'::jsonb
    CHECK(jsonb_typeof(join_path_json)='array' AND pg_column_size(join_path_json)<=262144),
  evidence_source text NOT NULL CHECK(evidence_source IN ('RULE','PROFILE','LLM','HUMAN')),
  confidence numeric(5,4) CHECK(confidence IS NULL OR confidence BETWEEN 0 AND 1),
  status text NOT NULL DEFAULT 'PROPOSED'
    CHECK(status IN ('PROPOSED','VERIFIED','REJECTED')),
  verified_by uuid,
  verified_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT dimension_metric_compatibility_dimension_fk
    FOREIGN KEY(dimension_id,tenant_id)
    REFERENCES platform.semantic_dimensions(id,tenant_id) ON DELETE CASCADE,
  CONSTRAINT dimension_metric_compatibility_metric_version_fk
    FOREIGN KEY(metric_version_id,metric_id,metric_dataset_version_id,tenant_id)
    REFERENCES platform.metric_versions(id,metric_id,dataset_version_id,tenant_id) ON DELETE CASCADE,
  CONSTRAINT dimension_metric_compatibility_verified_by_fk
    FOREIGN KEY(verified_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dimension_metric_compatibility_verification_shape_check CHECK(
    (status='VERIFIED' AND verified_by IS NOT NULL AND verified_at IS NOT NULL
      AND fanout_policy<>'UNSAFE')
    OR (status<>'VERIFIED' AND verified_by IS NULL AND verified_at IS NULL)
  ),
  CONSTRAINT dimension_metric_compatibility_identity_key
    UNIQUE(tenant_id,dimension_id,metric_version_id),
  CONSTRAINT dimension_metric_compatibility_identity_tenant_key UNIQUE(id,tenant_id)
);

CREATE UNIQUE INDEX dataset_materializations_one_active_dataset_idx
  ON platform.dataset_materializations(tenant_id,dataset_id) WHERE status='ACTIVE';
CREATE UNIQUE INDEX dataset_materializations_one_active_published_name_idx
  ON platform.dataset_materializations(published_schema,published_name) WHERE status='ACTIVE';
CREATE INDEX dataset_build_runs_claim_idx
  ON platform.dataset_build_runs(tenant_id,status,next_attempt_at,lease_expires_at,created_at)
  WHERE status IN ('QUEUED','RUNNING');
CREATE INDEX dataset_build_runs_dataset_time_idx
  ON platform.dataset_build_runs(tenant_id,dataset_id,created_at DESC);
CREATE INDEX dataset_materializations_version_status_idx
  ON platform.dataset_materializations(tenant_id,dataset_version_id,status,created_at DESC);
CREATE INDEX build_run_inputs_dataset_version_idx
  ON platform.build_run_inputs(tenant_id,input_dataset_version_id)
  WHERE input_dataset_version_id IS NOT NULL;
CREATE INDEX build_run_inputs_materialization_idx
  ON platform.build_run_inputs(tenant_id,input_materialization_id)
  WHERE input_materialization_id IS NOT NULL;
CREATE INDEX build_node_runs_run_status_idx
  ON platform.build_node_runs(tenant_id,build_run_id,status,node_id);
CREATE INDEX data_quality_results_run_gate_idx
  ON platform.data_quality_results(tenant_id,build_run_id,severity,status);
CREATE UNIQUE INDEX data_quality_results_rule_scope_idx
  ON platform.data_quality_results(
    tenant_id,build_run_id,rule_code,rule_version,scope,COALESCE(field_id,'')
  );

CREATE INDEX semantic_tags_taxonomy_idx
  ON platform.semantic_tags(tenant_id,category,status,parent_tag_id,code);
CREATE INDEX semantic_tag_aliases_lookup_idx
  ON platform.semantic_tag_aliases(tenant_id,lower(alias::text));
CREATE INDEX semantic_dimensions_dataset_idx
  ON platform.semantic_dimensions(tenant_id,dataset_version_id,status,code);
CREATE INDEX dimension_members_lookup_idx
  ON platform.dimension_members(tenant_id,dimension_id,normalized_value)
  WHERE status='ACTIVE';
CREATE INDEX dimension_members_label_trgm_idx
  ON platform.dimension_members USING gin(lower(canonical_label) gin_trgm_ops);
CREATE INDEX dimension_member_aliases_lookup_idx
  ON platform.dimension_member_aliases(tenant_id,dimension_id,normalized_alias);

CREATE UNIQUE INDEX asset_tag_bindings_dataset_version_key
  ON platform.asset_tag_bindings(tenant_id,tag_id,dataset_version_id)
  WHERE asset_type='DATASET_VERSION';
CREATE UNIQUE INDEX asset_tag_bindings_dataset_field_key
  ON platform.asset_tag_bindings(tenant_id,tag_id,dataset_version_id,dataset_field_id)
  WHERE asset_type='DATASET_FIELD';
CREATE UNIQUE INDEX asset_tag_bindings_dimension_key
  ON platform.asset_tag_bindings(tenant_id,tag_id,dimension_id)
  WHERE asset_type='DIMENSION';
CREATE UNIQUE INDEX asset_tag_bindings_member_key
  ON platform.asset_tag_bindings(tenant_id,tag_id,dimension_member_id)
  WHERE asset_type='DIMENSION_MEMBER';
CREATE UNIQUE INDEX asset_tag_bindings_metric_version_key
  ON platform.asset_tag_bindings(tenant_id,tag_id,metric_version_id)
  WHERE asset_type='METRIC_VERSION';
CREATE INDEX asset_tag_bindings_tag_status_idx
  ON platform.asset_tag_bindings(tenant_id,tag_id,status,asset_type);

CREATE UNIQUE INDEX semantic_documents_tag_key
  ON platform.semantic_documents(tenant_id,tag_id) WHERE subject_type='TAG';
CREATE UNIQUE INDEX semantic_documents_dataset_version_key
  ON platform.semantic_documents(tenant_id,dataset_version_id)
  WHERE subject_type='DATASET_VERSION';
CREATE UNIQUE INDEX semantic_documents_dataset_field_key
  ON platform.semantic_documents(tenant_id,dataset_version_id,dataset_field_id)
  WHERE subject_type='DATASET_FIELD';
CREATE UNIQUE INDEX semantic_documents_dimension_key
  ON platform.semantic_documents(tenant_id,dimension_id) WHERE subject_type='DIMENSION';
CREATE UNIQUE INDEX semantic_documents_member_key
  ON platform.semantic_documents(tenant_id,dimension_member_id)
  WHERE subject_type='DIMENSION_MEMBER';
CREATE UNIQUE INDEX semantic_documents_metric_version_key
  ON platform.semantic_documents(tenant_id,metric_version_id)
  WHERE subject_type='METRIC_VERSION';
CREATE INDEX semantic_documents_lexical_idx
  ON platform.semantic_documents USING gin(to_tsvector('simple',document));
CREATE INDEX semantic_documents_embedding_hnsw_idx
  ON platform.semantic_documents USING hnsw(embedding halfvec_cosine_ops)
  WITH (m=16,ef_construction=64)
  WHERE embedding_status='SUCCEEDED';
CREATE INDEX semantic_change_outbox_claim_idx
  ON platform.semantic_change_outbox(
    tenant_id,status,next_attempt_at,lease_expires_at,updated_at
  ) WHERE status IN ('PENDING','RUNNING','FAILED');
CREATE INDEX dimension_metric_compatibility_metric_idx
  ON platform.dimension_metric_compatibility(
    tenant_id,metric_version_id,status,fanout_policy
  );

CREATE OR REPLACE FUNCTION platform.enforce_dataset_build_run_source()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
BEGIN
  PERFORM 1
  FROM platform.dataset_versions
  WHERE id=NEW.dataset_version_id
    AND dataset_id=NEW.dataset_id
    AND tenant_id=NEW.tenant_id
    AND layer=NEW.layer
    AND status='PUBLISHED'
  FOR SHARE;
  IF NOT FOUND THEN
    RAISE EXCEPTION '构建运行只能固定同层级的已发布数据集版本'
      USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enforce_dataset_build_run_source() FROM PUBLIC;

CREATE TRIGGER dataset_build_runs_enforce_source
BEFORE INSERT ON platform.dataset_build_runs
FOR EACH ROW EXECUTE FUNCTION platform.enforce_dataset_build_run_source();

CREATE OR REPLACE FUNCTION platform.enforce_build_run_input_layer()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  target_layer text;
  source_matches boolean := true;
BEGIN
  SELECT layer INTO target_layer
  FROM platform.dataset_build_runs
  WHERE id=NEW.build_run_id AND tenant_id=NEW.tenant_id
  FOR SHARE;
  IF target_layer IS NULL
    OR (target_layer='ODS' AND NEW.input_layer<>'SOURCE')
    OR (target_layer='DWD' AND NEW.input_layer<>'ODS')
    OR (target_layer='DWS' AND NEW.input_layer<>'DWD') THEN
    RAISE EXCEPTION '构建输入层级不满足 ODS <- SOURCE、DWD <- ODS、DWS <- DWD'
      USING ERRCODE='23514';
  END IF;

  IF NEW.source_type='DATASET_VERSION' THEN
    SELECT EXISTS(
      SELECT 1
      FROM platform.dataset_versions AS version
      JOIN platform.datasets AS owner
        ON owner.id=version.dataset_id AND owner.tenant_id=version.tenant_id
      WHERE version.id=NEW.input_dataset_version_id
        AND version.dataset_id=NEW.input_dataset_id
        AND version.tenant_id=NEW.tenant_id
        AND version.layer=NEW.input_layer
        AND version.status='PUBLISHED'
        AND owner.status='PUBLISHED'
        AND owner.deleted_at IS NULL
    ) INTO source_matches;
  ELSIF NEW.source_type='MATERIALIZATION' THEN
    SELECT EXISTS(
      SELECT 1
      FROM platform.dataset_materializations AS materialization
      JOIN platform.dataset_versions AS version
        ON version.id=materialization.dataset_version_id
       AND version.dataset_id=materialization.dataset_id
       AND version.tenant_id=materialization.tenant_id
      JOIN platform.datasets AS owner
        ON owner.id=materialization.dataset_id
       AND owner.tenant_id=materialization.tenant_id
      WHERE materialization.id=NEW.input_materialization_id
        AND materialization.tenant_id=NEW.tenant_id
        AND materialization.layer=NEW.input_layer
        AND materialization.status='ACTIVE'
        AND version.layer=NEW.input_layer
        AND version.status='PUBLISHED'
        AND owner.status='PUBLISHED'
        AND owner.deleted_at IS NULL
    ) INTO source_matches;
  END IF;

  IF NOT source_matches THEN
    RAISE EXCEPTION '构建输入声明的层级或可用状态与精确上游对象不一致'
      USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enforce_build_run_input_layer() FROM PUBLIC;

CREATE TRIGGER build_run_inputs_enforce_layer
BEFORE INSERT OR UPDATE OF
  tenant_id,build_run_id,source_type,input_layer,input_dataset_id,
  input_dataset_version_id,input_materialization_id
ON platform.build_run_inputs
FOR EACH ROW EXECUTE FUNCTION platform.enforce_build_run_input_layer();

CREATE OR REPLACE FUNCTION platform.enforce_materialization_build_identity()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
BEGIN
  PERFORM 1
  FROM platform.dataset_build_runs
  WHERE id=NEW.build_run_id
    AND dataset_id=NEW.dataset_id
    AND dataset_version_id=NEW.dataset_version_id
    AND tenant_id=NEW.tenant_id
    AND layer=NEW.layer
    AND run_mode=NEW.refresh_mode
    AND status='RUNNING'
  FOR SHARE;
  IF NOT FOUND THEN
    RAISE EXCEPTION '物化定位与运行身份、层级或刷新方式不一致'
      USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enforce_materialization_build_identity() FROM PUBLIC;

CREATE TRIGGER dataset_materializations_enforce_build_identity
BEFORE INSERT ON platform.dataset_materializations
FOR EACH ROW EXECUTE FUNCTION platform.enforce_materialization_build_identity();

CREATE OR REPLACE FUNCTION platform.enforce_dataset_build_run_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF TG_OP='DELETE' THEN
    RAISE EXCEPTION '数据集构建运行不可删除' USING ERRCODE='23514';
  END IF;
  IF NEW.id IS DISTINCT FROM OLD.id
    OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.dataset_id IS DISTINCT FROM OLD.dataset_id
    OR NEW.dataset_version_id IS DISTINCT FROM OLD.dataset_version_id
    OR NEW.layer IS DISTINCT FROM OLD.layer
    OR NEW.run_mode IS DISTINCT FROM OLD.run_mode
    OR NEW.plan_version IS DISTINCT FROM OLD.plan_version
    OR NEW.plan_json IS DISTINCT FROM OLD.plan_json
    OR NEW.plan_hash IS DISTINCT FROM OLD.plan_hash
    OR NEW.input_snapshot_hash IS DISTINCT FROM OLD.input_snapshot_hash
    OR NEW.request_hash IS DISTINCT FROM OLD.request_hash
    OR NEW.idempotency_key IS DISTINCT FROM OLD.idempotency_key
    OR NEW.partition_key IS DISTINCT FROM OLD.partition_key
    OR NEW.requested_by IS DISTINCT FROM OLD.requested_by
    OR NEW.max_attempts IS DISTINCT FROM OLD.max_attempts
    OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION '数据集构建运行身份、输入和计划不可修改' USING ERRCODE='23514';
  END IF;
  IF OLD.status IN ('SUCCEEDED','FAILED','CANCELLED') THEN
    RAISE EXCEPTION '数据集构建终态不可修改' USING ERRCODE='23514';
  END IF;
  IF NOT (
    (OLD.status='QUEUED' AND NEW.status IN ('RUNNING','CANCELLED'))
    OR (OLD.status='RUNNING' AND NEW.status IN ('RUNNING','SUCCEEDED','FAILED','CANCELLED'))
  ) THEN
    RAISE EXCEPTION '非法的数据集构建状态转换：% -> %',OLD.status,NEW.status
      USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enforce_dataset_build_run_transition() FROM PUBLIC;

CREATE TRIGGER dataset_build_runs_enforce_transition
BEFORE UPDATE OR DELETE ON platform.dataset_build_runs
FOR EACH ROW EXECUTE FUNCTION platform.enforce_dataset_build_run_transition();

CREATE OR REPLACE FUNCTION platform.reject_data_quality_result_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  RAISE EXCEPTION '数据质量结果不可修改或删除' USING ERRCODE='23514';
END
$$;

REVOKE ALL ON FUNCTION platform.reject_data_quality_result_mutation() FROM PUBLIC;

CREATE TRIGGER data_quality_results_immutable
BEFORE UPDATE OR DELETE ON platform.data_quality_results
FOR EACH ROW EXECUTE FUNCTION platform.reject_data_quality_result_mutation();

CREATE OR REPLACE FUNCTION platform.enforce_materialization_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF TG_OP='DELETE' THEN
    RAISE EXCEPTION '物化记录不可删除' USING ERRCODE='23514';
  END IF;
  IF NEW.id IS DISTINCT FROM OLD.id
    OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.dataset_id IS DISTINCT FROM OLD.dataset_id
    OR NEW.dataset_version_id IS DISTINCT FROM OLD.dataset_version_id
    OR NEW.build_run_id IS DISTINCT FROM OLD.build_run_id
    OR NEW.layer IS DISTINCT FROM OLD.layer
    OR NEW.relation_kind IS DISTINCT FROM OLD.relation_kind
    OR NEW.refresh_mode IS DISTINCT FROM OLD.refresh_mode
    OR NEW.physical_schema IS DISTINCT FROM OLD.physical_schema
    OR NEW.physical_name IS DISTINCT FROM OLD.physical_name
    OR NEW.published_schema IS DISTINCT FROM OLD.published_schema
    OR NEW.published_name IS DISTINCT FROM OLD.published_name
    OR NEW.schema_hash IS DISTINCT FROM OLD.schema_hash
    OR NEW.snapshot_hash IS DISTINCT FROM OLD.snapshot_hash
    OR NEW.row_count IS DISTINCT FROM OLD.row_count
    OR NEW.size_bytes IS DISTINCT FROM OLD.size_bytes
    OR NEW.watermark_json IS DISTINCT FROM OLD.watermark_json
    OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION '物化身份、位置和快照不可修改' USING ERRCODE='23514';
  END IF;
  IF NOT (
    (OLD.status='BUILDING' AND NEW.status IN ('ACTIVE','FAILED'))
    OR (OLD.status='ACTIVE' AND NEW.status='RETIRED')
  ) THEN
    RAISE EXCEPTION '非法的物化状态转换：% -> %',OLD.status,NEW.status
      USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enforce_materialization_transition() FROM PUBLIC;

CREATE TRIGGER dataset_materializations_enforce_transition
BEFORE UPDATE OR DELETE ON platform.dataset_materializations
FOR EACH ROW EXECUTE FUNCTION platform.enforce_materialization_transition();

CREATE OR REPLACE FUNCTION platform.enforce_semantic_dimension_dws()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
BEGIN
  PERFORM 1
  FROM platform.dataset_versions
  WHERE id=NEW.dataset_version_id
    AND dataset_id=NEW.dataset_id
    AND tenant_id=NEW.tenant_id
    AND layer='DWS'
    AND status='PUBLISHED'
  FOR SHARE;
  IF NOT FOUND THEN
    RAISE EXCEPTION '语义维度只能绑定已发布的 DWS 数据集版本'
      USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enforce_semantic_dimension_dws() FROM PUBLIC;

CREATE TRIGGER semantic_dimensions_require_published_dws
BEFORE INSERT OR UPDATE OF dataset_id,dataset_version_id
ON platform.semantic_dimensions
FOR EACH ROW EXECUTE FUNCTION platform.enforce_semantic_dimension_dws();

CREATE OR REPLACE FUNCTION platform.enforce_dimension_member_alias_owner()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
BEGIN
  PERFORM 1 FROM platform.dimension_members
  WHERE id=NEW.dimension_member_id
    AND dimension_id=NEW.dimension_id
    AND tenant_id=NEW.tenant_id;
  IF NOT FOUND THEN
    RAISE EXCEPTION '维度成员别名与维度不匹配' USING ERRCODE='23503';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enforce_dimension_member_alias_owner() FROM PUBLIC;

CREATE TRIGGER dimension_member_aliases_enforce_owner
BEFORE INSERT OR UPDATE OF dimension_id,dimension_member_id
ON platform.dimension_member_aliases
FOR EACH ROW EXECUTE FUNCTION platform.enforce_dimension_member_alias_owner();

CREATE OR REPLACE FUNCTION platform.semantic_binding_ref(binding platform.asset_tag_bindings)
RETURNS text
LANGUAGE sql
IMMUTABLE
AS $$
  SELECT CASE binding.asset_type
    WHEN 'DATASET_VERSION' THEN binding.dataset_version_id::text
    WHEN 'DATASET_FIELD' THEN binding.dataset_version_id::text||':'||binding.dataset_field_id
    WHEN 'DIMENSION' THEN binding.dimension_id::text
    WHEN 'DIMENSION_MEMBER' THEN binding.dimension_member_id::text
    WHEN 'METRIC_VERSION' THEN binding.metric_version_id::text
  END
$$;

CREATE OR REPLACE FUNCTION platform.enqueue_semantic_change(
  changed_tenant_id uuid,
  changed_subject_type text,
  changed_subject_ref text,
  changed_event_kind text DEFAULT 'REBUILD'
)
RETURNS void
LANGUAGE sql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
  INSERT INTO platform.semantic_change_outbox(
    tenant_id,subject_type,subject_ref,event_kind
  ) VALUES(
    changed_tenant_id,changed_subject_type,changed_subject_ref,changed_event_kind
  )
  ON CONFLICT(tenant_id,subject_type,subject_ref) DO UPDATE SET
    event_kind=EXCLUDED.event_kind,
    event_version=platform.semantic_change_outbox.event_version+1,
    status='PENDING',attempt=0,error_code='',next_attempt_at=now(),
    lease_owner='',lease_token=NULL,lease_expires_at=NULL,
    completed_at=NULL,updated_at=now()
$$;

REVOKE ALL ON FUNCTION platform.enqueue_semantic_change(uuid,text,text,text) FROM PUBLIC;

CREATE OR REPLACE FUNCTION platform.enqueue_semantic_tag_change()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  changed_tag_id uuid;
  changed_tenant_id uuid;
  binding platform.asset_tag_bindings%ROWTYPE;
  changed_row jsonb;
BEGIN
  changed_row := CASE WHEN TG_OP='DELETE' THEN to_jsonb(OLD) ELSE to_jsonb(NEW) END;
  changed_tag_id := CASE
    WHEN TG_TABLE_NAME='semantic_tags' THEN (changed_row->>'id')::uuid
    ELSE (changed_row->>'tag_id')::uuid
  END;
  changed_tenant_id := (changed_row->>'tenant_id')::uuid;
  PERFORM platform.enqueue_semantic_change(
    changed_tenant_id,'TAG',changed_tag_id::text,
    CASE WHEN TG_OP='DELETE' AND TG_TABLE_NAME='semantic_tags' THEN 'DELETE' ELSE 'REBUILD' END
  );
  FOR binding IN
    SELECT * FROM platform.asset_tag_bindings
    WHERE tenant_id=changed_tenant_id AND tag_id=changed_tag_id AND status='APPROVED'
  LOOP
    PERFORM platform.enqueue_semantic_change(
      changed_tenant_id,binding.asset_type,platform.semantic_binding_ref(binding),'REBUILD'
    );
  END LOOP;
  IF TG_OP='DELETE' THEN
    RETURN OLD;
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enqueue_semantic_tag_change() FROM PUBLIC;

CREATE TRIGGER semantic_tags_enqueue_change
AFTER INSERT OR UPDATE OR DELETE ON platform.semantic_tags
FOR EACH ROW EXECUTE FUNCTION platform.enqueue_semantic_tag_change();
CREATE TRIGGER semantic_tag_aliases_enqueue_change
AFTER INSERT OR UPDATE OR DELETE ON platform.semantic_tag_aliases
FOR EACH ROW EXECUTE FUNCTION platform.enqueue_semantic_tag_change();

CREATE OR REPLACE FUNCTION platform.enqueue_asset_tag_binding_change()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  changed platform.asset_tag_bindings%ROWTYPE;
BEGIN
  IF TG_OP='DELETE' THEN
    changed := OLD;
  ELSE
    changed := NEW;
  END IF;
  PERFORM platform.enqueue_semantic_change(
    changed.tenant_id,changed.asset_type,platform.semantic_binding_ref(changed),'REBUILD'
  );
  IF TG_OP='DELETE' THEN
    RETURN OLD;
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enqueue_asset_tag_binding_change() FROM PUBLIC;

CREATE TRIGGER asset_tag_bindings_enqueue_change
AFTER INSERT OR UPDATE OR DELETE ON platform.asset_tag_bindings
FOR EACH ROW EXECUTE FUNCTION platform.enqueue_asset_tag_binding_change();

CREATE OR REPLACE FUNCTION platform.enqueue_dimension_change()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  changed_tenant_id uuid;
  changed_subject_type text;
  changed_subject_id uuid;
  changed_row jsonb;
BEGIN
  changed_row := CASE WHEN TG_OP='DELETE' THEN to_jsonb(OLD) ELSE to_jsonb(NEW) END;
  changed_tenant_id := (changed_row->>'tenant_id')::uuid;
  IF TG_TABLE_NAME='semantic_dimensions' THEN
    changed_subject_type := 'DIMENSION';
    changed_subject_id := (changed_row->>'id')::uuid;
  ELSIF TG_TABLE_NAME='dimension_members' THEN
    changed_subject_type := 'DIMENSION_MEMBER';
    changed_subject_id := (changed_row->>'id')::uuid;
  ELSE
    changed_subject_type := 'DIMENSION_MEMBER';
    changed_subject_id := (changed_row->>'dimension_member_id')::uuid;
  END IF;
  PERFORM platform.enqueue_semantic_change(
    changed_tenant_id,changed_subject_type,changed_subject_id::text,
    CASE WHEN TG_OP='DELETE' AND TG_TABLE_NAME<>'dimension_member_aliases'
      THEN 'DELETE' ELSE 'REBUILD' END
  );
  IF TG_OP='DELETE' THEN
    RETURN OLD;
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enqueue_dimension_change() FROM PUBLIC;

CREATE TRIGGER semantic_dimensions_enqueue_change
AFTER INSERT OR UPDATE OR DELETE ON platform.semantic_dimensions
FOR EACH ROW EXECUTE FUNCTION platform.enqueue_dimension_change();
CREATE TRIGGER dimension_members_enqueue_change
AFTER INSERT OR UPDATE OR DELETE ON platform.dimension_members
FOR EACH ROW EXECUTE FUNCTION platform.enqueue_dimension_change();
CREATE TRIGGER dimension_member_aliases_enqueue_change
AFTER INSERT OR UPDATE OR DELETE ON platform.dimension_member_aliases
FOR EACH ROW EXECUTE FUNCTION platform.enqueue_dimension_change();

CREATE OR REPLACE FUNCTION platform.semantic_document_ref(document platform.semantic_documents)
RETURNS text
LANGUAGE sql
IMMUTABLE
AS $$
  SELECT CASE document.subject_type
    WHEN 'TAG' THEN document.tag_id::text
    WHEN 'DATASET_VERSION' THEN document.dataset_version_id::text
    WHEN 'DATASET_FIELD' THEN document.dataset_version_id::text||':'||document.dataset_field_id
    WHEN 'DIMENSION' THEN document.dimension_id::text
    WHEN 'DIMENSION_MEMBER' THEN document.dimension_member_id::text
    WHEN 'METRIC_VERSION' THEN document.metric_version_id::text
  END
$$;

CREATE OR REPLACE FUNCTION platform.enqueue_semantic_document_change()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
BEGIN
  PERFORM platform.enqueue_semantic_change(
    NEW.tenant_id,'SEMANTIC_DOCUMENT',NEW.id::text,'REBUILD'
  );
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enqueue_semantic_document_change() FROM PUBLIC;

CREATE TRIGGER semantic_documents_enqueue_embedding
AFTER INSERT OR UPDATE OF document_version,document,input_hash,semantic_source
ON platform.semantic_documents
FOR EACH ROW EXECUTE FUNCTION platform.enqueue_semantic_document_change();

-- 数据集和指标的业务语义仍由既有主表/版本表维护。以下触发器只写 coalescing
-- outbox，不在写事务中生成文档或向量；它们位于全部历史回填之后，迁移本身不会
-- 为存量对象制造事件风暴。
CREATE OR REPLACE FUNCTION platform.enqueue_dataset_version_change()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  changed_row jsonb;
BEGIN
  changed_row := CASE WHEN TG_OP='DELETE' THEN to_jsonb(OLD) ELSE to_jsonb(NEW) END;
  PERFORM platform.enqueue_semantic_change(
    (changed_row->>'tenant_id')::uuid,
    'DATASET_VERSION',
    changed_row->>'id',
    CASE WHEN TG_OP='DELETE' THEN 'DELETE' ELSE 'REBUILD' END
  );
  IF TG_OP='DELETE' THEN
    RETURN OLD;
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enqueue_dataset_version_change() FROM PUBLIC;

CREATE TRIGGER dataset_versions_enqueue_semantic_change
AFTER INSERT OR DELETE OR UPDATE OF
  status,layer,dsl_json,schema_hash,logical_plan_json,plan_hash
ON platform.dataset_versions
FOR EACH ROW EXECUTE FUNCTION platform.enqueue_dataset_version_change();

CREATE OR REPLACE FUNCTION platform.enqueue_dataset_field_change()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  changed_row jsonb;
  previous_row jsonb;
  changed_ref text;
  previous_ref text;
BEGIN
  changed_row := CASE WHEN TG_OP='DELETE' THEN to_jsonb(OLD) ELSE to_jsonb(NEW) END;
  previous_row := CASE WHEN TG_OP='INSERT' THEN '{}'::jsonb ELSE to_jsonb(OLD) END;
  changed_ref := (changed_row->>'dataset_version_id')||':'||(changed_row->>'field_id');
  previous_ref := NULLIF(
    COALESCE(previous_row->>'dataset_version_id','')||':'||
    COALESCE(previous_row->>'field_id',''),
    ':'
  );

  IF TG_OP='UPDATE' AND previous_ref IS DISTINCT FROM changed_ref THEN
    PERFORM platform.enqueue_semantic_change(
      (previous_row->>'tenant_id')::uuid,'DATASET_FIELD',previous_ref,'DELETE'
    );
  END IF;
  PERFORM platform.enqueue_semantic_change(
    (changed_row->>'tenant_id')::uuid,
    'DATASET_FIELD',
    changed_ref,
    CASE WHEN TG_OP='DELETE' THEN 'DELETE' ELSE 'REBUILD' END
  );
  -- 字段名称、类型、角色、说明或表达式同时影响父数据集的检索文档。
  -- 版本级 DELETE 会级联删除字段；此时父版本已不可见，不能让字段触发器把
  -- 已写入的版本 DELETE 事件重新覆盖成 REBUILD。
  IF EXISTS(
    SELECT 1
    FROM platform.dataset_versions
    WHERE id=(changed_row->>'dataset_version_id')::uuid
      AND tenant_id=(changed_row->>'tenant_id')::uuid
  ) THEN
    PERFORM platform.enqueue_semantic_change(
      (changed_row->>'tenant_id')::uuid,
      'DATASET_VERSION',
      changed_row->>'dataset_version_id',
      'REBUILD'
    );
  END IF;
  IF TG_OP='UPDATE'
    AND previous_row->>'dataset_version_id'
      IS DISTINCT FROM changed_row->>'dataset_version_id' THEN
    IF EXISTS(
      SELECT 1
      FROM platform.dataset_versions
      WHERE id=(previous_row->>'dataset_version_id')::uuid
        AND tenant_id=(previous_row->>'tenant_id')::uuid
    ) THEN
      PERFORM platform.enqueue_semantic_change(
        (previous_row->>'tenant_id')::uuid,
        'DATASET_VERSION',
        previous_row->>'dataset_version_id',
        'REBUILD'
      );
    END IF;
  END IF;
  IF TG_OP='DELETE' THEN
    RETURN OLD;
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enqueue_dataset_field_change() FROM PUBLIC;

CREATE TRIGGER dataset_fields_enqueue_semantic_change
AFTER INSERT OR UPDATE OR DELETE ON platform.dataset_fields
FOR EACH ROW EXECUTE FUNCTION platform.enqueue_dataset_field_change();

CREATE OR REPLACE FUNCTION platform.enqueue_dataset_owner_change()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  changed_row jsonb;
  previous_row jsonb;
  subject_id uuid;
BEGIN
  changed_row := CASE WHEN TG_OP='DELETE' THEN to_jsonb(OLD) ELSE to_jsonb(NEW) END;
  previous_row := CASE WHEN TG_OP='INSERT' THEN '{}'::jsonb ELSE to_jsonb(OLD) END;
  FOR subject_id IN
    SELECT DISTINCT candidate
    FROM (VALUES
      (NULLIF(changed_row->>'current_draft_version_id','')::uuid),
      (NULLIF(changed_row->>'current_published_version_id','')::uuid),
      (NULLIF(previous_row->>'current_draft_version_id','')::uuid),
      (NULLIF(previous_row->>'current_published_version_id','')::uuid)
    ) AS versions(candidate)
    WHERE candidate IS NOT NULL
  LOOP
    PERFORM platform.enqueue_semantic_change(
      (changed_row->>'tenant_id')::uuid,
      'DATASET_VERSION',
      subject_id::text,
      'REBUILD'
    );
  END LOOP;
  IF TG_OP='DELETE' THEN
    RETURN OLD;
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enqueue_dataset_owner_change() FROM PUBLIC;

CREATE TRIGGER datasets_enqueue_semantic_change
AFTER INSERT OR DELETE OR UPDATE OF
  code,name,description,status,layer,current_draft_version_id,
  current_published_version_id,deleted_at
ON platform.datasets
FOR EACH ROW EXECUTE FUNCTION platform.enqueue_dataset_owner_change();

CREATE OR REPLACE FUNCTION platform.enqueue_metric_version_change()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  changed_row jsonb;
BEGIN
  changed_row := CASE WHEN TG_OP='DELETE' THEN to_jsonb(OLD) ELSE to_jsonb(NEW) END;
  PERFORM platform.enqueue_semantic_change(
    (changed_row->>'tenant_id')::uuid,
    'METRIC_VERSION',
    changed_row->>'id',
    CASE WHEN TG_OP='DELETE' THEN 'DELETE' ELSE 'REBUILD' END
  );
  IF TG_OP='DELETE' THEN
    RETURN OLD;
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enqueue_metric_version_change() FROM PUBLIC;

CREATE TRIGGER metric_versions_enqueue_semantic_change
AFTER INSERT OR DELETE OR UPDATE OF
  status,dataset_version_id,definition_json,definition_hash
ON platform.metric_versions
FOR EACH ROW EXECUTE FUNCTION platform.enqueue_metric_version_change();

CREATE OR REPLACE FUNCTION platform.enqueue_metric_dimension_change()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  changed_row jsonb;
  previous_row jsonb;
BEGIN
  changed_row := CASE WHEN TG_OP='DELETE' THEN to_jsonb(OLD) ELSE to_jsonb(NEW) END;
  previous_row := CASE WHEN TG_OP='INSERT' THEN '{}'::jsonb ELSE to_jsonb(OLD) END;
  PERFORM platform.enqueue_semantic_change(
    (changed_row->>'tenant_id')::uuid,
    'METRIC_VERSION',
    changed_row->>'metric_version_id',
    'REBUILD'
  );
  IF TG_OP='UPDATE'
    AND previous_row->>'metric_version_id'
      IS DISTINCT FROM changed_row->>'metric_version_id' THEN
    PERFORM platform.enqueue_semantic_change(
      (previous_row->>'tenant_id')::uuid,
      'METRIC_VERSION',
      previous_row->>'metric_version_id',
      'REBUILD'
    );
  END IF;
  IF TG_OP='DELETE' THEN
    RETURN OLD;
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enqueue_metric_dimension_change() FROM PUBLIC;

CREATE TRIGGER metric_dimensions_enqueue_semantic_change
AFTER INSERT OR UPDATE OR DELETE ON platform.metric_dimensions
FOR EACH ROW EXECUTE FUNCTION platform.enqueue_metric_dimension_change();

CREATE OR REPLACE FUNCTION platform.enqueue_metric_owner_change()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  changed_row jsonb;
  previous_row jsonb;
  subject_id uuid;
BEGIN
  changed_row := CASE WHEN TG_OP='DELETE' THEN to_jsonb(OLD) ELSE to_jsonb(NEW) END;
  previous_row := CASE WHEN TG_OP='INSERT' THEN '{}'::jsonb ELSE to_jsonb(OLD) END;
  FOR subject_id IN
    SELECT DISTINCT candidate
    FROM (VALUES
      (NULLIF(changed_row->>'current_draft_version_id','')::uuid),
      (NULLIF(changed_row->>'current_published_version_id','')::uuid),
      (NULLIF(previous_row->>'current_draft_version_id','')::uuid),
      (NULLIF(previous_row->>'current_published_version_id','')::uuid)
    ) AS versions(candidate)
    WHERE candidate IS NOT NULL
  LOOP
    PERFORM platform.enqueue_semantic_change(
      (changed_row->>'tenant_id')::uuid,
      'METRIC_VERSION',
      subject_id::text,
      'REBUILD'
    );
  END LOOP;
  IF TG_OP='DELETE' THEN
    RETURN OLD;
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enqueue_metric_owner_change() FROM PUBLIC;

CREATE TRIGGER metrics_enqueue_semantic_change
AFTER INSERT OR DELETE OR UPDATE OF
  code,name,description,metric_type,status,current_draft_version_id,
  current_published_version_id,deleted_at
ON platform.metrics
FOR EACH ROW EXECUTE FUNCTION platform.enqueue_metric_owner_change();

CREATE TRIGGER semantic_tags_set_updated_at
BEFORE UPDATE ON platform.semantic_tags
FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();
CREATE TRIGGER semantic_dimensions_set_updated_at
BEFORE UPDATE ON platform.semantic_dimensions
FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();
CREATE TRIGGER dimension_members_set_updated_at
BEFORE UPDATE ON platform.dimension_members
FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();
CREATE TRIGGER asset_tag_bindings_set_updated_at
BEFORE UPDATE ON platform.asset_tag_bindings
FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();
CREATE TRIGGER semantic_documents_set_updated_at
BEFORE UPDATE ON platform.semantic_documents
FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();
CREATE TRIGGER dimension_metric_compatibility_set_updated_at
BEFORE UPDATE ON platform.dimension_metric_compatibility
FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();

ALTER TABLE platform.dataset_build_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.dataset_build_runs FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.dataset_materializations ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.dataset_materializations FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.build_run_inputs ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.build_run_inputs FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.build_node_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.build_node_runs FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.data_quality_results ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.data_quality_results FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_tags ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_tags FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_tag_aliases ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_tag_aliases FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_dimensions ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_dimensions FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.dimension_members ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.dimension_members FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.dimension_member_aliases ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.dimension_member_aliases FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.asset_tag_bindings ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.asset_tag_bindings FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_documents ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_documents FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_change_outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_change_outbox FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.dimension_metric_compatibility ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.dimension_metric_compatibility FORCE ROW LEVEL SECURITY;

CREATE POLICY dataset_build_runs_tenant_isolation ON platform.dataset_build_runs
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY dataset_materializations_tenant_isolation ON platform.dataset_materializations
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY build_run_inputs_tenant_isolation ON platform.build_run_inputs
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY build_node_runs_tenant_isolation ON platform.build_node_runs
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY data_quality_results_tenant_isolation ON platform.data_quality_results
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY semantic_tags_tenant_isolation ON platform.semantic_tags
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY semantic_tag_aliases_tenant_isolation ON platform.semantic_tag_aliases
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY semantic_dimensions_tenant_isolation ON platform.semantic_dimensions
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY dimension_members_tenant_isolation ON platform.dimension_members
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY dimension_member_aliases_tenant_isolation ON platform.dimension_member_aliases
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY asset_tag_bindings_tenant_isolation ON platform.asset_tag_bindings
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY semantic_documents_tenant_isolation ON platform.semantic_documents
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY semantic_change_outbox_tenant_isolation ON platform.semantic_change_outbox
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY dimension_metric_compatibility_tenant_isolation
  ON platform.dimension_metric_compatibility
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

COMMENT ON TABLE platform.dataset_build_runs IS
  '固定精确数据集版本、受限执行计划、输入摘要和 worker 租约的构建运行';
COMMENT ON TABLE platform.build_run_inputs IS
  '构建开始前冻结的上游对象版本、水位、结构和内容摘要';
COMMENT ON TABLE platform.dataset_materializations IS
  'warehouse 数据面的物理关系定位和原子激活历史，不包含业务数据行';
COMMENT ON TABLE platform.data_quality_results IS
  '不可变的数据质量规则观测及发布门结果';
COMMENT ON TABLE platform.semantic_tags IS
  '租户可治理的动态标签 taxonomy；标签和值对象与资产绑定解耦';
COMMENT ON TABLE platform.semantic_documents IS
  '标签、数据集、字段、维度、成员和指标的确定性检索文档及向量';
COMMENT ON TABLE platform.semantic_change_outbox IS
  '语义或标签事务变更触发的可恢复文档重建和向量化 outbox';
COMMENT ON TABLE platform.dimension_metric_compatibility IS
  '维度到指标版本的小规模兼容图；不会按维度成员复制指标';
