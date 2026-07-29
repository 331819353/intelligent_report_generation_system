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
    '        SELECT 1 FROM latest_runs
        WHERE fact_status=''SUCCEEDED''
          AND dwd_output_count>=fact_count
      ) THEN ''DWD_MODELING_COMPLETED''',
    '        SELECT 1 FROM latest_runs WHERE fact_status=''SUCCEEDED''
      ) THEN ''DWD_MODELING_COMPLETED'''
  );
  definition := replace(
    definition,
    '      AND fact.stage=''FACT_MODELING''
      AND (
        (fact.status=''PENDING'' AND NOT fact.manual_enabled)
        OR (
          fact.status=''SUCCEEDED''
          AND ready.dwd_output_count<ready.fact_count
        )
      )
    RETURNING fact.tenant_id,fact.workflow_job_id',
    '      AND fact.stage=''FACT_MODELING''
      AND fact.status=''PENDING''
      AND NOT fact.manual_enabled
    RETURNING fact.tenant_id,fact.workflow_job_id'
  );
  definition := replace(
    definition,
    '    WHERE fact_count>0 AND dimensions_published
      AND (
        (fact_status=''PENDING'' AND NOT fact_manual_enabled)
        OR (
          fact_status=''SUCCEEDED''
          AND dwd_output_count<fact_count
        )
      )',
    '    WHERE fact_count>0 AND dimensions_published
      AND fact_status=''PENDING'' AND NOT fact_manual_enabled'
  );
  definition := replace(
    definition,
    '      ) AS dimensions_published,
      (
        SELECT count(*)::integer
        FROM platform.dwd_modeling_outputs AS output
        JOIN platform.datasets AS dwd
          ON dwd.tenant_id=output.tenant_id
         AND dwd.id=output.dwd_dataset_id
        WHERE output.tenant_id=ranked_runs.tenant_id
          AND output.last_job_id=ranked_runs.workflow_job_id
          AND dwd.deleted_at IS NULL
          AND dwd.layer=''DWD''
          AND dwd.status<>''DISABLED''
          AND (
            dwd.current_draft_version_id IS NOT NULL
            OR dwd.current_published_version_id IS NOT NULL
          )
      ) AS dwd_output_count
    FROM ranked_runs',
    '      ) AS dimensions_published
    FROM ranked_runs'
  );

  IF definition=original
     OR position('dwd_output_count' IN definition)>0 THEN
    RAISE EXCEPTION '无法回滚缺失 DWD 增量重建入口';
  END IF;
  EXECUTE definition;
END
$migration$;

COMMENT ON FUNCTION platform.trigger_manual_dwd_modeling(uuid) IS
  '仅选择当前业务领域最新成功 DIM 批次；以当前已发布 DIM 合同放行一次事实 DWD 建模，拒绝复活旧批次';
