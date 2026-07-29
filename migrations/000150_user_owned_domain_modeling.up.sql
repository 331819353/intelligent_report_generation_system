-- 业务领域是当前用户会话的访问边界，不再由 LLM 标签推断。终止仍按旧
-- BUSINESS_DOMAIN 标签运行的任务，防止迁移后写回跨领域结果。
UPDATE platform.dwd_modeling_stage_jobs AS stage
SET status='SKIPPED',
    error_code='DOMAIN_CONTEXT_SUPERSEDED',
    error_message='业务领域已改为当前用户所属领域，旧标签分组任务已终止',
    lease_owner='',lease_token=NULL,lease_expires_at=NULL,
    completed_at=now(),updated_at=now()
FROM platform.dwd_modeling_jobs AS workflow
WHERE workflow.id=stage.workflow_job_id
  AND workflow.tenant_id=stage.tenant_id
  AND workflow.status IN ('PENDING','RUNNING')
  AND stage.status IN ('PENDING','RUNNING');

UPDATE platform.dwd_modeling_jobs
SET status='SKIPPED',
    error_code='DOMAIN_CONTEXT_SUPERSEDED',
    error_message='业务领域已改为当前用户所属领域，旧标签分组任务已终止',
    lease_owner='',lease_token=NULL,lease_expires_at=NULL,
    completed_at=now(),updated_at=now()
WHERE status IN ('PENDING','RUNNING');

UPDATE platform.dataset_tag_suggestion_jobs
SET status='SKIPPED',
    error_code='PROMPT_SUPERSEDED',
    error_message='业务领域不再生成标签',
    lease_owner='',lease_token=NULL,lease_expires_at=NULL,
    ai_request_id=NULL,input_hash='',output_hash='',
    suggestion_count=0,binding_count=0,
    completed_at=now(),updated_at=now()
WHERE prompt_version='dataset-tag-suggestion-v5'
  AND status IN ('PENDING','RUNNING');

DO $migration$
DECLARE
  definition text;
BEGIN
  SELECT pg_get_functiondef(
    'platform.enqueue_dataset_tag_suggestion()'::regprocedure
  ) INTO definition;
  definition := replace(
    definition,
    'dataset-tag-suggestion-v5',
    'dataset-tag-suggestion-v6'
  );
  EXECUTE definition;
END
$migration$;

CREATE OR REPLACE FUNCTION platform.trigger_manual_dim_modeling(actor_id uuid)
RETURNS TABLE(
  eligible_count bigint,
  enqueued_count bigint,
  existing_count bigint,
  blocked_count bigint,
  blocked_reason text
)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
BEGIN
  IF platform.current_tenant_id() IS NULL
     OR platform.current_domain_id() IS NULL
     OR NOT EXISTS(
       SELECT 1
       FROM platform.users AS actor
       JOIN platform.domain_memberships AS membership
         ON membership.tenant_id=actor.tenant_id
        AND membership.user_id=actor.id
        AND membership.domain_id=platform.current_domain_id()
        AND membership.status='ACTIVE'
       JOIN platform.business_domains AS domain
         ON domain.tenant_id=membership.tenant_id
        AND domain.id=membership.domain_id
        AND domain.status='ACTIVE'
       WHERE actor.tenant_id=platform.current_tenant_id()
         AND actor.id=actor_id
         AND actor.status='ACTIVE'
         AND actor.deleted_at IS NULL
     ) THEN
    RAISE EXCEPTION '当前用户所属业务领域无效' USING ERRCODE='42501';
  END IF;

  PERFORM pg_advisory_xact_lock(
    hashtext('platform.manual_warehouse_modeling'),
    hashtext(
      platform.current_tenant_id()::text||':'||
      platform.current_domain_id()::text
    )
  );

  RETURN QUERY
  WITH candidates AS (
    SELECT version.tenant_id,version.dataset_id,
      version.id AS dataset_version_id
    FROM platform.dataset_versions AS version
    JOIN platform.datasets AS dataset
      ON dataset.tenant_id=version.tenant_id
     AND dataset.id=version.dataset_id
     AND dataset.current_published_version_id=version.id
    WHERE version.tenant_id=platform.current_tenant_id()
      AND dataset.domain_id=platform.current_domain_id()
      AND version.status='PUBLISHED'
      AND version.layer='ODS'
      AND dataset.status='PUBLISHED'
      AND dataset.deleted_at IS NULL
    ORDER BY dataset.code,dataset.id,version.id
    LIMIT 1
  ), activated AS (
    INSERT INTO platform.dwd_modeling_jobs(
      tenant_id,trigger_dataset_id,trigger_dataset_version_id,
      requested_by,not_before,next_attempt_at
    )
    SELECT tenant_id,dataset_id,dataset_version_id,actor_id,now(),now()
    FROM candidates
    WHERE NOT EXISTS(
      SELECT 1
      FROM platform.dwd_modeling_jobs AS active
      WHERE active.tenant_id=candidates.tenant_id
        AND active.trigger_dataset_version_id=candidates.dataset_version_id
        AND active.status IN ('PENDING','RUNNING')
    )
    ON CONFLICT DO NOTHING
    RETURNING tenant_id,id,requested_by
  ), stages AS (
    INSERT INTO platform.dwd_modeling_stage_jobs(
      tenant_id,workflow_job_id,stage,stage_order,requested_by,
      prompt_version,status,manual_enabled,
      not_before,next_attempt_at,requested_at
    )
    SELECT
      activated.tenant_id,activated.id,definition.stage,
      definition.stage_order,activated.requested_by,
      definition.prompt_version,'PENDING',definition.manual_enabled,
      now(),now(),now()
    FROM activated
    CROSS JOIN (VALUES
      ('DOMAIN_CLASSIFICATION',1,'warehouse-classification-v4',true),
      ('DIMENSION_MODELING',2,'warehouse-dimension-design-v3',true),
      ('FACT_MODELING',3,'warehouse-fact-design-v4',false)
    ) AS definition(stage,stage_order,prompt_version,manual_enabled)
    RETURNING workflow_job_id
  ), activated_workflows AS (
    SELECT DISTINCT workflow_job_id FROM stages
  )
  SELECT
    (SELECT count(*) FROM candidates),
    (SELECT count(*) FROM activated_workflows),
    (SELECT count(*) FROM candidates)-
      (SELECT count(*) FROM activated_workflows),
    0::bigint,
    ''::text;
END
$$;

CREATE OR REPLACE FUNCTION platform.trigger_manual_dwd_modeling(actor_id uuid)
RETURNS TABLE(
  eligible_count bigint,
  enqueued_count bigint,
  existing_count bigint,
  blocked_count bigint,
  blocked_reason text
)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
BEGIN
  IF platform.current_tenant_id() IS NULL
     OR platform.current_domain_id() IS NULL
     OR NOT EXISTS(
       SELECT 1
       FROM platform.users AS actor
       JOIN platform.domain_memberships AS membership
         ON membership.tenant_id=actor.tenant_id
        AND membership.user_id=actor.id
        AND membership.domain_id=platform.current_domain_id()
        AND membership.status='ACTIVE'
       JOIN platform.business_domains AS domain
         ON domain.tenant_id=membership.tenant_id
        AND domain.id=membership.domain_id
        AND domain.status='ACTIVE'
       WHERE actor.tenant_id=platform.current_tenant_id()
         AND actor.id=actor_id
         AND actor.status='ACTIVE'
         AND actor.deleted_at IS NULL
     ) THEN
    RAISE EXCEPTION '当前用户所属业务领域无效' USING ERRCODE='42501';
  END IF;

  PERFORM pg_advisory_xact_lock(
    hashtext('platform.manual_warehouse_modeling'),
    hashtext(
      platform.current_tenant_id()::text||':'||
      platform.current_domain_id()::text
    )
  );

  RETURN QUERY
  WITH candidates AS (
    SELECT
      platform.current_tenant_id() AS tenant_id,
      domain.name AS domain_key
    FROM platform.business_domains AS domain
    WHERE domain.tenant_id=platform.current_tenant_id()
      AND domain.id=platform.current_domain_id()
      AND domain.status='ACTIVE'
      AND EXISTS(
        SELECT 1
        FROM platform.datasets AS dataset
        JOIN platform.dataset_versions AS version
          ON version.tenant_id=dataset.tenant_id
         AND version.dataset_id=dataset.id
         AND version.id=dataset.current_published_version_id
         AND version.status='PUBLISHED'
         AND version.layer='ODS'
        WHERE dataset.tenant_id=domain.tenant_id
          AND dataset.domain_id=domain.id
          AND dataset.status='PUBLISHED'
          AND dataset.deleted_at IS NULL
      )
  ), ranked_runs AS (
    SELECT
      candidate.tenant_id,candidate.domain_key,
      workflow.id AS workflow_job_id,
      classification.result_json AS classification_result,
      row_number() OVER(
        PARTITION BY candidate.tenant_id,candidate.domain_key
        ORDER BY workflow.updated_at DESC,workflow.id DESC
      ) AS run_rank
    FROM candidates AS candidate
    JOIN platform.dwd_modeling_jobs AS workflow
      ON workflow.tenant_id=candidate.tenant_id
     AND workflow.domain_key=candidate.domain_key
    JOIN platform.dwd_modeling_stage_jobs AS classification
      ON classification.tenant_id=workflow.tenant_id
     AND classification.workflow_job_id=workflow.id
     AND classification.stage='DOMAIN_CLASSIFICATION'
     AND classification.status='SUCCEEDED'
    JOIN platform.dwd_modeling_stage_jobs AS dimension
      ON dimension.tenant_id=workflow.tenant_id
     AND dimension.workflow_job_id=workflow.id
     AND dimension.stage='DIMENSION_MODELING'
     AND dimension.status='SUCCEEDED'
    JOIN platform.dwd_modeling_stage_jobs AS fact
      ON fact.tenant_id=workflow.tenant_id
     AND fact.workflow_job_id=workflow.id
     AND fact.stage='FACT_MODELING'
     AND fact.status='PENDING'
     AND NOT fact.manual_enabled
  ), latest_runs AS (
    SELECT
      ranked_runs.*,
      COALESCE(
        ranked_runs.classification_result#>>
          '{classificationSummary,factTableCount}',
        '0'
      )::integer AS fact_count,
      NOT EXISTS(
        SELECT 1
        FROM platform.dim_modeling_outputs AS output
        JOIN platform.datasets AS dimension
          ON dimension.tenant_id=output.tenant_id
         AND dimension.id=output.dim_dataset_id
        WHERE output.tenant_id=ranked_runs.tenant_id
          AND output.last_job_id=ranked_runs.workflow_job_id
          AND (
            dimension.domain_id<>platform.current_domain_id()
            OR dimension.deleted_at IS NOT NULL
            OR dimension.status<>'PUBLISHED'
            OR dimension.current_published_version_id IS NULL
          )
      ) AS dimensions_published
    FROM ranked_runs
    WHERE run_rank=1
  ), ready AS (
    SELECT * FROM latest_runs
    WHERE fact_count>0 AND dimensions_published
  ), already_running AS (
    SELECT DISTINCT candidate.tenant_id,candidate.domain_key
    FROM candidates AS candidate
    JOIN platform.dwd_modeling_jobs AS workflow
      ON workflow.tenant_id=candidate.tenant_id
     AND workflow.domain_key=candidate.domain_key
     AND workflow.status IN ('PENDING','RUNNING')
    JOIN platform.dwd_modeling_stage_jobs AS fact
      ON fact.tenant_id=workflow.tenant_id
     AND fact.workflow_job_id=workflow.id
     AND fact.stage='FACT_MODELING'
     AND fact.manual_enabled
     AND fact.status IN ('PENDING','RUNNING')
  ), activated AS (
    UPDATE platform.dwd_modeling_stage_jobs AS fact
    SET manual_enabled=true,requested_by=actor_id,
        status='PENDING',not_before=now(),next_attempt_at=now(),
        attempt=0,lease_owner='',lease_token=NULL,lease_expires_at=NULL,
        result_json='{}'::jsonb,error_code='',error_message='',
        ai_request_id=NULL,started_at=NULL,completed_at=NULL,
        requested_at=now(),updated_at=now()
    FROM ready
    WHERE fact.tenant_id=ready.tenant_id
      AND fact.workflow_job_id=ready.workflow_job_id
      AND fact.stage='FACT_MODELING'
      AND fact.status='PENDING'
      AND NOT fact.manual_enabled
    RETURNING fact.tenant_id,fact.workflow_job_id
  ), activated_workflows AS (
    UPDATE platform.dwd_modeling_jobs AS workflow
    SET status='RUNNING',requested_by=actor_id,
        not_before=now(),next_attempt_at=now(),
        error_code='',error_message='',completed_at=NULL,updated_at=now()
    FROM activated
    WHERE workflow.tenant_id=activated.tenant_id
      AND workflow.id=activated.workflow_job_id
    RETURNING workflow.id
  ), counts AS (
    SELECT
      (SELECT count(*) FROM candidates)::bigint AS eligible,
      (SELECT count(*) FROM activated_workflows)::bigint AS enqueued,
      (SELECT count(*) FROM already_running)::bigint AS existing
  )
  SELECT
    counts.eligible,
    counts.enqueued,
    counts.existing,
    greatest(counts.eligible-counts.enqueued-counts.existing,0),
    CASE
      WHEN EXISTS(
        SELECT 1 FROM latest_runs
        WHERE fact_count>0 AND NOT dimensions_published
      ) THEN 'DIM_PUBLICATION_REQUIRED'
      WHEN EXISTS(
        SELECT 1 FROM candidates AS candidate
        WHERE NOT EXISTS(
          SELECT 1 FROM latest_runs AS latest
          WHERE latest.tenant_id=candidate.tenant_id
            AND latest.domain_key=candidate.domain_key
        )
      ) THEN 'DIM_MODELING_REQUIRED'
      WHEN EXISTS(
        SELECT 1 FROM latest_runs WHERE fact_count=0
      ) THEN 'NO_FACT_MODEL_AVAILABLE'
      ELSE ''
    END
  FROM counts;
END
$$;

-- 主题仍可按使用范围分组，但输入资产必须全部属于当前用户领域。
CREATE OR REPLACE FUNCTION platform.trigger_manual_dws_modeling(actor_id uuid)
RETURNS TABLE(
  eligible_count bigint,
  enqueued_count bigint,
  blocked_count bigint
)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
BEGIN
  IF platform.current_tenant_id() IS NULL
     OR platform.current_domain_id() IS NULL
     OR NOT EXISTS(
       SELECT 1
       FROM platform.users AS actor
       JOIN platform.domain_memberships AS membership
         ON membership.tenant_id=actor.tenant_id
        AND membership.user_id=actor.id
        AND membership.domain_id=platform.current_domain_id()
        AND membership.status='ACTIVE'
       JOIN platform.business_domains AS domain
         ON domain.tenant_id=membership.tenant_id
        AND domain.id=membership.domain_id
        AND domain.status='ACTIVE'
       WHERE actor.tenant_id=platform.current_tenant_id()
         AND actor.id=actor_id
         AND actor.status='ACTIVE'
         AND actor.deleted_at IS NULL
     ) THEN
    RAISE EXCEPTION '当前用户所属业务领域无效' USING ERRCODE='42501';
  END IF;

  RETURN QUERY
  WITH current_domain AS (
    SELECT id,code::text,name
    FROM platform.business_domains
    WHERE tenant_id=platform.current_tenant_id()
      AND id=platform.current_domain_id()
      AND status='ACTIVE'
  ), current_assets AS (
    SELECT
      version.tenant_id,dataset.id AS dataset_id,
      version.id AS version_id,version.layer,version.schema_hash,
      dataset.code,dataset.name,version.dsl_json,
      domain.id AS domain_id,domain.code AS domain_code,
      domain.name AS domain_name
    FROM platform.datasets AS dataset
    JOIN current_domain AS domain ON domain.id=dataset.domain_id
    JOIN platform.dataset_versions AS version
      ON version.tenant_id=dataset.tenant_id
     AND version.dataset_id=dataset.id
     AND version.id=dataset.current_published_version_id
    WHERE dataset.tenant_id=platform.current_tenant_id()
      AND dataset.status='PUBLISHED'
      AND dataset.deleted_at IS NULL
      AND version.status='PUBLISHED'
      AND version.layer IN ('DIM','DWD')
  ), tagged_dwd AS (
    SELECT
      asset.*,
      tag.code::text AS group_key,
      regexp_replace(tag.code::text,'^.*[.]','','g') AS subject_code,
      regexp_replace(tag.name,'^(范围|主题)[：:]','','g') AS subject_name
    FROM current_assets AS asset
    JOIN platform.asset_tag_bindings AS binding
      ON binding.tenant_id=asset.tenant_id
     AND binding.asset_type='DATASET_VERSION'
     AND binding.dataset_id=asset.dataset_id
     AND binding.dataset_version_id=asset.version_id
     AND binding.status IN ('SUGGESTED','APPROVED')
    JOIN platform.semantic_tags AS tag
      ON tag.tenant_id=binding.tenant_id
     AND tag.id=binding.tag_id
     AND tag.category='USAGE_SCOPE'
     AND tag.status IN ('DRAFT','ACTIVE')
    WHERE asset.layer='DWD'
  ), untagged_dwd AS (
    SELECT
      asset.*,
      'system.usage.general'::text AS group_key,
      'general'::text AS subject_code,
      '综合分析'::text AS subject_name
    FROM current_assets AS asset
    WHERE asset.layer='DWD'
      AND NOT EXISTS(
        SELECT 1 FROM tagged_dwd AS tagged
        WHERE tagged.version_id=asset.version_id
      )
  ), memberships AS (
    SELECT * FROM tagged_dwd
    UNION ALL
    SELECT * FROM untagged_dwd
  ), grouped AS (
    SELECT
      member.tenant_id,member.group_key,min(member.domain_id) AS domain_id,
      min(member.domain_code) AS domain_code,
      min(member.domain_name) AS domain_name,
      min(member.subject_code) AS subject_code,
      min(member.subject_name) AS subject_name,
      (array_agg(member.dataset_id ORDER BY member.code,member.version_id))[1]
        AS anchor_dataset_id,
      (array_agg(member.version_id ORDER BY member.code,member.version_id))[1]
        AS anchor_version_id,
      jsonb_agg(jsonb_build_object(
        'datasetId',member.dataset_id::text,
        'versionId',member.version_id::text,
        'dslHash',member.schema_hash,
        'code',member.code,
        'name',member.name,
        'dsl',member.dsl_json
      ) ORDER BY member.code,member.version_id) AS dwd_scope
    FROM memberships AS member
    GROUP BY member.tenant_id,member.group_key
  ), scopes AS (
    SELECT
      grouped.*,
      jsonb_build_object(
        'groupKey',grouped.group_key,
        'domainId',grouped.domain_id::text,
        'domainCode',grouped.domain_code,
        'domainName',grouped.domain_name,
        'subjectCode',grouped.subject_code,
        'subjectName',grouped.subject_name,
        'dwd',grouped.dwd_scope,
        'dim',COALESCE((
          SELECT jsonb_agg(jsonb_build_object(
            'datasetId',dimension.dataset_id::text,
            'versionId',dimension.version_id::text,
            'dslHash',dimension.schema_hash,
            'code',dimension.code,
            'name',dimension.name,
            'dsl',dimension.dsl_json
          ) ORDER BY dimension.code,dimension.version_id)
          FROM current_assets AS dimension
          WHERE dimension.layer='DIM'
        ),'[]'::jsonb)
      ) AS scope_json
    FROM grouped
  ), normalized_scopes AS (
    SELECT scopes.*,
      encode(public.digest(
        convert_to(scopes.scope_json::text,'UTF8'),'sha256'
      ),'hex') AS calculated_scope_hash
    FROM scopes
  ), blocked AS (
    SELECT version.id
    FROM platform.datasets AS dataset
    JOIN current_domain AS domain ON domain.id=dataset.domain_id
    JOIN platform.dataset_versions AS version
      ON version.tenant_id=dataset.tenant_id
     AND version.dataset_id=dataset.id
     AND version.id=dataset.current_draft_version_id
    WHERE version.tenant_id=platform.current_tenant_id()
      AND version.status='DRAFT'
      AND version.layer='DWD'
      AND dataset.current_published_version_id IS NULL
      AND dataset.deleted_at IS NULL
  ), activated AS (
    INSERT INTO platform.dws_modeling_jobs(
      tenant_id,source_dwd_dataset_id,source_dwd_version_id,
      requested_by,not_before,next_attempt_at,
      group_key,source_scope,scope_hash,prompt_version
    )
    SELECT
      tenant_id,anchor_dataset_id,anchor_version_id,
      actor_id,now(),now(),
      group_key,scope_json,calculated_scope_hash,'dws-group-planning-v2'
    FROM normalized_scopes
    ON CONFLICT(tenant_id,group_key,scope_hash) DO UPDATE
    SET requested_by=EXCLUDED.requested_by,
        status='PENDING',not_before=now(),next_attempt_at=now(),
        requested_at=now(),
        attempt=0,lease_owner='',lease_token=NULL,lease_expires_at=NULL,
        input_hash='',selection_json='[]'::jsonb,ai_request_id=NULL,
        generated_count=0,updated_count=0,skipped_count=0,
        result_json='{}'::jsonb,error_code='',error_message='',
        completed_at=NULL,updated_at=now()
    WHERE platform.dws_modeling_jobs.status IN (
      'SUCCEEDED','PARTIAL','FAILED','SKIPPED'
    )
    RETURNING id
  )
  SELECT
    (SELECT count(*) FROM normalized_scopes),
    (SELECT count(*) FROM activated),
    (SELECT count(*) FROM blocked);
END
$$;

COMMENT ON FUNCTION platform.trigger_manual_dim_modeling(uuid) IS
  '只为当前用户所属业务领域创建一个 ODS 领域分析与 DIM 设计工作流';
COMMENT ON FUNCTION platform.trigger_manual_dwd_modeling(uuid) IS
  '只放行当前用户所属业务领域最近一次 DIM 工作流中的 DWD 事实阶段';
COMMENT ON FUNCTION platform.trigger_manual_dws_modeling(uuid) IS
  '只用当前用户所属业务领域的已发布 DIM/DWD，按使用范围创建主题建模任务';
