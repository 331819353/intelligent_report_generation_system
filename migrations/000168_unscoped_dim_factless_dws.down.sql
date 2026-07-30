DELETE FROM platform.dws_modeling_outputs
WHERE template_code='ENTITY_COUNT';

DELETE FROM platform.dws_modeling_jobs
WHERE group_key LIKE 'single-dim:%';

DROP FUNCTION IF EXISTS platform.trigger_unscoped_dws_modeling(uuid);

ALTER TABLE platform.dws_modeling_outputs
  DROP CONSTRAINT dws_modeling_outputs_template_code_check,
  ADD CONSTRAINT dws_modeling_outputs_template_code_check CHECK(
    template_code IN (
      'TREND','PERIOD_COMPARISON','DISTRIBUTION',
      'RANKING','DRILLDOWN','ANOMALY','MULTI_FACT_COMPARISON'
    )
  );

ALTER TABLE platform.dws_modeling_jobs
  DROP CONSTRAINT dws_modeling_jobs_source_scope_check,
  ADD CONSTRAINT dws_modeling_jobs_source_scope_check CHECK(
    jsonb_typeof(source_scope)='object'
    AND jsonb_typeof(source_scope->'dwd')='array'
    AND jsonb_typeof(source_scope->'dim')='array'
    AND jsonb_array_length(source_scope->'dwd') BETWEEN 1 AND 32
    AND jsonb_array_length(source_scope->'dim') BETWEEN 0 AND 64
    AND pg_column_size(source_scope)<=262144
    AND platform.materialization_json_is_safe(source_scope)
  );

DO $migration$
DECLARE
  definition text;
  original text;
BEGIN
  SELECT pg_get_functiondef(
    'platform.enforce_build_run_input_layer()'::regprocedure
  ) INTO definition;
  original := definition;
  definition := replace(
    definition,
    '(target_layer=''DWS'' AND NEW.input_layer NOT IN (''DWD'',''DIM''))',
    '(target_layer=''DWS'' AND NEW.input_layer<>''DWD'')'
  );
  definition := replace(
    definition,
    'DWS <- DWD|DIM',
    'DWS <- DWD'
  );
  IF definition=original
     OR position('NEW.input_layer<>''DWD''' IN definition)=0 THEN
    RAISE EXCEPTION '无法回滚 DWS 的 DIM 无事实输入';
  END IF;
  EXECUTE definition;
END
$migration$;
