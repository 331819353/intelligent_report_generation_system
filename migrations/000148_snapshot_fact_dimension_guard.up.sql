-- 周期快照/日度聚合的主角色必须是 FACT；同一 ODS 仍可通过字段级实体投影
-- 额外产出 DIM。升级未领取的分类阶段并让新批次使用不可变的新提示版本。
UPDATE platform.dwd_modeling_stage_jobs
SET prompt_version='warehouse-classification-v4',
    updated_at=clock_timestamp()
WHERE stage='DOMAIN_CLASSIFICATION'
  AND status='PENDING'
  AND prompt_version='warehouse-classification-v3';

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
    'warehouse-classification-v3',
    'warehouse-classification-v4'
  );

  IF position(
    'warehouse-classification-v4' IN updated_definition
  )=0 THEN
    RAISE EXCEPTION
      'warehouse DIM modeling trigger contains an unknown classification contract';
  END IF;

  IF updated_definition<>function_definition THEN
    EXECUTE updated_definition;
  END IF;
END
$migration$;

COMMENT ON FUNCTION platform.trigger_manual_dim_modeling(uuid) IS
  '按领域提交 DIM 识别与设计；周期快照主角色强制为 FACT，稳定实体字段仍可额外抽取 DIM';
