UPDATE platform.dwd_modeling_stage_jobs
SET prompt_version='warehouse-classification-v3',
    updated_at=clock_timestamp()
WHERE stage='DOMAIN_CLASSIFICATION'
  AND status='PENDING'
  AND prompt_version='warehouse-classification-v4';

DO $migration$
DECLARE
  function_definition text;
  updated_definition text;
BEGIN
  SELECT pg_get_functiondef(
    'platform.trigger_manual_dim_modeling(uuid)'::regprocedure
  )
  INTO function_definition;

  IF function_definition IS NULL THEN
    RAISE EXCEPTION
      'warehouse DIM modeling trigger is unavailable';
  END IF;

  updated_definition := replace(
    function_definition,
    'warehouse-classification-v4',
    'warehouse-classification-v3'
  );

  IF position(
    'warehouse-classification-v3' IN updated_definition
  )=0 THEN
    RAISE EXCEPTION
      'warehouse DIM modeling trigger does not contain the expected classification contract';
  END IF;

  IF updated_definition<>function_definition THEN
    EXECUTE updated_definition;
  END IF;
END
$migration$;

COMMENT ON FUNCTION platform.trigger_manual_dim_modeling(uuid) IS
  '按领域提交 DIM 识别与设计；使用明确实体边界及生成前字段卫生合同';
