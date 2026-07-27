-- 对同一 ACTIVE 物化重复点击自动识别时，既有画像证据仍然有效。只有当前
-- 物化存在尚未建档的可画像字段时才调用 enqueue helper；否则直接复用成功
-- 画像或人工重放 FAILED 画像，避免把已经发布的 FULL 成员索引误收紧为 NONE。
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

    IF EXISTS(
      SELECT 1
      FROM platform.dataset_fields AS field
      WHERE field.tenant_id=platform.current_tenant_id()
        AND field.dataset_version_id=target.dataset_version_id
        AND field.field_role IN ('DIMENSION','ATTRIBUTE','TIME','IDENTIFIER')
        AND NOT EXISTS(
          SELECT 1
          FROM platform.dimension_profile_jobs AS profile
          WHERE profile.tenant_id=field.tenant_id
            AND profile.materialization_id=target.materialization_id
            AND profile.field_id=field.field_id
            AND profile.profile_version='dws-dimension-profile-v1'
            AND profile.policy_version='dimension-member-policy-v1'
        )
    ) THEN
      PERFORM platform.enqueue_dws_dimension_profiles(
        platform.current_tenant_id(),target.dataset_id,
        target.dataset_version_id,target.materialization_id
      );
    END IF;

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
  '人工自动识别入口：复用当前物化画像，仅补充缺失画像或重放失败画像；不自动批准治理对象';
