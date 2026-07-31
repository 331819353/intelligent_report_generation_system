-- 回滚函数行为；已经依据有效画像证据恢复的成员索引不做破坏性降级。
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

CREATE OR REPLACE FUNCTION platform.enqueue_active_dws_dimension_profiles()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
BEGIN
  IF NEW.layer='DWS' AND NEW.status='ACTIVE'
    AND (TG_OP='INSERT' OR OLD.status IS DISTINCT FROM 'ACTIVE') THEN
    PERFORM platform.enqueue_dws_dimension_profiles(
      NEW.tenant_id,NEW.dataset_id,NEW.dataset_version_id,NEW.id
    );
  END IF;
  RETURN NEW;
END
$$;
