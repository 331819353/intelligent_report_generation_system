ALTER TABLE platform.ads_modeling_jobs
  ADD COLUMN modeling_mode text NOT NULL DEFAULT 'SELECTED'
  CHECK(modeling_mode IN ('DEFAULT','SELECTED'));

ALTER TABLE platform.ads_modeling_outputs
  ADD COLUMN target_dws_dataset_id uuid,
  ADD COLUMN target_dws_version_id uuid,
  ADD CONSTRAINT ads_modeling_outputs_target_pair_check CHECK(
    (target_dws_dataset_id IS NULL AND target_dws_version_id IS NULL)
    OR
    (target_dws_dataset_id IS NOT NULL AND target_dws_version_id IS NOT NULL)
  ),
  ADD CONSTRAINT ads_modeling_outputs_target_dataset_fk
    FOREIGN KEY(target_dws_dataset_id,tenant_id)
    REFERENCES platform.datasets(id,tenant_id) ON DELETE RESTRICT,
  ADD CONSTRAINT ads_modeling_outputs_target_version_fk
    FOREIGN KEY(target_dws_version_id,target_dws_dataset_id,tenant_id)
    REFERENCES platform.dataset_versions(id,dataset_id,tenant_id)
    ON DELETE RESTRICT;

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
    'requested_by,not_before,next_attempt_at
    )',
    'requested_by,not_before,next_attempt_at,modeling_mode
    )'
  );
  definition := replace(
    definition,
    'actor_id,now(),now()
    FROM candidates',
    'actor_id,now(),now(),
      CASE WHEN selected_dataset_ids IS NULL
        THEN ''DEFAULT'' ELSE ''SELECTED'' END
    FROM candidates'
  );
  IF definition=original
     OR position('modeling_mode' IN definition)=0 THEN
    RAISE EXCEPTION '无法为 ADS 触发函数启用默认维度包含模式';
  END IF;
  EXECUTE definition;
END
$migration$;

COMMENT ON COLUMN platform.ads_modeling_jobs.modeling_mode IS
  'DEFAULT enables DWS dimension-containment rollups; SELECTED preserves the explicit boxed workflow.';

COMMENT ON COLUMN platform.ads_modeling_outputs.target_dws_dataset_id IS
  'Optional coarser-grain DWS whose dimensions are a strict subset of the source DWS dimensions.';

COMMENT ON COLUMN platform.ads_modeling_outputs.target_dws_version_id IS
  'Exact published target DWS version used to prove the ADS rollup grain.';
