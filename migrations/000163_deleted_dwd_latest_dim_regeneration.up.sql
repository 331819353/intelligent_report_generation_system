-- 最新 FACT 阶段虽然成功，但其 DWD 产物可能随后被用户删除。此时应在
-- 同一个最新 DIM 批次上增量重建缺失 DWD，而不是返回“已完成”或回退旧批次。
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
    '      ) AS dimensions_published
    FROM ranked_runs',
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
    FROM ranked_runs'
  );
  definition := replace(
    definition,
    '    WHERE fact_count>0 AND dimensions_published
      AND fact_status=''PENDING'' AND NOT fact_manual_enabled',
    '    WHERE fact_count>0 AND dimensions_published
      AND (
        (fact_status=''PENDING'' AND NOT fact_manual_enabled)
        OR (
          fact_status=''SUCCEEDED''
          AND dwd_output_count<fact_count
        )
      )'
  );
  definition := replace(
    definition,
    '      AND fact.stage=''FACT_MODELING''
      AND fact.status=''PENDING''
      AND NOT fact.manual_enabled
    RETURNING fact.tenant_id,fact.workflow_job_id',
    '      AND fact.stage=''FACT_MODELING''
      AND (
        (fact.status=''PENDING'' AND NOT fact.manual_enabled)
        OR (
          fact.status=''SUCCEEDED''
          AND ready.dwd_output_count<ready.fact_count
        )
      )
    RETURNING fact.tenant_id,fact.workflow_job_id'
  );
  definition := replace(
    definition,
    '        SELECT 1 FROM latest_runs WHERE fact_status=''SUCCEEDED''
      ) THEN ''DWD_MODELING_COMPLETED''',
    '        SELECT 1 FROM latest_runs
        WHERE fact_status=''SUCCEEDED''
          AND dwd_output_count>=fact_count
      ) THEN ''DWD_MODELING_COMPLETED'''
  );

  IF definition=original
     OR position('dwd_output_count' IN definition)=0
     OR position('dwd_output_count<fact_count' IN definition)=0
     OR position('dwd_output_count>=fact_count' IN definition)=0 THEN
    RAISE EXCEPTION '无法为最新 DIM 批次启用缺失 DWD 增量重建';
  END IF;
  EXECUTE definition;
END
$migration$;

COMMENT ON FUNCTION platform.trigger_manual_dwd_modeling(uuid) IS
  '仅选择当前领域最新成功 DIM 批次；DWD 完整时幂等返回，产物被删除时基于当前最新已发布 DIM 增量重建';
