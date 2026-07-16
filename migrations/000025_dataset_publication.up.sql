-- 建立数据集不可变发布、精确版本归属和发布幂等的数据库合同。
-- 锁定租户表，确保本次迁移为全部既有租户补齐发布权限时没有并发空窗。
LOCK TABLE platform.tenants IN SHARE ROW EXCLUSIVE MODE;

-- 当前代码尚未提供发布入口；若数据库中存在人工改写的非草稿版本，迁移应失败关闭，
-- 避免为缺少来源草稿和发布操作者的历史行伪造审计信息。
DO $$
BEGIN
  IF EXISTS(SELECT 1 FROM platform.dataset_versions WHERE status<>'DRAFT') THEN
    RAISE EXCEPTION '存在缺少可信发布来源的历史数据集版本，请先人工核验后再执行迁移';
  END IF;
END
$$;

ALTER TABLE platform.dataset_versions
  DROP CONSTRAINT dataset_versions_status_check,
  ADD COLUMN published_at timestamptz,
  ADD COLUMN published_by uuid,
  ADD COLUMN source_draft_version_id uuid,
  ADD COLUMN source_draft_record_version bigint;

ALTER TABLE platform.dataset_versions
  ADD CONSTRAINT dataset_versions_status_check
    CHECK(status IN ('DRAFT','PUBLISHING','PUBLISHED','STALE','DEPRECATED')),
  ADD CONSTRAINT dataset_versions_publication_metadata_check CHECK(
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
  ADD CONSTRAINT dataset_versions_source_draft_identity_check
    CHECK(source_draft_version_id IS NULL OR source_draft_version_id<>id),
  ADD CONSTRAINT dataset_versions_identity_dataset_tenant_key
    UNIQUE(id,dataset_id,tenant_id),
  ADD CONSTRAINT dataset_versions_published_by_fk
    FOREIGN KEY(published_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  ADD CONSTRAINT dataset_versions_source_draft_fk
    FOREIGN KEY(source_draft_version_id,dataset_id,tenant_id)
    REFERENCES platform.dataset_versions(id,dataset_id,tenant_id) ON DELETE RESTRICT;

-- 一个草稿修订只能生成一个发布版本；不同幂等键不能重复发布相同事实。
CREATE UNIQUE INDEX dataset_versions_source_draft_revision_idx
  ON platform.dataset_versions(tenant_id,dataset_id,source_draft_version_id,source_draft_record_version)
  WHERE source_draft_version_id IS NOT NULL;

-- 依赖快照保存发布时观察到的上游版本和摘要；空值仅用于兼容迁移前已有草稿。
ALTER TABLE platform.dataset_dependencies
  ADD COLUMN source_version bigint NOT NULL DEFAULT 0,
  ADD COLUMN source_hash text NOT NULL DEFAULT '',
  ADD COLUMN source_plan_hash text NOT NULL DEFAULT '',
  ADD CONSTRAINT dataset_dependencies_source_version_check CHECK(source_version>=0),
  ADD CONSTRAINT dataset_dependencies_source_hash_check
    CHECK(source_hash='' OR source_hash ~ '^[0-9a-f]{64}$'),
  ADD CONSTRAINT dataset_dependencies_source_plan_hash_check
    CHECK(source_plan_hash='' OR source_plan_hash ~ '^[0-9a-f]{64}$'),
  ADD CONSTRAINT dataset_dependencies_snapshot_shape_check CHECK(
    (source_version=0 AND source_hash='' AND source_plan_hash='')
    OR (source_version>0
      AND source_hash ~ '^[0-9a-f]{64}$'
      AND (source_type='DATASET_VERSION' AND source_plan_hash ~ '^[0-9a-f]{64}$'
        OR source_type IN ('TABLE','FILE_VERSION') AND source_plan_hash=''))
  );

-- 指针和查询审计必须同时固定版本所属的数据集，禁止同租户内串接其他数据集版本。
ALTER TABLE platform.datasets
  DROP CONSTRAINT datasets_current_draft_fk,
  DROP CONSTRAINT datasets_current_published_fk,
  ADD CONSTRAINT datasets_current_draft_fk
    FOREIGN KEY(current_draft_version_id,id,tenant_id)
    REFERENCES platform.dataset_versions(id,dataset_id,tenant_id),
  ADD CONSTRAINT datasets_current_published_fk
    FOREIGN KEY(current_published_version_id,id,tenant_id)
    REFERENCES platform.dataset_versions(id,dataset_id,tenant_id);

ALTER TABLE platform.query_runs
  DROP CONSTRAINT query_runs_dataset_version_id_tenant_id_fkey,
  DROP CONSTRAINT query_runs_run_type_check,
  ADD CONSTRAINT query_runs_dataset_version_dataset_tenant_fk
    FOREIGN KEY(dataset_version_id,dataset_id,tenant_id)
    REFERENCES platform.dataset_versions(id,dataset_id,tenant_id),
  ADD CONSTRAINT query_runs_run_type_check
    CHECK(run_type IN ('PREVIEW','VALIDATION','ONLINE','EXPORT','SCHEDULED'));

-- 发布幂等记录绑定首次操作者、请求摘要和精确发布版本，重放不能成为越权读取旁路。
CREATE TABLE platform.dataset_publish_idempotency(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  dataset_id uuid NOT NULL,
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
  CONSTRAINT dataset_publish_idempotency_dataset_fk
    FOREIGN KEY(dataset_id,tenant_id)
    REFERENCES platform.datasets(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dataset_publish_idempotency_actor_fk
    FOREIGN KEY(actor_user_id,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dataset_publish_idempotency_version_fk
    FOREIGN KEY(published_version_id,dataset_id,tenant_id)
    REFERENCES platform.dataset_versions(id,dataset_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dataset_publish_idempotency_key
    UNIQUE(tenant_id,dataset_id,idempotency_key),
  UNIQUE(id,tenant_id)
);

CREATE INDEX dataset_publish_idempotency_actor_time_idx
  ON platform.dataset_publish_idempotency(tenant_id,actor_user_id,created_at DESC);
CREATE INDEX dataset_publish_idempotency_version_idx
  ON platform.dataset_publish_idempotency(tenant_id,published_version_id);

-- 首次发布响应一旦登记便不可改写或删除，后续重放始终返回同一可信事实。
CREATE OR REPLACE FUNCTION platform.reject_dataset_publish_idempotency_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  RAISE EXCEPTION '数据集发布幂等响应不可修改或删除';
END
$$;

REVOKE ALL ON FUNCTION platform.reject_dataset_publish_idempotency_mutation() FROM PUBLIC;

CREATE TRIGGER dataset_publish_idempotency_immutable
BEFORE UPDATE OR DELETE ON platform.dataset_publish_idempotency
FOR EACH ROW EXECUTE FUNCTION platform.reject_dataset_publish_idempotency_mutation();

-- 校验 PUBLISHING 行确实是指定且未变化草稿的独立副本。
CREATE OR REPLACE FUNCTION platform.dataset_publication_source_matches(candidate platform.dataset_versions)
RETURNS boolean
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
  SELECT EXISTS(
    SELECT 1
    FROM platform.dataset_versions AS draft
    WHERE draft.id=candidate.source_draft_version_id
      AND draft.dataset_id=candidate.dataset_id
      AND draft.tenant_id=candidate.tenant_id
      AND draft.status='DRAFT'
      AND draft.record_version=candidate.source_draft_record_version
      AND draft.dsl_version=candidate.dsl_version
      AND draft.dsl_json=candidate.dsl_json
      AND draft.schema_hash=candidate.schema_hash
      AND draft.logical_plan_json=candidate.logical_plan_json
      AND draft.plan_hash=candidate.plan_hash
  )
$$;

REVOKE ALL ON FUNCTION platform.dataset_publication_source_matches(platform.dataset_versions) FROM PUBLIC;

-- 草稿只能保持为草稿；发布必须先创建独立 PUBLISHING 副本，再一次性完成终态。
CREATE OR REPLACE FUNCTION platform.enforce_dataset_version_publication()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
BEGIN
  IF TG_OP='INSERT' THEN
    IF NEW.status NOT IN ('DRAFT','PUBLISHING') THEN
      RAISE EXCEPTION '数据集版本必须从草稿或发布构建态创建' USING ERRCODE='23514';
    END IF;
    IF NEW.status='PUBLISHING' AND NOT platform.dataset_publication_source_matches(NEW) THEN
      RAISE EXCEPTION '发布副本与指定草稿修订不一致' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
  END IF;

  IF TG_OP='DELETE' THEN
    IF OLD.status IN ('PUBLISHED','STALE','DEPRECATED') THEN
      RAISE EXCEPTION '已发布数据集版本不可删除';
    END IF;
    RETURN OLD;
  END IF;

  IF NEW.id IS DISTINCT FROM OLD.id
    OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.dataset_id IS DISTINCT FROM OLD.dataset_id
    OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION '数据集版本身份不可修改';
  END IF;

  IF OLD.status='DRAFT' THEN
    IF NEW.status<>'DRAFT' THEN
      RAISE EXCEPTION '草稿不能原地转换为发布版本' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
  END IF;

  IF OLD.status='PUBLISHING' THEN
    IF NEW.status<>'PUBLISHED' THEN
      RAISE EXCEPTION '发布构建态只能转换为已发布状态' USING ERRCODE='23514';
    END IF;
    IF ROW(
      NEW.id,NEW.tenant_id,NEW.dataset_id,NEW.version_no,NEW.dsl_version,NEW.dsl_json,
      NEW.schema_hash,NEW.logical_plan_json,NEW.plan_hash,NEW.record_version,
      NEW.created_by,NEW.created_at,NEW.published_at,NEW.published_by,
      NEW.source_draft_version_id,NEW.source_draft_record_version
    ) IS DISTINCT FROM ROW(
      OLD.id,OLD.tenant_id,OLD.dataset_id,OLD.version_no,OLD.dsl_version,OLD.dsl_json,
      OLD.schema_hash,OLD.logical_plan_json,OLD.plan_hash,OLD.record_version,
      OLD.created_by,OLD.created_at,OLD.published_at,OLD.published_by,
      OLD.source_draft_version_id,OLD.source_draft_record_version
    ) THEN
      RAISE EXCEPTION '发布完成时不能改写版本快照';
    END IF;
    IF NOT platform.dataset_publication_source_matches(NEW) THEN
      RAISE EXCEPTION '发布完成前草稿已经变化' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
  END IF;

  IF NOT (
    OLD.status='PUBLISHED' AND NEW.status IN ('STALE','DEPRECATED')
    OR OLD.status='STALE' AND NEW.status='DEPRECATED'
  ) THEN
    RAISE EXCEPTION '数据集发布版本状态转换无效' USING ERRCODE='23514';
  END IF;

  -- 已发布版本只允许推进生命周期；定义、版本号、来源和发布审计均保持不可变。
  IF ROW(
    NEW.id,NEW.tenant_id,NEW.dataset_id,NEW.version_no,NEW.dsl_version,NEW.dsl_json,
    NEW.schema_hash,NEW.logical_plan_json,NEW.plan_hash,NEW.record_version,
    NEW.created_by,NEW.created_at,NEW.published_at,NEW.published_by,
    NEW.source_draft_version_id,NEW.source_draft_record_version
  ) IS DISTINCT FROM ROW(
    OLD.id,OLD.tenant_id,OLD.dataset_id,OLD.version_no,OLD.dsl_version,OLD.dsl_json,
    OLD.schema_hash,OLD.logical_plan_json,OLD.plan_hash,OLD.record_version,
    OLD.created_by,OLD.created_at,OLD.published_at,OLD.published_by,
    OLD.source_draft_version_id,OLD.source_draft_record_version
  ) THEN
    RAISE EXCEPTION '已发布数据集版本内容不可修改';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enforce_dataset_version_publication() FROM PUBLIC;

CREATE TRIGGER dataset_versions_enforce_publication
BEFORE INSERT OR UPDATE OR DELETE ON platform.dataset_versions
FOR EACH ROW EXECUTE FUNCTION platform.enforce_dataset_version_publication();

-- PUBLISHING 仅允许存在于发布事务内部，提交时仍未完成必须整体回滚。
CREATE OR REPLACE FUNCTION platform.reject_unfinished_dataset_publication()
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
    SELECT 1 FROM platform.dataset_versions
    WHERE id=target_id AND status='PUBLISHING'
  ) THEN
    RAISE EXCEPTION '数据集发布事务不能以 PUBLISHING 状态提交' USING ERRCODE='23514';
  END IF;
  RETURN NULL;
END
$$;

REVOKE ALL ON FUNCTION platform.reject_unfinished_dataset_publication() FROM PUBLIC;

CREATE CONSTRAINT TRIGGER dataset_versions_require_final_publication
AFTER INSERT OR UPDATE ON platform.dataset_versions
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION platform.reject_unfinished_dataset_publication();

-- 发布版本的派生字段、参数和依赖与规范 DSL 共同构成不可变快照。
CREATE OR REPLACE FUNCTION platform.reject_published_dataset_index_mutation()
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
    old_version_id=OLD.dataset_version_id;
  END IF;
  IF TG_OP<>'DELETE' THEN
    new_version_id=NEW.dataset_version_id;
  END IF;
  IF EXISTS(
    SELECT 1
    FROM platform.dataset_versions
    WHERE id IN (old_version_id,new_version_id)
      AND status IN ('PUBLISHED','STALE','DEPRECATED')
  ) THEN
    RAISE EXCEPTION '已发布数据集版本的派生索引不可修改';
  END IF;
  IF TG_OP='DELETE' THEN
    RETURN OLD;
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.reject_published_dataset_index_mutation() FROM PUBLIC;

CREATE TRIGGER dataset_fields_reject_published_mutation
BEFORE INSERT OR UPDATE OR DELETE ON platform.dataset_fields
FOR EACH ROW EXECUTE FUNCTION platform.reject_published_dataset_index_mutation();
CREATE TRIGGER dataset_parameters_reject_published_mutation
BEFORE INSERT OR UPDATE OR DELETE ON platform.dataset_parameters
FOR EACH ROW EXECUTE FUNCTION platform.reject_published_dataset_index_mutation();
CREATE TRIGGER dataset_dependencies_reject_published_mutation
BEFORE INSERT OR UPDATE OR DELETE ON platform.dataset_dependencies
FOR EACH ROW EXECUTE FUNCTION platform.reject_published_dataset_index_mutation();

-- 数据集主对象和版本任一发生变化时，都在事务提交前检查最终指针与状态。
CREATE OR REPLACE FUNCTION platform.enforce_dataset_pointer_consistency()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  target_dataset_id uuid;
  dataset_status text;
  dataset_deleted_at timestamptz;
  draft_pointer uuid;
  published_pointer uuid;
  draft_count integer;
  pointed_draft_status text;
  pointed_published_status text;
  pointed_published_at timestamptz;
BEGIN
  IF TG_TABLE_NAME='datasets' THEN
    target_dataset_id=COALESCE(NEW.id,OLD.id);
  ELSE
    target_dataset_id=COALESCE(NEW.dataset_id,OLD.dataset_id);
  END IF;

  SELECT status,deleted_at,current_draft_version_id,current_published_version_id
  INTO dataset_status,dataset_deleted_at,draft_pointer,published_pointer
  FROM platform.datasets
  WHERE id=target_dataset_id;
  IF NOT FOUND OR dataset_deleted_at IS NOT NULL THEN
    RETURN NULL;
  END IF;

  SELECT count(*) FILTER(WHERE status='DRAFT')
  INTO draft_count
  FROM platform.dataset_versions
  WHERE dataset_id=target_dataset_id;
  IF draft_pointer IS NULL OR draft_count<>1 THEN
    RAISE EXCEPTION '活跃数据集必须且只能保留一个当前草稿' USING ERRCODE='23514';
  END IF;

  SELECT status INTO pointed_draft_status
  FROM platform.dataset_versions
  WHERE id=draft_pointer AND dataset_id=target_dataset_id;
  IF pointed_draft_status IS DISTINCT FROM 'DRAFT' THEN
    RAISE EXCEPTION '当前草稿指针必须指向本数据集的 DRAFT 版本' USING ERRCODE='23514';
  END IF;

  IF published_pointer IS NULL THEN
    IF dataset_status='PUBLISHED' THEN
      RAISE EXCEPTION 'PUBLISHED 数据集必须保留发布版本指针' USING ERRCODE='23514';
    END IF;
    RETURN NULL;
  END IF;

  SELECT status,published_at
  INTO pointed_published_status,pointed_published_at
  FROM platform.dataset_versions
  WHERE id=published_pointer AND dataset_id=target_dataset_id;
  IF pointed_published_status NOT IN ('PUBLISHED','STALE','DEPRECATED')
    OR pointed_published_at IS NULL THEN
    RAISE EXCEPTION '当前发布指针必须指向已经完成发布的本数据集版本' USING ERRCODE='23514';
  END IF;
  IF dataset_status='DRAFT' THEN
    RAISE EXCEPTION 'DRAFT 数据集不能保留当前发布指针' USING ERRCODE='23514';
  END IF;
  IF dataset_status IN ('PUBLISHED','STALE','DEPRECATED')
    AND dataset_status<>pointed_published_status THEN
    RAISE EXCEPTION '数据集状态必须与当前发布版本状态一致' USING ERRCODE='23514';
  END IF;
  RETURN NULL;
END
$$;

REVOKE ALL ON FUNCTION platform.enforce_dataset_pointer_consistency() FROM PUBLIC;

CREATE CONSTRAINT TRIGGER datasets_pointer_consistency
AFTER INSERT OR UPDATE OR DELETE ON platform.datasets
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION platform.enforce_dataset_pointer_consistency();
CREATE CONSTRAINT TRIGGER dataset_versions_pointer_consistency
AFTER INSERT OR UPDATE OR DELETE ON platform.dataset_versions
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION platform.enforce_dataset_pointer_consistency();

-- 迁移时立即核验既有最终态；约束触发器只负责迁移后的新事务。
DO $$
BEGIN
  IF EXISTS(
    SELECT 1
    FROM platform.datasets AS dataset
    LEFT JOIN platform.dataset_versions AS draft
      ON draft.id=dataset.current_draft_version_id
      AND draft.dataset_id=dataset.id
      AND draft.tenant_id=dataset.tenant_id
    WHERE dataset.deleted_at IS NULL
      AND (draft.id IS NULL OR draft.status<>'DRAFT')
  ) OR EXISTS(
    SELECT 1
    FROM platform.datasets AS dataset
    WHERE dataset.deleted_at IS NULL
      AND (SELECT count(*) FROM platform.dataset_versions AS version
        WHERE version.dataset_id=dataset.id AND version.tenant_id=dataset.tenant_id AND version.status='DRAFT')<>1
  ) THEN
    RAISE EXCEPTION '既有数据集草稿指针或草稿数量不一致';
  END IF;
  IF EXISTS(
    SELECT 1
    FROM platform.datasets AS dataset
    LEFT JOIN platform.dataset_versions AS published
      ON published.id=dataset.current_published_version_id
      AND published.dataset_id=dataset.id
      AND published.tenant_id=dataset.tenant_id
    WHERE dataset.deleted_at IS NULL
      AND (
        dataset.status='DRAFT' AND dataset.current_published_version_id IS NOT NULL
        OR dataset.status='PUBLISHED'
          AND (published.id IS NULL OR published.status<>'PUBLISHED' OR published.published_at IS NULL)
        OR dataset.status IN ('STALE','DEPRECATED')
          AND dataset.current_published_version_id IS NOT NULL
          AND (published.id IS NULL OR published.status<>dataset.status OR published.published_at IS NULL)
        OR dataset.current_published_version_id IS NOT NULL
          AND (published.id IS NULL OR published.status NOT IN ('PUBLISHED','STALE','DEPRECATED'))
      )
  ) THEN
    RAISE EXCEPTION '既有数据集发布指针与状态不一致';
  END IF;
END
$$;

-- 文件版本元数据创建后不可改写；仍被非草稿数据集引用时也不可删除。
CREATE OR REPLACE FUNCTION platform.protect_file_asset_version()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
BEGIN
  IF TG_OP='UPDATE' THEN
    RAISE EXCEPTION '文件资产版本不可修改';
  END IF;
  IF EXISTS(
    SELECT 1
    FROM platform.dataset_dependencies AS dependency
    JOIN platform.dataset_versions AS version
      ON version.id=dependency.dataset_version_id
      AND version.tenant_id=dependency.tenant_id
    WHERE dependency.tenant_id=OLD.tenant_id
      AND dependency.source_type='FILE_VERSION'
      AND dependency.source_id=OLD.id::text
      AND version.status<>'DRAFT'
  ) THEN
    RAISE EXCEPTION '文件资产版本仍被非草稿数据集版本引用，不能删除';
  END IF;
  RETURN OLD;
END
$$;

REVOKE ALL ON FUNCTION platform.protect_file_asset_version() FROM PUBLIC;

CREATE TRIGGER file_asset_versions_protect_immutable
BEFORE UPDATE OR DELETE ON platform.file_asset_versions
FOR EACH ROW EXECUTE FUNCTION platform.protect_file_asset_version();

ALTER TABLE platform.dataset_publish_idempotency ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.dataset_publish_idempotency FORCE ROW LEVEL SECURITY;
CREATE POLICY dataset_publish_idempotency_tenant_isolation
  ON platform.dataset_publish_idempotency
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

-- 为既有租户登记独立发布权限，并授予当前内置平台、租户和数据管理员角色。
INSERT INTO platform.permissions(tenant_id,code,name,resource_type,action,description)
SELECT id,'dataset.publish','发布数据集','DATASET','PUBLISH','发布不可变数据集版本并切换当前发布指针'
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
  AND permission.code='dataset.publish'
WHERE role.code::text IN ('platform_admin','tenant_admin','data_admin')
ON CONFLICT DO NOTHING;

COMMENT ON COLUMN platform.dataset_versions.published_at IS '发布事务完成并形成不可变快照的时间';
COMMENT ON COLUMN platform.dataset_versions.published_by IS '完成数据集发布的可信操作者';
COMMENT ON COLUMN platform.dataset_versions.source_draft_version_id IS '生成该发布版本的独立草稿版本标识';
COMMENT ON COLUMN platform.dataset_versions.source_draft_record_version IS '发布前最后复核的草稿记录版本';
COMMENT ON COLUMN platform.dataset_dependencies.source_version IS '发布时观察到的上游元数据、文件或数据集版本号；零仅兼容旧草稿';
COMMENT ON COLUMN platform.dataset_dependencies.source_hash IS '发布时观察到的上游结构或文件 SHA-256；空串仅兼容旧草稿';
COMMENT ON COLUMN platform.dataset_dependencies.source_plan_hash IS '上游数据集逻辑计划 SHA-256；非数据集依赖可为空串';
COMMENT ON TABLE platform.dataset_publish_idempotency IS '数据集发布请求与首次可信响应的租户隔离幂等快照';
COMMENT ON COLUMN platform.dataset_publish_idempotency.request_hash IS '规范发布请求的 SHA-256，不包含业务数据正文';
COMMENT ON COLUMN platform.dataset_publish_idempotency.response_json IS '首次成功发布响应，用于同操作者同请求精确重放';
