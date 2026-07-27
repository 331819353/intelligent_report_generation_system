-- 人工再次点击“自动识别指标与维度”时，应能恢复同一物化上已经耗尽重试的
-- 画像任务。只允许 FAILED -> QUEUED 的显式人工重放，并清空上一轮运行态；
-- 任务身份、物化快照和资源边界仍由既有不可变校验保护。
CREATE OR REPLACE FUNCTION platform.enforce_dimension_profile_job_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF TG_OP='DELETE' THEN
    RAISE EXCEPTION '维度画像任务不可删除' USING ERRCODE='23514';
  END IF;
  IF ROW(
    NEW.id,NEW.tenant_id,NEW.dataset_id,NEW.dataset_version_id,
    NEW.schema_hash,NEW.materialization_id,NEW.materialization_snapshot_hash,
    NEW.expected_row_count,NEW.field_id,NEW.field_code,NEW.field_role,
    NEW.canonical_type,NEW.semantic_type,NEW.profile_version,NEW.policy_version,
    NEW.distinct_cap,NEW.high_ratio_threshold,NEW.high_ratio_min_non_null,
    NEW.timeout_seconds,NEW.work_mem_kb,NEW.temp_file_limit_kb,
    NEW.max_attempts,NEW.requested_by,NEW.created_at
  ) IS DISTINCT FROM ROW(
    OLD.id,OLD.tenant_id,OLD.dataset_id,OLD.dataset_version_id,
    OLD.schema_hash,OLD.materialization_id,OLD.materialization_snapshot_hash,
    OLD.expected_row_count,OLD.field_id,OLD.field_code,OLD.field_role,
    OLD.canonical_type,OLD.semantic_type,OLD.profile_version,OLD.policy_version,
    OLD.distinct_cap,OLD.high_ratio_threshold,OLD.high_ratio_min_non_null,
    OLD.timeout_seconds,OLD.work_mem_kb,OLD.temp_file_limit_kb,
    OLD.max_attempts,OLD.requested_by,OLD.created_at
  ) THEN
    RAISE EXCEPTION '维度画像任务身份与资源边界不可修改' USING ERRCODE='23514';
  END IF;

  IF OLD.status='QUEUED' AND NEW.status='RUNNING' THEN
    IF NEW.attempt<>OLD.attempt+1 THEN
      RAISE EXCEPTION '维度画像 claim 必须推进 attempt' USING ERRCODE='23514';
    END IF;
  ELSIF OLD.status='RUNNING' AND NEW.status='RUNNING' THEN
    IF NEW.attempt=OLD.attempt THEN
      IF NEW.lease_owner<>OLD.lease_owner
        OR NEW.lease_token IS DISTINCT FROM OLD.lease_token
        OR NEW.lease_expires_at<=OLD.lease_expires_at THEN
        RAISE EXCEPTION '维度画像 heartbeat 必须保持栅栏并延长租约'
          USING ERRCODE='23514';
      END IF;
    ELSIF OLD.lease_expires_at>now() OR NEW.attempt<>OLD.attempt+1 THEN
      RAISE EXCEPTION '只能重新认领已过期的维度画像租约'
        USING ERRCODE='23514';
    END IF;
  ELSIF OLD.status='RUNNING'
    AND NEW.status IN (
      'QUEUED','SUCCEEDED','SKIPPED_POLICY','FAILED','STALE'
    ) THEN
    IF NEW.attempt<>OLD.attempt THEN
      RAISE EXCEPTION '维度画像完成或重试不能改写 attempt'
        USING ERRCODE='23514';
    END IF;
  ELSIF OLD.status IN ('QUEUED','SUCCEEDED','SKIPPED_POLICY')
    AND NEW.status='STALE' THEN
    IF NEW.attempt<>OLD.attempt THEN
      RAISE EXCEPTION '维度画像失效不能改写 attempt' USING ERRCODE='23514';
    END IF;
  ELSIF OLD.status='FAILED' AND NEW.status='QUEUED' THEN
    IF NEW.attempt<>0 OR NEW.started_at IS NOT NULL
      OR NEW.completed_at IS NOT NULL OR NEW.result_code<>'' THEN
      RAISE EXCEPTION '维度画像人工重放必须清空上一轮运行状态'
        USING ERRCODE='23514';
    END IF;
  ELSE
    RAISE EXCEPTION '非法的维度画像状态转换：% -> %',OLD.status,NEW.status
      USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION platform.trigger_manual_dws_dimension_identification(
  actor_id uuid
)
RETURNS TABLE(
  eligible_count bigint,
  profiled_count bigint
)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  target record;
  replayed record;
BEGIN
  IF platform.current_tenant_id() IS NULL OR NOT EXISTS(
    SELECT 1 FROM platform.users AS actor
    WHERE actor.tenant_id=platform.current_tenant_id()
      AND actor.id=actor_id
      AND actor.status='ACTIVE'
      AND actor.deleted_at IS NULL
  ) THEN
    RAISE EXCEPTION '自动识别操作者无效' USING ERRCODE='42501';
  END IF;

  eligible_count := 0;
  profiled_count := 0;
  FOR target IN
    SELECT dataset.id AS dataset_id,version.id AS dataset_version_id,
      materialization.id AS materialization_id
    FROM platform.datasets AS dataset
    JOIN platform.dataset_versions AS version
      ON version.tenant_id=dataset.tenant_id
     AND version.dataset_id=dataset.id
     AND version.id=dataset.current_published_version_id
     AND version.layer='DWS'
     AND version.status='PUBLISHED'
    JOIN platform.dataset_materializations AS materialization
      ON materialization.tenant_id=version.tenant_id
     AND materialization.dataset_id=version.dataset_id
     AND materialization.dataset_version_id=version.id
     AND materialization.layer='DWS'
     AND materialization.status='ACTIVE'
     AND materialization.schema_hash=version.schema_hash
    WHERE dataset.tenant_id=platform.current_tenant_id()
      AND dataset.status='PUBLISHED'
      AND dataset.deleted_at IS NULL
    ORDER BY dataset.code,materialization.activated_at DESC NULLS LAST,
      materialization.id
  LOOP
    eligible_count := eligible_count+1;
    PERFORM platform.materialize_dws_dimension_survey(
      platform.current_tenant_id(),target.dataset_id,
      target.dataset_version_id,target.materialization_id
    );
    PERFORM platform.enqueue_dws_dimension_profiles(
      platform.current_tenant_id(),target.dataset_id,
      target.dataset_version_id,target.materialization_id
    );

    FOR replayed IN
      UPDATE platform.dimension_profile_jobs AS profile
      SET status='QUEUED',attempt=0,next_attempt_at=now(),
        row_count=NULL,non_null_count=NULL,null_count=NULL,
        distinct_count=NULL,distinct_overflow=false,distinct_ratio=NULL,
        risk_high_cardinality=false,recommended_member_index_policy='',
        evidence_hash='',result_code='',started_at=NULL,completed_at=NULL,
        lease_owner='',lease_token=NULL,lease_expires_at=NULL
      WHERE profile.tenant_id=platform.current_tenant_id()
        AND profile.materialization_id=target.materialization_id
        AND profile.status='FAILED'
      RETURNING profile.id
    LOOP
      INSERT INTO platform.audit_logs(
        tenant_id,actor_user_id,action,resource_type,resource_id,detail
      ) VALUES (
        platform.current_tenant_id(),actor_id,
        'REPLAY_DIMENSION_PROFILE','DIMENSION_PROFILE_JOB',
        replayed.id::text,
        jsonb_build_object(
          'materializationId',target.materialization_id::text,
          'reason','MANUAL_IDENTIFICATION_REPLAY'
        )
      );
    END LOOP;
    profiled_count := profiled_count+1;
  END LOOP;
  RETURN NEXT;
END
$$;

REVOKE ALL ON FUNCTION
  platform.trigger_manual_dws_dimension_identification(uuid)
FROM PUBLIC;

COMMENT ON FUNCTION
  platform.trigger_manual_dws_dimension_identification(uuid) IS
  '人工自动识别入口：为当前 ACTIVE DWS 建立维度候选和画像任务，并重放同一物化上已失败的画像；不自动批准治理对象';
