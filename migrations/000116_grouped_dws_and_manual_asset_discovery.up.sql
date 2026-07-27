-- 主题建模按实际分析范围组织多张 DWD；DIM 作为规划上下文参与 LLM 设计，
-- DWS 物理依赖仍严格保持 DWS <- DWD。
ALTER TABLE platform.dws_modeling_jobs
  ADD COLUMN group_key text NOT NULL DEFAULT '',
  ADD COLUMN source_scope jsonb NOT NULL DEFAULT '{}'::jsonb,
  ADD COLUMN scope_hash text NOT NULL DEFAULT '';

UPDATE platform.dws_modeling_jobs
SET group_key='legacy:'||source_dwd_version_id::text,
    source_scope=jsonb_build_object(
      'groupKey','legacy:'||source_dwd_version_id::text,
      'domainCode','general',
      'subjectCode','general',
      'subjectName','综合分析',
      'dwd',jsonb_build_array(jsonb_build_object(
        'datasetId',source_dwd_dataset_id::text,
        'versionId',source_dwd_version_id::text,
        'dslHash',input_hash
      )),
      'dim','[]'::jsonb
    ),
    scope_hash=encode(digest(
      convert_to(source_dwd_version_id::text||':'||input_hash,'UTF8'),'sha256'
    ),'hex')
WHERE group_key='';

UPDATE platform.dws_modeling_jobs
SET status='SKIPPED',
    error_code='GROUP_SCOPE_SUPERSEDED',
    error_message='旧版逐表主题建模任务已由主题分组规划取代，请重新点击主题建模',
    lease_owner='',lease_token=NULL,lease_expires_at=NULL,
    completed_at=now(),updated_at=now()
WHERE group_key LIKE 'legacy:%'
  AND status IN ('PENDING','WAITING_DEPENDENCY','RUNNING');

ALTER TABLE platform.dws_modeling_jobs
  ADD CONSTRAINT dws_modeling_jobs_group_key_check CHECK(
    length(group_key) BETWEEN 1 AND 160
    AND group_key=btrim(group_key)
    AND group_key !~ '[[:cntrl:]]'
  ),
  ADD CONSTRAINT dws_modeling_jobs_source_scope_check CHECK(
    jsonb_typeof(source_scope)='object'
    AND jsonb_typeof(source_scope->'dwd')='array'
    AND jsonb_typeof(source_scope->'dim')='array'
    AND jsonb_array_length(source_scope->'dwd') BETWEEN 1 AND 32
    AND jsonb_array_length(source_scope->'dim') BETWEEN 0 AND 64
    AND pg_column_size(source_scope)<=262144
    AND platform.materialization_json_is_safe(source_scope)
  ),
  ADD CONSTRAINT dws_modeling_jobs_scope_hash_check CHECK(
    scope_hash ~ '^[0-9a-f]{64}$'
  );

ALTER TABLE platform.dws_modeling_jobs
  DROP CONSTRAINT dws_modeling_jobs_version_key;
ALTER TABLE platform.dws_modeling_jobs
  ADD CONSTRAINT dws_modeling_jobs_scope_key
    UNIQUE(tenant_id,group_key,scope_hash);

ALTER TABLE platform.dws_modeling_outputs
  ADD COLUMN group_key text NOT NULL DEFAULT '';
UPDATE platform.dws_modeling_outputs
SET group_key='legacy:'||last_source_dwd_version_id::text
WHERE group_key='';
ALTER TABLE platform.dws_modeling_outputs
  ADD CONSTRAINT dws_modeling_outputs_group_key_check CHECK(
    length(group_key) BETWEEN 1 AND 160
    AND group_key=btrim(group_key)
    AND group_key !~ '[[:cntrl:]]'
  );
ALTER TABLE platform.dws_modeling_outputs
  DROP CONSTRAINT dws_modeling_outputs_source_template_key;
ALTER TABLE platform.dws_modeling_outputs
  ADD CONSTRAINT dws_modeling_outputs_group_template_key
    UNIQUE(tenant_id,group_key,template_code);
ALTER TABLE platform.dws_modeling_outputs
  DROP CONSTRAINT dws_modeling_outputs_template_code_check;
ALTER TABLE platform.dws_modeling_outputs
  ADD CONSTRAINT dws_modeling_outputs_template_code_check CHECK(
    template_code IN (
      'TREND','PERIOD_COMPARISON','DISTRIBUTION',
      'RANKING','DRILLDOWN','ANOMALY','MULTI_FACT_COMPARISON'
    )
  );

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
  WITH current_assets AS (
    SELECT
      version.tenant_id,dataset.id AS dataset_id,
      version.id AS version_id,version.layer,version.schema_hash,
      dataset.code,dataset.name,version.dsl_json
    FROM platform.datasets AS dataset
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
      member.tenant_id,member.group_key,
      min(member.subject_code) AS subject_code,
      min(member.subject_name) AS subject_name,
      (array_agg(
        member.dataset_id ORDER BY member.code,member.version_id
      ))[1] AS anchor_dataset_id,
      (array_agg(
        member.version_id ORDER BY member.code,member.version_id
      ))[1] AS anchor_version_id,
      min(regexp_replace(member.code,'^dwd_([^_]+).*$','\1')) AS domain_code,
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
        'domainCode',COALESCE(NULLIF(grouped.domain_code,''),'general'),
        'subjectCode',COALESCE(NULLIF(grouped.subject_code,''),'general'),
        'subjectName',COALESCE(NULLIF(grouped.subject_name,''),'综合分析'),
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
      ),'hex')
        AS calculated_scope_hash
    FROM scopes
  ), blocked AS (
    SELECT version.id
    FROM platform.datasets AS dataset
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
    (SELECT count(*) FROM current_assets WHERE layer='DWD'),
    (SELECT count(*) FROM activated),
    (SELECT count(*) FROM blocked);
END
$$;

COMMENT ON FUNCTION platform.trigger_manual_dws_modeling(uuid) IS
  '按当前使用范围聚合全部 DWD，并携带全部 DIM 元信息创建主题级 LLM 规划任务';
COMMENT ON COLUMN platform.dws_modeling_jobs.source_scope IS
  '本次主题建模固定的全部 DWD 输入和 DIM 规划上下文；不包含业务行或样本值';

-- 指标和维度发现改为资产中心人工触发。发布 DWS/ADS 不再自动创建候选、
-- 画像或审批项；人工已经接受的正式资产不在此迁移中删除。
DROP TRIGGER IF EXISTS dataset_versions_enqueue_dws_metric_discovery
  ON platform.dataset_versions;
DROP TRIGGER IF EXISTS dataset_versions_enqueue_dimension_survey
  ON platform.dataset_versions;
DROP TRIGGER IF EXISTS dataset_materializations_00_enqueue_dimension_profiles
  ON platform.dataset_materializations;
DROP TRIGGER IF EXISTS dataset_materializations_complete_dimension_survey
  ON platform.dataset_materializations;

UPDATE platform.metric_extraction_jobs AS job
SET status='FAILED',
    error_code='AUTOMATIC_DISCOVERY_DISABLED',
    error_message='DWS/ADS 自动指标识别已停用，请在资产管理中心手动执行自动识别',
    lease_owner='',lease_expires_at=NULL,completed_at=now()
FROM platform.dataset_versions AS version
WHERE version.tenant_id=job.tenant_id
  AND version.id=job.dataset_version_id
  AND version.layer IN ('DWS','ADS')
  AND job.extractor_version<>'metric-candidate-manual-v1'
  AND job.status IN ('PENDING','RUNNING');

UPDATE platform.dimension_survey_candidates AS candidate
SET status='STALE',version=version+1,
    decision_reason='AUTOMATIC_DISCOVERY_DISABLED',
    updated_at=now()
WHERE candidate.status='SUGGESTED';

UPDATE platform.dimension_survey_runs
SET status='STALE',completed_at=now()
WHERE status IN ('WAITING_MATERIALIZATION','SUCCEEDED');
