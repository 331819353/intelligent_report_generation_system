BEGIN;

DROP TRIGGER IF EXISTS
  dimension_metric_compatibility_enqueue_semantic_change
ON platform.dimension_metric_compatibility;

DROP FUNCTION IF EXISTS
  platform.enqueue_dimension_metric_compatibility_change();

COMMIT;
