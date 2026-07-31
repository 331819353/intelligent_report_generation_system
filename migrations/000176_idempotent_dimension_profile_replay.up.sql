-- 同一 ACTIVE DWS 物化的画像已经存在时，全局资产同步不得再次调用底层
-- 调度函数。底层函数会先失效旧 FULL 成员快照；重复调用随后又因画像任务
-- 唯一键而无法补建任务，最终会让仍有有效 FULL 画像证据的维度停在 NONE。
CREATE OR REPLACE FUNCTION platform.enqueue_current_dws_dimension_profiles(
  actor_id uuid
)
RETURNS bigint
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  target record;
  enqueued_dataset_count bigint := 0;
BEGIN
  IF platform.current_tenant_id() IS NULL OR NOT EXISTS(
    SELECT 1
    FROM platform.users AS actor
    WHERE actor.tenant_id=platform.current_tenant_id()
      AND actor.id=actor_id
      AND actor.status='ACTIVE'
      AND actor.deleted_at IS NULL
  ) THEN
    RAISE EXCEPTION '数据资产同步操作者无效' USING ERRCODE='42501';
  END IF;

  FOR target IN
    SELECT
      materialization.dataset_id,
      materialization.dataset_version_id,
      materialization.id AS materialization_id
    FROM platform.dataset_materializations AS materialization
    JOIN platform.datasets AS dataset
      ON dataset.tenant_id=materialization.tenant_id
     AND dataset.id=materialization.dataset_id
     AND dataset.current_published_version_id=materialization.dataset_version_id
    JOIN platform.dataset_versions AS version
      ON version.tenant_id=materialization.tenant_id
     AND version.dataset_id=materialization.dataset_id
     AND version.id=materialization.dataset_version_id
    WHERE materialization.tenant_id=platform.current_tenant_id()
      AND materialization.layer='DWS'
      AND materialization.status='ACTIVE'
      AND dataset.status='PUBLISHED'
      AND dataset.deleted_at IS NULL
      AND version.status='PUBLISHED'
      AND version.layer='DWS'
      AND NOT EXISTS(
        SELECT 1
        FROM platform.dimension_profile_jobs AS profile
        WHERE profile.tenant_id=materialization.tenant_id
          AND profile.materialization_id=materialization.id
          AND profile.materialization_snapshot_hash=
            materialization.snapshot_hash
          AND profile.profile_version='dws-dimension-profile-v1'
          AND profile.policy_version='dimension-member-policy-v1'
      )
    ORDER BY materialization.dataset_id,materialization.id
  LOOP
    PERFORM platform.enqueue_dws_dimension_profiles(
      platform.current_tenant_id(),
      target.dataset_id,
      target.dataset_version_id,
      target.materialization_id
    );
    enqueued_dataset_count := enqueued_dataset_count+1;
  END LOOP;

  RETURN enqueued_dataset_count;
END
$$;

-- 物化状态重复切回 ACTIVE 时也遵守相同幂等边界。
CREATE OR REPLACE FUNCTION platform.enqueue_active_dws_dimension_profiles()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
BEGIN
  IF NEW.layer='DWS' AND NEW.status='ACTIVE'
    AND (TG_OP='INSERT' OR OLD.status IS DISTINCT FROM 'ACTIVE')
    AND NOT EXISTS(
      SELECT 1
      FROM platform.dimension_profile_jobs AS profile
      WHERE profile.tenant_id=NEW.tenant_id
        AND profile.materialization_id=NEW.id
        AND profile.materialization_snapshot_hash=NEW.snapshot_hash
        AND profile.profile_version='dws-dimension-profile-v1'
        AND profile.policy_version='dimension-member-policy-v1'
    ) THEN
    PERFORM platform.enqueue_dws_dimension_profiles(
      NEW.tenant_id,NEW.dataset_id,NEW.dataset_version_id,NEW.id
    );
  END IF;
  RETURN NEW;
END
$$;

-- 恢复被历史重复同步错误收紧的程序批准维度。只接受当前发布版本、当前
-- ACTIVE 物化、SUCCEEDED+FULL 画像和程序批准审计四重证据，不会放宽人工
-- 关闭、高基数或敏感维度。
WITH eligible AS (
  SELECT DISTINCT ON (dimension.tenant_id,dimension.id)
    dimension.tenant_id,dimension.id,profile.requested_by
  FROM platform.semantic_dimensions AS dimension
  JOIN platform.datasets AS dataset
    ON dataset.tenant_id=dimension.tenant_id
   AND dataset.id=dimension.dataset_id
   AND dataset.current_published_version_id=dimension.dataset_version_id
   AND dataset.status='PUBLISHED'
   AND dataset.deleted_at IS NULL
  JOIN platform.dataset_materializations AS materialization
    ON materialization.tenant_id=dimension.tenant_id
   AND materialization.dataset_id=dimension.dataset_id
   AND materialization.dataset_version_id=dimension.dataset_version_id
   AND materialization.layer='DWS'
   AND materialization.status='ACTIVE'
  JOIN platform.dimension_profile_jobs AS profile
    ON profile.tenant_id=materialization.tenant_id
   AND profile.materialization_id=materialization.id
   AND profile.materialization_snapshot_hash=materialization.snapshot_hash
   AND profile.dataset_version_id=dimension.dataset_version_id
   AND profile.field_id=dimension.field_id
   AND profile.status='SUCCEEDED'
   AND profile.recommended_member_index_policy='FULL'
  WHERE dimension.status='PUBLISHED'
    AND dimension.member_index_policy='NONE'
    AND NOT dimension.high_cardinality
    AND NOT dimension.sensitive
    AND EXISTS(
      SELECT 1
      FROM platform.audit_logs AS approval
      WHERE approval.tenant_id=dimension.tenant_id
        AND approval.resource_type='SEMANTIC_DIMENSION'
        AND approval.resource_id=dimension.id::text
        AND approval.action IN (
          'PROGRAM_APPROVE_DWS_DIMENSION',
          'PROGRAM_APPROVE_GOVERNED_DIMENSION'
        )
    )
  ORDER BY dimension.tenant_id,dimension.id,profile.completed_at DESC
), changed AS (
  UPDATE platform.semantic_dimensions AS dimension
  SET member_index_policy='FULL',
      member_refresh_generation=NULL,
      member_count=NULL,
      member_refreshed_at=NULL,
      last_member_refresh_job_id=NULL,
      definition_hash=encode(public.digest(
        convert_to(
          concat_ws(E'\x1f',
            dimension.dataset_id::text,
            dimension.dataset_version_id::text,
            dimension.field_id,
            dimension.code::text,
            dimension.name,
            dimension.description,
            dimension.dimension_type,
            'FULL',
            dimension.high_cardinality::text,
            dimension.sensitive::text,
            dimension.status
          ),
          'UTF8'
        ),
        'sha256'
      ),'hex'),
      version=dimension.version+1,
      updated_by=eligible.requested_by,
      updated_at=clock_timestamp()
  FROM eligible
  WHERE dimension.tenant_id=eligible.tenant_id
    AND dimension.id=eligible.id
  RETURNING dimension.tenant_id,dimension.id,dimension.updated_by,
    dimension.dataset_version_id,dimension.field_id,
    dimension.version-1 AS previous_version
)
INSERT INTO platform.audit_logs(
  tenant_id,actor_user_id,action,resource_type,resource_id,detail
)
SELECT changed.tenant_id,changed.updated_by,
  'MIGRATION_RESTORE_CURRENT_DIMENSION_MEMBER_INDEX',
  'SEMANTIC_DIMENSION',changed.id::text,
  jsonb_build_object(
    'datasetVersionId',changed.dataset_version_id::text,
    'fieldId',changed.field_id,
    'previousVersion',changed.previous_version,
    'memberIndexPolicy','FULL',
    'reason','CURRENT_SUCCEEDED_FULL_PROFILE'
  )
FROM changed;

-- 为恢复的 FULL 维度重新抽取成员。worker 后续会完成成员向量和决策图物化。
INSERT INTO platform.dimension_member_refresh_jobs(
  tenant_id,dimension_id,dimension_version,dataset_id,dataset_version_id,
  field_id,field_code,member_index_policy,materialization_id,status,
  max_members,timeout_seconds,request_hash,idempotency_key,requested_by
)
SELECT dimension.tenant_id,dimension.id,dimension.version,
  dimension.dataset_id,dimension.dataset_version_id,
  dimension.field_id,field.field_code::text,'FULL',materialization.id,
  'QUEUED',100000,60,
  encode(public.digest(convert_to(concat_ws(':',
    'current-profile-repair-request-v1',dimension.id::text,
    dimension.version::text,materialization.id::text
  ),'UTF8'),'sha256'),'hex'),
  encode(public.digest(convert_to(concat_ws(':',
    'current-profile-repair-idempotency-v1',dimension.id::text,
    dimension.version::text,materialization.id::text
  ),'UTF8'),'sha256'),'hex'),
  dimension.updated_by
FROM platform.semantic_dimensions AS dimension
JOIN platform.dataset_fields AS field
  ON field.tenant_id=dimension.tenant_id
 AND field.dataset_version_id=dimension.dataset_version_id
 AND field.field_id=dimension.field_id
JOIN platform.dataset_materializations AS materialization
  ON materialization.tenant_id=dimension.tenant_id
 AND materialization.dataset_id=dimension.dataset_id
 AND materialization.dataset_version_id=dimension.dataset_version_id
 AND materialization.layer='DWS'
 AND materialization.status='ACTIVE'
WHERE dimension.status='PUBLISHED'
  AND dimension.member_index_policy='FULL'
  AND dimension.member_count IS NULL
  AND NOT dimension.high_cardinality
  AND NOT dimension.sensitive
  AND EXISTS(
    SELECT 1
    FROM platform.audit_logs AS repair
    WHERE repair.tenant_id=dimension.tenant_id
      AND repair.resource_type='SEMANTIC_DIMENSION'
      AND repair.resource_id=dimension.id::text
      AND repair.action='MIGRATION_RESTORE_CURRENT_DIMENSION_MEMBER_INDEX'
  )
  AND EXISTS(
    SELECT 1
    FROM platform.dimension_profile_jobs AS profile
    WHERE profile.tenant_id=dimension.tenant_id
      AND profile.materialization_id=materialization.id
      AND profile.materialization_snapshot_hash=materialization.snapshot_hash
      AND profile.dataset_version_id=dimension.dataset_version_id
      AND profile.field_id=dimension.field_id
      AND profile.status='SUCCEEDED'
      AND profile.recommended_member_index_policy='FULL'
  )
  AND NOT EXISTS(
    SELECT 1
    FROM platform.dimension_member_refresh_jobs AS active_job
    WHERE active_job.tenant_id=dimension.tenant_id
      AND active_job.dimension_id=dimension.id
      AND active_job.status IN ('QUEUED','RUNNING')
  )
ON CONFLICT DO NOTHING;
