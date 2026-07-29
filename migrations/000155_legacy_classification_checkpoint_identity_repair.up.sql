-- 000154 首次部署曾把历史 classification-v4 阶段标签统一改成 v5，但历史
-- 检查点仍真实属于领域级 v4。恢复审计身份；新批次由触发函数直接创建 v5，
-- worker 对 v4 只读兼容，不再写入新的领域级检查点。
UPDATE platform.dwd_modeling_stage_jobs AS stage
SET prompt_version='warehouse-classification-v4',updated_at=now()
FROM platform.dwd_modeling_jobs AS workflow
WHERE stage.tenant_id=workflow.tenant_id
  AND stage.workflow_job_id=workflow.id
  AND stage.stage='DOMAIN_CLASSIFICATION'
  AND stage.prompt_version='warehouse-classification-v5'
  AND EXISTS(
    SELECT 1
    FROM platform.dwd_modeling_checkpoints AS checkpoint
    WHERE checkpoint.tenant_id=stage.tenant_id
      AND checkpoint.job_id=stage.workflow_job_id
      AND checkpoint.checkpoint_kind='CLASSIFICATION'
      AND checkpoint.subject_dataset_version_id=
          workflow.trigger_dataset_version_id
      AND checkpoint.prompt_version='warehouse-classification-v4'
  )
  AND NOT EXISTS(
    SELECT 1
    FROM platform.dwd_modeling_checkpoints AS checkpoint
    WHERE checkpoint.tenant_id=stage.tenant_id
      AND checkpoint.job_id=stage.workflow_job_id
      AND checkpoint.checkpoint_kind='CLASSIFICATION'
      AND checkpoint.prompt_version='warehouse-classification-v5'
  );

COMMENT ON COLUMN platform.dwd_modeling_stage_jobs.prompt_version IS
  '阶段实际使用的 prompt 身份；历史领域级分类保留 v4，新逐 ODS 分类使用 v5';
