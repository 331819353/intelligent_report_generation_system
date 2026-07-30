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

REVOKE ALL ON FUNCTION
  platform.trigger_manual_dws_modeling(uuid,uuid[]) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION
  platform.trigger_manual_dws_modeling(uuid,uuid[]) TO report_app;
