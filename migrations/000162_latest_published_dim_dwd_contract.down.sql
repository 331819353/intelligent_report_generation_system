DO $migration$
DECLARE
  definition text;
  original text;
BEGIN
  SELECT pg_get_functiondef(
    'platform.retry_dwd_modeling_stage_task(uuid,uuid)'::regprocedure
  ) INTO definition;
  original := definition;
  definition := replace(
    definition,
    '    AND workflow.tenant_id=stage_job.tenant_id
    AND workflow.id=stage_job.workflow_job_id
    AND (
      stage_job.stage<>''FACT_MODELING''
      OR NOT EXISTS(
        SELECT 1
        FROM platform.dwd_modeling_jobs AS newer
        JOIN platform.datasets AS newer_dataset
          ON newer_dataset.tenant_id=newer.tenant_id
         AND newer_dataset.id=newer.trigger_dataset_id
         AND newer_dataset.domain_id=dataset.domain_id
        JOIN platform.dwd_modeling_stage_jobs AS newer_dimension
          ON newer_dimension.tenant_id=newer.tenant_id
         AND newer_dimension.workflow_job_id=newer.id
         AND newer_dimension.stage=''DIMENSION_MODELING''
         AND newer_dimension.status=''SUCCEEDED''
        JOIN platform.dwd_modeling_stage_jobs AS selected_dimension
          ON selected_dimension.tenant_id=workflow.tenant_id
         AND selected_dimension.workflow_job_id=workflow.id
         AND selected_dimension.stage=''DIMENSION_MODELING''
         AND selected_dimension.status=''SUCCEEDED''
        WHERE newer.tenant_id=workflow.tenant_id
          AND (
            newer_dimension.completed_at,
            newer.created_at,
            newer.id
          )>(
            selected_dimension.completed_at,
            workflow.created_at,
            workflow.id
          )
      )
    )
  RETURNING stage_job.workflow_job_id,stage_job.stage_order',
    '    AND workflow.tenant_id=stage_job.tenant_id
    AND workflow.id=stage_job.workflow_job_id
  RETURNING stage_job.workflow_job_id,stage_job.stage_order'
  );
  IF definition=original
     OR position('newer_dimension.completed_at' IN definition)>0 THEN
    RAISE EXCEPTION '无法回滚旧 DWD 阶段重试保护';
  END IF;
  EXECUTE definition;
END
$migration$;

DO $migration$
DECLARE
  definition text;
  original text;
BEGIN
  SELECT pg_get_functiondef(
    'platform.trigger_manual_dwd_modeling(uuid)'::regprocedure
  ) INTO definition;
  original := definition;

  definition := replace(
    definition,
    '      WHEN EXISTS(
        SELECT 1 FROM latest_runs WHERE fact_count=0
      ) THEN ''NO_FACT_MODEL_AVAILABLE''
      WHEN EXISTS(
        SELECT 1 FROM latest_runs WHERE fact_status=''SUCCEEDED''
      ) THEN ''DWD_MODELING_COMPLETED''
      WHEN EXISTS(
        SELECT 1 FROM latest_runs
        WHERE fact_status IN (''FAILED'',''PARTIAL'',''SKIPPED'')
      ) THEN ''DWD_MODELING_RETRY_REQUIRED''
      ELSE ''''',
    '      WHEN EXISTS(
        SELECT 1 FROM latest_runs WHERE fact_count=0
      ) THEN ''NO_FACT_MODEL_AVAILABLE''
      ELSE '''''
  );
  definition := replace(
    definition,
    '  ), already_running AS (
    SELECT tenant_id,domain_key
    FROM latest_runs
    WHERE fact_manual_enabled
      AND fact_status IN (''PENDING'',''RUNNING'')
  ), activated AS (',
    '  ), already_running AS (
    SELECT DISTINCT candidate.tenant_id,candidate.domain_key
    FROM candidates AS candidate
    JOIN platform.dwd_modeling_jobs AS workflow
      ON workflow.tenant_id=candidate.tenant_id
     AND workflow.domain_key=candidate.domain_key
     AND workflow.status IN (''PENDING'',''RUNNING'')
    JOIN platform.dwd_modeling_stage_jobs AS fact
      ON fact.tenant_id=workflow.tenant_id
     AND fact.workflow_job_id=workflow.id
     AND fact.stage=''FACT_MODELING''
     AND fact.manual_enabled
     AND fact.status IN (''PENDING'',''RUNNING'')
  ), activated AS ('
  );
  definition := replace(
    definition,
    '    WHERE fact_count>0 AND dimensions_published
      AND fact_status=''PENDING'' AND NOT fact_manual_enabled',
    '    WHERE fact_count>0 AND dimensions_published'
  );
  definition := replace(
    definition,
    '     AND fact.stage=''FACT_MODELING''
  ), latest_runs AS (',
    '     AND fact.stage=''FACT_MODELING''
     AND fact.status=''PENDING''
     AND NOT fact.manual_enabled
  ), latest_runs AS ('
  );
  definition := replace(
    definition,
    '        ORDER BY dimension.completed_at DESC,
          workflow.created_at DESC,workflow.id DESC',
    '        ORDER BY workflow.updated_at DESC,workflow.id DESC'
  );
  definition := replace(
    definition,
    '      classification.result_json AS classification_result,
      fact.status AS fact_status,
      fact.manual_enabled AS fact_manual_enabled,
      row_number() OVER(',
    '      classification.result_json AS classification_result,
      row_number() OVER('
  );

  IF definition=original
     OR position('DWD_MODELING_COMPLETED' IN definition)>0
     OR position('fact.status AS fact_status' IN definition)>0
     OR position('ORDER BY workflow.updated_at DESC' IN definition)=0 THEN
    RAISE EXCEPTION '无法回滚明细建模入口的旧批次选择逻辑';
  END IF;
  EXECUTE definition;
END
$migration$;

COMMENT ON FUNCTION platform.trigger_manual_dwd_modeling(uuid) IS
  '使用当前用户业务领域的普通名称恢复最新成功 DIM 批次；全部关联 DIM 发布后人工触发事实 DWD 建模';

COMMENT ON FUNCTION platform.retry_dwd_modeling_stage_task(uuid,uuid) IS
  '重试指定建模阶段；classification-v6 保留逐 ODS 检查点并恢复所有受影响的依赖阶段';
