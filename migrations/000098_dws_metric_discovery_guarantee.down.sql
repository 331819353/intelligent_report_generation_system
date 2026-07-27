DROP TRIGGER IF EXISTS dataset_versions_enqueue_dws_metric_discovery
  ON platform.dataset_versions;
DROP FUNCTION IF EXISTS platform.enqueue_dws_metric_discovery();
