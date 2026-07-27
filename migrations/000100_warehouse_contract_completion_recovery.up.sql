-- Re-run one current ODS job for each domain that still has a partial warehouse
-- result or a legacy synthetic DIM code. Incremental planning reuses valid
-- checkpoints and existing DWD outputs, so only missing facts are sent back to
-- the LLM while unchanged models remain untouched.
WITH recoverable_domains AS (
  SELECT DISTINCT job.tenant_id, job.domain_key
  FROM platform.dwd_modeling_jobs AS job
  WHERE job.domain_key <> ''
    AND (
      (
        job.status='PARTIAL'
        AND job.error_code='SOME_LAYER_DESIGNS_SKIPPED'
      )
      OR EXISTS (
        SELECT 1
        FROM platform.dim_modeling_outputs AS output
        JOIN platform.datasets AS modeled
          ON modeled.id=output.dim_dataset_id
         AND modeled.tenant_id=output.tenant_id
         AND modeled.deleted_at IS NULL
        WHERE output.tenant_id=job.tenant_id
          AND output.domain_key=job.domain_key
          AND modeled.code ~ '^dim_auto_[0-9a-f]{16,}$'
      )
    )
),
recovery_jobs AS (
  SELECT DISTINCT ON (job.tenant_id, job.domain_key) job.id
  FROM platform.dwd_modeling_jobs AS job
  JOIN recoverable_domains AS domain
    ON domain.tenant_id=job.tenant_id
   AND domain.domain_key=job.domain_key
  JOIN platform.datasets AS dataset
    ON dataset.id=job.trigger_dataset_id
   AND dataset.tenant_id=job.tenant_id
   AND dataset.current_published_version_id=job.trigger_dataset_version_id
   AND dataset.deleted_at IS NULL
  WHERE job.status IN ('SUCCEEDED','PARTIAL','FAILED','SKIPPED')
  ORDER BY job.tenant_id, job.domain_key, job.completed_at DESC NULLS LAST,
           job.created_at DESC, job.id
)
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
FROM recovery_jobs
WHERE recovery_jobs.id=job.id;

COMMENT ON TABLE platform.dwd_modeling_jobs IS
  'ODS 发布后的可恢复仓库建模任务；支持同领域并发 LLM、历史来源对比、缺失事实补建和旧业务编码修复';
