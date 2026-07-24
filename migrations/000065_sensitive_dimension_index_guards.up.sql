-- 敏感维度成员只能保留在租户内的精确匹配索引中，不能做 FULL 扫描，
-- 也不能把成员值发送到外部向量服务。应用层先返回友好错误；这里封住
-- 绕过 API 的直接写入，并安全收敛升级前已经存在的不合规状态。

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
  ELSIF OLD.status IN ('QUEUED','RUNNING') AND NEW.status='SKIPPED' THEN
    IF NEW.attempt<>OLD.attempt THEN
      RAISE EXCEPTION '隐私策略终止任务不能改写 attempt' USING ERRCODE='23514';
    END IF;
  ELSE
    RAISE EXCEPTION '非法的成员刷新状态转换：% -> %',OLD.status,NEW.status
      USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

ALTER TABLE platform.dimension_member_refresh_jobs
  DROP CONSTRAINT dimension_member_refresh_jobs_status_shape_check;

ALTER TABLE platform.dimension_member_refresh_jobs
  ADD CONSTRAINT dimension_member_refresh_jobs_status_shape_check CHECK(
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
    (status='SKIPPED'
      AND (
        (member_index_policy IN ('EXACT_ONLY','NONE')
          AND attempt=0 AND started_at IS NULL)
        OR
        (member_index_policy='FULL'
          AND ((attempt=0 AND started_at IS NULL)
               OR (attempt>0 AND started_at IS NOT NULL)))
      )
      AND completed_at IS NOT NULL
      AND (started_at IS NULL OR completed_at>=started_at)
      AND lease_owner='' AND lease_token IS NULL AND lease_expires_at IS NULL
      AND member_count IS NULL AND btrim(result_code)<>'' AND error_message='')
  );

-- 先收敛历史不合规状态，再启用不可绕过的约束。
UPDATE platform.semantic_dimensions
SET member_index_policy='EXACT_ONLY',
    definition_hash=encode(public.digest(
      convert_to(
        concat_ws(E'\x1f',dataset_id::text,dataset_version_id::text,field_id,
          code::text,name,description,dimension_type,'EXACT_ONLY',
          high_cardinality::text,sensitive::text,status
        ),
        'UTF8'
      ),
      'sha256'
    ),'hex'),
    version=version+1,
    updated_at=now()
WHERE sensitive AND member_index_policy='FULL';

UPDATE platform.dimension_members AS member
SET status='DEPRECATED',updated_at=now()
FROM platform.semantic_dimensions AS dimension
WHERE dimension.id=member.dimension_id
  AND dimension.tenant_id=member.tenant_id
  AND (dimension.sensitive OR dimension.member_index_policy<>'FULL'
       OR dimension.status<>'PUBLISHED')
  AND member.status='ACTIVE';

UPDATE platform.dimension_member_refresh_jobs AS job
SET status='SKIPPED',
    result_code=CASE
      WHEN dimension.sensitive THEN 'SENSITIVE_DIMENSION_INDEX_DISABLED'
      ELSE 'MEMBER_INDEX_POLICY_CHANGED'
    END,
    error_message='',lease_owner='',lease_token=NULL,lease_expires_at=NULL,
    completed_at=now(),updated_at=now()
FROM platform.semantic_dimensions AS dimension
WHERE dimension.id=job.dimension_id
  AND dimension.tenant_id=job.tenant_id
  AND (dimension.sensitive OR dimension.member_index_policy<>'FULL'
       OR dimension.status<>'PUBLISHED')
  AND job.status IN ('QUEUED','RUNNING');

-- 成员值的租户内精确/倒排检索直接使用 dimension_members/aliases。语义文档
-- 并非该路径所需，删除历史正文可同时关闭普通语义检索与运维查询的枚举面。
DELETE FROM platform.semantic_documents
WHERE subject_type='DIMENSION_MEMBER';

CREATE OR REPLACE FUNCTION platform.reject_dimension_member_semantic_document()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF NEW.subject_type='DIMENSION_MEMBER' THEN
    RAISE EXCEPTION '维度成员只能使用租户内精确索引，禁止创建语义文档'
      USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

CREATE TRIGGER semantic_documents_reject_dimension_member
BEFORE INSERT OR UPDATE OF subject_type,dimension_member_id,document
ON platform.semantic_documents
FOR EACH ROW EXECUTE FUNCTION platform.reject_dimension_member_semantic_document();

ALTER TABLE platform.semantic_dimensions
  ADD CONSTRAINT semantic_dimensions_sensitive_index_policy_check
  CHECK(NOT sensitive OR member_index_policy<>'FULL') NOT VALID;

ALTER TABLE platform.semantic_dimensions
  VALIDATE CONSTRAINT semantic_dimensions_sensitive_index_policy_check;

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
  current_sensitive boolean;
BEGIN
  SELECT dimension.version,dimension.status,dimension.member_index_policy,
    field.field_code::text,dimension.sensitive
  INTO current_dimension_version,current_dimension_status,current_policy,
    current_field_code,current_sensitive
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
    IF current_sensitive THEN
      RAISE EXCEPTION '敏感维度禁止 FULL 成员扫描'
        USING ERRCODE='23514';
    END IF;
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

CREATE OR REPLACE FUNCTION platform.apply_dimension_index_privacy_guard()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
BEGIN
  IF NEW.sensitive OR NEW.member_index_policy<>'FULL' OR NEW.status<>'PUBLISHED' THEN
    UPDATE platform.dimension_members
    SET status='DEPRECATED',updated_at=now()
    WHERE tenant_id=NEW.tenant_id
      AND dimension_id=NEW.id
      AND status='ACTIVE';

    UPDATE platform.dimension_member_refresh_jobs
    SET status='SKIPPED',
        result_code=CASE
          WHEN NEW.sensitive THEN 'SENSITIVE_DIMENSION_INDEX_DISABLED'
          ELSE 'MEMBER_INDEX_POLICY_CHANGED'
        END,
        error_message='',lease_owner='',lease_token=NULL,lease_expires_at=NULL,
        completed_at=now(),updated_at=now()
    WHERE tenant_id=NEW.tenant_id
      AND dimension_id=NEW.id
      AND status IN ('QUEUED','RUNNING');

    DELETE FROM platform.semantic_documents
    WHERE tenant_id=NEW.tenant_id
      AND dimension_id=NEW.id
      AND subject_type='DIMENSION_MEMBER';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.apply_dimension_index_privacy_guard() FROM PUBLIC;

CREATE TRIGGER semantic_dimensions_apply_index_privacy_guard
AFTER UPDATE OF member_index_policy,sensitive,status
ON platform.semantic_dimensions
FOR EACH ROW EXECUTE FUNCTION platform.apply_dimension_index_privacy_guard();

COMMENT ON CONSTRAINT semantic_dimensions_sensitive_index_policy_check
  ON platform.semantic_dimensions IS
  '敏感维度不得执行或持久化 FULL 成员扫描；只允许租户内精确匹配或禁用成员索引';
