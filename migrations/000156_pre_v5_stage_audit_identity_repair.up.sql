-- 修复 000154 早期部署对无检查点历史阶段造成的 prompt 标签漂移。
-- 真正的 v5 阶段只可能在 000154 应用后由新版触发函数创建。
UPDATE platform.dwd_modeling_stage_jobs AS stage
SET prompt_version='warehouse-classification-v4',updated_at=now()
WHERE stage.stage='DOMAIN_CLASSIFICATION'
  AND stage.prompt_version='warehouse-classification-v5'
  AND stage.created_at<(
    SELECT migration.applied_at
    FROM public.platform_schema_migrations AS migration
    WHERE migration.version='000154_per_dataset_parallel_warehouse_modeling'
  )
  AND NOT EXISTS(
    SELECT 1
    FROM platform.dwd_modeling_checkpoints AS checkpoint
    WHERE checkpoint.tenant_id=stage.tenant_id
      AND checkpoint.job_id=stage.workflow_job_id
      AND checkpoint.checkpoint_kind='CLASSIFICATION'
      AND checkpoint.prompt_version='warehouse-classification-v5'
  );
