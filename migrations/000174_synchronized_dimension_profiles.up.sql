-- 全局数据资产同步在程序批准 DWS 维度后补建成员画像。ACTIVE 物化可能在
-- 数据集版本仍为 PUBLISHING 时产生，单靠物化激活触发器会按设计失败关闭；
-- 此受控入口只重放当前租户、当前发布 DWS 的幂等画像调度。
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

REVOKE ALL ON FUNCTION
  platform.enqueue_current_dws_dimension_profiles(uuid)
FROM PUBLIC;

DO $$
BEGIN
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='report_app') THEN
    GRANT EXECUTE ON FUNCTION
      platform.enqueue_current_dws_dimension_profiles(uuid)
    TO report_app;
  END IF;
END
$$;

COMMENT ON FUNCTION
  platform.enqueue_current_dws_dimension_profiles(uuid)
IS
  '全局资产同步入口：为当前发布且已 ACTIVE 的 DWS 幂等补建维度画像任务';
