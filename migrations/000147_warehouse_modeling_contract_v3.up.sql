-- DIM/DWD 识别先使用明确的分层与同表边界，再执行字段设计；生成边界
-- 同时强制字符串、空值和 YYYYMMDD 日粒度卫生合同。升级尚未领取的阶段，
-- 避免继续显示或复用旧提示版本。
UPDATE platform.dwd_modeling_stage_jobs
SET prompt_version=CASE stage
      WHEN 'DOMAIN_CLASSIFICATION' THEN 'warehouse-classification-v3'
      WHEN 'DIMENSION_MODELING' THEN 'warehouse-dimension-design-v3'
      WHEN 'FACT_MODELING' THEN 'warehouse-fact-design-v4'
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
    OR
    (stage='FACT_MODELING'
      AND prompt_version='warehouse-fact-design-v3')
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

    -- DIM 入口创建三阶段任务，包含三个提示版本；DWD 入口只放行既有
    -- FACT_MODELING 阶段，在当前定义中不包含任何提示版本字面量。
    updated_definition := replace(
      function_definition,
      'warehouse-classification-v2',
      'warehouse-classification-v3'
    );
    updated_definition := replace(
      updated_definition,
      'warehouse-dimension-design-v2',
      'warehouse-dimension-design-v3'
    );
    updated_definition := replace(
      updated_definition,
      'warehouse-fact-design-v3',
      'warehouse-fact-design-v4'
    );

    IF function_signature::text=
         'platform.trigger_manual_dim_modeling(uuid)'
       AND (
         position(
           'warehouse-classification-v3' IN updated_definition
         )=0
         OR position(
           'warehouse-dimension-design-v3' IN updated_definition
         )=0
         OR position(
           'warehouse-fact-design-v4' IN updated_definition
         )=0
       ) THEN
      RAISE EXCEPTION
        'warehouse modeling trigger % contains an unknown prompt contract',
        function_signature;
    END IF;

    IF updated_definition<>function_definition THEN
      EXECUTE updated_definition;
    END IF;
  END LOOP;
END
$migration$;

COMMENT ON FUNCTION platform.trigger_manual_dim_modeling(uuid) IS
  '按领域提交 DIM 识别与设计；使用明确实体边界及生成前字段卫生合同';

COMMENT ON FUNCTION platform.trigger_manual_dwd_modeling(uuid) IS
  '按领域放行 DWD 原子明细设计；使用可靠 DIM 关联及生成前字段卫生合同';
