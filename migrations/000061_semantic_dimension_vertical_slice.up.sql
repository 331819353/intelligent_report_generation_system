-- 语义维度纵切：为人工管理增加乐观版本，并为严格绑定已发布 DWS
-- 物化的维度成员刷新建立可恢复任务。业务值仍只进入 dimension_members，
-- 不进入任务控制面 JSON 或日志。

ALTER TABLE platform.dimension_member_aliases
  ADD COLUMN version bigint NOT NULL DEFAULT 1 CHECK(version>0),
  ADD COLUMN updated_by uuid,
  ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now(),
  ADD CONSTRAINT dimension_member_aliases_updated_by_fk
    FOREIGN KEY(updated_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE SET NULL (updated_by);

UPDATE platform.dimension_member_aliases
SET updated_by=created_by
WHERE updated_by IS NULL AND created_by IS NOT NULL;

ALTER TABLE platform.dimension_metric_compatibility
  ADD COLUMN version bigint NOT NULL DEFAULT 1 CHECK(version>0),
  ADD COLUMN created_by uuid,
  ADD COLUMN updated_by uuid,
  ADD CONSTRAINT dimension_metric_compatibility_created_by_fk
    FOREIGN KEY(created_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE SET NULL (created_by),
  ADD CONSTRAINT dimension_metric_compatibility_updated_by_fk
    FOREIGN KEY(updated_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE SET NULL (updated_by);

UPDATE platform.dimension_metric_compatibility
SET created_by=verified_by,updated_by=verified_by
WHERE verified_by IS NOT NULL;

ALTER TABLE platform.semantic_dimensions
  ADD COLUMN member_refresh_generation uuid,
  ADD COLUMN member_count bigint CHECK(member_count IS NULL OR member_count>=0),
  ADD COLUMN member_refreshed_at timestamptz,
  ADD CONSTRAINT semantic_dimensions_member_refresh_shape_check CHECK(
    (member_refresh_generation IS NULL AND member_count IS NULL AND member_refreshed_at IS NULL)
    OR
    (member_refresh_generation IS NOT NULL AND member_count IS NOT NULL
      AND member_refreshed_at IS NOT NULL)
  );

ALTER TABLE platform.semantic_dimensions
  ADD CONSTRAINT semantic_dimensions_refresh_identity_key
    UNIQUE(id,dataset_id,dataset_version_id,field_id,tenant_id);

ALTER TABLE platform.dataset_materializations
  ADD CONSTRAINT dataset_materializations_refresh_identity_key
    UNIQUE(id,dataset_id,dataset_version_id,tenant_id);

CREATE TABLE platform.dimension_member_refresh_jobs(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  dimension_id uuid NOT NULL,
  dimension_version bigint NOT NULL CHECK(dimension_version>0),
  dataset_id uuid NOT NULL,
  dataset_version_id uuid NOT NULL,
  field_id text NOT NULL CHECK(length(field_id) BETWEEN 1 AND 256),
  field_code text NOT NULL CHECK(
    length(field_code) BETWEEN 1 AND 63
    AND field_code ~ '^[A-Za-z][A-Za-z0-9_]*$'
  ),
  member_index_policy text NOT NULL
    CHECK(member_index_policy IN ('FULL','EXACT_ONLY','NONE')),
  materialization_id uuid,
  refresh_generation uuid NOT NULL DEFAULT gen_random_uuid(),
  status text NOT NULL CHECK(
    status IN ('QUEUED','RUNNING','SUCCEEDED','FAILED','SKIPPED')
  ),
  max_members integer NOT NULL CHECK(max_members BETWEEN 1 AND 1000000),
  timeout_seconds integer NOT NULL CHECK(timeout_seconds BETWEEN 1 AND 300),
  request_hash text NOT NULL CHECK(request_hash ~ '^[0-9a-f]{64}$'),
  idempotency_key text NOT NULL CHECK(idempotency_key ~ '^[0-9a-f]{64}$'),
  requested_by uuid NOT NULL,
  attempt integer NOT NULL DEFAULT 0 CHECK(attempt>=0),
  max_attempts integer NOT NULL DEFAULT 3 CHECK(max_attempts BETWEEN 1 AND 10),
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  lease_owner text NOT NULL DEFAULT '' CHECK(length(lease_owner)<=128),
  lease_token uuid,
  lease_expires_at timestamptz,
  member_count bigint CHECK(member_count IS NULL OR member_count>=0),
  result_code text NOT NULL DEFAULT '' CHECK(length(result_code)<=128),
  error_message text NOT NULL DEFAULT '' CHECK(length(error_message)<=1024),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  started_at timestamptz,
  completed_at timestamptz,
  CONSTRAINT dimension_member_refresh_jobs_dimension_fk
    FOREIGN KEY(dimension_id,dataset_id,dataset_version_id,field_id,tenant_id)
    REFERENCES platform.semantic_dimensions(
      id,dataset_id,dataset_version_id,field_id,tenant_id
    ) ON DELETE RESTRICT,
  CONSTRAINT dimension_member_refresh_jobs_materialization_fk
    FOREIGN KEY(materialization_id,dataset_id,dataset_version_id,tenant_id)
    REFERENCES platform.dataset_materializations(
      id,dataset_id,dataset_version_id,tenant_id
    ) ON DELETE RESTRICT,
  CONSTRAINT dimension_member_refresh_jobs_requested_by_fk
    FOREIGN KEY(requested_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dimension_member_refresh_jobs_attempt_bounds_check
    CHECK(attempt<=max_attempts),
  CONSTRAINT dimension_member_refresh_jobs_policy_shape_check CHECK(
    (member_index_policy='FULL' AND materialization_id IS NOT NULL)
    OR (member_index_policy IN ('EXACT_ONLY','NONE') AND materialization_id IS NULL)
  ),
  CONSTRAINT dimension_member_refresh_jobs_status_shape_check CHECK(
    (status='QUEUED' AND member_index_policy='FULL'
      AND ((attempt=0 AND started_at IS NULL) OR (attempt>0 AND started_at IS NOT NULL))
      AND completed_at IS NULL
      AND lease_owner='' AND lease_token IS NULL AND lease_expires_at IS NULL
      AND member_count IS NULL AND result_code='' AND error_message='')
    OR
    (status='RUNNING' AND member_index_policy='FULL' AND attempt>0
      AND started_at IS NOT NULL AND completed_at IS NULL
      AND lease_owner<>'' AND lease_token IS NOT NULL AND lease_expires_at IS NOT NULL
      AND member_count IS NULL AND result_code='' AND error_message='')
    OR
    (status='SUCCEEDED' AND member_index_policy='FULL' AND attempt>0
      AND started_at IS NOT NULL AND completed_at IS NOT NULL
      AND completed_at>=started_at
      AND lease_owner='' AND lease_token IS NULL AND lease_expires_at IS NULL
      AND member_count IS NOT NULL AND result_code='' AND error_message='')
    OR
    (status='FAILED' AND member_index_policy='FULL' AND attempt>0
      AND started_at IS NOT NULL AND completed_at IS NOT NULL
      AND completed_at>=started_at
      AND lease_owner='' AND lease_token IS NULL AND lease_expires_at IS NULL
      AND member_count IS NULL AND btrim(result_code)<>'')
    OR
    (status='SKIPPED' AND member_index_policy IN ('EXACT_ONLY','NONE') AND attempt=0
      AND started_at IS NULL AND completed_at IS NOT NULL
      AND lease_owner='' AND lease_token IS NULL AND lease_expires_at IS NULL
      AND member_count IS NULL AND btrim(result_code)<>'' AND error_message='')
  ),
  CONSTRAINT dimension_member_refresh_jobs_idempotency_key
    UNIQUE(tenant_id,dimension_id,idempotency_key),
  CONSTRAINT dimension_member_refresh_jobs_identity_tenant_key
    UNIQUE(id,tenant_id),
  CONSTRAINT dimension_member_refresh_jobs_identity_dimension_tenant_key
    UNIQUE(id,dimension_id,tenant_id)
);

ALTER TABLE platform.dimension_members
  ADD COLUMN refresh_generation uuid,
  ADD COLUMN last_refresh_job_id uuid,
  ADD CONSTRAINT dimension_members_refresh_job_fk
    FOREIGN KEY(last_refresh_job_id,dimension_id,tenant_id)
    REFERENCES platform.dimension_member_refresh_jobs(id,dimension_id,tenant_id)
    ON DELETE SET NULL (last_refresh_job_id);

ALTER TABLE platform.semantic_dimensions
  ADD COLUMN last_member_refresh_job_id uuid,
  ADD CONSTRAINT semantic_dimensions_member_refresh_job_fk
    FOREIGN KEY(last_member_refresh_job_id,id,tenant_id)
    REFERENCES platform.dimension_member_refresh_jobs(id,dimension_id,tenant_id)
    ON DELETE SET NULL (last_member_refresh_job_id),
  DROP CONSTRAINT semantic_dimensions_member_refresh_shape_check,
  ADD CONSTRAINT semantic_dimensions_member_refresh_shape_check CHECK(
    (member_refresh_generation IS NULL AND member_count IS NULL
      AND member_refreshed_at IS NULL AND last_member_refresh_job_id IS NULL)
    OR
    (member_refresh_generation IS NOT NULL AND member_count IS NOT NULL
      AND member_refreshed_at IS NOT NULL AND last_member_refresh_job_id IS NOT NULL)
  );

CREATE INDEX dimension_member_refresh_jobs_claim_idx
  ON platform.dimension_member_refresh_jobs(
    tenant_id,status,next_attempt_at,lease_expires_at,created_at
  ) WHERE status IN ('QUEUED','RUNNING');
CREATE INDEX dimension_member_refresh_jobs_dimension_time_idx
  ON platform.dimension_member_refresh_jobs(tenant_id,dimension_id,created_at DESC);
CREATE UNIQUE INDEX dimension_member_refresh_jobs_one_active_idx
  ON platform.dimension_member_refresh_jobs(tenant_id,dimension_id)
  WHERE status IN ('QUEUED','RUNNING');
CREATE INDEX dimension_members_refresh_generation_idx
  ON platform.dimension_members(tenant_id,dimension_id,refresh_generation);
CREATE INDEX dimension_metric_compatibility_dimension_status_idx
  ON platform.dimension_metric_compatibility(
    tenant_id,dimension_id,status,metric_version_id
  );

-- refresh_generation、last_seen_at 和 last_refresh_job_id 是运行元数据；
-- 仅这些字段推进时不应让每个稳定成员重复生成语义文档。
DROP TRIGGER dimension_members_enqueue_change ON platform.dimension_members;
CREATE TRIGGER dimension_members_enqueue_change
AFTER INSERT OR DELETE OR UPDATE OF
  member_key,canonical_label,normalized_value,status,valid_from,valid_to
ON platform.dimension_members
FOR EACH ROW EXECUTE FUNCTION platform.enqueue_dimension_change();

-- 000060 已强制绑定已发布 DWS 版本。这里收紧到可作为维度的精确字段角色，
-- 并保留 SECURITY DEFINER，以免 RLS 让约束检查误判为不存在。
CREATE OR REPLACE FUNCTION platform.enforce_semantic_dimension_dws()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
BEGIN
  IF TG_OP='UPDATE' AND NEW.status='DEPRECATED' THEN
    RETURN NEW;
  END IF;
  PERFORM 1
  FROM platform.dataset_versions AS version
  JOIN platform.datasets AS dataset
    ON dataset.id=version.dataset_id
    AND dataset.tenant_id=version.tenant_id
  JOIN platform.dataset_fields AS field
    ON field.tenant_id=version.tenant_id
    AND field.dataset_version_id=version.id
  WHERE version.id=NEW.dataset_version_id
    AND version.dataset_id=NEW.dataset_id
    AND version.tenant_id=NEW.tenant_id
    AND version.layer='DWS'
    AND version.status='PUBLISHED'
    AND dataset.layer='DWS'
    AND dataset.deleted_at IS NULL
    AND field.field_id=NEW.field_id
    AND field.field_role IN ('DIMENSION','ATTRIBUTE','TIME','IDENTIFIER')
  FOR SHARE OF version,dataset,field;
  IF NOT FOUND THEN
    RAISE EXCEPTION '语义维度只能绑定已发布 DWS 的非度量字段'
      USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enforce_semantic_dimension_dws() FROM PUBLIC;

DROP TRIGGER semantic_dimensions_require_published_dws
  ON platform.semantic_dimensions;
CREATE TRIGGER semantic_dimensions_require_published_dws
BEFORE INSERT OR UPDATE OF dataset_id,dataset_version_id,field_id,status
ON platform.semantic_dimensions
FOR EACH ROW EXECUTE FUNCTION platform.enforce_semantic_dimension_dws();

CREATE OR REPLACE FUNCTION platform.enforce_semantic_dimension_identity()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF TG_OP='DELETE' THEN
    RAISE EXCEPTION '语义维度必须停用而不能删除' USING ERRCODE='23514';
  END IF;
  IF NEW.id IS DISTINCT FROM OLD.id
    OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.dataset_id IS DISTINCT FROM OLD.dataset_id
    OR NEW.dataset_version_id IS DISTINCT FROM OLD.dataset_version_id
    OR NEW.field_id IS DISTINCT FROM OLD.field_id
    OR NEW.created_by IS DISTINCT FROM OLD.created_by
    OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION '语义维度的数据集版本与字段身份不可修改'
      USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

CREATE TRIGGER semantic_dimensions_enforce_identity
BEFORE UPDATE OR DELETE ON platform.semantic_dimensions
FOR EACH ROW EXECUTE FUNCTION platform.enforce_semantic_dimension_identity();

-- 兼容关系的主体、乐观版本和终态由数据库再次封口。API 先做同样的
-- 校验以返回友好错误；触发器负责阻止绕开 API 写入可自动回答的脏关系。
CREATE OR REPLACE FUNCTION platform.enforce_dimension_metric_compatibility()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
BEGIN
  IF TG_OP='INSERT' THEN
    IF NEW.version<>1 OR NEW.created_by IS NULL OR NEW.updated_by IS NULL THEN
      RAISE EXCEPTION '兼容关系必须携带创建人与初始版本'
        USING ERRCODE='23514';
    END IF;
  ELSE
    IF ROW(
      NEW.id,NEW.tenant_id,NEW.dimension_id,NEW.metric_id,
      NEW.metric_version_id,NEW.metric_dataset_version_id,
      NEW.created_by,NEW.created_at
    ) IS DISTINCT FROM ROW(
      OLD.id,OLD.tenant_id,OLD.dimension_id,OLD.metric_id,
      OLD.metric_version_id,OLD.metric_dataset_version_id,
      OLD.created_by,OLD.created_at
    ) THEN
      RAISE EXCEPTION '兼容关系主体与创建身份不可修改'
        USING ERRCODE='23514';
    END IF;
    IF OLD.status<>'PROPOSED' THEN
      RAISE EXCEPTION '已决策的兼容关系不可再次修改'
        USING ERRCODE='23514';
    END IF;
    IF NEW.version<>OLD.version+1 OR NEW.updated_by IS NULL THEN
      RAISE EXCEPTION '兼容关系更新必须推进版本并记录修改人'
        USING ERRCODE='23514';
    END IF;
    IF NEW.status NOT IN ('PROPOSED','VERIFIED','REJECTED') THEN
      RAISE EXCEPTION '非法的兼容关系状态转换' USING ERRCODE='23514';
    END IF;
  END IF;

  IF NEW.status='VERIFIED' THEN
    PERFORM 1
    FROM platform.semantic_dimensions AS dimension
    JOIN platform.dataset_versions AS dimension_version
      ON dimension_version.tenant_id=dimension.tenant_id
      AND dimension_version.id=dimension.dataset_version_id
      AND dimension_version.dataset_id=dimension.dataset_id
    JOIN platform.metric_versions AS metric_version
      ON metric_version.tenant_id=dimension.tenant_id
      AND metric_version.id=NEW.metric_version_id
      AND metric_version.metric_id=NEW.metric_id
      AND metric_version.dataset_version_id=NEW.metric_dataset_version_id
    JOIN platform.dataset_versions AS metric_dataset_version
      ON metric_dataset_version.tenant_id=metric_version.tenant_id
      AND metric_dataset_version.id=metric_version.dataset_version_id
      AND metric_dataset_version.dataset_id=metric_version.dataset_id
    JOIN platform.metrics AS metric
      ON metric.tenant_id=metric_version.tenant_id
      AND metric.id=metric_version.metric_id
    WHERE dimension.id=NEW.dimension_id
      AND dimension.tenant_id=NEW.tenant_id
      AND dimension.status='PUBLISHED'
      AND dimension_version.layer='DWS'
      AND dimension_version.status='PUBLISHED'
      AND metric_dataset_version.layer='DWS'
      AND metric_dataset_version.status='PUBLISHED'
      AND metric_version.status='PUBLISHED'
      AND metric.status='PUBLISHED'
      AND metric.current_published_version_id=metric_version.id
      AND metric.deleted_at IS NULL
      AND EXISTS(
        SELECT 1
        FROM platform.dataset_materializations AS materialization
        WHERE materialization.tenant_id=dimension.tenant_id
          AND materialization.dataset_id=dimension.dataset_id
          AND materialization.dataset_version_id=dimension.dataset_version_id
          AND materialization.layer='DWS'
          AND materialization.status='ACTIVE'
      )
      AND EXISTS(
        SELECT 1
        FROM platform.dataset_materializations AS materialization
        WHERE materialization.tenant_id=metric_version.tenant_id
          AND materialization.dataset_id=metric_version.dataset_id
          AND materialization.dataset_version_id=metric_version.dataset_version_id
          AND materialization.layer='DWS'
          AND materialization.status='ACTIVE'
      )
    FOR SHARE OF dimension,dimension_version,metric_version,
      metric_dataset_version,metric;
    IF NOT FOUND THEN
      RAISE EXCEPTION 'VERIFIED 关系必须绑定可用的已发布 DWS 维度、指标与物化'
        USING ERRCODE='23514';
    END IF;
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enforce_dimension_metric_compatibility()
  FROM PUBLIC;

CREATE TRIGGER dimension_metric_compatibility_enforce_lifecycle
BEFORE INSERT OR UPDATE ON platform.dimension_metric_compatibility
FOR EACH ROW EXECUTE FUNCTION platform.enforce_dimension_metric_compatibility();

CREATE OR REPLACE FUNCTION platform.enforce_dimension_member_refresh_source()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  current_dimension_version bigint;
  current_dimension_status text;
  current_policy text;
  current_field_code text;
BEGIN
  SELECT dimension.version,dimension.status,dimension.member_index_policy,
    field.field_code::text
  INTO current_dimension_version,current_dimension_status,current_policy,current_field_code
  FROM platform.semantic_dimensions AS dimension
  JOIN platform.dataset_fields AS field
    ON field.tenant_id=dimension.tenant_id
    AND field.dataset_version_id=dimension.dataset_version_id
    AND field.field_id=dimension.field_id
    AND field.field_role IN ('DIMENSION','ATTRIBUTE','TIME','IDENTIFIER')
  WHERE dimension.id=NEW.dimension_id
    AND dimension.dataset_id=NEW.dataset_id
    AND dimension.dataset_version_id=NEW.dataset_version_id
    AND dimension.field_id=NEW.field_id
    AND dimension.tenant_id=NEW.tenant_id
  FOR SHARE OF dimension;
  IF NOT FOUND
    OR current_dimension_version<>NEW.dimension_version
    OR current_dimension_status<>'PUBLISHED'
    OR current_policy<>NEW.member_index_policy
    OR current_field_code<>NEW.field_code THEN
    RAISE EXCEPTION '成员刷新任务与当前已发布维度不一致'
      USING ERRCODE='23514';
  END IF;
  IF NEW.member_index_policy='FULL' THEN
    PERFORM 1
    FROM platform.dataset_materializations
    WHERE id=NEW.materialization_id
      AND dataset_id=NEW.dataset_id
      AND dataset_version_id=NEW.dataset_version_id
      AND tenant_id=NEW.tenant_id
      AND layer='DWS' AND status='ACTIVE'
    FOR SHARE;
    IF NOT FOUND THEN
      RAISE EXCEPTION 'FULL 成员刷新必须固定精确的 ACTIVE DWS 物化'
        USING ERRCODE='23514';
    END IF;
  ELSIF NEW.materialization_id IS NOT NULL THEN
    RAISE EXCEPTION '非 FULL 策略不能携带物化身份' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enforce_dimension_member_refresh_source() FROM PUBLIC;

CREATE TRIGGER dimension_member_refresh_jobs_enforce_source
BEFORE INSERT ON platform.dimension_member_refresh_jobs
FOR EACH ROW EXECUTE FUNCTION platform.enforce_dimension_member_refresh_source();

CREATE OR REPLACE FUNCTION platform.enforce_dimension_member_refresh_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF TG_OP='DELETE' THEN
    RAISE EXCEPTION '成员刷新任务不可删除';
  END IF;
  IF ROW(
    NEW.id,NEW.tenant_id,NEW.dimension_id,NEW.dimension_version,
    NEW.dataset_id,NEW.dataset_version_id,NEW.field_id,NEW.field_code,
    NEW.member_index_policy,NEW.materialization_id,NEW.refresh_generation,
    NEW.max_members,NEW.timeout_seconds,NEW.request_hash,NEW.idempotency_key,
    NEW.requested_by,NEW.max_attempts,NEW.created_at
  ) IS DISTINCT FROM ROW(
    OLD.id,OLD.tenant_id,OLD.dimension_id,OLD.dimension_version,
    OLD.dataset_id,OLD.dataset_version_id,OLD.field_id,OLD.field_code,
    OLD.member_index_policy,OLD.materialization_id,OLD.refresh_generation,
    OLD.max_members,OLD.timeout_seconds,OLD.request_hash,OLD.idempotency_key,
    OLD.requested_by,OLD.max_attempts,OLD.created_at
  ) THEN
    RAISE EXCEPTION '成员刷新任务身份与边界不可修改' USING ERRCODE='23514';
  END IF;
  IF OLD.status='QUEUED' AND NEW.status='RUNNING' THEN
    IF NEW.attempt<>OLD.attempt+1 THEN
      RAISE EXCEPTION '成员刷新 claim 必须推进 attempt' USING ERRCODE='23514';
    END IF;
  ELSIF OLD.status='RUNNING' AND NEW.status='RUNNING' THEN
    IF OLD.lease_expires_at>now() OR NEW.attempt<>OLD.attempt+1 THEN
      RAISE EXCEPTION '只能重新认领已过期的成员刷新租约' USING ERRCODE='23514';
    END IF;
  ELSIF OLD.status='RUNNING' AND NEW.status IN ('QUEUED','SUCCEEDED','FAILED') THEN
    IF NEW.attempt<>OLD.attempt THEN
      RAISE EXCEPTION '完成或重试不能改写 attempt' USING ERRCODE='23514';
    END IF;
  ELSE
    RAISE EXCEPTION '非法的成员刷新状态转换：% -> %',OLD.status,NEW.status
      USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

CREATE TRIGGER dimension_member_refresh_jobs_enforce_transition
BEFORE UPDATE OR DELETE ON platform.dimension_member_refresh_jobs
FOR EACH ROW EXECUTE FUNCTION platform.enforce_dimension_member_refresh_transition();

CREATE TRIGGER dimension_member_aliases_set_updated_at
BEFORE UPDATE ON platform.dimension_member_aliases
FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();

CREATE TRIGGER dimension_member_refresh_jobs_set_updated_at
BEFORE UPDATE ON platform.dimension_member_refresh_jobs
FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();

ALTER TABLE platform.dimension_member_refresh_jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.dimension_member_refresh_jobs FORCE ROW LEVEL SECURITY;
CREATE POLICY dimension_member_refresh_jobs_tenant_isolation
  ON platform.dimension_member_refresh_jobs
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

COMMENT ON TABLE platform.dimension_member_refresh_jobs IS
  '固定维度版本、精确 DWS 物化与安全边界的异步成员去重刷新任务';
COMMENT ON COLUMN platform.dimension_member_refresh_jobs.refresh_generation IS
  '一次完整成功快照的栅栏；只在同事务完整写入后推进维度当前代';
COMMENT ON COLUMN platform.dimension_member_aliases.version IS
  '人工别名编辑的显式乐观锁；历史编码如 690 由数据管理而非代码硬编码';
