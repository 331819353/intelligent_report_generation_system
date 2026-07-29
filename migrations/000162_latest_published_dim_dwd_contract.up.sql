-- 明细建模必须以最新完成的 DIM 批次及其当前已发布合同为准。
-- 旧实现先过滤“尚未人工开启的 PENDING FACT”，再按 workflow.updated_at
-- 排名；最新批次已经成功后，重复点击会错误复活更早的待执行批次。
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
    '      classification.result_json AS classification_result,
      row_number() OVER(',
    '      classification.result_json AS classification_result,
      fact.status AS fact_status,
      fact.manual_enabled AS fact_manual_enabled,
      row_number() OVER('
  );
  definition := replace(
    definition,
    '        ORDER BY workflow.updated_at DESC,workflow.id DESC',
    '        ORDER BY dimension.completed_at DESC,
          workflow.created_at DESC,workflow.id DESC'
  );
  definition := replace(
    definition,
    '     AND fact.stage=''FACT_MODELING''
     AND fact.status=''PENDING''
     AND NOT fact.manual_enabled',
    '     AND fact.stage=''FACT_MODELING'''
  );
  definition := replace(
    definition,
    '    WHERE fact_count>0 AND dimensions_published',
    '    WHERE fact_count>0 AND dimensions_published
      AND fact_status=''PENDING'' AND NOT fact_manual_enabled'
  );
  definition := replace(
    definition,
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
  ), activated AS (',
    '  ), already_running AS (
    SELECT tenant_id,domain_key
    FROM latest_runs
    WHERE fact_manual_enabled
      AND fact_status IN (''PENDING'',''RUNNING'')
  ), activated AS ('
  );
  definition := replace(
    definition,
    '      WHEN EXISTS(
        SELECT 1 FROM latest_runs WHERE fact_count=0
      ) THEN ''NO_FACT_MODEL_AVAILABLE''
      ELSE ''''',
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
      ELSE '''''
  );

  IF definition=original
     OR position(
       'dimension.completed_at DESC' IN definition
     )=0
     OR position('fact.status AS fact_status' IN definition)=0
     OR position('DWD_MODELING_COMPLETED' IN definition)=0
     OR position('ORDER BY workflow.updated_at DESC' IN definition)>0
     OR position(
       'AND fact.status=''PENDING''
     AND NOT fact.manual_enabled' IN definition
     )>0 THEN
    RAISE EXCEPTION '无法把明细建模入口升级为最新 DIM 批次合同';
  END IF;
  EXECUTE definition;
END
$migration$;

-- 已被更新 DIM 批次取代的旧 FACT 失败任务不允许从任务中心复活。
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
  RETURNING stage_job.workflow_job_id,stage_job.stage_order',
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
  RETURNING stage_job.workflow_job_id,stage_job.stage_order'
  );
  IF definition=original
     OR position(
       'newer_dimension.completed_at' IN definition
     )=0
     OR position(
       'newer_dataset.domain_id=dataset.domain_id' IN definition
     )=0 THEN
    RAISE EXCEPTION '无法阻止已被取代的旧 DWD 阶段重试';
  END IF;
  EXECUTE definition;
END
$migration$;

COMMENT ON FUNCTION platform.trigger_manual_dwd_modeling(uuid) IS
  '仅选择当前业务领域最新成功 DIM 批次；以当前已发布 DIM 合同放行一次事实 DWD 建模，拒绝复活旧批次';

COMMENT ON FUNCTION platform.retry_dwd_modeling_stage_task(uuid,uuid) IS
  '重试当前有效建模阶段；已被更新成功 DIM 批次取代的旧 FACT 阶段不可复活';
