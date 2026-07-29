CREATE OR REPLACE FUNCTION platform.retry_dwd_modeling_stage_task(
  selected_task_id uuid,selected_actor_id uuid
)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
SET row_security=off
AS $$
DECLARE
  selected_tenant_id uuid := platform.current_tenant_id();
  selected_workflow_id uuid;
  selected_order integer;
BEGIN
  IF selected_tenant_id IS NULL OR selected_task_id IS NULL
     OR selected_actor_id IS NULL OR NOT EXISTS(
       SELECT 1 FROM platform.users AS actor
       WHERE actor.id=selected_actor_id
         AND actor.tenant_id=selected_tenant_id
         AND actor.status='ACTIVE' AND actor.deleted_at IS NULL
     ) THEN
    RETURN false;
  END IF;

  UPDATE platform.dwd_modeling_stage_jobs AS stage_job
  SET requested_by=selected_actor_id,status='PENDING',
      not_before=clock_timestamp(),next_attempt_at=clock_timestamp(),
      requested_at=clock_timestamp(),attempt=0,
      lease_owner='',lease_token=NULL,lease_expires_at=NULL,
      generated_count=0,updated_count=0,skipped_count=0,
      result_json='{}'::jsonb,error_code='',error_message='',
      ai_request_id=NULL,started_at=NULL,completed_at=NULL,
      updated_at=clock_timestamp()
  FROM platform.dwd_modeling_jobs AS workflow
  JOIN platform.datasets AS dataset
    ON dataset.tenant_id=workflow.tenant_id
   AND dataset.id=workflow.trigger_dataset_id
   AND dataset.deleted_at IS NULL
   AND dataset.status='PUBLISHED'
   AND dataset.current_published_version_id=
       workflow.trigger_dataset_version_id
  WHERE stage_job.tenant_id=selected_tenant_id
    AND stage_job.id=selected_task_id
    AND stage_job.status IN ('PARTIAL','FAILED','SKIPPED')
    AND workflow.tenant_id=stage_job.tenant_id
    AND workflow.id=stage_job.workflow_job_id
  RETURNING stage_job.workflow_job_id,stage_job.stage_order
  INTO selected_workflow_id,selected_order;
  IF selected_workflow_id IS NULL THEN
    RETURN false;
  END IF;

  UPDATE platform.dwd_modeling_stage_jobs
  SET requested_by=selected_actor_id,status='PENDING',
      not_before=clock_timestamp(),next_attempt_at=clock_timestamp(),
      requested_at=clock_timestamp(),attempt=0,
      lease_owner='',lease_token=NULL,lease_expires_at=NULL,
      generated_count=0,updated_count=0,skipped_count=0,
      result_json='{}'::jsonb,error_code='',error_message='',
      ai_request_id=NULL,started_at=NULL,completed_at=NULL,
      updated_at=clock_timestamp()
  WHERE tenant_id=selected_tenant_id
    AND workflow_job_id=selected_workflow_id
    AND (
      stage_order>selected_order
      OR (stage_order<selected_order AND status<>'SUCCEEDED')
    );

  UPDATE platform.dwd_modeling_jobs
  SET requested_by=selected_actor_id,status='RUNNING',
      error_code='',error_message='',completed_at=NULL,
      updated_at=clock_timestamp()
  WHERE tenant_id=selected_tenant_id AND id=selected_workflow_id;

  INSERT INTO platform.audit_logs(
    tenant_id,actor_user_id,action,resource_type,resource_id,detail
  ) VALUES(
    selected_tenant_id,selected_actor_id,'RETRY_BACKGROUND_TASK',
    'BACKGROUND_TASK',selected_task_id,
    jsonb_build_object('kind','WAREHOUSE_MODELING_STAGE',
      'workflowJobId',selected_workflow_id)
  );
  RETURN true;
END
$$;

REVOKE ALL ON FUNCTION
  platform.retry_dwd_modeling_stage_task(uuid,uuid)
FROM PUBLIC;
GRANT EXECUTE ON FUNCTION
  platform.retry_dwd_modeling_stage_task(uuid,uuid)
TO report_app;

COMMENT ON FUNCTION platform.retry_dwd_modeling_stage_task(uuid,uuid) IS
  '重试指定建模阶段，并恢复所有受其影响或因流程中止而未成功的依赖阶段';
