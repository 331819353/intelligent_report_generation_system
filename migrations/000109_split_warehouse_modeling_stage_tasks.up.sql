-- 明细建模拆成三个独立、串行依赖的持久任务。父表继续作为领域工作流、
-- 检查点和自动产物的稳定身份；任务中心与 worker 只消费阶段任务。
CREATE TABLE platform.dwd_modeling_stage_jobs(
  id uuid PRIMARY KEY DEFAULT public.gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  workflow_job_id uuid NOT NULL,
  stage text NOT NULL CHECK(stage IN (
    'DOMAIN_CLASSIFICATION','DIMENSION_MODELING','FACT_MODELING'
  )),
  stage_order integer NOT NULL,
  requested_by uuid,
  prompt_version text NOT NULL CHECK(
    length(prompt_version) BETWEEN 1 AND 128
    AND prompt_version=btrim(prompt_version)
    AND prompt_version !~ '[[:cntrl:]]'
  ),
  ai_request_id uuid,
  status text NOT NULL DEFAULT 'PENDING' CHECK(status IN (
    'PENDING','RUNNING','SUCCEEDED','PARTIAL','FAILED','SKIPPED'
  )),
  not_before timestamptz NOT NULL DEFAULT now(),
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  attempt integer NOT NULL DEFAULT 0 CHECK(attempt>=0),
  max_attempts integer NOT NULL DEFAULT 3 CHECK(max_attempts BETWEEN 1 AND 5),
  lease_owner text NOT NULL DEFAULT '' CHECK(
    length(lease_owner)<=128
    AND lease_owner=btrim(lease_owner)
    AND lease_owner !~ '[[:cntrl:]]'
  ),
  lease_token uuid,
  lease_expires_at timestamptz,
  generated_count integer NOT NULL DEFAULT 0 CHECK(generated_count>=0),
  updated_count integer NOT NULL DEFAULT 0 CHECK(updated_count>=0),
  skipped_count integer NOT NULL DEFAULT 0 CHECK(skipped_count>=0),
  result_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK(
    jsonb_typeof(result_json)='object'
    AND pg_column_size(result_json)<=262144
    AND platform.materialization_json_is_safe(result_json)
  ),
  error_code text NOT NULL DEFAULT '' CHECK(
    error_code='' OR error_code ~ '^[A-Z][A-Z0-9_]{1,127}$'
  ),
  error_message text NOT NULL DEFAULT '' CHECK(
    length(error_message)<=1024
    AND error_message !~ '[[:cntrl:]]'
  ),
  requested_at timestamptz NOT NULL DEFAULT now(),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  started_at timestamptz,
  completed_at timestamptz,
  CONSTRAINT dwd_modeling_stage_jobs_workflow_fk
    FOREIGN KEY(workflow_job_id,tenant_id)
    REFERENCES platform.dwd_modeling_jobs(id,tenant_id) ON DELETE CASCADE,
  CONSTRAINT dwd_modeling_stage_jobs_actor_fk
    FOREIGN KEY(requested_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dwd_modeling_stage_jobs_ai_request_fk
    FOREIGN KEY(ai_request_id,tenant_id)
    REFERENCES platform.ai_requests(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dwd_modeling_stage_jobs_stage_order_check CHECK(
    (stage='DOMAIN_CLASSIFICATION' AND stage_order=1)
    OR (stage='DIMENSION_MODELING' AND stage_order=2)
    OR (stage='FACT_MODELING' AND stage_order=3)
  ),
  CONSTRAINT dwd_modeling_stage_jobs_attempt_budget_check
    CHECK(attempt<=max_attempts),
  CONSTRAINT dwd_modeling_stage_jobs_workflow_stage_key
    UNIQUE(tenant_id,workflow_job_id,stage),
  CONSTRAINT dwd_modeling_stage_jobs_identity_tenant_key UNIQUE(id,tenant_id)
);

CREATE INDEX dwd_modeling_stage_jobs_claim_idx
  ON platform.dwd_modeling_stage_jobs(
    tenant_id,status,next_attempt_at,stage_order,created_at,id
  )
  WHERE status IN ('PENDING','RUNNING');

CREATE TRIGGER dwd_modeling_stage_jobs_set_updated_at
BEFORE UPDATE ON platform.dwd_modeling_stage_jobs
FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();

ALTER TABLE platform.dwd_modeling_stage_jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.dwd_modeling_stage_jobs FORCE ROW LEVEL SECURITY;

CREATE POLICY dwd_modeling_stage_jobs_tenant_isolation
  ON platform.dwd_modeling_stage_jobs
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

-- 历史工作流保留为一个“事实落地”阶段记录；新提交才生成完整三任务。
INSERT INTO platform.dwd_modeling_stage_jobs(
  tenant_id,workflow_job_id,stage,stage_order,requested_by,prompt_version,
  ai_request_id,status,not_before,next_attempt_at,attempt,max_attempts,
  lease_owner,lease_token,lease_expires_at,
  generated_count,updated_count,skipped_count,result_json,
  error_code,error_message,requested_at,created_at,updated_at,
  started_at,completed_at
)
SELECT
  job.tenant_id,job.id,'FACT_MODELING',3,job.requested_by,
  'warehouse-fact-design-v3',job.ai_request_id,
  CASE WHEN job.status IN ('PENDING','RUNNING') THEN 'PENDING'
       ELSE job.status END,
  now(),now(),
  CASE WHEN job.status IN ('PENDING','RUNNING') THEN 0 ELSE job.attempt END,
  job.max_attempts,'',NULL,NULL,
  job.generated_count,job.updated_count,job.skipped_count,job.result_json,
  CASE WHEN job.status IN ('PENDING','RUNNING') THEN '' ELSE job.error_code END,
  CASE WHEN job.status IN ('PENDING','RUNNING') THEN '' ELSE job.error_message END,
  job.requested_at,job.created_at,job.updated_at,
  CASE WHEN job.status IN ('PENDING','RUNNING') THEN NULL ELSE job.started_at END,
  CASE WHEN job.status IN ('PENDING','RUNNING') THEN NULL ELSE job.completed_at END
FROM platform.dwd_modeling_jobs AS job
ON CONFLICT(tenant_id,workflow_job_id,stage) DO NOTHING;

INSERT INTO platform.dwd_modeling_stage_jobs(
  tenant_id,workflow_job_id,stage,stage_order,requested_by,prompt_version,
  status,not_before,next_attempt_at,requested_at
)
SELECT job.tenant_id,job.id,definition.stage,definition.stage_order,
  job.requested_by,definition.prompt_version,'PENDING',now(),now(),
  job.requested_at
FROM platform.dwd_modeling_jobs AS job
CROSS JOIN (VALUES
  ('DOMAIN_CLASSIFICATION',1,'warehouse-classification-v2'),
  ('DIMENSION_MODELING',2,'warehouse-dimension-design-v2')
) AS definition(stage,stage_order,prompt_version)
WHERE job.status IN ('PENDING','RUNNING')
ON CONFLICT(tenant_id,workflow_job_id,stage) DO NOTHING;

UPDATE platform.dwd_modeling_jobs
SET status='PENDING',attempt=0,next_attempt_at=now(),
    lease_owner='',lease_token=NULL,lease_expires_at=NULL,
    error_code='',error_message='',started_at=NULL,completed_at=NULL,
    updated_at=now()
WHERE status='RUNNING';

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
    )
    RETURNING tenant_id,id,requested_by
  ), stages AS (
    INSERT INTO platform.dwd_modeling_stage_jobs(
      tenant_id,workflow_job_id,stage,stage_order,requested_by,
      prompt_version,status,not_before,next_attempt_at,requested_at
    )
    SELECT activated.tenant_id,activated.id,definition.stage,
      definition.stage_order,activated.requested_by,
      definition.prompt_version,'PENDING',now(),now(),now()
    FROM activated
    CROSS JOIN (VALUES
      ('DOMAIN_CLASSIFICATION',1,'warehouse-classification-v2'),
      ('DIMENSION_MODELING',2,'warehouse-dimension-design-v2'),
      ('FACT_MODELING',3,'warehouse-fact-design-v3')
    ) AS definition(stage,stage_order,prompt_version)
    ON CONFLICT(tenant_id,workflow_job_id,stage) DO UPDATE
    SET requested_by=EXCLUDED.requested_by,
        prompt_version=EXCLUDED.prompt_version,status='PENDING',
        not_before=now(),next_attempt_at=now(),requested_at=now(),
        attempt=0,lease_owner='',lease_token=NULL,lease_expires_at=NULL,
        generated_count=0,updated_count=0,skipped_count=0,
        result_json='{}'::jsonb,error_code='',error_message='',
        ai_request_id=NULL,started_at=NULL,completed_at=NULL,
        updated_at=now()
    RETURNING workflow_job_id
  )
  SELECT
    (SELECT count(*) FROM candidates),
    (SELECT count(DISTINCT workflow_job_id) FROM stages);
END
$$;

CREATE OR REPLACE FUNCTION platform.cancel_dwd_modeling_stage_task(
  selected_task_id uuid,selected_actor_id uuid
)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
SET row_security=off
AS $$
DECLARE
  selected_tenant_id uuid := platform.current_tenant_id();
  selected_workflow_id uuid;
  selected_order integer;
BEGIN
  IF selected_tenant_id IS NULL OR selected_task_id IS NULL
     OR selected_actor_id IS NULL OR NOT EXISTS(
       SELECT 1 FROM platform.users AS actor
       WHERE actor.id=selected_actor_id
         AND actor.tenant_id=selected_tenant_id
         AND actor.status='ACTIVE' AND actor.deleted_at IS NULL
     ) THEN
    RETURN false;
  END IF;

  UPDATE platform.dwd_modeling_stage_jobs
  SET status='SKIPPED',error_code='USER_CANCELLED',
      error_message='用户从工作台中止任务',
      completed_at=clock_timestamp(),updated_at=clock_timestamp(),
      lease_owner='',lease_token=NULL,lease_expires_at=NULL
  WHERE tenant_id=selected_tenant_id AND id=selected_task_id
    AND status IN ('PENDING','RUNNING')
  RETURNING workflow_job_id,stage_order
  INTO selected_workflow_id,selected_order;
  IF selected_workflow_id IS NULL THEN
    RETURN false;
  END IF;

  -- 任一阶段被中止都终结同一领域工作流的所有未完成阶段。否则用户
  -- 中止尚未开始的下游任务时，仍在运行的上游任务可能再次把父流程
  -- 改回 RUNNING，并留下永远无法领取的依赖链。
  UPDATE platform.dwd_modeling_stage_jobs
  SET status='SKIPPED',
      error_code=CASE
        WHEN stage_order<selected_order THEN 'WORKFLOW_CANCELLED'
        ELSE 'UPSTREAM_CANCELLED'
      END,
      error_message=CASE
        WHEN stage_order<selected_order
          THEN '同一建模流程的后续任务已中止'
        ELSE '上游建模任务已中止'
      END,
      completed_at=clock_timestamp(),updated_at=clock_timestamp(),
      lease_owner='',lease_token=NULL,lease_expires_at=NULL
  WHERE tenant_id=selected_tenant_id
    AND workflow_job_id=selected_workflow_id
    AND id<>selected_task_id
    AND status IN ('PENDING','RUNNING');

  UPDATE platform.dwd_modeling_jobs
  SET status='SKIPPED',error_code='USER_CANCELLED',
      error_message='用户从工作台中止建模阶段任务',
      completed_at=clock_timestamp(),updated_at=clock_timestamp(),
      lease_owner='',lease_token=NULL,lease_expires_at=NULL
  WHERE tenant_id=selected_tenant_id AND id=selected_workflow_id;

  INSERT INTO platform.audit_logs(
    tenant_id,actor_user_id,action,resource_type,resource_id,detail
  ) VALUES(
    selected_tenant_id,selected_actor_id,'CANCEL_BACKGROUND_TASK',
    'BACKGROUND_TASK',selected_task_id,
    jsonb_build_object('kind','WAREHOUSE_MODELING_STAGE',
      'workflowJobId',selected_workflow_id,'reason','USER_CANCELLED')
  );
  RETURN true;
END
$$;

CREATE OR REPLACE FUNCTION platform.retry_dwd_modeling_stage_task(
  selected_task_id uuid,selected_actor_id uuid
)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
SET row_security=off
AS $$
DECLARE
  selected_tenant_id uuid := platform.current_tenant_id();
  selected_workflow_id uuid;
  selected_order integer;
BEGIN
  IF selected_tenant_id IS NULL OR selected_task_id IS NULL
     OR selected_actor_id IS NULL OR NOT EXISTS(
       SELECT 1 FROM platform.users AS actor
       WHERE actor.id=selected_actor_id
         AND actor.tenant_id=selected_tenant_id
         AND actor.status='ACTIVE' AND actor.deleted_at IS NULL
     ) THEN
    RETURN false;
  END IF;

  UPDATE platform.dwd_modeling_stage_jobs AS stage_job
  SET requested_by=selected_actor_id,status='PENDING',
      not_before=clock_timestamp(),next_attempt_at=clock_timestamp(),
      requested_at=clock_timestamp(),attempt=0,
      lease_owner='',lease_token=NULL,lease_expires_at=NULL,
      generated_count=0,updated_count=0,skipped_count=0,
      result_json='{}'::jsonb,error_code='',error_message='',
      ai_request_id=NULL,started_at=NULL,completed_at=NULL,
      updated_at=clock_timestamp()
  FROM platform.dwd_modeling_jobs AS workflow
  JOIN platform.datasets AS dataset
    ON dataset.tenant_id=workflow.tenant_id
   AND dataset.id=workflow.trigger_dataset_id
   AND dataset.deleted_at IS NULL
   AND dataset.status='PUBLISHED'
   AND dataset.current_published_version_id=
       workflow.trigger_dataset_version_id
  WHERE stage_job.tenant_id=selected_tenant_id
    AND stage_job.id=selected_task_id
    AND stage_job.status IN ('PARTIAL','FAILED','SKIPPED')
    AND workflow.tenant_id=stage_job.tenant_id
    AND workflow.id=stage_job.workflow_job_id
  RETURNING stage_job.workflow_job_id,stage_job.stage_order
  INTO selected_workflow_id,selected_order;
  IF selected_workflow_id IS NULL THEN
    RETURN false;
  END IF;

  UPDATE platform.dwd_modeling_stage_jobs
  SET requested_by=selected_actor_id,status='PENDING',
      not_before=clock_timestamp(),next_attempt_at=clock_timestamp(),
      requested_at=clock_timestamp(),attempt=0,
      lease_owner='',lease_token=NULL,lease_expires_at=NULL,
      generated_count=0,updated_count=0,skipped_count=0,
      result_json='{}'::jsonb,error_code='',error_message='',
      ai_request_id=NULL,started_at=NULL,completed_at=NULL,
      updated_at=clock_timestamp()
  WHERE tenant_id=selected_tenant_id
    AND workflow_job_id=selected_workflow_id
    AND (
      stage_order>selected_order
      OR (stage_order<selected_order AND status<>'SUCCEEDED')
    );

  UPDATE platform.dwd_modeling_jobs
  SET requested_by=selected_actor_id,status='RUNNING',
      error_code='',error_message='',completed_at=NULL,updated_at=clock_timestamp()
  WHERE tenant_id=selected_tenant_id AND id=selected_workflow_id;

  INSERT INTO platform.audit_logs(
    tenant_id,actor_user_id,action,resource_type,resource_id,detail
  ) VALUES(
    selected_tenant_id,selected_actor_id,'RETRY_BACKGROUND_TASK',
    'BACKGROUND_TASK',selected_task_id,
    jsonb_build_object('kind','WAREHOUSE_MODELING_STAGE',
      'workflowJobId',selected_workflow_id)
  );
  RETURN true;
END
$$;

REVOKE ALL ON FUNCTION
  platform.cancel_dwd_modeling_stage_task(uuid,uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION
  platform.retry_dwd_modeling_stage_task(uuid,uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION
  platform.cancel_dwd_modeling_stage_task(uuid,uuid) TO report_app;
GRANT EXECUTE ON FUNCTION
  platform.retry_dwd_modeling_stage_task(uuid,uuid) TO report_app;

COMMENT ON TABLE platform.dwd_modeling_stage_jobs IS
  '领域分类、维度建模、事实落地三个独立且串行依赖的 LLM 建模任务';
COMMENT ON FUNCTION platform.trigger_manual_dwd_modeling(uuid) IS
  '租户内按领域提交三个小上下文、可独立重试的串行智能建模任务';
