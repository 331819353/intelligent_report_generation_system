-- Re-run one current ODS job in domains where the previous recovery exposed a
-- case-sensitive grain reference at the DSL boundary or a redundant physical
-- layer prefix in an existing DWD code. Incremental selection keeps completed
-- outputs unchanged unless their business code itself needs correction.
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
        FROM platform.dwd_modeling_outputs AS output
        JOIN platform.datasets AS modeled
          ON modeled.id=output.dwd_dataset_id
         AND modeled.tenant_id=output.tenant_id
         AND modeled.deleted_at IS NULL
        WHERE output.tenant_id=job.tenant_id
          AND output.domain_key=job.domain_key
          AND modeled.code ~ '^dwd_(agg|fact|fct|ods|mapped)_'
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

COMMENT ON TABLE platform.dwd_modeling_outputs IS
  '事实 ODS 与生成 DWD 的稳定映射；按精确来源版本和规范业务编码做增量建模';
