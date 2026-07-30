-- 未框选主题建模同时扫描 DWD 与 DIM：
--   * 每张 DWD 仍形成一个独立 LLM 主题识别任务；
--   * 每张 DIM 形成一个独立无事实 ENTITY_COUNT 任务，只允许单一计数指标。
-- 显式框选继续使用 trigger_manual_dws_modeling(uuid,uuid[]) 的既有合同。

ALTER TABLE platform.dws_modeling_jobs
  DROP CONSTRAINT dws_modeling_jobs_source_scope_check,
  ADD CONSTRAINT dws_modeling_jobs_source_scope_check CHECK(
    jsonb_typeof(source_scope)='object'
    AND jsonb_typeof(source_scope->'dwd')='array'
    AND jsonb_typeof(source_scope->'dim')='array'
    AND jsonb_array_length(source_scope->'dwd') BETWEEN 0 AND 32
    AND jsonb_array_length(source_scope->'dim') BETWEEN 0 AND 64
    AND (
      jsonb_array_length(source_scope->'dwd')>=1
      OR jsonb_array_length(source_scope->'dim')=1
    )
    AND pg_column_size(source_scope)<=262144
    AND platform.materialization_json_is_safe(source_scope)
  );

ALTER TABLE platform.dws_modeling_outputs
  DROP CONSTRAINT dws_modeling_outputs_template_code_check,
  ADD CONSTRAINT dws_modeling_outputs_template_code_check CHECK(
    template_code IN (
      'TREND','PERIOD_COMPARISON','DISTRIBUTION',
      'RANKING','DRILLDOWN','ANOMALY',
      'MULTI_FACT_COMPARISON','ENTITY_COUNT'
    )
  );

CREATE OR REPLACE FUNCTION platform.trigger_unscoped_dws_modeling(
  actor_id uuid
)
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
  ), dwd_scopes AS (
    SELECT
      fact.tenant_id,fact.dataset_id,fact.version_id,
      'single-dwd:'||fact.dataset_id::text AS group_key,
      'dws-single-fact-planning-v3'::text AS prompt_version,
      jsonb_build_object(
        'groupKey','single-dwd:'||fact.dataset_id::text,
        'domainId',fact.domain_id::text,
        'domainCode',fact.domain_code,
        'domainName',fact.domain_name,
        'subjectCode','default',
        'subjectName',fact.name,
        'dwd',jsonb_build_array(jsonb_build_object(
          'datasetId',fact.dataset_id::text,
          'versionId',fact.version_id::text,
          'dslHash',fact.schema_hash,
          'code',fact.code,
          'name',fact.name,
          'dsl',fact.dsl_json
        )),
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
    FROM current_assets AS fact
    WHERE fact.layer='DWD'
  ), dim_scopes AS (
    SELECT
      dimension.tenant_id,dimension.dataset_id,dimension.version_id,
      'single-dim:'||dimension.dataset_id::text AS group_key,
      'dws-dimension-count-planning-v1'::text AS prompt_version,
      jsonb_build_object(
        'groupKey','single-dim:'||dimension.dataset_id::text,
        'domainId',dimension.domain_id::text,
        'domainCode',dimension.domain_code,
        'domainName',dimension.domain_name,
        'subjectCode','entity_count',
        'subjectName',dimension.name||'数量',
        'dwd','[]'::jsonb,
        'dim',jsonb_build_array(jsonb_build_object(
          'datasetId',dimension.dataset_id::text,
          'versionId',dimension.version_id::text,
          'dslHash',dimension.schema_hash,
          'code',dimension.code,
          'name',dimension.name,
          'dsl',dimension.dsl_json
        ))
      ) AS scope_json
    FROM current_assets AS dimension
    WHERE dimension.layer='DIM'
  ), scopes AS (
    SELECT * FROM dwd_scopes
    UNION ALL
    SELECT * FROM dim_scopes
  ), normalized_scopes AS (
    SELECT scopes.*,
      encode(public.digest(
        convert_to(scopes.scope_json::text,'UTF8'),'sha256'
      ),'hex') AS calculated_scope_hash
    FROM scopes
  ), blocked AS (
    SELECT version.id
    FROM platform.datasets AS dataset
    JOIN platform.dataset_versions AS version
      ON version.tenant_id=dataset.tenant_id
     AND version.dataset_id=dataset.id
     AND version.id=dataset.current_draft_version_id
    WHERE dataset.tenant_id=platform.current_tenant_id()
      AND dataset.domain_id=platform.current_domain_id()
      AND dataset.current_published_version_id IS NULL
      AND dataset.deleted_at IS NULL
      AND version.status='DRAFT'
      AND version.layer IN ('DIM','DWD')
  ), activated AS (
    INSERT INTO platform.dws_modeling_jobs(
      tenant_id,source_dwd_dataset_id,source_dwd_version_id,
      requested_by,not_before,next_attempt_at,
      group_key,source_scope,scope_hash,prompt_version
    )
    SELECT
      tenant_id,dataset_id,version_id,
      actor_id,now(),now(),
      group_key,scope_json,calculated_scope_hash,prompt_version
    FROM normalized_scopes
    ON CONFLICT(tenant_id,group_key,scope_hash) DO UPDATE
    SET requested_by=EXCLUDED.requested_by,
        status='PENDING',not_before=now(),next_attempt_at=now(),
        requested_at=now(),
        attempt=0,lease_owner='',lease_token=NULL,lease_expires_at=NULL,
        input_hash='',selection_json='[]'::jsonb,ai_request_id=NULL,
        generated_count=0,updated_count=0,skipped_count=0,
        result_json='{}'::jsonb,error_code='',error_message='',
        completed_at=NULL,updated_at=now(),
        prompt_version=EXCLUDED.prompt_version,
        source_scope=EXCLUDED.source_scope
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

REVOKE ALL ON FUNCTION
  platform.trigger_unscoped_dws_modeling(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION
  platform.trigger_unscoped_dws_modeling(uuid) TO report_app;

COMMENT ON FUNCTION platform.trigger_unscoped_dws_modeling(uuid) IS
  '未框选主题建模：逐 DWD 识别主题，并逐 DIM 生成单一实体数量指标';

-- 运行时层级门同步允许 DWS 读取 DIM；其他层级方向保持不变。
DO $migration$
DECLARE
  definition text;
  original text;
BEGIN
  SELECT pg_get_functiondef(
    'platform.enforce_build_run_input_layer()'::regprocedure
  ) INTO definition;
  original := definition;
  definition := replace(
    definition,
    '(target_layer=''DWS'' AND NEW.input_layer<>''DWD'')',
    '(target_layer=''DWS'' AND NEW.input_layer NOT IN (''DWD'',''DIM''))'
  );
  definition := replace(
    definition,
    'DWS <- DWD',
    'DWS <- DWD|DIM'
  );
  IF definition=original
     OR position(
       'NEW.input_layer NOT IN (''DWD'',''DIM'')' IN definition
     )=0 THEN
    RAISE EXCEPTION '无法启用 DWS 的 DIM 无事实输入';
  END IF;
  EXECUTE definition;
END
$migration$;
