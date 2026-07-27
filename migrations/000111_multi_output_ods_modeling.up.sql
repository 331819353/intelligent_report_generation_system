-- 一张 ODS 的主要行粒度与可抽取实体维度不再互斥。升级尚未领取的阶段
-- 任务，并让人工重提交流程使用支持 FACT + DIM 多产物的提示合同。
UPDATE platform.dwd_modeling_stage_jobs
SET prompt_version=CASE stage
      WHEN 'DOMAIN_CLASSIFICATION' THEN 'warehouse-classification-v2'
      WHEN 'DIMENSION_MODELING' THEN 'warehouse-dimension-design-v2'
      ELSE prompt_version
    END,
    updated_at=clock_timestamp()
WHERE status='PENDING'
  AND (
    (stage='DOMAIN_CLASSIFICATION'
      AND prompt_version<>'warehouse-classification-v2')
    OR
    (stage='DIMENSION_MODELING'
      AND prompt_version<>'warehouse-dimension-design-v2')
  );

DO $migration$
DECLARE
  function_definition text;
BEGIN
  SELECT pg_get_functiondef(
    'platform.trigger_manual_dwd_modeling(uuid)'::regprocedure
  ) INTO function_definition;

  IF function_definition IS NULL THEN
    RAISE EXCEPTION
      'manual DWD trigger is unavailable';
  END IF;

  IF position(
       'warehouse-classification-v1' IN function_definition
     )>0
     AND position(
       'warehouse-dimension-design-v1' IN function_definition
     )>0 THEN
    function_definition := replace(
      function_definition,
      'warehouse-classification-v1',
      'warehouse-classification-v2'
    );
    function_definition := replace(
      function_definition,
      'warehouse-dimension-design-v1',
      'warehouse-dimension-design-v2'
    );
    EXECUTE function_definition;
  ELSIF position(
          'warehouse-classification-v2' IN function_definition
        )=0
        OR position(
          'warehouse-dimension-design-v2' IN function_definition
        )=0 THEN
    RAISE EXCEPTION
      'manual DWD trigger contains an unknown prompt contract';
  END IF;
END
$migration$;

COMMENT ON FUNCTION platform.trigger_manual_dwd_modeling(uuid) IS
  '按领域提交三阶段 DIM/DWD 增量建模；一张 ODS 可按实际粒度同时产出 DWD 与抽取 DIM';
