DROP FUNCTION IF EXISTS
  platform.retry_dwd_modeling_stage_task(uuid,uuid);
DROP FUNCTION IF EXISTS
  platform.cancel_dwd_modeling_stage_task(uuid,uuid);
DROP TABLE IF EXISTS platform.dwd_modeling_stage_jobs;

-- 回滚后恢复上一版按领域提交单一父任务的入口。
CREATE OR REPLACE FUNCTION platform.trigger_manual_dwd_modeling(actor_id uuid)
RETURNS TABLE(eligible_count bigint,enqueued_count bigint)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
BEGIN
  IF platform.current_tenant_id() IS NULL OR NOT EXISTS(
    SELECT 1
    FROM platform.users AS actor
    WHERE actor.tenant_id=platform.current_tenant_id()
      AND actor.id=actor_id
      AND actor.status='ACTIVE'
      AND actor.deleted_at IS NULL
  ) THEN
    RAISE EXCEPTION '人工建模操作者无效' USING ERRCODE='42501';
  END IF;

  RETURN QUERY
  WITH published AS (
    SELECT version.tenant_id,version.dataset_id,
      version.id AS dataset_version_id,domain.domain_key
    FROM platform.dataset_versions AS version
    JOIN platform.datasets AS dataset
      ON dataset.tenant_id=version.tenant_id
     AND dataset.id=version.dataset_id
     AND dataset.current_published_version_id=version.id
    LEFT JOIN platform.metadata_tables AS metadata_table
      ON metadata_table.tenant_id=dataset.tenant_id
     AND metadata_table.id=dataset.origin_table_id
    LEFT JOIN LATERAL (
      SELECT min(candidate.domain_tag) AS domain_key
      FROM (
        SELECT replace(btrim(raw_tag),'：',':') AS domain_tag
        FROM unnest(COALESCE(metadata_table.tags,'{}'::text[])) AS raw_tag
        UNION
        SELECT CASE
          WHEN replace(btrim(tag.name),'：',':') LIKE '领域:%'
            THEN replace(btrim(tag.name),'：',':')
          ELSE '领域:'||btrim(tag.name)
        END
        FROM platform.asset_tag_bindings AS binding
        JOIN platform.semantic_tags AS tag
          ON tag.tenant_id=binding.tenant_id AND tag.id=binding.tag_id
        WHERE binding.tenant_id=version.tenant_id
          AND binding.asset_type='DATASET_VERSION'
          AND binding.dataset_id=version.dataset_id
          AND binding.dataset_version_id=version.id
          AND binding.status IN ('SUGGESTED','APPROVED')
          AND tag.status IN ('DRAFT','ACTIVE')
          AND tag.category='BUSINESS_DOMAIN'
      ) AS candidate
      WHERE candidate.domain_tag LIKE '领域:%'
        AND length(btrim(candidate.domain_tag))>length('领域:')
    ) AS domain ON true
    WHERE version.tenant_id=platform.current_tenant_id()
      AND version.status='PUBLISHED' AND version.layer='ODS'
      AND dataset.status='PUBLISHED' AND dataset.deleted_at IS NULL
  ), ranked AS (
    SELECT published.*,row_number() OVER(
      PARTITION BY published.domain_key
      ORDER BY published.dataset_id,published.dataset_version_id
    ) AS domain_rank
    FROM published
    WHERE NULLIF(published.domain_key,'') IS NOT NULL
  ), candidates AS (
    SELECT tenant_id,dataset_id,dataset_version_id
    FROM ranked WHERE domain_rank=1
  ), activated AS (
    INSERT INTO platform.dwd_modeling_jobs(
      tenant_id,trigger_dataset_id,trigger_dataset_version_id,
      requested_by,not_before,next_attempt_at
    )
    SELECT tenant_id,dataset_id,dataset_version_id,actor_id,now(),now()
    FROM candidates
    ON CONFLICT(tenant_id,trigger_dataset_version_id) DO UPDATE
    SET requested_by=EXCLUDED.requested_by,status='PENDING',
        not_before=now(),next_attempt_at=now(),requested_at=now(),
        attempt=0,lease_owner='',lease_token=NULL,lease_expires_at=NULL,
        domain_key='',trigger_role='',generated_count=0,updated_count=0,
        skipped_count=0,result_json='{}'::jsonb,error_code='',
        error_message='',ai_request_id=NULL,started_at=NULL,
        completed_at=NULL,
        claimed_checkpoint_version=
          platform.dwd_modeling_jobs.checkpoint_version,
        updated_at=now()
    WHERE platform.dwd_modeling_jobs.status IN(
      'SUCCEEDED','PARTIAL','FAILED','SKIPPED'
    ) OR (
      platform.dwd_modeling_jobs.status='PENDING'
      AND (
        platform.dwd_modeling_jobs.not_before>now()
        OR platform.dwd_modeling_jobs.next_attempt_at>now()
      )
    )
    RETURNING id
  )
  SELECT
    (SELECT count(*) FROM candidates),
    (SELECT count(*) FROM activated);
END
$$;

COMMENT ON FUNCTION platform.trigger_manual_dwd_modeling(uuid) IS
  '租户内按业务领域各提交一个 DIM/DWD 增量建模任务，避免逐 ODS 重复任务';
