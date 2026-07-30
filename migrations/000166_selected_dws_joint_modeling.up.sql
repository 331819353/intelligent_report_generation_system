-- 显式选择数据集时，主题建模把全部所选 DWD 与所选 DIM 固定为一个联合
-- 分析范围，只创建一个 durable task；未选择时继续调用既有的默认分批入口。
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
  IF selected_dataset_ids IS NULL OR cardinality(selected_dataset_ids)=0 THEN
    RETURN QUERY
    SELECT *
    FROM platform.trigger_manual_dws_modeling(actor_id);
    RETURN;
  END IF;

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
  ), selected_assets AS (
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
      AND dataset.id=ANY(selected_dataset_ids)
      AND dataset.status='PUBLISHED'
      AND dataset.deleted_at IS NULL
      AND version.status='PUBLISHED'
      AND version.layer IN ('DIM','DWD')
  ), aggregated_scope AS (
    SELECT
      asset.tenant_id,asset.domain_id,asset.domain_code,asset.domain_name,
      (array_agg(
        asset.dataset_id ORDER BY asset.code,asset.version_id
      ) FILTER(WHERE asset.layer='DWD'))[1] AS anchor_dataset_id,
      (array_agg(
        asset.version_id ORDER BY asset.code,asset.version_id
      ) FILTER(WHERE asset.layer='DWD'))[1] AS anchor_version_id,
      string_agg(
        asset.dataset_id::text,',' ORDER BY asset.dataset_id::text
      ) AS selection_identity,
      jsonb_agg(jsonb_build_object(
        'datasetId',asset.dataset_id::text,
        'versionId',asset.version_id::text,
        'dslHash',asset.schema_hash,
        'code',asset.code,
        'name',asset.name,
        'dsl',asset.dsl_json
      ) ORDER BY asset.code,asset.version_id)
        FILTER(WHERE asset.layer='DWD') AS dwd_scope,
      COALESCE(jsonb_agg(jsonb_build_object(
        'datasetId',asset.dataset_id::text,
        'versionId',asset.version_id::text,
        'dslHash',asset.schema_hash,
        'code',asset.code,
        'name',asset.name,
        'dsl',asset.dsl_json
      ) ORDER BY asset.code,asset.version_id)
        FILTER(WHERE asset.layer='DIM'),'[]'::jsonb) AS dim_scope
    FROM selected_assets AS asset
    GROUP BY
      asset.tenant_id,asset.domain_id,asset.domain_code,asset.domain_name
    HAVING count(*) FILTER(WHERE asset.layer='DWD') BETWEEN 1 AND 32
       AND count(*) FILTER(WHERE asset.layer='DIM') BETWEEN 0 AND 64
  ), identified_scope AS (
    SELECT
      aggregated.*,
      'selected-dws:'||encode(public.digest(
        convert_to(aggregated.selection_identity,'UTF8'),'sha256'
      ),'hex') AS group_key
    FROM aggregated_scope AS aggregated
  ), scopes AS (
    SELECT
      identified.*,
      jsonb_build_object(
        'groupKey',identified.group_key,
        'domainId',identified.domain_id::text,
        'domainCode',identified.domain_code,
        'domainName',identified.domain_name,
        'subjectCode','selected',
        'subjectName','所选数据集联合主题分析',
        'dwd',identified.dwd_scope,
        'dim',identified.dim_scope
      ) AS scope_json
    FROM identified_scope AS identified
  ), normalized_scopes AS (
    SELECT
      scopes.*,
      encode(public.digest(
        convert_to(scopes.scope_json::text,'UTF8'),'sha256'
      ),'hex') AS calculated_scope_hash
    FROM scopes
  ), activated AS (
    INSERT INTO platform.dws_modeling_jobs(
      tenant_id,source_dwd_dataset_id,source_dwd_version_id,
      requested_by,not_before,next_attempt_at,
      group_key,source_scope,scope_hash,prompt_version
    )
    SELECT
      tenant_id,anchor_dataset_id,anchor_version_id,
      actor_id,now(),now(),
      group_key,scope_json,calculated_scope_hash,
      'dws-group-planning-v3'
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
    0::bigint;
END
$$;

REVOKE ALL ON FUNCTION
  platform.trigger_manual_dws_modeling(uuid,uuid[]) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION
  platform.trigger_manual_dws_modeling(uuid,uuid[]) TO report_app;
