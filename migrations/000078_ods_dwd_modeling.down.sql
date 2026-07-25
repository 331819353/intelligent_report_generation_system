DROP TRIGGER IF EXISTS datasets_prevent_referenced_ods_soft_delete
  ON platform.datasets;
DROP FUNCTION IF EXISTS platform.prevent_referenced_ods_soft_delete();

DROP TRIGGER IF EXISTS dataset_versions_enqueue_dwd_modeling
  ON platform.dataset_versions;
DROP FUNCTION IF EXISTS platform.enqueue_ods_dwd_modeling();

DROP TABLE IF EXISTS platform.dwd_modeling_outputs;
DROP TABLE IF EXISTS platform.dwd_modeling_jobs;
