-- “自动识别指标与维度”是唯一人工入口：同一事务既登记指标提取，也为当前
-- ACTIVE DWS 物化生成维度候选和画像任务。候选仍经过现有治理审批，成员值只
-- 在 FULL、非敏感、低基数维度获批后进入刷新与向量队列。
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
  '人工自动识别入口：为当前 ACTIVE DWS 建立维度候选和画像任务，不自动批准治理对象';
