DO $migration$
DECLARE
  definition text;
  original text;
BEGIN
  SELECT pg_get_functiondef(
    'platform.trigger_manual_dwd_modeling(uuid)'::regprocedure
  ) INTO definition;
  original := definition;

  definition := replace(
    definition,
    '     AND (
       workflow.source_dataset_ids IS NULL
       OR NOT EXISTS(
         SELECT 1
         FROM platform.datasets AS current_ods
         JOIN platform.dataset_versions AS current_version
           ON current_version.tenant_id=current_ods.tenant_id
          AND current_version.dataset_id=current_ods.id
          AND current_version.id=current_ods.current_published_version_id
          AND current_version.status=''PUBLISHED''
          AND current_version.layer=''ODS''
         WHERE current_ods.tenant_id=candidate.tenant_id
           AND current_ods.domain_id=platform.current_domain_id()
           AND current_ods.status=''PUBLISHED''
           AND current_ods.deleted_at IS NULL
           AND NOT (
             current_ods.id=ANY(workflow.source_dataset_ids)
           )
       )
     )
    JOIN platform.dwd_modeling_stage_jobs AS classification',
    '    JOIN platform.dwd_modeling_stage_jobs AS classification'
  );

  definition := replace(
    definition,
    '        fact_source_dataset_ids=NULL,
        fact_dimension_dataset_ids=NULL,
        error_code='''',error_message='''',completed_at=NULL,updated_at=now()',
    '        error_code='''',error_message='''',completed_at=NULL,updated_at=now()'
  );

  IF definition=original
     OR position(
       'current_ods.id=ANY(workflow.source_dataset_ids)' IN definition
     )>0
     OR position('fact_source_dataset_ids=NULL' IN definition)>0
     OR position('fact_dimension_dataset_ids=NULL' IN definition)>0 THEN
    RAISE EXCEPTION '无法回滚默认明细建模范围重置';
  END IF;
  EXECUTE definition;
END
$migration$;

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

REVOKE ALL ON FUNCTION
  platform.trigger_manual_dwd_modeling(uuid,uuid[]) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION
  platform.trigger_manual_dwd_modeling(uuid,uuid[]) TO report_app;
