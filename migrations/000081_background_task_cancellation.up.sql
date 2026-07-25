-- 工作台统一后台任务中止入口。
--
-- API 角色不获得 worker 表的宽泛 UPDATE 权限；只能通过该租户限定函数把
-- 活动态迁移到既有终态。worker 的后续写回均带 status/lease 栅栏，因此会
-- 丢失所有权并保留中止前已经提交的结果。
CREATE OR REPLACE FUNCTION platform.cancel_background_task(
  selected_kind text,
  selected_task_id uuid,
  selected_actor_id uuid
)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
SET row_security=off
AS $$
DECLARE
  selected_tenant_id uuid := platform.current_tenant_id();
  changed_rows bigint := 0;
  cancel_message constant text := '用户从工作台中止任务';
BEGIN
  IF selected_tenant_id IS NULL
     OR selected_task_id IS NULL
     OR selected_actor_id IS NULL
     OR NOT EXISTS(
       SELECT 1 FROM platform.users AS actor
       WHERE actor.id=selected_actor_id
         AND actor.tenant_id=selected_tenant_id
         AND actor.status='ACTIVE'
     ) THEN
    RETURN false;
  END IF;

  CASE selected_kind
    WHEN 'DATA_SOURCE_METADATA' THEN
      UPDATE platform.data_source_metadata_jobs
      SET status='FAILED',stage='FAILED',error_code='USER_CANCELLED',
          error_message=cancel_message,completed_at=clock_timestamp(),
          lease_owner='',lease_expires_at=NULL,heartbeat_at=clock_timestamp()
      WHERE tenant_id=selected_tenant_id AND id=selected_task_id
        AND status IN ('QUEUED','RUNNING');
      GET DIAGNOSTICS changed_rows=ROW_COUNT;
      IF changed_rows=1 THEN
        UPDATE platform.data_source_metadata_job_items
        SET status='FAILED',stage='FAILED',error_code='USER_CANCELLED',
            error_message=cancel_message,completed_at=clock_timestamp()
        WHERE tenant_id=selected_tenant_id AND job_id=selected_task_id
          AND status IN ('QUEUED','RUNNING');
      END IF;

    WHEN 'DATA_SOURCE_CONNECTION_TEST' THEN
      UPDATE platform.data_source_connection_test_jobs
      SET status='CANCELLED',error_code='USER_CANCELLED',
          error_message=cancel_message,completed_at=clock_timestamp(),
          updated_at=clock_timestamp(),lease_owner=NULL,lease_token=NULL,
          lease_expires_at=NULL,heartbeat_at=clock_timestamp()
      WHERE tenant_id=selected_tenant_id AND id=selected_task_id
        AND status IN ('QUEUED','RUNNING');
      GET DIAGNOSTICS changed_rows=ROW_COUNT;

    WHEN 'DATASET_BUILD' THEN
      UPDATE platform.dataset_build_runs
      SET status='CANCELLED',error_code='USER_CANCELLED',
          error_message=cancel_message,completed_at=clock_timestamp(),
          updated_at=clock_timestamp(),lease_owner='',lease_token=NULL,
          lease_expires_at=NULL
      WHERE tenant_id=selected_tenant_id AND id=selected_task_id
        AND status IN ('QUEUED','RUNNING');
      GET DIAGNOSTICS changed_rows=ROW_COUNT;

    WHEN 'DATASET_TAG_SUGGESTION' THEN
      UPDATE platform.dataset_tag_suggestion_jobs
      SET status='SKIPPED',error_code='USER_CANCELLED',
          error_message=cancel_message,completed_at=clock_timestamp(),
          updated_at=clock_timestamp(),lease_owner='',lease_token=NULL,
          lease_expires_at=NULL
      WHERE tenant_id=selected_tenant_id AND id=selected_task_id
        AND status IN ('PENDING','RUNNING');
      GET DIAGNOSTICS changed_rows=ROW_COUNT;

    WHEN 'METRIC_EXTRACTION' THEN
      UPDATE platform.metric_extraction_jobs
      SET status='FAILED',error_code='USER_CANCELLED',
          error_message=cancel_message,completed_at=clock_timestamp(),
          heartbeat_at=clock_timestamp(),lease_owner='',lease_expires_at=NULL
      WHERE tenant_id=selected_tenant_id AND id=selected_task_id
        AND status IN ('PENDING','RUNNING');
      GET DIAGNOSTICS changed_rows=ROW_COUNT;

    WHEN 'METRIC_CANDIDATE_PREPARATION' THEN
      UPDATE platform.metric_candidate_preparation_jobs
      SET status='CANCELLED',error_code='USER_CANCELLED',
          error_message=cancel_message,completed_at=clock_timestamp(),
          updated_at=clock_timestamp(),lease_owner='',lease_expires_at=NULL
      WHERE tenant_id=selected_tenant_id AND id=selected_task_id
        AND status IN ('PENDING','RUNNING');
      GET DIAGNOSTICS changed_rows=ROW_COUNT;
      IF changed_rows=1 THEN
        UPDATE platform.dataset_publication_requests AS request
        SET metric_candidate_generation_status='FAILED',
            metric_candidate_error_code='USER_CANCELLED',
            updated_at=clock_timestamp()
        FROM platform.metric_candidate_preparation_jobs AS job
        WHERE job.tenant_id=selected_tenant_id
          AND job.id=selected_task_id
          AND request.tenant_id=job.tenant_id
          AND request.id=job.publication_request_id
          AND request.status='PENDING';
      END IF;

    WHEN 'DIMENSION_MEMBER_REFRESH' THEN
      -- v61 没有 QUEUED -> terminal 边。先建立短租约，再在同一事务内按
      -- RUNNING -> FAILED 进入 USER_CANCELLED 终态，不给 worker 可见窗口。
      UPDATE platform.dimension_member_refresh_jobs
      SET status='RUNNING',attempt=attempt+1,
          started_at=COALESCE(started_at,clock_timestamp()),
          lease_owner='background-task-cancel',lease_token=gen_random_uuid(),
          lease_expires_at=clock_timestamp()+interval '1 minute'
      WHERE tenant_id=selected_tenant_id AND id=selected_task_id
        AND status='QUEUED' AND attempt<max_attempts;

      UPDATE platform.dimension_member_refresh_jobs
      SET status='FAILED',result_code='USER_CANCELLED',
          error_message=cancel_message,completed_at=clock_timestamp(),
          updated_at=clock_timestamp(),lease_owner='',lease_token=NULL,
          lease_expires_at=NULL
      WHERE tenant_id=selected_tenant_id AND id=selected_task_id
        AND status='RUNNING';
      GET DIAGNOSTICS changed_rows=ROW_COUNT;

    WHEN 'DIMENSION_PROFILE' THEN
      UPDATE platform.dimension_profile_jobs
      SET status='STALE',result_code='USER_CANCELLED',
          completed_at=clock_timestamp(),updated_at=clock_timestamp(),
          lease_owner='',lease_token=NULL,lease_expires_at=NULL
      WHERE tenant_id=selected_tenant_id AND id=selected_task_id
        AND status IN ('QUEUED','RUNNING');
      GET DIAGNOSTICS changed_rows=ROW_COUNT;

    WHEN 'DWD_MODELING' THEN
      UPDATE platform.dwd_modeling_jobs
      SET status='SKIPPED',error_code='USER_CANCELLED',
          error_message=cancel_message,completed_at=clock_timestamp(),
          updated_at=clock_timestamp(),lease_owner='',lease_token=NULL,
          lease_expires_at=NULL
      WHERE tenant_id=selected_tenant_id AND id=selected_task_id
        AND status IN ('PENDING','RUNNING');
      GET DIAGNOSTICS changed_rows=ROW_COUNT;

    ELSE
      RETURN false;
  END CASE;

  IF changed_rows<>1 THEN
    RETURN false;
  END IF;

  INSERT INTO platform.audit_logs(
    tenant_id,actor_user_id,action,resource_type,resource_id,detail
  ) VALUES(
    selected_tenant_id,selected_actor_id,'CANCEL_BACKGROUND_TASK',
    'BACKGROUND_TASK',selected_task_id,
    jsonb_build_object('kind',selected_kind,'reason','USER_CANCELLED')
  );
  RETURN true;
END
$$;

REVOKE ALL ON FUNCTION platform.cancel_background_task(text,uuid,uuid)
  FROM PUBLIC;
GRANT EXECUTE ON FUNCTION platform.cancel_background_task(text,uuid,uuid)
  TO report_app;

COMMENT ON FUNCTION platform.cancel_background_task(text,uuid,uuid) IS
  'Tenant-fenced cooperative cancellation for the workbench background task control plane';
