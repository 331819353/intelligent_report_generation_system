-- Retry only current ODS jobs that were terminated by the old all-or-nothing
-- warehouse validator or by the former lease/subject-change misclassification.
-- Existing classification checkpoints remain reusable; fact-design-v2 ignores
-- the unsafe v1 fact checkpoints and regenerates only facts selected by the
-- incremental history comparison.
UPDATE platform.dwd_modeling_jobs AS job
SET status='PENDING',
    attempt=0,
    claimed_checkpoint_version=checkpoint_version,
    not_before=now(),
    next_attempt_at=now(),
    lease_owner='',
    lease_token=NULL,
    lease_expires_at=NULL,
    ai_request_id=NULL,
    error_code='',
    error_message='',
    result_json='{}'::jsonb,
    generated_count=0,
    updated_count=0,
    skipped_count=0,
    started_at=NULL,
    completed_at=NULL,
    updated_at=now()
FROM platform.datasets AS dataset
WHERE dataset.id=job.trigger_dataset_id
  AND dataset.tenant_id=job.tenant_id
  AND dataset.current_published_version_id=job.trigger_dataset_version_id
  AND dataset.deleted_at IS NULL
  AND job.status IN ('FAILED','SKIPPED')
  AND job.error_code IN (
    'WAREHOUSE_MODELING_INVALID_OUTPUT',
    'SUBJECT_CHANGED'
  );

COMMENT ON TABLE platform.dwd_modeling_outputs IS
  '事实 ODS 与后台生成 DWD 的稳定映射；Worker 比较历史来源版本后仅更新变化模型，并保护人工修改草稿';
