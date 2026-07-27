UPDATE platform.dwd_modeling_stage_jobs
SET prompt_version=CASE stage
      WHEN 'DOMAIN_CLASSIFICATION' THEN 'warehouse-classification-v1'
      WHEN 'DIMENSION_MODELING' THEN 'warehouse-dimension-design-v1'
      ELSE prompt_version
    END,
    updated_at=clock_timestamp()
WHERE status='PENDING'
  AND (
    (stage='DOMAIN_CLASSIFICATION'
      AND prompt_version='warehouse-classification-v2')
    OR
    (stage='DIMENSION_MODELING'
      AND prompt_version='warehouse-dimension-design-v2')
  );

DO $migration$
DECLARE
  function_definition text;
BEGIN
  SELECT pg_get_functiondef(
    'platform.trigger_manual_dwd_modeling(uuid)'::regprocedure
  ) INTO function_definition;

  IF function_definition IS NULL
     OR position(
       'warehouse-classification-v2' IN function_definition
     )=0
     OR position(
       'warehouse-dimension-design-v2' IN function_definition
     )=0 THEN
    RAISE EXCEPTION
      'manual DWD trigger does not contain the expected v2 prompt contracts';
  END IF;

  function_definition := replace(
    function_definition,
    'warehouse-classification-v2',
    'warehouse-classification-v1'
  );
  function_definition := replace(
    function_definition,
    'warehouse-dimension-design-v2',
    'warehouse-dimension-design-v1'
  );
  EXECUTE function_definition;
END
$migration$;

COMMENT ON FUNCTION platform.trigger_manual_dwd_modeling(uuid) IS
  '租户内按业务领域各提交一个 DIM/DWD 增量建模任务，避免逐 ODS 重复任务';
