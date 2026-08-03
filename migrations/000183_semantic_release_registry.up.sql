-- 语义发布包把分散的权威对象固定到同一 semantic_version/content_hash。
-- PostgreSQL 是发布状态和版本清单的事务事实源；执行语义层、检索和
-- NebulaGraph 都是必须与该 hash 对齐的派生投影。
CREATE TABLE platform.semantic_releases(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  semantic_version text NOT NULL CHECK(
    length(semantic_version) BETWEEN 3 AND 128
    AND semantic_version=btrim(semantic_version)
    AND semantic_version ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{2,127}$'
  ),
  content_hash text NOT NULL CHECK(content_hash ~ '^[0-9a-f]{64}$'),
  status text NOT NULL DEFAULT 'DRAFT' CHECK(status IN (
    'DRAFT','VALIDATING','PROJECTING','READY','ACTIVE','BLOCKED','SUPERSEDED'
  )),
  base_release_id uuid,
  notes text NOT NULL DEFAULT '' CHECK(
    length(notes)<=4096 AND notes !~ '[[:cntrl:]]'
  ),
  object_count integer NOT NULL CHECK(object_count BETWEEN 1 AND 10000),
  validation_summary jsonb NOT NULL DEFAULT '{}'::jsonb CHECK(
    jsonb_typeof(validation_summary)='object'
    AND pg_column_size(validation_summary)<=65536
    AND platform.materialization_json_is_safe(validation_summary)
  ),
  version bigint NOT NULL DEFAULT 1 CHECK(version>0),
  created_by uuid NOT NULL,
  updated_by uuid NOT NULL,
  activated_by uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  validated_at timestamptz,
  activated_at timestamptz,
  CONSTRAINT semantic_releases_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT semantic_releases_tenant_version_key
    UNIQUE(tenant_id,semantic_version),
  CONSTRAINT semantic_releases_created_by_fk
    FOREIGN KEY(created_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT semantic_releases_updated_by_fk
    FOREIGN KEY(updated_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT semantic_releases_activated_by_fk
    FOREIGN KEY(activated_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT semantic_releases_base_fk
    FOREIGN KEY(base_release_id,tenant_id)
    REFERENCES platform.semantic_releases(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT semantic_releases_activation_shape_check CHECK(
    (status='ACTIVE' AND activated_by IS NOT NULL AND activated_at IS NOT NULL)
    OR (status<>'ACTIVE')
  ),
  CONSTRAINT semantic_releases_validation_shape_check CHECK(
    (status='DRAFT' AND validated_at IS NULL)
    OR (status<>'DRAFT' AND validated_at IS NOT NULL)
  )
);

CREATE UNIQUE INDEX semantic_releases_one_active_idx
  ON platform.semantic_releases(tenant_id) WHERE status='ACTIVE';
CREATE INDEX semantic_releases_lifecycle_idx
  ON platform.semantic_releases(tenant_id,status,created_at DESC,id);

CREATE TABLE platform.semantic_release_objects(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  release_id uuid NOT NULL,
  object_type text NOT NULL CHECK(object_type IN (
    'DOMAIN','BUSINESS_TERM','ENTITY','SEMANTIC_MODEL','MEASURE','METRIC',
    'DIMENSION','DIMENSION_VALUE','TIME','COHORT','RELATION','DATASET',
    'TABLE_COLUMN','POLICY','QUALITY_RULE','CERTIFIED_EXAMPLE','PARSING_RULE'
  )),
  object_id text NOT NULL CHECK(
    length(object_id) BETWEEN 1 AND 256
    AND object_id=btrim(object_id)
    AND object_id !~ '[[:cntrl:]]'
  ),
  object_version text NOT NULL CHECK(
    length(object_version) BETWEEN 1 AND 128
    AND object_version=btrim(object_version)
    AND object_version !~ '[[:cntrl:]]'
  ),
  content_hash text NOT NULL CHECK(content_hash ~ '^[0-9a-f]{64}$'),
  domain_id text NOT NULL DEFAULT '' CHECK(length(domain_id)<=256),
  owner_id uuid NOT NULL,
  certification text NOT NULL CHECK(certification='CERTIFIED'),
  sensitivity text NOT NULL CHECK(sensitivity IN (
    'PUBLIC','INTERNAL','CONFIDENTIAL','RESTRICTED'
  )),
  valid_from timestamptz NOT NULL,
  valid_to timestamptz,
  contract_json jsonb NOT NULL CHECK(
    jsonb_typeof(contract_json)='object'
    AND pg_column_size(contract_json)<=65536
    AND platform.materialization_json_is_safe(contract_json)
  ),
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT semantic_release_objects_release_fk
    FOREIGN KEY(release_id,tenant_id)
    REFERENCES platform.semantic_releases(id,tenant_id) ON DELETE CASCADE,
  CONSTRAINT semantic_release_objects_owner_fk
    FOREIGN KEY(owner_id,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT semantic_release_objects_validity_check
    CHECK(valid_to IS NULL OR valid_to>valid_from),
  CONSTRAINT semantic_release_objects_identity_key
    UNIQUE(tenant_id,release_id,object_type,object_id,object_version),
  CONSTRAINT semantic_release_objects_identity_tenant_key UNIQUE(id,tenant_id)
);

CREATE INDEX semantic_release_objects_lookup_idx
  ON platform.semantic_release_objects(
    tenant_id,release_id,object_type,object_id,object_version
  );
CREATE INDEX semantic_release_objects_domain_idx
  ON platform.semantic_release_objects(
    tenant_id,release_id,domain_id,object_type
  );

CREATE TABLE platform.semantic_release_projections(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  release_id uuid NOT NULL,
  target text NOT NULL CHECK(target IN (
    'EXECUTION_SEMANTIC_LAYER','POSTGRES_REGISTRY','SEARCH_INDEX','NEBULA_GRAPH'
  )),
  status text NOT NULL DEFAULT 'PENDING' CHECK(status IN (
    'PENDING','RUNNING','READY','FAILED','STALE'
  )),
  expected_content_hash text NOT NULL CHECK(
    expected_content_hash ~ '^[0-9a-f]{64}$'
  ),
  applied_content_hash text NOT NULL DEFAULT '' CHECK(
    applied_content_hash='' OR applied_content_hash ~ '^[0-9a-f]{64}$'
  ),
  resource_version text NOT NULL DEFAULT '' CHECK(length(resource_version)<=256),
  object_count integer NOT NULL DEFAULT 0 CHECK(object_count>=0),
  error_code text NOT NULL DEFAULT '' CHECK(length(error_code)<=128),
  detail jsonb NOT NULL DEFAULT '{}'::jsonb CHECK(
    jsonb_typeof(detail)='object' AND pg_column_size(detail)<=65536
    AND platform.materialization_json_is_safe(detail)
  ),
  version bigint NOT NULL DEFAULT 1 CHECK(version>0),
  started_at timestamptz,
  completed_at timestamptz,
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT semantic_release_projections_release_fk
    FOREIGN KEY(release_id,tenant_id)
    REFERENCES platform.semantic_releases(id,tenant_id) ON DELETE CASCADE,
  CONSTRAINT semantic_release_projections_target_key
    UNIQUE(tenant_id,release_id,target),
  CONSTRAINT semantic_release_projections_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT semantic_release_projections_ready_shape_check CHECK(
    (status='READY' AND applied_content_hash=expected_content_hash
      AND resource_version<>'' AND completed_at IS NOT NULL AND error_code='')
    OR status<>'READY'
  )
);

CREATE INDEX semantic_release_projections_status_idx
  ON platform.semantic_release_projections(tenant_id,status,target,updated_at,id);

CREATE TABLE platform.semantic_release_state(
  tenant_id uuid PRIMARY KEY REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  active_release_id uuid,
  version bigint NOT NULL DEFAULT 1 CHECK(version>0),
  updated_by uuid,
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT semantic_release_state_active_fk
    FOREIGN KEY(active_release_id,tenant_id)
    REFERENCES platform.semantic_releases(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT semantic_release_state_updated_by_fk
    FOREIGN KEY(updated_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);

INSERT INTO platform.semantic_release_state(tenant_id)
SELECT id FROM platform.tenants ON CONFLICT(tenant_id) DO NOTHING;

CREATE OR REPLACE FUNCTION platform.initialize_semantic_release_state()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
BEGIN
  INSERT INTO platform.semantic_release_state(tenant_id)
  VALUES(NEW.id) ON CONFLICT(tenant_id) DO NOTHING;
  RETURN NEW;
END
$$;
REVOKE ALL ON FUNCTION platform.initialize_semantic_release_state() FROM PUBLIC;

CREATE TRIGGER tenants_initialize_semantic_release_state
AFTER INSERT ON platform.tenants
FOR EACH ROW EXECUTE FUNCTION platform.initialize_semantic_release_state();

CREATE TABLE platform.semantic_release_events(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  release_id uuid NOT NULL,
  event_type text NOT NULL CHECK(event_type IN (
    'CREATED','VALIDATED','VALIDATION_BLOCKED','PROJECTION_UPDATED',
    'READY','ACTIVATED','SUPERSEDED'
  )),
  actor_id uuid,
  detail jsonb NOT NULL DEFAULT '{}'::jsonb CHECK(
    jsonb_typeof(detail)='object' AND pg_column_size(detail)<=65536
    AND platform.materialization_json_is_safe(detail)
  ),
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT semantic_release_events_release_fk
    FOREIGN KEY(release_id,tenant_id)
    REFERENCES platform.semantic_releases(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT semantic_release_events_actor_fk
    FOREIGN KEY(actor_id,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);

CREATE INDEX semantic_release_events_lookup_idx
  ON platform.semantic_release_events(tenant_id,release_id,created_at,id);

CREATE OR REPLACE FUNCTION platform.reject_semantic_release_event_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  RAISE EXCEPTION '语义发布事件不可修改或删除';
END
$$;
REVOKE ALL ON FUNCTION platform.reject_semantic_release_event_mutation()
  FROM PUBLIC;
CREATE TRIGGER semantic_release_events_immutable
BEFORE UPDATE OR DELETE ON platform.semantic_release_events
FOR EACH ROW EXECUTE FUNCTION platform.reject_semantic_release_event_mutation();

CREATE TRIGGER semantic_releases_set_updated_at
BEFORE UPDATE ON platform.semantic_releases
FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();
CREATE TRIGGER semantic_release_projections_set_updated_at
BEFORE UPDATE ON platform.semantic_release_projections
FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();
CREATE TRIGGER semantic_release_state_set_updated_at
BEFORE UPDATE ON platform.semantic_release_state
FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();

ALTER TABLE platform.semantic_releases ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_releases FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_release_objects ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_release_objects FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_release_projections ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_release_projections FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_release_state ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_release_state FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_release_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_release_events FORCE ROW LEVEL SECURITY;

CREATE POLICY semantic_releases_tenant_isolation
  ON platform.semantic_releases
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY semantic_release_objects_tenant_isolation
  ON platform.semantic_release_objects
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY semantic_release_projections_tenant_isolation
  ON platform.semantic_release_projections
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY semantic_release_state_tenant_isolation
  ON platform.semantic_release_state
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY semantic_release_events_tenant_isolation
  ON platform.semantic_release_events
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

COMMENT ON TABLE platform.semantic_releases IS
  '可原子激活的统一语义发布包；固定同一 semantic_version 下的对象和投影 content hash';
COMMENT ON TABLE platform.semantic_release_objects IS
  '发布包内不可变语义对象清单；覆盖七层语义合同和运行时派生对象';
COMMENT ON TABLE platform.semantic_release_projections IS
  '执行语义层、PostgreSQL Registry、搜索索引和 NebulaGraph 的版本一致性门禁';
COMMENT ON TABLE platform.semantic_release_state IS
  '租户当前活动语义版本指针；激活时使用 version 做乐观锁';
COMMENT ON TABLE platform.semantic_release_events IS
  '语义发布生命周期的不可变审计事件';
