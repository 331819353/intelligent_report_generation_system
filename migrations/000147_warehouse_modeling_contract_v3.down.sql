UPDATE platform.dwd_modeling_stage_jobs
SET prompt_version=CASE stage
      WHEN 'DOMAIN_CLASSIFICATION' THEN 'warehouse-classification-v2'
      WHEN 'DIMENSION_MODELING' THEN 'warehouse-dimension-design-v2'
      WHEN 'FACT_MODELING' THEN 'warehouse-fact-design-v3'
      ELSE prompt_version
    END,
    updated_at=clock_timestamp()
WHERE status='PENDING'
  AND (
    (stage='DOMAIN_CLASSIFICATION'
      AND prompt_version='warehouse-classification-v3')
    OR
    (stage='DIMENSION_MODELING'
      AND prompt_version='warehouse-dimension-design-v3')
    OR
    (stage='FACT_MODELING'
      AND prompt_version='warehouse-fact-design-v4')
  );

DO $migration$
DECLARE
  function_signature regprocedure;
  function_definition text;
  updated_definition text;
BEGIN
  FOREACH function_signature IN ARRAY ARRAY[
    'platform.trigger_manual_dim_modeling(uuid)'::regprocedure,
    'platform.trigger_manual_dwd_modeling(uuid)'::regprocedure
  ]
  LOOP
    SELECT pg_get_functiondef(function_signature)
    INTO function_definition;

    IF function_definition IS NULL THEN
      RAISE EXCEPTION
        'warehouse modeling trigger % is unavailable',
        function_signature;
    END IF;

    updated_definition := replace(
      function_definition,
      'warehouse-classification-v3',
      'warehouse-classification-v2'
    );
    updated_definition := replace(
      updated_definition,
      'warehouse-dimension-design-v3',
      'warehouse-dimension-design-v2'
    );
    updated_definition := replace(
      updated_definition,
      'warehouse-fact-design-v4',
      'warehouse-fact-design-v3'
    );

    IF function_signature::text=
         'platform.trigger_manual_dim_modeling(uuid)'
       AND (
         position(
           'warehouse-classification-v2' IN updated_definition
         )=0
         OR position(
           'warehouse-dimension-design-v2' IN updated_definition
         )=0
         OR position(
           'warehouse-fact-design-v3' IN updated_definition
         )=0
       ) THEN
      RAISE EXCEPTION
        'warehouse modeling trigger % does not contain the expected prompt contracts',
        function_signature;
    END IF;

    IF updated_definition<>function_definition THEN
      EXECUTE updated_definition;
    END IF;
  END LOOP;
END
$migration$;

COMMENT ON FUNCTION platform.trigger_manual_dim_modeling(uuid) IS
  '按领域提交 DIM 识别与设计';

COMMENT ON FUNCTION platform.trigger_manual_dwd_modeling(uuid) IS
  '按领域放行已通过维度阶段的 DWD 事实落地';
