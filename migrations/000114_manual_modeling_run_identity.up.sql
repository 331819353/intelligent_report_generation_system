-- “智能建模”按钮代表一次新的业务批次；任务中心的“重试”才代表恢复原批次。
-- 旧实现按 ODS 版本永久唯一并通过 ON CONFLICT 重置原记录，导致多次点击始终
-- 显示相同任务 ID，也让新一轮设计和历史审计混在同一行中。
ALTER TABLE platform.dwd_modeling_jobs
  DROP CONSTRAINT dwd_modeling_jobs_version_key;

-- 同一精确 ODS 版本仍只允许一个活动批次，防止重复点击和并发请求创建两套
-- 正在运行的领域流程；终态批次不占用该唯一性，可完整保留历史。
CREATE UNIQUE INDEX dwd_modeling_jobs_active_version_uidx
  ON platform.dwd_modeling_jobs(tenant_id,trigger_dataset_version_id)
  WHERE status IN ('PENDING','RUNNING');

CREATE INDEX dwd_modeling_jobs_version_history_idx
  ON platform.dwd_modeling_jobs(
    tenant_id,trigger_dataset_version_id,requested_at DESC,id
  );

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
        FROM unnest(
          COALESCE(metadata_table.tags,'{}'::text[])
        ) AS raw_tag
        UNION
        SELECT CASE
          WHEN replace(btrim(tag.name),'：',':') LIKE '领域:%'
            THEN replace(btrim(tag.name),'：',':')
          ELSE '领域:'||btrim(tag.name)
        END
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
    SELECT published.*,row_number() OVER(
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
    SELECT
      candidate.tenant_id,candidate.dataset_id,
      candidate.dataset_version_id,actor_id,now(),now()
    FROM candidates AS candidate
    WHERE NOT EXISTS(
      SELECT 1
      FROM platform.dwd_modeling_jobs AS active
      WHERE active.tenant_id=candidate.tenant_id
        AND active.trigger_dataset_version_id=
            candidate.dataset_version_id
        AND active.status IN ('PENDING','RUNNING')
    )
    ON CONFLICT DO NOTHING
    RETURNING tenant_id,id,requested_by
  ), stages AS (
    INSERT INTO platform.dwd_modeling_stage_jobs(
      tenant_id,workflow_job_id,stage,stage_order,requested_by,
      prompt_version,status,not_before,next_attempt_at,requested_at
    )
    SELECT
      activated.tenant_id,activated.id,definition.stage,
      definition.stage_order,activated.requested_by,
      definition.prompt_version,'PENDING',now(),now(),now()
    FROM activated
    CROSS JOIN (VALUES
      ('DOMAIN_CLASSIFICATION',1,'warehouse-classification-v2'),
      ('DIMENSION_MODELING',2,'warehouse-dimension-design-v2'),
      ('FACT_MODELING',3,'warehouse-fact-design-v3')
    ) AS definition(stage,stage_order,prompt_version)
    RETURNING workflow_job_id
  )
  SELECT
    (SELECT count(*) FROM candidates),
    (SELECT count(DISTINCT workflow_job_id) FROM stages);
END
$$;

-- 自动发布触发已经由 000096 移除；保留函数的安全定义，避免未来恢复触发器
-- 时重新依赖已删除的永久唯一约束。
CREATE OR REPLACE FUNCTION platform.enqueue_ods_dwd_modeling()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  scheduled_at timestamptz;
BEGIN
  IF NEW.layer='ODS'
     AND NEW.status='PUBLISHED'
     AND OLD.status IS DISTINCT FROM 'PUBLISHED' THEN
    scheduled_at :=
      COALESCE(NEW.published_at,now())+interval '5 minutes';
    INSERT INTO platform.dwd_modeling_jobs(
      tenant_id,trigger_dataset_id,trigger_dataset_version_id,
      requested_by,not_before,next_attempt_at
    )
    SELECT
      NEW.tenant_id,NEW.dataset_id,NEW.id,NEW.published_by,
      scheduled_at,scheduled_at
    WHERE NOT EXISTS(
      SELECT 1
      FROM platform.dwd_modeling_jobs AS active
      WHERE active.tenant_id=NEW.tenant_id
        AND active.trigger_dataset_version_id=NEW.id
        AND active.status IN ('PENDING','RUNNING')
    )
    ON CONFLICT DO NOTHING;
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION
  platform.trigger_manual_dwd_modeling(uuid),
  platform.enqueue_ods_dwd_modeling()
FROM PUBLIC;
GRANT EXECUTE ON FUNCTION
  platform.trigger_manual_dwd_modeling(uuid)
TO report_app;

COMMENT ON TABLE platform.dwd_modeling_jobs IS
  '按领域保存的 DIM/DWD 建模批次；每次人工点击创建新批次，任务重试只恢复原批次';
COMMENT ON INDEX platform.dwd_modeling_jobs_active_version_uidx IS
  '同一 ODS 精确版本最多一个活动建模批次，终态历史批次可并存';
