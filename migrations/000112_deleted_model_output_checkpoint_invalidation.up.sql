-- 自动建模产物被用户删除时，这不是一次失败重试，而是一次明确的重新设计请求。
-- 终止仍在运行的旧领域流程，并只清除受影响的有效检查点：
--   * 删除 DIM：领域分类、全部 DIM 设计和全部 FACT 设计都需重新执行；
--   * 删除 DWD：仅对应 ODS 事实源的 FACT 设计需重新执行。
-- 其他失败/中止重试继续复用检查点，保留断点恢复和增量建模能力。
CREATE OR REPLACE FUNCTION platform.invalidate_deleted_modeled_dataset(
  selected_dataset_id uuid,
  selected_layer text,
  selected_actor_id uuid
)
RETURNS TABLE(
  invalidated_workflow_job_id uuid,
  invalidated_checkpoint_count bigint
)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
SET row_security=off
AS $$
DECLARE
  selected_tenant_id uuid := platform.current_tenant_id();
  selected_workflow_id uuid;
  selected_subject_version_id uuid;
  removed_checkpoint_count bigint := 0;
BEGIN
  IF selected_tenant_id IS NULL
     OR selected_dataset_id IS NULL
     OR selected_actor_id IS NULL
     OR selected_layer NOT IN ('DIM','DWD')
     OR NOT EXISTS(
       SELECT 1
       FROM platform.users AS actor
       WHERE actor.id=selected_actor_id
         AND actor.tenant_id=selected_tenant_id
         AND actor.status='ACTIVE'
         AND actor.deleted_at IS NULL
     ) THEN
    RETURN;
  END IF;

  IF selected_layer='DIM' THEN
    SELECT output.last_job_id
    INTO selected_workflow_id
    FROM platform.dim_modeling_outputs AS output
    WHERE output.tenant_id=selected_tenant_id
      AND output.dim_dataset_id=selected_dataset_id
    FOR UPDATE;
  ELSE
    SELECT output.last_job_id,source.current_published_version_id
    INTO selected_workflow_id,selected_subject_version_id
    FROM platform.dwd_modeling_outputs AS output
    JOIN platform.datasets AS source
      ON source.tenant_id=output.tenant_id
     AND source.id=output.fact_dataset_id
    WHERE output.tenant_id=selected_tenant_id
      AND output.dwd_dataset_id=selected_dataset_id
    FOR UPDATE OF output;
  END IF;

  -- 人工创建或已解除自动建模所有权的数据集没有可失效的检查点。
  IF selected_workflow_id IS NULL THEN
    RETURN;
  END IF;

  -- 租约状态是 worker 的写入栅栏。先把活动阶段终结，旧 worker 即使已经
  -- 取得 LLM 响应，也无法再把被删除的产物或检查点写回。
  UPDATE platform.dwd_modeling_stage_jobs
  SET status='SKIPPED',
      error_code='MODEL_OUTPUT_DELETED',
      error_message='自动建模产物已删除，需重新触发智能建模',
      completed_at=clock_timestamp(),
      updated_at=clock_timestamp(),
      lease_owner='',lease_token=NULL,lease_expires_at=NULL
  WHERE tenant_id=selected_tenant_id
    AND workflow_job_id=selected_workflow_id
    AND status IN ('PENDING','RUNNING');

  UPDATE platform.dwd_modeling_jobs
  SET status='SKIPPED',
      error_code='MODEL_OUTPUT_DELETED',
      error_message='自动建模产物已删除，需重新触发智能建模',
      completed_at=clock_timestamp(),
      updated_at=clock_timestamp(),
      lease_owner='',lease_token=NULL,lease_expires_at=NULL
  WHERE tenant_id=selected_tenant_id
    AND id=selected_workflow_id
    AND status IN ('PENDING','RUNNING');

  DELETE FROM platform.dwd_modeling_checkpoints AS checkpoint
  WHERE checkpoint.tenant_id=selected_tenant_id
    AND checkpoint.job_id=selected_workflow_id
    AND (
      selected_layer='DIM'
      OR (
        checkpoint.checkpoint_kind='FACT_DESIGN'
        AND (
          selected_subject_version_id IS NULL
          OR checkpoint.subject_dataset_version_id=
             selected_subject_version_id
        )
      )
    );
  GET DIAGNOSTICS removed_checkpoint_count=ROW_COUNT;

  INSERT INTO platform.audit_logs(
    tenant_id,actor_user_id,action,resource_type,resource_id,detail
  ) VALUES(
    selected_tenant_id,selected_actor_id,
    'INVALIDATE_MODELING_CHECKPOINTS','DATASET',selected_dataset_id,
    jsonb_build_object(
      'layer',selected_layer,
      'workflowJobId',selected_workflow_id,
      'checkpointCount',removed_checkpoint_count,
      'reason','MODEL_OUTPUT_DELETED'
    )
  );

  RETURN QUERY
  SELECT selected_workflow_id,removed_checkpoint_count;
END
$$;

REVOKE ALL ON FUNCTION
  platform.invalidate_deleted_modeled_dataset(uuid,text,uuid)
FROM PUBLIC;
GRANT EXECUTE ON FUNCTION
  platform.invalidate_deleted_modeled_dataset(uuid,text,uuid)
TO report_app;

COMMENT ON FUNCTION
  platform.invalidate_deleted_modeled_dataset(uuid,text,uuid) IS
  '删除自动生成 DIM/DWD 时终止旧流程并精准失效设计检查点；普通失败重试仍可断点恢复';
