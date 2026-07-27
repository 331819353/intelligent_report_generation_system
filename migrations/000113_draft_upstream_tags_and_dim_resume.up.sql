-- 同批次 DWD 草稿允许引用当前 DIM 草稿。标签任务此前只接受发布上游，
-- 会把这一合法建模中间态误判为 SUBJECT_CHANGED。用新的 prompt 身份为当前
-- 草稿重新登记任务；旧任务保留为不可变审计。
CREATE OR REPLACE FUNCTION platform.enqueue_dataset_tag_suggestion()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  source_snapshot jsonb;
  request_actor uuid;
BEGIN
  IF NEW.layer IN ('DIM','DWD','DWS','ADS')
     AND NEW.status IN ('DRAFT','PUBLISHED')
     AND (
       TG_OP='INSERT'
       OR OLD.status IS DISTINCT FROM NEW.status
       OR OLD.schema_hash IS DISTINCT FROM NEW.schema_hash
     ) THEN
    SELECT COALESCE(
      jsonb_agg(
        jsonb_build_object(
          'dataSourceId',source_fact.data_source_id,
          'dataSourceVersionId',source_fact.data_source_version_id
        )
        ORDER BY source_fact.data_source_id
      ),
      '[]'::jsonb
    )
    INTO source_snapshot
    FROM (
      SELECT DISTINCT
        source.id::text AS data_source_id,
        COALESCE(
          source.current_published_version_id::text,''
        ) AS data_source_version_id
      FROM platform.dataset_dependencies AS dependency
      JOIN platform.metadata_tables AS source_table
        ON dependency.source_type='TABLE'
       AND source_table.id::text=dependency.source_id
       AND source_table.tenant_id=dependency.tenant_id
      JOIN platform.data_sources AS source
        ON source.id=source_table.data_source_id
       AND source.tenant_id=source_table.tenant_id
      WHERE dependency.tenant_id=NEW.tenant_id
        AND dependency.dataset_version_id=NEW.id
    ) AS source_fact;

    request_actor := COALESCE(NEW.published_by,NEW.created_by);
    INSERT INTO platform.dataset_tag_suggestion_jobs(
      tenant_id,dataset_id,dataset_version_id,schema_hash,
      source_version_snapshot,source_version_snapshot_hash,layer,
      prompt_version,requested_by
    ) VALUES(
      NEW.tenant_id,NEW.dataset_id,NEW.id,NEW.schema_hash,
      source_snapshot,
      encode(public.digest(source_snapshot::text,'sha256'),'hex'),
      NEW.layer,'dataset-tag-suggestion-v5',request_actor
    )
    ON CONFLICT(
      tenant_id,dataset_version_id,prompt_version,schema_hash
    ) DO NOTHING;
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enqueue_dataset_tag_suggestion() FROM PUBLIC;

DROP TRIGGER IF EXISTS dataset_versions_enqueue_tag_suggestion
  ON platform.dataset_versions;
CREATE TRIGGER dataset_versions_enqueue_tag_suggestion
AFTER INSERT OR UPDATE OF status,schema_hash ON platform.dataset_versions
FOR EACH ROW EXECUTE FUNCTION platform.enqueue_dataset_tag_suggestion();

-- v5 worker 不消费旧 prompt 的活动任务；以明确终态关闭后再登记 v5。
UPDATE platform.dataset_tag_suggestion_jobs
SET status='SKIPPED',
    error_code='PROMPT_SUPERSEDED',
    error_message='标签任务已由支持当前草稿上游的新版本替代',
    lease_owner='',lease_token=NULL,lease_expires_at=NULL,
    completed_at=now(),updated_at=now()
WHERE prompt_version='dataset-tag-suggestion-v4'
  AND status IN ('PENDING','RUNNING');

INSERT INTO platform.dataset_tag_suggestion_jobs(
  tenant_id,dataset_id,dataset_version_id,schema_hash,
  source_version_snapshot,source_version_snapshot_hash,layer,
  prompt_version,requested_by
)
SELECT
  version.tenant_id,version.dataset_id,version.id,version.schema_hash,
  source_facts.snapshot,
  encode(public.digest(source_facts.snapshot::text,'sha256'),'hex'),
  version.layer,'dataset-tag-suggestion-v5',
  COALESCE(version.published_by,version.created_by)
FROM platform.dataset_versions AS version
JOIN platform.datasets AS dataset
  ON dataset.id=version.dataset_id
 AND dataset.tenant_id=version.tenant_id
 AND (
   dataset.current_draft_version_id=version.id
   OR dataset.current_published_version_id=version.id
 )
CROSS JOIN LATERAL (
  SELECT COALESCE(
    jsonb_agg(
      jsonb_build_object(
        'dataSourceId',source_fact.data_source_id,
        'dataSourceVersionId',source_fact.data_source_version_id
      )
      ORDER BY source_fact.data_source_id
    ),
    '[]'::jsonb
  ) AS snapshot
  FROM (
    SELECT DISTINCT
      source.id::text AS data_source_id,
      COALESCE(
        source.current_published_version_id::text,''
      ) AS data_source_version_id
    FROM platform.dataset_dependencies AS dependency
    JOIN platform.metadata_tables AS source_table
      ON dependency.source_type='TABLE'
     AND source_table.id::text=dependency.source_id
     AND source_table.tenant_id=dependency.tenant_id
    JOIN platform.data_sources AS source
      ON source.id=source_table.data_source_id
     AND source.tenant_id=source_table.tenant_id
    WHERE dependency.tenant_id=version.tenant_id
      AND dependency.dataset_version_id=version.id
  ) AS source_fact
) AS source_facts
WHERE version.layer IN ('DIM','DWD','DWS','ADS')
  AND version.status IN ('DRAFT','PUBLISHED')
  AND dataset.deleted_at IS NULL
ON CONFLICT(
  tenant_id,dataset_version_id,prompt_version,schema_hash
) DO NOTHING;

-- DIM 发布属于人工治理边界。事实建模在等待期间进入 PARTIAL，不再无限轮询；
-- 同一领域的全部自动 DIM 发布后，由精确发布指针变化自动恢复 FACT 阶段。
CREATE OR REPLACE FUNCTION platform.resume_fact_modeling_after_dim_publication()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
SET row_security=off
AS $$
BEGIN
  IF NEW.layer<>'DIM'
     OR NEW.deleted_at IS NOT NULL
     OR NEW.status<>'PUBLISHED'
     OR NEW.current_published_version_id IS NULL
     OR NEW.current_published_version_id IS NOT DISTINCT FROM
        OLD.current_published_version_id THEN
    RETURN NEW;
  END IF;

  WITH candidate AS (
    SELECT DISTINCT output.last_job_id AS workflow_job_id
    FROM platform.dim_modeling_outputs AS output
    WHERE output.tenant_id=NEW.tenant_id
      AND output.dim_dataset_id=NEW.id
  ), ready AS (
    SELECT candidate.workflow_job_id
    FROM candidate
    WHERE NOT EXISTS(
      SELECT 1
      FROM platform.dim_modeling_outputs AS required
      JOIN platform.datasets AS dimension
        ON dimension.tenant_id=required.tenant_id
       AND dimension.id=required.dim_dataset_id
      WHERE required.tenant_id=NEW.tenant_id
        AND required.last_job_id=candidate.workflow_job_id
        AND (
          dimension.deleted_at IS NOT NULL
          OR dimension.status<>'PUBLISHED'
          OR dimension.current_published_version_id IS NULL
        )
    )
  ), resumed AS (
    UPDATE platform.dwd_modeling_stage_jobs AS stage
    SET status='PENDING',requested_at=clock_timestamp(),
        not_before=clock_timestamp(),next_attempt_at=clock_timestamp(),
        attempt=0,lease_owner='',lease_token=NULL,lease_expires_at=NULL,
        result_json='{}'::jsonb,error_code='',error_message='',
        ai_request_id=NULL,started_at=NULL,completed_at=NULL,
        updated_at=clock_timestamp()
    FROM ready
    WHERE stage.tenant_id=NEW.tenant_id
      AND stage.workflow_job_id=ready.workflow_job_id
      AND stage.stage='FACT_MODELING'
      AND stage.status='PARTIAL'
      AND stage.error_code='DIM_PUBLICATION_REQUIRED'
    RETURNING stage.workflow_job_id
  )
  UPDATE platform.dwd_modeling_jobs AS workflow
  SET status='RUNNING',error_code='',error_message='',
      completed_at=NULL,updated_at=clock_timestamp()
  WHERE workflow.tenant_id=NEW.tenant_id
    AND workflow.id IN(
      SELECT resumed.workflow_job_id FROM resumed
    );

  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION
  platform.resume_fact_modeling_after_dim_publication()
FROM PUBLIC;

DROP TRIGGER IF EXISTS datasets_resume_fact_modeling_after_dim_publication
  ON platform.datasets;
CREATE TRIGGER datasets_resume_fact_modeling_after_dim_publication
AFTER UPDATE OF status,current_published_version_id
ON platform.datasets
FOR EACH ROW
EXECUTE FUNCTION platform.resume_fact_modeling_after_dim_publication();

COMMENT ON FUNCTION
  platform.resume_fact_modeling_after_dim_publication() IS
  '同一智能建模流程的全部 DIM 人工发布后，自动恢复等待中的事实建模阶段';
