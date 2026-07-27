-- 人工 DIM/DWD 建模的执行单位是“领域”，因此提交阶段也必须按领域只创建
-- 一个代表任务，不能先按每张 ODS 建任务再把同领域兄弟任务标记为错误。
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
    SELECT
      version.tenant_id,
      version.dataset_id,
      version.id AS dataset_version_id,
      domain.domain_key
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
        END AS domain_tag
        FROM platform.asset_tag_bindings AS binding
        JOIN platform.semantic_tags AS tag
          ON tag.tenant_id=binding.tenant_id
         AND tag.id=binding.tag_id
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
      AND version.status='PUBLISHED'
      AND version.layer='ODS'
      AND dataset.status='PUBLISHED'
      AND dataset.deleted_at IS NULL
  ), ranked AS (
    SELECT
      published.*,
      row_number() OVER(
        PARTITION BY published.domain_key
        ORDER BY published.dataset_id,published.dataset_version_id
      ) AS domain_rank
    FROM published
    WHERE NULLIF(published.domain_key,'') IS NOT NULL
  ), candidates AS (
    SELECT tenant_id,dataset_id,dataset_version_id
    FROM ranked
    WHERE domain_rank=1
  ), activated AS (
    INSERT INTO platform.dwd_modeling_jobs(
      tenant_id,trigger_dataset_id,trigger_dataset_version_id,
      requested_by,not_before,next_attempt_at
    )
    SELECT tenant_id,dataset_id,dataset_version_id,actor_id,now(),now()
    FROM candidates
    ON CONFLICT(tenant_id,trigger_dataset_version_id) DO UPDATE
    SET requested_by=EXCLUDED.requested_by,
        status='PENDING',not_before=now(),next_attempt_at=now(),
        requested_at=now(),
        attempt=0,lease_owner='',lease_token=NULL,lease_expires_at=NULL,
        domain_key='',trigger_role='',generated_count=0,updated_count=0,
        skipped_count=0,result_json='{}'::jsonb,error_code='',
        error_message='',ai_request_id=NULL,started_at=NULL,
        completed_at=NULL,
        claimed_checkpoint_version=
          platform.dwd_modeling_jobs.checkpoint_version,
        updated_at=now()
    WHERE platform.dwd_modeling_jobs.status IN (
        'SUCCEEDED','PARTIAL','FAILED','SKIPPED'
      )
      OR (
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

-- 任务运行中心只通过租户限定 SECURITY DEFINER 函数重试当前已支持安全
-- 恢复协议的建模任务。有效检查点和既有产物映射保留，重试只清理运行态。
CREATE OR REPLACE FUNCTION platform.retry_background_task(
  selected_kind text,
  selected_task_id uuid,
  selected_actor_id uuid
)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
SET row_security=off
AS $$
DECLARE
  selected_tenant_id uuid := platform.current_tenant_id();
  changed_rows bigint := 0;
BEGIN
  IF selected_tenant_id IS NULL
     OR selected_task_id IS NULL
     OR selected_actor_id IS NULL
     OR NOT EXISTS(
       SELECT 1 FROM platform.users AS actor
       WHERE actor.id=selected_actor_id
         AND actor.tenant_id=selected_tenant_id
         AND actor.status='ACTIVE'
         AND actor.deleted_at IS NULL
     ) THEN
    RETURN false;
  END IF;

  CASE selected_kind
    WHEN 'DWD_MODELING' THEN
      UPDATE platform.dwd_modeling_jobs AS job
      SET requested_by=selected_actor_id,
          status='PENDING',not_before=clock_timestamp(),
          next_attempt_at=clock_timestamp(),requested_at=clock_timestamp(),
          attempt=0,lease_owner='',lease_token=NULL,lease_expires_at=NULL,
          domain_key='',trigger_role='',generated_count=0,updated_count=0,
          skipped_count=0,result_json='{}'::jsonb,error_code='',
          error_message='',ai_request_id=NULL,started_at=NULL,
          completed_at=NULL,
          claimed_checkpoint_version=job.checkpoint_version,
          updated_at=clock_timestamp()
      FROM platform.datasets AS dataset
      WHERE job.tenant_id=selected_tenant_id
        AND job.id=selected_task_id
        AND job.status IN ('PARTIAL','FAILED','SKIPPED')
        AND job.error_code<>'DOMAIN_PLAN_COALESCED'
        AND dataset.tenant_id=job.tenant_id
        AND dataset.id=job.trigger_dataset_id
        AND dataset.deleted_at IS NULL
        AND dataset.status='PUBLISHED'
        AND dataset.current_published_version_id=job.trigger_dataset_version_id;
      GET DIAGNOSTICS changed_rows=ROW_COUNT;

    WHEN 'DWS_MODELING' THEN
      UPDATE platform.dws_modeling_jobs AS job
      SET requested_by=selected_actor_id,
          status='PENDING',not_before=clock_timestamp(),
          next_attempt_at=clock_timestamp(),requested_at=clock_timestamp(),
          attempt=0,lease_owner='',lease_token=NULL,lease_expires_at=NULL,
          generated_count=0,updated_count=0,skipped_count=0,
          result_json='{}'::jsonb,error_code='',completed_at=NULL,
          updated_at=clock_timestamp()
      FROM platform.datasets AS dataset
      WHERE job.tenant_id=selected_tenant_id
        AND job.id=selected_task_id
        AND job.status IN ('PARTIAL','FAILED','SKIPPED')
        AND dataset.tenant_id=job.tenant_id
        AND dataset.id=job.source_dwd_dataset_id
        AND dataset.deleted_at IS NULL
        AND dataset.status='PUBLISHED'
        AND dataset.current_published_version_id=job.source_dwd_version_id;
      GET DIAGNOSTICS changed_rows=ROW_COUNT;

    ELSE
      RETURN false;
  END CASE;

  IF changed_rows<>1 THEN
    RETURN false;
  END IF;

  INSERT INTO platform.audit_logs(
    tenant_id,actor_user_id,action,resource_type,resource_id,detail
  ) VALUES(
    selected_tenant_id,selected_actor_id,'RETRY_BACKGROUND_TASK',
    'BACKGROUND_TASK',selected_task_id,
    jsonb_build_object('kind',selected_kind)
  );
  RETURN true;
END
$$;

REVOKE ALL ON FUNCTION platform.retry_background_task(text,uuid,uuid)
  FROM PUBLIC;
GRANT EXECUTE ON FUNCTION platform.retry_background_task(text,uuid,uuid)
  TO report_app;

COMMENT ON FUNCTION platform.retry_background_task(text,uuid,uuid) IS
  'Tenant-fenced retry for resumable DIM/DWD and DWS modeling tasks';
