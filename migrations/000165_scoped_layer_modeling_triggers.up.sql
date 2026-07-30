-- 智能建模入口统一使用显式数据集范围。NULL 表示当前业务领域内的默认
-- 全量上游；非 NULL 数组已经由 API 在同一事务中校验为当前发布版本。

ALTER TABLE platform.dwd_modeling_jobs
  ADD COLUMN source_dataset_ids uuid[],
  ADD COLUMN fact_source_dataset_ids uuid[],
  ADD COLUMN fact_dimension_dataset_ids uuid[];

ALTER TABLE platform.dwd_modeling_jobs
  ADD CONSTRAINT dwd_modeling_jobs_source_scope_check CHECK(
    source_dataset_ids IS NULL
    OR cardinality(source_dataset_ids) BETWEEN 1 AND 200
  ),
  ADD CONSTRAINT dwd_modeling_jobs_fact_source_scope_check CHECK(
    fact_source_dataset_ids IS NULL
    OR cardinality(fact_source_dataset_ids) BETWEEN 1 AND 200
  ),
  ADD CONSTRAINT dwd_modeling_jobs_fact_dimension_scope_check CHECK(
    fact_dimension_dataset_ids IS NULL
    OR cardinality(fact_dimension_dataset_ids) BETWEEN 0 AND 200
  );

CREATE OR REPLACE FUNCTION platform.trigger_manual_dim_modeling(
  actor_id uuid,selected_dataset_ids uuid[]
)
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
      version.id AS dataset_version_id,dataset.code
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
      AND (
        selected_dataset_ids IS NULL
        OR dataset.id=ANY(selected_dataset_ids)
      )
  ), modeling_scope AS (
    SELECT
      (array_agg(tenant_id ORDER BY code,dataset_id))[1] AS tenant_id,
      (array_agg(dataset_id ORDER BY code,dataset_id))[1] AS anchor_dataset_id,
      (array_agg(dataset_version_id ORDER BY code,dataset_id))[1]
        AS anchor_version_id,
      array_agg(dataset_id ORDER BY code,dataset_id) AS source_dataset_ids
    FROM candidates
    HAVING count(*)>0
  ), activated AS (
    INSERT INTO platform.dwd_modeling_jobs(
      tenant_id,trigger_dataset_id,trigger_dataset_version_id,
      requested_by,not_before,next_attempt_at,source_dataset_ids
    )
    SELECT
      tenant_id,anchor_dataset_id,anchor_version_id,
      actor_id,now(),now(),source_dataset_ids
    FROM modeling_scope
    WHERE NOT EXISTS(
      SELECT 1
      FROM platform.dwd_modeling_jobs AS active
      WHERE active.tenant_id=modeling_scope.tenant_id
        AND active.trigger_dataset_version_id=
            modeling_scope.anchor_version_id
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
      ('DOMAIN_CLASSIFICATION',1,'warehouse-classification-v6',true),
      ('DIMENSION_MODELING',2,'warehouse-dimension-design-v4',true),
      ('FACT_MODELING',3,'warehouse-fact-design-v4',false)
    ) AS definition(stage,stage_order,prompt_version,manual_enabled)
    RETURNING workflow_job_id
  ), activated_workflows AS (
    SELECT DISTINCT workflow_job_id FROM stages
  )
  SELECT
    (SELECT count(*) FROM candidates),
    (SELECT count(*) FROM activated_workflows),
    CASE
      WHEN EXISTS(SELECT 1 FROM candidates)
       AND NOT EXISTS(SELECT 1 FROM activated_workflows)
      THEN 1::bigint ELSE 0::bigint
    END,
    0::bigint,
    ''::text;
END
$$;

-- DWD 仍复用最近一次已经完成 DIM 的分类批次；选中的 ODS 只决定本次
-- FACT 阶段允许生成哪些明细草稿，选中的 DIM 作为可审计的规划范围保存。
CREATE OR REPLACE FUNCTION platform.trigger_manual_dwd_modeling(
  actor_id uuid,selected_dataset_ids uuid[]
)
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
DECLARE
  selected_ods uuid[];
  selected_dim uuid[];
  activated_workflow_id uuid;
BEGIN
  SELECT
    array_agg(dataset.id ORDER BY dataset.code)
      FILTER(WHERE version.layer='ODS'),
    COALESCE(
      array_agg(dataset.id ORDER BY dataset.code)
        FILTER(WHERE version.layer='DIM'),
      '{}'::uuid[]
    )
  INTO selected_ods,selected_dim
  FROM platform.datasets AS dataset
  JOIN platform.dataset_versions AS version
    ON version.id=dataset.current_published_version_id
   AND version.dataset_id=dataset.id
   AND version.tenant_id=dataset.tenant_id
   AND version.status='PUBLISHED'
  WHERE selected_dataset_ids IS NOT NULL
    AND dataset.id=ANY(selected_dataset_ids)
    AND dataset.tenant_id=platform.current_tenant_id()
    AND dataset.domain_id=platform.current_domain_id()
    AND dataset.status='PUBLISHED'
    AND dataset.deleted_at IS NULL;

  SELECT *
  INTO eligible_count,enqueued_count,existing_count,
       blocked_count,blocked_reason
  FROM platform.trigger_manual_dwd_modeling(actor_id);

  IF selected_dataset_ids IS NOT NULL AND enqueued_count>0 THEN
    SELECT workflow.id
    INTO activated_workflow_id
    FROM platform.dwd_modeling_jobs AS workflow
    JOIN platform.dwd_modeling_stage_jobs AS fact
      ON fact.tenant_id=workflow.tenant_id
     AND fact.workflow_job_id=workflow.id
     AND fact.stage='FACT_MODELING'
     AND fact.manual_enabled
     AND fact.requested_by=actor_id
    WHERE workflow.tenant_id=platform.current_tenant_id()
      AND workflow.requested_by=actor_id
      AND workflow.status='RUNNING'
    ORDER BY fact.requested_at DESC,workflow.id DESC
    LIMIT 1
    FOR UPDATE OF workflow;

    UPDATE platform.dwd_modeling_jobs
    SET fact_source_dataset_ids=selected_ods,
        fact_dimension_dataset_ids=selected_dim,
        updated_at=now()
    WHERE id=activated_workflow_id;
  END IF;
  RETURN NEXT;
END
$$;

CREATE OR REPLACE FUNCTION platform.trigger_manual_dws_modeling(
  actor_id uuid,selected_dataset_ids uuid[]
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
      AND (
        version.layer='DIM'
        OR selected_dataset_ids IS NULL
        OR dataset.id=ANY(selected_dataset_ids)
      )
  ), dwd_scopes AS (
    SELECT
      fact.*,
      'single-dwd:'||fact.dataset_id::text AS group_key,
      COALESCE(usage_scope.subject_code,'general') AS subject_code,
      COALESCE(usage_scope.subject_name,fact.name) AS subject_name
    FROM current_assets AS fact
    LEFT JOIN LATERAL (
      SELECT
        regexp_replace(tag.code::text,'^.*[.]','','g') AS subject_code,
        regexp_replace(tag.name,'^(范围|主题)[：:]','','g') AS subject_name
      FROM platform.asset_tag_bindings AS binding
      JOIN platform.semantic_tags AS tag
        ON tag.tenant_id=binding.tenant_id
       AND tag.id=binding.tag_id
       AND tag.category='USAGE_SCOPE'
       AND tag.status IN ('DRAFT','ACTIVE')
      WHERE binding.tenant_id=fact.tenant_id
        AND binding.asset_type='DATASET_VERSION'
        AND binding.dataset_id=fact.dataset_id
        AND binding.dataset_version_id=fact.version_id
        AND binding.status IN ('SUGGESTED','APPROVED')
      ORDER BY
        CASE binding.status WHEN 'APPROVED' THEN 0 ELSE 1 END,
        tag.code,tag.id
      LIMIT 1
    ) AS usage_scope ON true
    WHERE fact.layer='DWD'
  ), scopes AS (
    SELECT
      fact.tenant_id,fact.dataset_id,fact.version_id,fact.group_key,
      jsonb_build_object(
        'groupKey',fact.group_key,
        'domainId',fact.domain_id::text,
        'domainCode',fact.domain_code,
        'domainName',fact.domain_name,
        'subjectCode',fact.subject_code,
        'subjectName',fact.subject_name,
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
    FROM dwd_scopes AS fact
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
      AND (
        selected_dataset_ids IS NULL
        OR dataset.id=ANY(selected_dataset_ids)
      )
  ), activated AS (
    INSERT INTO platform.dws_modeling_jobs(
      tenant_id,source_dwd_dataset_id,source_dwd_version_id,
      requested_by,not_before,next_attempt_at,
      group_key,source_scope,scope_hash,prompt_version
    )
    SELECT
      tenant_id,dataset_id,version_id,
      actor_id,now(),now(),
      group_key,scope_json,calculated_scope_hash,
      'dws-single-fact-planning-v3'
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

CREATE TABLE platform.ads_modeling_jobs(
  id uuid PRIMARY KEY DEFAULT public.gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  source_dws_dataset_id uuid NOT NULL,
  source_dws_version_id uuid NOT NULL,
  requested_by uuid NOT NULL,
  status text NOT NULL DEFAULT 'PENDING' CHECK(status IN (
    'PENDING','WAITING_DEPENDENCY','RUNNING','SUCCEEDED',
    'FAILED','SKIPPED'
  )),
  not_before timestamptz NOT NULL DEFAULT now(),
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  attempt integer NOT NULL DEFAULT 0 CHECK(attempt BETWEEN 0 AND 5),
  max_attempts integer NOT NULL DEFAULT 3 CHECK(max_attempts BETWEEN 1 AND 5),
  lease_owner text NOT NULL DEFAULT '' CHECK(
    length(lease_owner)<=128 AND lease_owner !~ '[[:cntrl:]]'
  ),
  lease_token uuid,
  lease_expires_at timestamptz,
  generated_count integer NOT NULL DEFAULT 0 CHECK(generated_count>=0),
  updated_count integer NOT NULL DEFAULT 0 CHECK(updated_count>=0),
  skipped_count integer NOT NULL DEFAULT 0 CHECK(skipped_count>=0),
  result_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK(
    jsonb_typeof(result_json)='object'
    AND pg_column_size(result_json)<=65536
    AND platform.materialization_json_is_safe(result_json)
  ),
  error_code text NOT NULL DEFAULT '' CHECK(length(error_code)<=128),
  error_message text NOT NULL DEFAULT '' CHECK(
    length(error_message)<=1024 AND error_message !~ '[[:cntrl:]]'
  ),
  requested_at timestamptz NOT NULL DEFAULT now(),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz,
  CONSTRAINT ads_modeling_jobs_source_version_fk
    FOREIGN KEY(source_dws_version_id,source_dws_dataset_id,tenant_id)
    REFERENCES platform.dataset_versions(id,dataset_id,tenant_id)
    ON DELETE RESTRICT,
  CONSTRAINT ads_modeling_jobs_actor_fk
    FOREIGN KEY(requested_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT ads_modeling_jobs_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT ads_modeling_jobs_lease_shape_check CHECK(
    (status='RUNNING' AND lease_owner<>'' AND lease_token IS NOT NULL
      AND lease_expires_at IS NOT NULL AND completed_at IS NULL)
    OR
    (status<>'RUNNING' AND lease_owner='' AND lease_token IS NULL
      AND lease_expires_at IS NULL)
  )
);

CREATE UNIQUE INDEX ads_modeling_jobs_active_source_uidx
  ON platform.ads_modeling_jobs(tenant_id,source_dws_version_id)
  WHERE status IN ('PENDING','WAITING_DEPENDENCY','RUNNING');
CREATE INDEX ads_modeling_jobs_claim_idx
  ON platform.ads_modeling_jobs(
    tenant_id,status,next_attempt_at,lease_expires_at,created_at,id
  )
  WHERE status IN ('PENDING','WAITING_DEPENDENCY','RUNNING');

CREATE TABLE platform.ads_modeling_outputs(
  id uuid PRIMARY KEY DEFAULT public.gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  source_dws_dataset_id uuid NOT NULL,
  ads_dataset_id uuid NOT NULL,
  last_source_dws_version_id uuid NOT NULL,
  last_job_id uuid NOT NULL,
  last_generated_dsl_hash text NOT NULL CHECK(
    last_generated_dsl_hash ~ '^[0-9a-f]{64}$'
  ),
  last_action text NOT NULL CHECK(
    last_action IN ('CREATED','UPDATED','UNCHANGED','MANUAL_OWNED')
  ),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT ads_modeling_outputs_source_fk
    FOREIGN KEY(source_dws_dataset_id,tenant_id)
    REFERENCES platform.datasets(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT ads_modeling_outputs_ads_fk
    FOREIGN KEY(ads_dataset_id,tenant_id)
    REFERENCES platform.datasets(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT ads_modeling_outputs_source_version_fk
    FOREIGN KEY(last_source_dws_version_id,source_dws_dataset_id,tenant_id)
    REFERENCES platform.dataset_versions(id,dataset_id,tenant_id)
    ON DELETE RESTRICT,
  CONSTRAINT ads_modeling_outputs_job_fk
    FOREIGN KEY(last_job_id,tenant_id)
    REFERENCES platform.ads_modeling_jobs(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT ads_modeling_outputs_source_key
    UNIQUE(tenant_id,source_dws_dataset_id),
  CONSTRAINT ads_modeling_outputs_ads_key UNIQUE(tenant_id,ads_dataset_id),
  CONSTRAINT ads_modeling_outputs_identity_tenant_key UNIQUE(id,tenant_id)
);

CREATE TRIGGER ads_modeling_jobs_set_updated_at
BEFORE UPDATE ON platform.ads_modeling_jobs
FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();
CREATE TRIGGER ads_modeling_outputs_set_updated_at
BEFORE UPDATE ON platform.ads_modeling_outputs
FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();

ALTER TABLE platform.ads_modeling_jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.ads_modeling_jobs FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.ads_modeling_outputs ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.ads_modeling_outputs FORCE ROW LEVEL SECURITY;

CREATE POLICY ads_modeling_jobs_tenant_isolation
  ON platform.ads_modeling_jobs
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY ads_modeling_outputs_tenant_isolation
  ON platform.ads_modeling_outputs
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

CREATE OR REPLACE FUNCTION platform.trigger_manual_ads_modeling(
  actor_id uuid,selected_dataset_ids uuid[]
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
       WHERE actor.tenant_id=platform.current_tenant_id()
         AND actor.id=actor_id
         AND actor.status='ACTIVE'
         AND actor.deleted_at IS NULL
     ) THEN
    RAISE EXCEPTION '当前用户所属业务领域无效' USING ERRCODE='42501';
  END IF;

  RETURN QUERY
  WITH candidates AS (
    SELECT
      dataset.tenant_id,dataset.id AS dataset_id,
      version.id AS version_id
    FROM platform.datasets AS dataset
    JOIN platform.dataset_versions AS version
      ON version.tenant_id=dataset.tenant_id
     AND version.dataset_id=dataset.id
     AND version.id=dataset.current_published_version_id
     AND version.status='PUBLISHED'
     AND version.layer='DWS'
    WHERE dataset.tenant_id=platform.current_tenant_id()
      AND dataset.domain_id=platform.current_domain_id()
      AND dataset.status='PUBLISHED'
      AND dataset.deleted_at IS NULL
      AND (
        selected_dataset_ids IS NULL
        OR dataset.id=ANY(selected_dataset_ids)
      )
  ), blocked AS (
    SELECT version.id
    FROM platform.datasets AS dataset
    JOIN platform.dataset_versions AS version
      ON version.tenant_id=dataset.tenant_id
     AND version.dataset_id=dataset.id
     AND version.id=dataset.current_draft_version_id
     AND version.status='DRAFT'
     AND version.layer='DWS'
    WHERE dataset.tenant_id=platform.current_tenant_id()
      AND dataset.domain_id=platform.current_domain_id()
      AND dataset.current_published_version_id IS NULL
      AND dataset.deleted_at IS NULL
      AND (
        selected_dataset_ids IS NULL
        OR dataset.id=ANY(selected_dataset_ids)
      )
  ), activated AS (
    INSERT INTO platform.ads_modeling_jobs(
      tenant_id,source_dws_dataset_id,source_dws_version_id,
      requested_by,not_before,next_attempt_at
    )
    SELECT
      candidate.tenant_id,candidate.dataset_id,candidate.version_id,
      actor_id,now(),now()
    FROM candidates AS candidate
    WHERE NOT EXISTS(
      SELECT 1
      FROM platform.ads_modeling_jobs AS active
      WHERE active.tenant_id=candidate.tenant_id
        AND active.source_dws_version_id=candidate.version_id
        AND active.status IN ('PENDING','WAITING_DEPENDENCY','RUNNING')
    )
    RETURNING id
  )
  SELECT
    (SELECT count(*) FROM candidates),
    (SELECT count(*) FROM activated),
    (SELECT count(*) FROM blocked);
END
$$;

REVOKE ALL ON FUNCTION
  platform.trigger_manual_dim_modeling(uuid,uuid[]),
  platform.trigger_manual_dwd_modeling(uuid,uuid[]),
  platform.trigger_manual_dws_modeling(uuid,uuid[]),
  platform.trigger_manual_ads_modeling(uuid,uuid[])
FROM PUBLIC;
GRANT EXECUTE ON FUNCTION
  platform.trigger_manual_dim_modeling(uuid,uuid[]),
  platform.trigger_manual_dwd_modeling(uuid,uuid[]),
  platform.trigger_manual_dws_modeling(uuid,uuid[]),
  platform.trigger_manual_ads_modeling(uuid,uuid[])
TO report_app;

COMMENT ON FUNCTION platform.trigger_manual_dim_modeling(uuid,uuid[]) IS
  '按显式 ODS 范围或当前领域全部 ODS 创建 DIM 建模批次';
COMMENT ON FUNCTION platform.trigger_manual_dwd_modeling(uuid,uuid[]) IS
  '按显式 ODS+DIM 范围或默认全量范围放行最近 DIM 批次的事实建模';
COMMENT ON FUNCTION platform.trigger_manual_dws_modeling(uuid,uuid[]) IS
  '按显式 DWD 范围或当前领域全部 DWD 创建逐事实 DWS 建模任务';
COMMENT ON FUNCTION platform.trigger_manual_ads_modeling(uuid,uuid[]) IS
  '按显式 DWS 范围或当前领域全部 DWS 创建逐数据集 ADS 建模任务';
