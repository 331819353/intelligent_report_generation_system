DROP TRIGGER IF EXISTS dataset_versions_enqueue_dws_modeling
  ON platform.dataset_versions;
DROP FUNCTION IF EXISTS platform.enqueue_dws_modeling();
DROP TABLE IF EXISTS platform.dws_modeling_outputs;
DROP TABLE IF EXISTS platform.dws_modeling_jobs;
