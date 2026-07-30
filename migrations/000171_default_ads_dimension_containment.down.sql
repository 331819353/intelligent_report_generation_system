DO $migration$
DECLARE
  definition text;
  original text;
BEGIN
  SELECT pg_get_functiondef(
    'platform.trigger_manual_ads_modeling(uuid,uuid[])'::regprocedure
  ) INTO definition;
  original := definition;
  definition := replace(
    definition,
    'requested_by,not_before,next_attempt_at,modeling_mode
    )',
    'requested_by,not_before,next_attempt_at
    )'
  );
  definition := replace(
    definition,
    'actor_id,now(),now(),
      CASE WHEN selected_dataset_ids IS NULL
        THEN ''DEFAULT'' ELSE ''SELECTED'' END
    FROM candidates',
    'actor_id,now(),now()
    FROM candidates'
  );
  IF definition=original
     OR position('modeling_mode' IN definition)>0 THEN
    RAISE EXCEPTION '无法恢复 ADS 触发函数';
  END IF;
  EXECUTE definition;
END
$migration$;

ALTER TABLE platform.ads_modeling_outputs
  DROP CONSTRAINT ads_modeling_outputs_target_version_fk,
  DROP CONSTRAINT ads_modeling_outputs_target_dataset_fk,
  DROP CONSTRAINT ads_modeling_outputs_target_pair_check,
  DROP COLUMN target_dws_version_id,
  DROP COLUMN target_dws_dataset_id;

ALTER TABLE platform.ads_modeling_jobs
  DROP COLUMN modeling_mode;
