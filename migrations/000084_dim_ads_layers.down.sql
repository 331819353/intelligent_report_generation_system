DO $$
BEGIN
  IF EXISTS(
    SELECT 1 FROM platform.datasets WHERE layer IN ('DIM','ADS')
  ) OR EXISTS(
    SELECT 1 FROM platform.dataset_versions WHERE layer IN ('DIM','ADS')
  ) THEN
    RAISE EXCEPTION '存在 DIM 或 ADS 数据集，不能安全回退分层合同';
  END IF;
END
$$;

DROP TABLE IF EXISTS platform.dim_modeling_outputs;

DROP TRIGGER IF EXISTS build_run_inputs_require_layer
  ON platform.build_run_inputs;
DROP FUNCTION IF EXISTS platform.enforce_build_run_required_input_layers();

COMMENT ON TABLE platform.dwd_modeling_jobs IS
  'ODS 发布五分钟后执行的同领域 DWD 自动建模 outbox；带租约、幂等和有界重试';

ALTER TABLE platform.dataset_materialization_cleanup_jobs
  DROP CONSTRAINT dataset_materialization_cleanup_jobs_layer_check,
  ADD CONSTRAINT dataset_materialization_cleanup_jobs_layer_check
    CHECK(layer IN ('DWD','DWS'));

ALTER TABLE platform.query_candidate_run_materializations
  DROP CONSTRAINT query_candidate_run_materializations_layer_check,
  ADD CONSTRAINT query_candidate_run_materializations_layer_check
    CHECK(layer IN ('ODS','DWD','DWS')),
  DROP CONSTRAINT query_candidate_run_materializations_published_name_check,
  ADD CONSTRAINT query_candidate_run_materializations_published_name_check CHECK(
    published_name ~ '^(ods|dwd|dws)_t[0-9a-f]{12}_d[0-9a-f]{12}$'
  );

ALTER TABLE platform.query_run_materializations
  DROP CONSTRAINT query_run_materializations_layer_check,
  ADD CONSTRAINT query_run_materializations_layer_check
    CHECK(layer IN ('ODS','DWD','DWS')),
  DROP CONSTRAINT query_run_materializations_published_name_check,
  ADD CONSTRAINT query_run_materializations_published_name_check CHECK(
    published_name ~ '^(ods|dwd|dws)_t[0-9a-f]{12}_d[0-9a-f]{12}$'
  );

ALTER TABLE platform.dataset_tag_suggestion_jobs
  DROP CONSTRAINT dataset_tag_suggestion_jobs_layer_check,
  ADD CONSTRAINT dataset_tag_suggestion_jobs_layer_check
    CHECK(layer IN ('ODS','DWD','DWS'));

ALTER TABLE platform.build_run_inputs
  DROP CONSTRAINT build_run_inputs_input_layer_check,
  ADD CONSTRAINT build_run_inputs_input_layer_check
    CHECK(input_layer IN ('SOURCE','ODS','DWD','DWS'));

ALTER TABLE platform.dataset_materializations
  DROP CONSTRAINT dataset_materializations_layer_check,
  ADD CONSTRAINT dataset_materializations_layer_check
    CHECK(layer IN ('ODS','DWD','DWS')),
  DROP CONSTRAINT dataset_materializations_physical_schema_check,
  ADD CONSTRAINT dataset_materializations_physical_schema_check CHECK(
    physical_schema IN ('warehouse_ods','warehouse_dwd','warehouse_dws')
  ),
  DROP CONSTRAINT dataset_materializations_layer_schema_check,
  ADD CONSTRAINT dataset_materializations_layer_schema_check CHECK(
    physical_schema=CASE layer
      WHEN 'ODS' THEN 'warehouse_ods'
      WHEN 'DWD' THEN 'warehouse_dwd'
      WHEN 'DWS' THEN 'warehouse_dws'
    END
  );

ALTER TABLE platform.dataset_build_runs
  DROP CONSTRAINT dataset_build_runs_layer_check,
  ADD CONSTRAINT dataset_build_runs_layer_check
    CHECK(layer IN ('ODS','DWD','DWS'));

ALTER TABLE platform.datasets
  DROP CONSTRAINT datasets_layer_check,
  ADD CONSTRAINT datasets_layer_check
    CHECK(layer IN ('ODS','DWD','DWS'));

ALTER TABLE platform.dataset_versions
  DROP CONSTRAINT dataset_versions_layer_check,
  ADD CONSTRAINT dataset_versions_layer_check
    CHECK(layer IN ('ODS','DWD','DWS'));
