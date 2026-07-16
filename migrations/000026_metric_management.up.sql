-- 建立指标草稿、不可变发布版本、精确数据集依赖和查询审计合同。
-- 锁定租户表，确保迁移为全部既有租户补齐发布权限时不存在并发空窗。
LOCK TABLE platform.tenants IN SHARE ROW EXCLUSIVE MODE;

CREATE TABLE platform.metrics(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  dataset_id uuid NOT NULL,
  code citext NOT NULL,
  name text NOT NULL CHECK(btrim(name)<>''),
  description text NOT NULL DEFAULT '',
  metric_type text NOT NULL CHECK(metric_type IN ('ATOMIC','DERIVED','RATIO')),
  status text NOT NULL DEFAULT 'DRAFT' CHECK(status IN ('DRAFT','PUBLISHED','STALE','DEPRECATED')),
  current_draft_version_id uuid,
  current_published_version_id uuid,
  version bigint NOT NULL DEFAULT 1 CHECK(version>0),
  created_by uuid,
  updated_by uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  deleted_at timestamptz,
  CONSTRAINT metrics_dataset_fk
    FOREIGN KEY(dataset_id,tenant_id)
    REFERENCES platform.datasets(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT metrics_created_by_fk
    FOREIGN KEY(created_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE SET NULL (created_by),
  CONSTRAINT metrics_updated_by_fk
    FOREIGN KEY(updated_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE SET NULL (updated_by),
  CONSTRAINT metrics_tenant_code_key UNIQUE(tenant_id,code),
  CONSTRAINT metrics_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT metrics_identity_dataset_tenant_key UNIQUE(id,dataset_id,tenant_id)
);

CREATE TABLE platform.metric_versions(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  metric_id uuid NOT NULL,
  dataset_id uuid NOT NULL,
  dataset_version_id uuid NOT NULL,
  version_no integer NOT NULL CHECK(version_no>0),
  status text NOT NULL DEFAULT 'DRAFT'
    CHECK(status IN ('DRAFT','PUBLISHING','PUBLISHED','STALE','DEPRECATED')),
  definition_version text NOT NULL CHECK(definition_version='1.0'),
  definition_json jsonb NOT NULL CHECK(jsonb_typeof(definition_json)='object'),
  definition_hash text NOT NULL CHECK(definition_hash ~ '^[0-9a-f]{64}$'),
  record_version bigint NOT NULL DEFAULT 1 CHECK(record_version>0),
  created_by uuid,
  updated_by uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  published_at timestamptz,
  published_by uuid,
  source_draft_version_id uuid,
  source_draft_record_version bigint,
  CONSTRAINT metric_versions_metric_fk
    FOREIGN KEY(metric_id,dataset_id,tenant_id)
    REFERENCES platform.metrics(id,dataset_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT metric_versions_dataset_version_fk
    FOREIGN KEY(dataset_version_id,dataset_id,tenant_id)
    REFERENCES platform.dataset_versions(id,dataset_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT metric_versions_created_by_fk
    FOREIGN KEY(created_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE SET NULL (created_by),
  CONSTRAINT metric_versions_updated_by_fk
    FOREIGN KEY(updated_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE SET NULL (updated_by),
  CONSTRAINT metric_versions_published_by_fk
    FOREIGN KEY(published_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT metric_versions_publication_metadata_check CHECK(
    (status='DRAFT'
      AND published_at IS NULL
      AND published_by IS NULL
      AND source_draft_version_id IS NULL
      AND source_draft_record_version IS NULL)
    OR (status='PUBLISHING'
      AND published_at IS NOT NULL
      AND published_at>=created_at
      AND published_by IS NOT NULL
      AND source_draft_version_id IS NOT NULL
      AND source_draft_record_version>0)
    OR (status IN ('PUBLISHED','STALE','DEPRECATED')
      AND published_at IS NOT NULL
      AND published_at>=created_at
      AND published_by IS NOT NULL
      AND source_draft_version_id IS NOT NULL
      AND source_draft_record_version>0)
  ),
  CONSTRAINT metric_versions_source_draft_identity_check
    CHECK(source_draft_version_id IS NULL OR source_draft_version_id<>id),
  CONSTRAINT metric_versions_tenant_number_key UNIQUE(tenant_id,metric_id,version_no),
  CONSTRAINT metric_versions_identity_metric_tenant_key UNIQUE(id,metric_id,tenant_id),
  CONSTRAINT metric_versions_identity_dataset_version_tenant_key
    UNIQUE(id,dataset_version_id,tenant_id),
  CONSTRAINT metric_versions_identity_full_key
    UNIQUE(id,metric_id,dataset_version_id,tenant_id)
);

ALTER TABLE platform.metric_versions
  ADD CONSTRAINT metric_versions_source_draft_fk
  FOREIGN KEY(source_draft_version_id,metric_id,tenant_id)
  REFERENCES platform.metric_versions(id,metric_id,tenant_id) ON DELETE RESTRICT;

ALTER TABLE platform.metrics
  ADD CONSTRAINT metrics_current_draft_fk
    FOREIGN KEY(current_draft_version_id,id,tenant_id)
    REFERENCES platform.metric_versions(id,metric_id,tenant_id),
  ADD CONSTRAINT metrics_current_published_fk
    FOREIGN KEY(current_published_version_id,id,tenant_id)
    REFERENCES platform.metric_versions(id,metric_id,tenant_id);

-- 维度索引引用数据集版本中的逻辑 field_id，而不是可变字段名称或物理列名。
CREATE TABLE platform.metric_dimensions(
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  metric_version_id uuid NOT NULL,
  metric_id uuid NOT NULL,
  dataset_version_id uuid NOT NULL,
  field_id text NOT NULL,
  dimension_name text NOT NULL CHECK(btrim(dimension_name)<>''),
  hierarchy_field_ids text[] NOT NULL DEFAULT '{}',
  sort_direction text NOT NULL CHECK(sort_direction IN ('ASC','DESC')),
  null_label text NOT NULL DEFAULT '',
  ordinal_position integer NOT NULL CHECK(ordinal_position>0),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(tenant_id,metric_version_id,field_id),
  CONSTRAINT metric_dimensions_metric_version_fk
    FOREIGN KEY(metric_version_id,metric_id,dataset_version_id,tenant_id)
    REFERENCES platform.metric_versions(id,metric_id,dataset_version_id,tenant_id) ON DELETE CASCADE,
  CONSTRAINT metric_dimensions_dataset_field_fk
    FOREIGN KEY(tenant_id,dataset_version_id,field_id)
    REFERENCES platform.dataset_fields(tenant_id,dataset_version_id,field_id) ON DELETE RESTRICT,
  CONSTRAINT metric_dimensions_version_ordinal_key
    UNIQUE(tenant_id,metric_version_id,ordinal_position)
);

-- 指标引用始终固定精确的已发布指标版本，并由复合外键保证属于同一数据集版本。
CREATE TABLE platform.metric_dependencies(
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  metric_version_id uuid NOT NULL,
  metric_id uuid NOT NULL,
  dataset_version_id uuid NOT NULL,
  dependency_metric_version_id uuid NOT NULL,
  dependency_metric_id uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(tenant_id,metric_version_id,dependency_metric_version_id),
  CONSTRAINT metric_dependencies_owner_fk
    FOREIGN KEY(metric_version_id,metric_id,dataset_version_id,tenant_id)
    REFERENCES platform.metric_versions(id,metric_id,dataset_version_id,tenant_id) ON DELETE CASCADE,
  CONSTRAINT metric_dependencies_target_fk
    FOREIGN KEY(dependency_metric_version_id,dependency_metric_id,dataset_version_id,tenant_id)
    REFERENCES platform.metric_versions(id,metric_id,dataset_version_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT metric_dependencies_no_same_version_check
    CHECK(metric_version_id<>dependency_metric_version_id)
);

CREATE TABLE platform.metric_publish_idempotency(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  metric_id uuid NOT NULL,
  actor_user_id uuid NOT NULL,
  idempotency_key text NOT NULL CHECK(
    length(idempotency_key) BETWEEN 1 AND 128
    AND idempotency_key=btrim(idempotency_key)
    AND idempotency_key !~ '[[:cntrl:]]'
  ),
  request_hash text NOT NULL CHECK(request_hash ~ '^[0-9a-f]{64}$'),
  response_json jsonb NOT NULL CHECK(jsonb_typeof(response_json)='object'),
  published_version_id uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT metric_publish_idempotency_metric_fk
    FOREIGN KEY(metric_id,tenant_id)
    REFERENCES platform.metrics(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT metric_publish_idempotency_actor_fk
    FOREIGN KEY(actor_user_id,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT metric_publish_idempotency_version_fk
    FOREIGN KEY(published_version_id,metric_id,tenant_id)
    REFERENCES platform.metric_versions(id,metric_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT metric_publish_idempotency_key
    UNIQUE(tenant_id,metric_id,idempotency_key),
  UNIQUE(id,tenant_id)
);

-- 报告草稿新写入使用 METRIC_VERSION；METRIC 仅兼容迁移前的过渡索引。
ALTER TABLE platform.report_draft_dependencies
  DROP CONSTRAINT report_draft_dependencies_dependency_type_check,
  ADD CONSTRAINT report_draft_dependencies_dependency_type_check
    CHECK(dependency_type IN ('DATASET_VERSION','METRIC','METRIC_VERSION','SOURCE_TRACE'));

-- 查询审计可选地关联草稿或发布指标版本；两列必须同时为空或同时存在。
ALTER TABLE platform.query_runs
  DROP CONSTRAINT query_runs_actor_user_id_tenant_id_fkey,
  ADD CONSTRAINT query_runs_actor_tenant_fk
    FOREIGN KEY(actor_user_id,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  ADD COLUMN metric_id uuid,
  ADD COLUMN metric_version_id uuid,
  ADD CONSTRAINT query_runs_metric_shape_check CHECK(
    (metric_id IS NULL AND metric_version_id IS NULL)
    OR (metric_id IS NOT NULL AND metric_version_id IS NOT NULL)
  ),
  ADD CONSTRAINT query_runs_metric_version_metric_tenant_fk
    FOREIGN KEY(metric_version_id,metric_id,tenant_id)
    REFERENCES platform.metric_versions(id,metric_id,tenant_id) ON DELETE RESTRICT;

-- 写入查询审计时冻结当时的数据集版本关系；后续草稿升级数据集版本时不得改写历史审计。
CREATE OR REPLACE FUNCTION platform.enforce_query_run_metric_snapshot()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
BEGIN
  IF NEW.metric_version_id IS NULL THEN
    RETURN NEW;
  END IF;
  PERFORM 1
  FROM platform.metric_versions
  WHERE id=NEW.metric_version_id
    AND metric_id=NEW.metric_id
    AND dataset_version_id=NEW.dataset_version_id
    AND tenant_id=NEW.tenant_id
    AND status IN ('DRAFT','PUBLISHED')
  FOR SHARE;
  IF NOT FOUND THEN
    RAISE EXCEPTION '查询审计的指标版本、数据集版本或租户不匹配'
      USING ERRCODE='23503',
        CONSTRAINT='query_runs_metric_version_dataset_tenant_snapshot_check';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enforce_query_run_metric_snapshot() FROM PUBLIC;

CREATE TRIGGER query_runs_enforce_metric_snapshot
BEFORE INSERT
ON platform.query_runs
FOR EACH ROW EXECUTE FUNCTION platform.enforce_query_run_metric_snapshot();

-- 查询审计只允许从 RUNNING 收口一次；身份、计划、终态和整条记录随后都不可改写或删除。
CREATE OR REPLACE FUNCTION platform.reject_query_run_identity_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF TG_OP='DELETE' THEN
    RAISE EXCEPTION '查询审计不可删除' USING ERRCODE='23514';
  END IF;
  IF NEW.id IS DISTINCT FROM OLD.id
    OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.dataset_id IS DISTINCT FROM OLD.dataset_id
    OR NEW.dataset_version_id IS DISTINCT FROM OLD.dataset_version_id
    OR NEW.metric_id IS DISTINCT FROM OLD.metric_id
    OR NEW.metric_version_id IS DISTINCT FROM OLD.metric_version_id
    OR NEW.actor_user_id IS DISTINCT FROM OLD.actor_user_id
    OR NEW.data_source_id IS DISTINCT FROM OLD.data_source_id
    OR NEW.run_type IS DISTINCT FROM OLD.run_type
    OR NEW.plan_hash IS DISTINCT FROM OLD.plan_hash
    OR NEW.parameter_hash IS DISTINCT FROM OLD.parameter_hash
    OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION '查询审计的执行身份与计划摘要不可修改' USING ERRCODE='23514';
  END IF;
  IF OLD.status<>'RUNNING' OR NEW.status='RUNNING'
    OR NEW.completed_at IS NULL OR NEW.completed_at<OLD.created_at THEN
    RAISE EXCEPTION '查询审计只能从 RUNNING 一次性收口到终态' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.reject_query_run_identity_mutation() FROM PUBLIC;

CREATE TRIGGER query_runs_reject_identity_mutation
BEFORE UPDATE OR DELETE
ON platform.query_runs
FOR EACH ROW EXECUTE FUNCTION platform.reject_query_run_identity_mutation();

CREATE UNIQUE INDEX metric_versions_one_draft_idx
  ON platform.metric_versions(tenant_id,metric_id) WHERE status='DRAFT';
CREATE UNIQUE INDEX metric_versions_source_draft_revision_idx
  ON platform.metric_versions(tenant_id,metric_id,source_draft_version_id,source_draft_record_version)
  WHERE source_draft_version_id IS NOT NULL;
CREATE INDEX metrics_tenant_status_idx
  ON platform.metrics(tenant_id,status,updated_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX metric_versions_metric_time_idx
  ON platform.metric_versions(tenant_id,metric_id,version_no DESC);
CREATE INDEX metric_dimensions_dataset_field_idx
  ON platform.metric_dimensions(tenant_id,dataset_version_id,field_id);
CREATE INDEX metric_dependencies_target_idx
  ON platform.metric_dependencies(tenant_id,dependency_metric_version_id);
CREATE INDEX metric_publish_idempotency_actor_time_idx
  ON platform.metric_publish_idempotency(tenant_id,actor_user_id,created_at DESC);
CREATE INDEX metric_publish_idempotency_version_idx
  ON platform.metric_publish_idempotency(tenant_id,published_version_id);
CREATE INDEX query_runs_metric_version_status_idx
  ON platform.query_runs(tenant_id,metric_version_id,status,created_at DESC)
  WHERE metric_version_id IS NOT NULL;

CREATE TRIGGER metrics_set_updated_at
BEFORE UPDATE ON platform.metrics
FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();
CREATE TRIGGER metric_versions_set_updated_at
BEFORE UPDATE ON platform.metric_versions
FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();

-- 首次发布响应不可改写或删除，重放始终返回同一可信事实。
CREATE OR REPLACE FUNCTION platform.reject_metric_publish_idempotency_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  RAISE EXCEPTION '指标发布幂等响应不可修改或删除';
END
$$;

REVOKE ALL ON FUNCTION platform.reject_metric_publish_idempotency_mutation() FROM PUBLIC;

CREATE TRIGGER metric_publish_idempotency_immutable
BEFORE UPDATE OR DELETE ON platform.metric_publish_idempotency
FOR EACH ROW EXECUTE FUNCTION platform.reject_metric_publish_idempotency_mutation();

-- 校验发布构建态确实是指定且未变化草稿的独立副本。
CREATE OR REPLACE FUNCTION platform.metric_publication_source_matches(candidate platform.metric_versions)
RETURNS boolean
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
  SELECT EXISTS(
    SELECT 1
    FROM platform.metric_versions AS draft
    WHERE draft.id=candidate.source_draft_version_id
      AND draft.metric_id=candidate.metric_id
      AND draft.tenant_id=candidate.tenant_id
      AND draft.status='DRAFT'
      AND draft.record_version=candidate.source_draft_record_version
      AND draft.dataset_id=candidate.dataset_id
      AND draft.dataset_version_id=candidate.dataset_version_id
      AND draft.definition_version=candidate.definition_version
      AND draft.definition_json=candidate.definition_json
      AND draft.definition_hash=candidate.definition_hash
  )
$$;

REVOKE ALL ON FUNCTION platform.metric_publication_source_matches(platform.metric_versions) FROM PUBLIC;

-- 草稿保持可变但不能原地发布；发布版本仅允许单向推进生命周期。
CREATE OR REPLACE FUNCTION platform.enforce_metric_version_publication()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
BEGIN
  IF TG_OP='INSERT' THEN
    IF NEW.status NOT IN ('DRAFT','PUBLISHING') THEN
      RAISE EXCEPTION '指标版本必须从草稿或发布构建态创建' USING ERRCODE='23514';
    END IF;
    IF NEW.status='PUBLISHING' AND NOT platform.metric_publication_source_matches(NEW) THEN
      RAISE EXCEPTION '指标发布副本与指定草稿修订不一致' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
  END IF;

  IF TG_OP='DELETE' THEN
    IF OLD.status IN ('PUBLISHED','STALE','DEPRECATED') THEN
      RAISE EXCEPTION '已发布指标版本不可删除';
    END IF;
    RETURN OLD;
  END IF;

  IF NEW.id IS DISTINCT FROM OLD.id
    OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.metric_id IS DISTINCT FROM OLD.metric_id
    OR NEW.dataset_id IS DISTINCT FROM OLD.dataset_id
    OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION '指标版本身份不可修改';
  END IF;

  IF OLD.status='DRAFT' THEN
    IF NEW.status<>'DRAFT' THEN
      RAISE EXCEPTION '指标草稿不能原地转换为发布版本' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
  END IF;

  IF OLD.status='PUBLISHING' THEN
    IF NEW.status<>'PUBLISHED' THEN
      RAISE EXCEPTION '指标发布构建态只能转换为已发布状态' USING ERRCODE='23514';
    END IF;
    IF ROW(
      NEW.id,NEW.tenant_id,NEW.metric_id,NEW.dataset_id,NEW.dataset_version_id,
      NEW.version_no,NEW.definition_version,NEW.definition_json,NEW.definition_hash,
      NEW.record_version,NEW.created_by,NEW.created_at,NEW.published_at,NEW.published_by,
      NEW.source_draft_version_id,NEW.source_draft_record_version
    ) IS DISTINCT FROM ROW(
      OLD.id,OLD.tenant_id,OLD.metric_id,OLD.dataset_id,OLD.dataset_version_id,
      OLD.version_no,OLD.definition_version,OLD.definition_json,OLD.definition_hash,
      OLD.record_version,OLD.created_by,OLD.created_at,OLD.published_at,OLD.published_by,
      OLD.source_draft_version_id,OLD.source_draft_record_version
    ) THEN
      RAISE EXCEPTION '指标发布完成时不能改写版本快照';
    END IF;
    IF NOT platform.metric_publication_source_matches(NEW) THEN
      RAISE EXCEPTION '指标发布完成前草稿已经变化' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
  END IF;

  IF NOT (
    OLD.status='PUBLISHED' AND NEW.status IN ('STALE','DEPRECATED')
    OR OLD.status='STALE' AND NEW.status='DEPRECATED'
  ) THEN
    RAISE EXCEPTION '指标发布版本状态转换无效' USING ERRCODE='23514';
  END IF;

  IF NEW.status='DEPRECATED' AND EXISTS(
    SELECT 1
    FROM platform.metric_dependencies AS dependency
    JOIN platform.metric_versions AS downstream
      ON downstream.id=dependency.metric_version_id
      AND downstream.tenant_id=dependency.tenant_id
    WHERE dependency.dependency_metric_version_id=OLD.id
      AND downstream.status IN ('PUBLISHED','STALE')
  ) THEN
    RAISE EXCEPTION '仍有可运行的已发布下游指标依赖该版本' USING ERRCODE='23514';
  END IF;

  IF ROW(
    NEW.id,NEW.tenant_id,NEW.metric_id,NEW.dataset_id,NEW.dataset_version_id,
    NEW.version_no,NEW.definition_version,NEW.definition_json,NEW.definition_hash,
    NEW.record_version,NEW.created_by,NEW.created_at,NEW.published_at,NEW.published_by,
    NEW.source_draft_version_id,NEW.source_draft_record_version
  ) IS DISTINCT FROM ROW(
    OLD.id,OLD.tenant_id,OLD.metric_id,OLD.dataset_id,OLD.dataset_version_id,
    OLD.version_no,OLD.definition_version,OLD.definition_json,OLD.definition_hash,
    OLD.record_version,OLD.created_by,OLD.created_at,OLD.published_at,OLD.published_by,
    OLD.source_draft_version_id,OLD.source_draft_record_version
  ) THEN
    RAISE EXCEPTION '已发布指标版本内容不可修改';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enforce_metric_version_publication() FROM PUBLIC;

CREATE TRIGGER metric_versions_enforce_publication
BEFORE INSERT OR UPDATE OR DELETE ON platform.metric_versions
FOR EACH ROW EXECUTE FUNCTION platform.enforce_metric_version_publication();

-- PUBLISHING 只能存在于发布事务内部，提交时仍未完成必须整体回滚。
CREATE OR REPLACE FUNCTION platform.reject_unfinished_metric_publication()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  target_id uuid;
BEGIN
  target_id=COALESCE(NEW.id,OLD.id);
  IF EXISTS(
    SELECT 1 FROM platform.metric_versions
    WHERE id=target_id AND status='PUBLISHING'
  ) THEN
    RAISE EXCEPTION '指标发布事务不能以 PUBLISHING 状态提交' USING ERRCODE='23514';
  END IF;
  RETURN NULL;
END
$$;

REVOKE ALL ON FUNCTION platform.reject_unfinished_metric_publication() FROM PUBLIC;

CREATE CONSTRAINT TRIGGER metric_versions_require_final_publication
AFTER INSERT OR UPDATE ON platform.metric_versions
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION platform.reject_unfinished_metric_publication();

-- 发布版本的维度和依赖索引是定义快照的一部分，进入终态后不可修改。
CREATE OR REPLACE FUNCTION platform.reject_published_metric_index_mutation()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  old_version_id uuid;
  new_version_id uuid;
BEGIN
  IF TG_OP<>'INSERT' THEN
    old_version_id=OLD.metric_version_id;
  END IF;
  IF TG_OP<>'DELETE' THEN
    new_version_id=NEW.metric_version_id;
  END IF;
  IF EXISTS(
    SELECT 1 FROM platform.metric_versions
    WHERE id IN (old_version_id,new_version_id)
      AND status IN ('PUBLISHED','STALE','DEPRECATED')
  ) THEN
    RAISE EXCEPTION '已发布指标版本的派生索引不可修改';
  END IF;
  IF TG_OP='DELETE' THEN
    RETURN OLD;
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.reject_published_metric_index_mutation() FROM PUBLIC;

CREATE TRIGGER metric_dimensions_reject_published_mutation
BEFORE INSERT OR UPDATE OR DELETE ON platform.metric_dimensions
FOR EACH ROW EXECUTE FUNCTION platform.reject_published_metric_index_mutation();
CREATE TRIGGER metric_dependencies_reject_published_mutation
BEFORE INSERT OR UPDATE OR DELETE ON platform.metric_dependencies
FOR EACH ROW EXECUTE FUNCTION platform.reject_published_metric_index_mutation();

-- 依赖目标必须是同一数据集版本的已发布指标，并拒绝精确版本依赖图中的循环。
CREATE OR REPLACE FUNCTION platform.enforce_metric_dependency()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  owner_metric_id uuid;
  dependency_status text;
  dependency_dataset_version_id uuid;
  dependency_metric_id uuid;
  cycle_found boolean;
BEGIN
  SELECT metric_id INTO owner_metric_id
  FROM platform.metric_versions
  WHERE id=NEW.metric_version_id AND tenant_id=NEW.tenant_id;
  IF NOT FOUND OR owner_metric_id<>NEW.metric_id THEN
    RAISE EXCEPTION '指标依赖的所属版本无效' USING ERRCODE='23514';
  END IF;

  SELECT status,dataset_version_id,metric_id
  INTO dependency_status,dependency_dataset_version_id,dependency_metric_id
  FROM platform.metric_versions
  WHERE id=NEW.dependency_metric_version_id AND tenant_id=NEW.tenant_id
  FOR SHARE;
  IF NOT FOUND
    OR dependency_status<>'PUBLISHED'
    OR dependency_dataset_version_id<>NEW.dataset_version_id
    OR dependency_metric_id<>NEW.dependency_metric_id THEN
    RAISE EXCEPTION '指标依赖必须引用同一数据集版本的已发布指标版本' USING ERRCODE='23514';
  END IF;
  IF dependency_metric_id=owner_metric_id THEN
    RAISE EXCEPTION '指标不能引用自身的其他版本' USING ERRCODE='23514';
  END IF;

  WITH RECURSIVE graph(version_id,metric_id) AS (
    SELECT version.id,version.metric_id
    FROM platform.metric_versions AS version
    WHERE version.id=NEW.dependency_metric_version_id
      AND version.tenant_id=NEW.tenant_id
    UNION
    SELECT target.id,target.metric_id
    FROM graph
    JOIN platform.metric_dependencies AS dependency
      ON dependency.metric_version_id=graph.version_id
      AND dependency.tenant_id=NEW.tenant_id
    JOIN platform.metric_versions AS target
      ON target.id=dependency.dependency_metric_version_id
      AND target.tenant_id=dependency.tenant_id
  )
  SELECT EXISTS(SELECT 1 FROM graph WHERE metric_id=owner_metric_id)
  INTO cycle_found;
  IF cycle_found THEN
    RAISE EXCEPTION '指标依赖图存在循环' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enforce_metric_dependency() FROM PUBLIC;

CREATE TRIGGER metric_dependencies_validate
BEFORE INSERT OR UPDATE ON platform.metric_dependencies
FOR EACH ROW EXECUTE FUNCTION platform.enforce_metric_dependency();

-- 主对象和版本在事务提交前必须形成唯一草稿和一致的当前发布指针。
CREATE OR REPLACE FUNCTION platform.enforce_metric_pointer_consistency()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  target_metric_id uuid;
  metric_status text;
  metric_deleted_at timestamptz;
  draft_pointer uuid;
  published_pointer uuid;
  draft_count integer;
  pointed_draft_status text;
  pointed_published_status text;
  pointed_published_at timestamptz;
BEGIN
  IF TG_TABLE_NAME='metrics' THEN
    target_metric_id=COALESCE(NEW.id,OLD.id);
  ELSE
    target_metric_id=COALESCE(NEW.metric_id,OLD.metric_id);
  END IF;

  SELECT status,deleted_at,current_draft_version_id,current_published_version_id
  INTO metric_status,metric_deleted_at,draft_pointer,published_pointer
  FROM platform.metrics WHERE id=target_metric_id;
  IF NOT FOUND OR metric_deleted_at IS NOT NULL THEN
    RETURN NULL;
  END IF;

  SELECT count(*) FILTER(WHERE status='DRAFT')
  INTO draft_count
  FROM platform.metric_versions WHERE metric_id=target_metric_id;
  IF draft_pointer IS NULL OR draft_count<>1 THEN
    RAISE EXCEPTION '活跃指标必须且只能保留一个当前草稿' USING ERRCODE='23514';
  END IF;

  SELECT status INTO pointed_draft_status
  FROM platform.metric_versions
  WHERE id=draft_pointer AND metric_id=target_metric_id;
  IF pointed_draft_status IS DISTINCT FROM 'DRAFT' THEN
    RAISE EXCEPTION '当前指标草稿指针必须指向本指标的 DRAFT 版本' USING ERRCODE='23514';
  END IF;

  IF published_pointer IS NULL THEN
    IF metric_status='PUBLISHED' THEN
      RAISE EXCEPTION 'PUBLISHED 指标必须保留发布版本指针' USING ERRCODE='23514';
    END IF;
    RETURN NULL;
  END IF;

  SELECT status,published_at INTO pointed_published_status,pointed_published_at
  FROM platform.metric_versions
  WHERE id=published_pointer AND metric_id=target_metric_id;
  IF pointed_published_status NOT IN ('PUBLISHED','STALE','DEPRECATED')
    OR pointed_published_at IS NULL THEN
    RAISE EXCEPTION '当前指标发布指针必须指向已经完成发布的本指标版本' USING ERRCODE='23514';
  END IF;
  IF metric_status='DRAFT' THEN
    RAISE EXCEPTION 'DRAFT 指标不能保留当前发布指针' USING ERRCODE='23514';
  END IF;
  IF metric_status IN ('PUBLISHED','STALE','DEPRECATED')
    AND metric_status<>pointed_published_status THEN
    RAISE EXCEPTION '指标状态必须与当前发布版本状态一致' USING ERRCODE='23514';
  END IF;
  RETURN NULL;
END
$$;

REVOKE ALL ON FUNCTION platform.enforce_metric_pointer_consistency() FROM PUBLIC;

CREATE CONSTRAINT TRIGGER metrics_pointer_consistency
AFTER INSERT OR UPDATE OR DELETE ON platform.metrics
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION platform.enforce_metric_pointer_consistency();
CREATE CONSTRAINT TRIGGER metric_versions_pointer_consistency
AFTER INSERT OR UPDATE OR DELETE ON platform.metric_versions
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION platform.enforce_metric_pointer_consistency();

ALTER TABLE platform.metrics ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.metrics FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.metric_versions ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.metric_versions FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.metric_dimensions ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.metric_dimensions FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.metric_dependencies ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.metric_dependencies FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.metric_publish_idempotency ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.metric_publish_idempotency FORCE ROW LEVEL SECURITY;

CREATE POLICY metrics_tenant_isolation ON platform.metrics
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY metric_versions_tenant_isolation ON platform.metric_versions
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY metric_dimensions_tenant_isolation ON platform.metric_dimensions
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY metric_dependencies_tenant_isolation ON platform.metric_dependencies
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY metric_publish_idempotency_tenant_isolation ON platform.metric_publish_idempotency
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

-- 为既有租户登记独立指标发布权限，并授予内置平台、租户和数据管理员角色。
INSERT INTO platform.permissions(tenant_id,code,name,resource_type,action,description)
SELECT id,'metric.publish','发布指标','METRIC','PUBLISH','发布不可变指标版本并切换当前发布指针'
FROM platform.tenants
ON CONFLICT(tenant_id,code) DO UPDATE SET
  name=EXCLUDED.name,
  resource_type=EXCLUDED.resource_type,
  action=EXCLUDED.action,
  description=EXCLUDED.description;

INSERT INTO platform.role_permissions(tenant_id,role_id,permission_id)
SELECT role.tenant_id,role.id,permission.id
FROM platform.roles AS role
JOIN platform.permissions AS permission
  ON permission.tenant_id=role.tenant_id
  AND permission.code='metric.publish'
WHERE role.code::text IN ('platform_admin','tenant_admin','data_admin')
ON CONFLICT DO NOTHING;

COMMENT ON TABLE platform.metrics IS '租户内指标主对象及可变草稿和当前发布指针';
COMMENT ON TABLE platform.metric_versions IS '固定精确数据集版本的指标定义草稿及不可变发布快照';
COMMENT ON TABLE platform.metric_dimensions IS '由指标定义重建并指向数据集逻辑字段的适用维度索引';
COMMENT ON TABLE platform.metric_dependencies IS '指标版本对同数据集精确已发布指标版本的依赖索引';
COMMENT ON TABLE platform.metric_publish_idempotency IS '指标发布请求与首次可信响应的租户隔离幂等快照';
COMMENT ON COLUMN platform.metric_versions.dataset_version_id IS '指标定义固定的精确数据集发布版本';
COMMENT ON COLUMN platform.metric_versions.source_draft_version_id IS '生成该发布版本的独立指标草稿标识';
COMMENT ON COLUMN platform.metric_versions.source_draft_record_version IS '发布前最后复核的指标草稿记录版本';
COMMENT ON COLUMN platform.query_runs.metric_version_id IS '指标试算关联的可变草稿身份或不可变发布版本，可为空；草稿历史定义当前不可恢复';
