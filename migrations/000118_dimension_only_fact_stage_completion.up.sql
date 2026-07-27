-- 三阶段任务身份固定存在，但领域分类没有 FACT 时，事实落地没有任何工作，
-- 不应因为 DIM 草稿尚未发布而进入 DIM_PUBLICATION_REQUIRED。DIM 发布是 DWD
-- 引用它时的依赖边界；纯维度领域没有这条依赖。
UPDATE platform.dwd_modeling_stage_jobs AS fact_stage
SET status='SUCCEEDED',
    generated_count=0,updated_count=0,skipped_count=0,
    result_json=dimension_stage.result_json,
    error_code='',error_message='',
    lease_owner='',lease_token=NULL,lease_expires_at=NULL,
    completed_at=COALESCE(fact_stage.completed_at,clock_timestamp()),
    updated_at=clock_timestamp()
FROM platform.dwd_modeling_stage_jobs AS classification_stage
JOIN platform.dwd_modeling_stage_jobs AS dimension_stage
  ON dimension_stage.tenant_id=classification_stage.tenant_id
 AND dimension_stage.workflow_job_id=classification_stage.workflow_job_id
 AND dimension_stage.stage='DIMENSION_MODELING'
 AND dimension_stage.status='SUCCEEDED'
WHERE fact_stage.tenant_id=classification_stage.tenant_id
  AND fact_stage.workflow_job_id=classification_stage.workflow_job_id
  AND fact_stage.stage='FACT_MODELING'
  AND fact_stage.status='PARTIAL'
  AND fact_stage.error_code='DIM_PUBLICATION_REQUIRED'
  AND classification_stage.stage='DOMAIN_CLASSIFICATION'
  AND classification_stage.status='SUCCEEDED'
  AND classification_stage.result_json#>>'{classificationSummary,factTableCount}'='0';

UPDATE platform.dwd_modeling_jobs AS workflow
SET status='SUCCEEDED',
    generated_count=dimension_stage.generated_count,
    updated_count=dimension_stage.updated_count,
    skipped_count=dimension_stage.skipped_count,
    result_json=dimension_stage.result_json,
    error_code='',error_message='',
    lease_owner='',lease_token=NULL,lease_expires_at=NULL,
    completed_at=COALESCE(workflow.completed_at,clock_timestamp()),
    updated_at=clock_timestamp()
FROM platform.dwd_modeling_stage_jobs AS fact_stage
JOIN platform.dwd_modeling_stage_jobs AS classification_stage
  ON classification_stage.tenant_id=fact_stage.tenant_id
 AND classification_stage.workflow_job_id=fact_stage.workflow_job_id
 AND classification_stage.stage='DOMAIN_CLASSIFICATION'
 AND classification_stage.status='SUCCEEDED'
JOIN platform.dwd_modeling_stage_jobs AS dimension_stage
  ON dimension_stage.tenant_id=fact_stage.tenant_id
 AND dimension_stage.workflow_job_id=fact_stage.workflow_job_id
 AND dimension_stage.stage='DIMENSION_MODELING'
 AND dimension_stage.status='SUCCEEDED'
WHERE workflow.tenant_id=fact_stage.tenant_id
  AND workflow.id=fact_stage.workflow_job_id
  AND workflow.status='PARTIAL'
  AND workflow.error_code='DIM_PUBLICATION_REQUIRED'
  AND fact_stage.stage='FACT_MODELING'
  AND fact_stage.status='SUCCEEDED'
  AND classification_stage.result_json#>>'{classificationSummary,factTableCount}'='0';

COMMENT ON COLUMN platform.dwd_modeling_stage_jobs.stage IS
  '领域建模阶段；无 FACT 的领域仍保留事实落地任务身份，但直接成功完成且不等待 DIM 发布';
