-- DWD 允许引用已治理的 DIM，但旧执行器把 DIM 误判为越层输入，导致当前
-- 发布版本以 TRUSTED_PLAN_INVALID 结束。主题建模随后只等待 ACTIVE 物化，
-- 无法识别上游已经进入终态。
--
-- 构建运行通常保持终态不可变；这里仅为“可信计划校验失败”开放受控的
-- FAILED -> QUEUED 恢复边，而且只能由表所有者（SECURITY DEFINER 函数）
-- 执行。身份、输入快照和计划仍然不可修改。
CREATE OR REPLACE FUNCTION platform.enforce_dataset_build_run_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  invoked_by_table_owner boolean := false;
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

  SELECT pg_get_userbyid(relation.relowner)=current_user
  INTO invoked_by_table_owner
  FROM pg_catalog.pg_class AS relation
  WHERE relation.oid=TG_RELID;

  IF OLD.status IN ('SUCCEEDED','FAILED','CANCELLED') THEN
    IF NOT (
      OLD.status='FAILED'
      AND OLD.error_code='TRUSTED_PLAN_INVALID'
      AND NEW.status='QUEUED'
      AND invoked_by_table_owner
    ) THEN
      RAISE EXCEPTION '数据集构建终态不可修改' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
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

-- 修复部署前已经被旧边界误判、且仍是数据集当前发布版本的 DWD 构建。
-- 节点必须先复位，避免 worker 领取 QUEUED 运行后看到旧终态节点。
UPDATE platform.build_node_runs AS node
SET status='PENDING',attempt=0,
    input_row_count=NULL,output_row_count=NULL,output_size_bytes=NULL,
    error_code='',error_message='',started_at=NULL,completed_at=NULL,
    updated_at=clock_timestamp()
FROM platform.dataset_build_runs AS run
JOIN platform.datasets AS dataset
  ON dataset.tenant_id=run.tenant_id
 AND dataset.id=run.dataset_id
 AND dataset.current_published_version_id=run.dataset_version_id
 AND dataset.status='PUBLISHED'
 AND dataset.deleted_at IS NULL
WHERE node.tenant_id=run.tenant_id
  AND node.build_run_id=run.id
  AND run.layer='DWD'
  AND run.status='FAILED'
  AND run.error_code='TRUSTED_PLAN_INVALID'
  AND EXISTS(
    SELECT 1
    FROM platform.build_run_inputs AS input
    WHERE input.tenant_id=run.tenant_id
      AND input.build_run_id=run.id
      AND input.input_layer='DIM'
  );

UPDATE platform.dataset_build_runs AS run
SET status='QUEUED',attempt=0,next_attempt_at=clock_timestamp(),
    lease_owner='',lease_token=NULL,lease_expires_at=NULL,
    error_code='',error_message='',started_at=NULL,completed_at=NULL,
    updated_at=clock_timestamp()
FROM platform.datasets AS dataset
WHERE dataset.tenant_id=run.tenant_id
  AND dataset.id=run.dataset_id
  AND dataset.current_published_version_id=run.dataset_version_id
  AND dataset.status='PUBLISHED'
  AND dataset.deleted_at IS NULL
  AND run.layer='DWD'
  AND run.status='FAILED'
  AND run.error_code='TRUSTED_PLAN_INVALID'
  AND EXISTS(
    SELECT 1
    FROM platform.build_run_inputs AS input
    WHERE input.tenant_id=run.tenant_id
      AND input.build_run_id=run.id
      AND input.input_layer='DIM'
  );

-- 任务运行中心可以对同一份不可变计划执行一次受控恢复。只有当前发布版本、
-- TRUSTED_PLAN_INVALID 终态且没有 ACTIVE 物化的构建符合条件。
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
    WHEN 'DATASET_BUILD' THEN
      UPDATE platform.build_node_runs AS node
      SET status='PENDING',attempt=0,
          input_row_count=NULL,output_row_count=NULL,output_size_bytes=NULL,
          error_code='',error_message='',started_at=NULL,completed_at=NULL,
          updated_at=clock_timestamp()
      FROM platform.dataset_build_runs AS run
      JOIN platform.datasets AS dataset
        ON dataset.tenant_id=run.tenant_id
       AND dataset.id=run.dataset_id
       AND dataset.current_published_version_id=run.dataset_version_id
       AND dataset.status='PUBLISHED'
       AND dataset.deleted_at IS NULL
      WHERE run.tenant_id=selected_tenant_id
        AND run.id=selected_task_id
        AND run.status='FAILED'
        AND run.error_code='TRUSTED_PLAN_INVALID'
        AND NOT EXISTS(
          SELECT 1
          FROM platform.dataset_materializations AS materialization
          WHERE materialization.tenant_id=run.tenant_id
            AND materialization.build_run_id=run.id
        )
        AND node.tenant_id=run.tenant_id
        AND node.build_run_id=run.id;

      UPDATE platform.dataset_build_runs AS run
      SET status='QUEUED',attempt=0,next_attempt_at=clock_timestamp(),
          lease_owner='',lease_token=NULL,lease_expires_at=NULL,
          error_code='',error_message='',started_at=NULL,completed_at=NULL,
          updated_at=clock_timestamp()
      FROM platform.datasets AS dataset
      WHERE run.tenant_id=selected_tenant_id
        AND run.id=selected_task_id
        AND run.status='FAILED'
        AND run.error_code='TRUSTED_PLAN_INVALID'
        AND dataset.tenant_id=run.tenant_id
        AND dataset.id=run.dataset_id
        AND dataset.deleted_at IS NULL
        AND dataset.status='PUBLISHED'
        AND dataset.current_published_version_id=run.dataset_version_id
        AND NOT EXISTS(
          SELECT 1
          FROM platform.dataset_materializations AS materialization
          WHERE materialization.tenant_id=run.tenant_id
            AND materialization.build_run_id=run.id
        );
      GET DIAGNOSTICS changed_rows=ROW_COUNT;

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
  'Tenant-fenced retry for resumable modeling tasks and pre-materialization trusted-plan failures';
