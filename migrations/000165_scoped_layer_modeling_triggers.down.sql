DROP FUNCTION IF EXISTS platform.trigger_manual_ads_modeling(uuid,uuid[]);
DROP FUNCTION IF EXISTS platform.trigger_manual_dws_modeling(uuid,uuid[]);
DROP FUNCTION IF EXISTS platform.trigger_manual_dwd_modeling(uuid,uuid[]);
DROP FUNCTION IF EXISTS platform.trigger_manual_dim_modeling(uuid,uuid[]);

DROP TABLE IF EXISTS platform.ads_modeling_outputs;
DROP TABLE IF EXISTS platform.ads_modeling_jobs;

ALTER TABLE platform.dwd_modeling_jobs
  DROP CONSTRAINT IF EXISTS dwd_modeling_jobs_fact_dimension_scope_check,
  DROP CONSTRAINT IF EXISTS dwd_modeling_jobs_fact_source_scope_check,
  DROP CONSTRAINT IF EXISTS dwd_modeling_jobs_source_scope_check,
  DROP COLUMN IF EXISTS fact_dimension_dataset_ids,
  DROP COLUMN IF EXISTS fact_source_dataset_ids,
  DROP COLUMN IF EXISTS source_dataset_ids;
