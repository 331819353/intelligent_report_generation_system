CREATE OR REPLACE FUNCTION platform.enforce_dataset_build_run_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF TG_OP='DELETE' THEN
    RAISE EXCEPTION '数据集构建运行不可删除' USING ERRCODE='23514';
  END IF;
  IF NEW.id IS DISTINCT FROM OLD.id
    OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.dataset_id IS DISTINCT FROM OLD.dataset_id
    OR NEW.dataset_version_id IS DISTINCT FROM OLD.dataset_version_id
    OR NEW.layer IS DISTINCT FROM OLD.layer
    OR NEW.run_mode IS DISTINCT FROM OLD.run_mode
    OR NEW.plan_version IS DISTINCT FROM OLD.plan_version
    OR NEW.plan_json IS DISTINCT FROM OLD.plan_json
    OR NEW.plan_hash IS DISTINCT FROM OLD.plan_hash
    OR NEW.input_snapshot_hash IS DISTINCT FROM OLD.input_snapshot_hash
    OR NEW.request_hash IS DISTINCT FROM OLD.request_hash
    OR NEW.idempotency_key IS DISTINCT FROM OLD.idempotency_key
    OR NEW.partition_key IS DISTINCT FROM OLD.partition_key
    OR NEW.requested_by IS DISTINCT FROM OLD.requested_by
    OR NEW.max_attempts IS DISTINCT FROM OLD.max_attempts
    OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION '数据集构建运行身份、输入和计划不可修改' USING ERRCODE='23514';
  END IF;
  IF OLD.status IN ('SUCCEEDED','FAILED','CANCELLED') THEN
    RAISE EXCEPTION '数据集构建终态不可修改' USING ERRCODE='23514';
  END IF;
  IF NOT (
    (OLD.status='QUEUED' AND NEW.status IN ('RUNNING','CANCELLED'))
    OR (OLD.status='RUNNING' AND NEW.status IN ('RUNNING','SUCCEEDED','FAILED','CANCELLED'))
  ) THEN
    RAISE EXCEPTION '非法的数据集构建状态转换：% -> %',OLD.status,NEW.status
      USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enforce_dataset_build_run_transition()
  FROM PUBLIC;

CREATE OR REPLACE FUNCTION platform.retry_background_task(
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
BEGIN
  IF selected_tenant_id IS NULL
     OR selected_task_id IS NULL
     OR selected_actor_id IS NULL
     OR NOT EXISTS(
       SELECT 1 FROM platform.users AS actor
       WHERE actor.id=selected_actor_id
         AND actor.tenant_id=selected_tenant_id
         AND actor.status='ACTIVE'
         AND actor.deleted_at IS NULL
     ) THEN
    RETURN false;
  END IF;

  CASE selected_kind
    WHEN 'DWD_MODELING' THEN
      UPDATE platform.dwd_modeling_jobs AS job
      SET requested_by=selected_actor_id,
          status='PENDING',not_before=clock_timestamp(),
          next_attempt_at=clock_timestamp(),requested_at=clock_timestamp(),
          attempt=0,lease_owner='',lease_token=NULL,lease_expires_at=NULL,
          domain_key='',trigger_role='',generated_count=0,updated_count=0,
          skipped_count=0,result_json='{}'::jsonb,error_code='',
          error_message='',ai_request_id=NULL,started_at=NULL,
          completed_at=NULL,
          claimed_checkpoint_version=job.checkpoint_version,
          updated_at=clock_timestamp()
      FROM platform.datasets AS dataset
      WHERE job.tenant_id=selected_tenant_id
        AND job.id=selected_task_id
        AND job.status IN ('PARTIAL','FAILED','SKIPPED')
        AND job.error_code<>'DOMAIN_PLAN_COALESCED'
        AND dataset.tenant_id=job.tenant_id
        AND dataset.id=job.trigger_dataset_id
        AND dataset.deleted_at IS NULL
        AND dataset.status='PUBLISHED'
        AND dataset.current_published_version_id=job.trigger_dataset_version_id;
      GET DIAGNOSTICS changed_rows=ROW_COUNT;

    WHEN 'DWS_MODELING' THEN
      UPDATE platform.dws_modeling_jobs AS job
      SET requested_by=selected_actor_id,
          status='PENDING',not_before=clock_timestamp(),
          next_attempt_at=clock_timestamp(),requested_at=clock_timestamp(),
          attempt=0,lease_owner='',lease_token=NULL,lease_expires_at=NULL,
          generated_count=0,updated_count=0,skipped_count=0,
          result_json='{}'::jsonb,error_code='',error_message='',
          completed_at=NULL,updated_at=clock_timestamp()
      FROM platform.datasets AS dataset
      WHERE job.tenant_id=selected_tenant_id
        AND job.id=selected_task_id
        AND job.status IN ('PARTIAL','FAILED','SKIPPED')
        AND dataset.tenant_id=job.tenant_id
        AND dataset.id=job.source_dwd_dataset_id
        AND dataset.deleted_at IS NULL
        AND dataset.status='PUBLISHED'
        AND dataset.current_published_version_id=job.source_dwd_version_id;
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
    selected_tenant_id,selected_actor_id,'RETRY_BACKGROUND_TASK',
    'BACKGROUND_TASK',selected_task_id,
    jsonb_build_object('kind',selected_kind)
  );
  RETURN true;
END
$$;

REVOKE ALL ON FUNCTION platform.retry_background_task(text,uuid,uuid)
  FROM PUBLIC;
GRANT EXECUTE ON FUNCTION platform.retry_background_task(text,uuid,uuid)
  TO report_app;

COMMENT ON FUNCTION platform.retry_background_task(text,uuid,uuid) IS
  'Tenant-fenced retry for resumable DIM/DWD and DWS modeling tasks';
