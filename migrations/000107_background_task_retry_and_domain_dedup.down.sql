REVOKE ALL ON FUNCTION platform.retry_background_task(text,uuid,uuid)
  FROM report_app;
DROP FUNCTION IF EXISTS platform.retry_background_task(text,uuid,uuid);

-- 回滚为逐 ODS 提交；仅用于迁移可逆性。
CREATE OR REPLACE FUNCTION platform.trigger_manual_dwd_modeling(actor_id uuid)
RETURNS TABLE(eligible_count bigint,enqueued_count bigint)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
BEGIN
  IF platform.current_tenant_id() IS NULL OR NOT EXISTS(
    SELECT 1
    FROM platform.users AS actor
    WHERE actor.tenant_id=platform.current_tenant_id()
      AND actor.id=actor_id
      AND actor.status='ACTIVE'
      AND actor.deleted_at IS NULL
  ) THEN
    RAISE EXCEPTION '人工建模操作者无效' USING ERRCODE='42501';
  END IF;

  RETURN QUERY
  WITH candidates AS (
    SELECT version.tenant_id,version.dataset_id,version.id AS dataset_version_id
    FROM platform.dataset_versions AS version
    JOIN platform.datasets AS dataset
      ON dataset.tenant_id=version.tenant_id
     AND dataset.id=version.dataset_id
     AND dataset.current_published_version_id=version.id
    WHERE version.tenant_id=platform.current_tenant_id()
      AND version.status='PUBLISHED'
      AND version.layer='ODS'
      AND dataset.status='PUBLISHED'
      AND dataset.deleted_at IS NULL
  ), activated AS (
    INSERT INTO platform.dwd_modeling_jobs(
      tenant_id,trigger_dataset_id,trigger_dataset_version_id,
      requested_by,not_before,next_attempt_at
    )
    SELECT tenant_id,dataset_id,dataset_version_id,actor_id,now(),now()
    FROM candidates
    ON CONFLICT(tenant_id,trigger_dataset_version_id) DO UPDATE
    SET requested_by=EXCLUDED.requested_by,
        status='PENDING',not_before=now(),next_attempt_at=now(),
        requested_at=now(),
        attempt=0,lease_owner='',lease_token=NULL,lease_expires_at=NULL,
        domain_key='',trigger_role='',generated_count=0,updated_count=0,
        skipped_count=0,result_json='{}'::jsonb,error_code='',
        error_message='',ai_request_id=NULL,started_at=NULL,
        completed_at=NULL,
        claimed_checkpoint_version=
          platform.dwd_modeling_jobs.checkpoint_version,
        updated_at=now()
    WHERE platform.dwd_modeling_jobs.status IN (
        'SUCCEEDED','PARTIAL','FAILED','SKIPPED'
      )
      OR (
        platform.dwd_modeling_jobs.status='PENDING'
        AND (
          platform.dwd_modeling_jobs.not_before>now()
          OR platform.dwd_modeling_jobs.next_attempt_at>now()
        )
      )
    RETURNING id
  )
  SELECT
    (SELECT count(*) FROM candidates),
    (SELECT count(*) FROM activated);
END
$$;

COMMENT ON FUNCTION platform.trigger_manual_dwd_modeling(uuid) IS
  '租户内人工重新提交 DIM/DWD 增量建模；稳定 job 身份保留检查点';
